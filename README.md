# image-builder-operator

Kubernetes operator for building container images from source code.

## Prerequisites

- Kubernetes cluster (v1.28+)
- [Tekton Pipelines](https://tekton.dev/)
- [Shipwright Build](https://shipwright.io/)
- [cert-manager](https://cert-manager.io/)

See [Makefile](Makefile) for tested versions.

## Quick Start

### Install prerequisites

```bash
make prereq
```

### Deploy the operator

**Using Helm (recommended):**

```bash
helm install image-builder-operator \
  oci://ghcr.io/dana-team/helm-charts/image-builder-operator \
  --create-namespace \
  --namespace image-builder-operator-system
```

**Using kubectl:**

```bash
# Install CRDs
make install

# Deploy operator
make deploy IMG=ghcr.io/dana-team/image-builder-operator:main
```

### Create an ImageBuildPolicy

**Note:** If you installed via Helm with default values, an ImageBuildPolicy is already created.

Otherwise, create one manually:

```bash
kubectl apply -f - <<EOF
apiVersion: build.dana.io/v1alpha1
kind: ImageBuildPolicy
metadata:
  name: default
spec:
  clusterBuildStrategy:
    buildFile:
      present: buildah-strategy-managed-push
      absent: buildpacks-v3
EOF
```

### Create your first build

```bash
kubectl apply -f config/samples/build_v1alpha1_imagebuild.yaml
```

### Check build status

```bash
kubectl get imagebuild imagebuild-sample -o yaml
```

## Usage

### Basic ImageBuild

```yaml
apiVersion: build.dana.io/v1alpha1
kind: ImageBuild
metadata:
  name: my-app
  namespace: default
spec:
  buildFile:
    mode: Present
  source:
    type: Git
    git:
      url: https://github.com/myorg/my-app.git
      revision: main
  output:
    image: docker.io/myorg/my-app:latest
    pushSecret:
      name: registry-credentials
```

### Automatic rebuilds on commit

```yaml
apiVersion: build.dana.io/v1alpha1
kind: ImageBuild
metadata:
  name: my-app
spec:
  rebuild:
    mode: OnCommit
  onCommit:
    webhookSecretRef:
      name: webhook-secret
      key: token
  # ... other fields
```

The operator exposes webhook endpoints:
- GitHub: `http://<service>:8081/webhooks/github`
- GitLab: `http://<service>:8081/webhooks/gitlab`

## Development

### Prerequisites

- Go 1.24+
- Docker or Podman
- kubectl
- kind (for local testing)

### Running locally

```bash
# Install dependencies
make manifests generate fmt vet

# Run unit tests
make test

# Run e2e tests (creates local kind cluster)
make test-e2e

# Run the operator locally
make run
```

### Build and push image

```bash
make docker-build docker-push IMG=myregistry/image-builder-operator:tag
```

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.
