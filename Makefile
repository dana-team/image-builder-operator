IMG ?= controller:latest

ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTAINER_TOOL ?= docker

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate RBAC, CRD and Webhook manifests.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: sync-helm-crds
sync-helm-crds: manifests ## Sync CRDs to Helm chart.
	cp config/crd/bases/*.yaml charts/image-builder-operator/crds/

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run unit tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

ifdef E2E_GINKGO_PROCS
GINKGO_E2E_PROCS_FLAG := -p -procs=$(E2E_GINKGO_PROCS)
endif

KIND_CLUSTER ?= image-builder-operator-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up Kind cluster for e2e tests.
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ginkgo ## Run e2e tests.
	KIND_CLUSTER=$(KIND_CLUSTER) $(GINKGO) -v $(GINKGO_E2E_PROCS_FLAG) ./test/e2e/...
ifndef E2E_SKIP_CLEANUP
	$(MAKE) cleanup-test-e2e
endif

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Delete Kind cluster.
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with fixes.
	$(GOLANGCI_LINT) run --fix

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run controller locally.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image.
	$(CONTAINER_TOOL) push ${IMG}

PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le

.PHONY: docker-buildx
docker-buildx: ## Build and push multi-arch docker image.
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name image-builder-operator-builder
	$(CONTAINER_TOOL) buildx use image-builder-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm image-builder-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate install.yaml.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

.PHONY: create-imagebuildpolicy
create-imagebuildpolicy: ## Create ImageBuildPolicy for testing.
	$(KUBECTL) apply -f hack/manifests/imagebuildpolicy.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Prerequisites

TEKTON_PIPELINES_VERSION ?= v0.68.0
TEKTON_PIPELINES_URL ?= https://storage.googleapis.com/tekton-releases/pipeline/previous/$(TEKTON_PIPELINES_VERSION)/release.yaml
SHIPWRIGHT_BUILD_VERSION ?= v0.17.0
SHIPWRIGHT_BUILD_URL ?= https://github.com/shipwright-io/build/releases/download/$(SHIPWRIGHT_BUILD_VERSION)/release.yaml
CERT_MANAGER_VERSION ?= v1.16.2
CERT_MANAGER_URL ?= https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml

.PHONY: prereq
prereq: install-shipwright ## Install all prerequisites.

.PHONY: uninstall-prereq
uninstall-prereq: uninstall-shipwright uninstall-tekton uninstall-cert-manager ## Uninstall all prerequisites.

.PHONY: install-cert-manager
install-cert-manager: ## Install cert-manager.
	$(KUBECTL) apply -f $(CERT_MANAGER_URL)
	@echo "Waiting for cert-manager..."
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=5m
	@timeout 120 bash -c 'until $(KUBECTL) get validatingwebhookconfigurations cert-manager-webhook -o jsonpath="{.webhooks[0].clientConfig.caBundle}" 2>/dev/null | grep -q .; do sleep 2; done'

.PHONY: uninstall-cert-manager
uninstall-cert-manager: ## Uninstall cert-manager.
	$(KUBECTL) delete -f $(CERT_MANAGER_URL) --ignore-not-found=true

.PHONY: install-tekton
install-tekton: ## Install Tekton Pipelines.
	$(KUBECTL) apply -f $(TEKTON_PIPELINES_URL)
	$(KUBECTL) -n tekton-pipelines rollout status deployment/tekton-pipelines-controller --timeout=5m
	$(KUBECTL) -n tekton-pipelines rollout status deployment/tekton-pipelines-webhook --timeout=5m

.PHONY: uninstall-tekton
uninstall-tekton: ## Uninstall Tekton Pipelines.
	$(KUBECTL) delete -f $(TEKTON_PIPELINES_URL) --ignore-not-found=true

.PHONY: install-shipwright
install-shipwright: install-cert-manager install-tekton ## Install Shipwright Build.
	$(KUBECTL) apply --server-side -f $(SHIPWRIGHT_BUILD_URL)
	$(KUBECTL) apply -f hack/shipwright/certs.yaml
	@for crd in $$($(KUBECTL) get crd -oname | grep shipwright.io); do \
		$(KUBECTL) annotate $$crd cert-manager.io/inject-ca-from=shipwright-build/shipwright-build-webhook-cert --overwrite; \
	done
	$(KUBECTL) -n shipwright-build rollout status deployment/shipwright-build-controller --timeout=5m
	$(KUBECTL) -n shipwright-build rollout status deployment/shipwright-build-webhook --timeout=5m
	$(KUBECTL) apply -f hack/shipwright/strategies.yaml

.PHONY: uninstall-shipwright
uninstall-shipwright: ## Uninstall Shipwright Build.
	$(KUBECTL) delete -f hack/shipwright/strategies.yaml --ignore-not-found=true
	$(KUBECTL) delete -f hack/shipwright/certs.yaml --ignore-not-found=true
	$(KUBECTL) delete -f $(SHIPWRIGHT_BUILD_URL) --ignore-not-found=true

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO ?= $(LOCALBIN)/ginkgo
HELM_DOCS ?= $(LOCALBIN)/helm-docs

KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
ENVTEST_K8S_VERSION = 1.31.0
GOLANGCI_LINT_VERSION ?= v2.7.2
GINKGO_VERSION ?= $(shell go list -m -f "{{ .Version }}" github.com/onsi/ginkgo/v2)
HELM_DOCS_VERSION ?= v1.14.2

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest
	@echo "Setting up envtest binaries..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: ginkgo
ginkgo: $(GINKGO)
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

$(HELM_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(HELM_DOCS),github.com/norwoodj/helm-docs/cmd/helm-docs,$(HELM_DOCS_VERSION))

.PHONY: helm-docs
helm-docs: $(HELM_DOCS) ## Regenerate Helm chart README from values.yaml.
	$(HELM_DOCS) --chart-search-root=charts

.PHONY: verify-helm-docs
verify-helm-docs: helm-docs ## Verify Helm chart README is in sync with values.yaml.
	@if ! git diff --exit-code charts/image-builder-operator/README.md; then \
		echo "ERROR: Helm chart README is out of sync with values.yaml."; \
		echo "Run 'make helm-docs' and commit the changes."; \
		exit 1; \
	fi

define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
