// Package git implements the Git webhook handler for on-commit rebuilds.
package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// WebhookPath is the HTTP path the git webhook handler is registered at.
const WebhookPath = "/webhooks/git"

// Sentinel errors for webhook handling.
var (
	errMethodNotAllowed        = errors.New("method not allowed")
	errUnsupportedWebhook      = errors.New("unsupported webhook event: only GitHub and GitLab push events are supported")
	errMissingOnCommit         = errors.New("missing spec.onCommit")
	errMissingWebhookSecretKey = errors.New("missing key in webhook secret")
	errMissingPushEventFields  = errors.New("missing required fields: ref or repository URL")
)

// Handler handles incoming Git webhook requests and triggers on-commit rebuilds for matching ImageBuilds.
type Handler struct {
	Client        client.Client
	EventRecorder record.EventRecorder
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx).WithName("git-webhook")

	body, provider, err := h.detectProvider(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	event, err := provider.ReadPushEvent(body)
	if err != nil {
		logger.Error(err, "failed to read push event", "provider", provider.Name())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	var list buildv1alpha1.ImageBuildList
	if err := h.Client.List(ctx, &list, client.MatchingLabels{buildv1alpha1.LabelKeyOnCommitEnabled: "true"}); err != nil {
		logger.Error(err, "failed to list imagebuilds")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	normalizedRepoURL := normalizeRepoURL(event.RepoURL)
	matches := make([]buildv1alpha1.ImageBuild, 0, len(list.Items))
	for _, ib := range list.Items {

		isMatch := ib.Spec.Rebuild != nil &&
			ib.Spec.Rebuild.Mode == buildv1alpha1.ImageBuildRebuildModeOnCommit &&
			ib.Spec.Source.Type == buildv1alpha1.ImageBuildSourceTypeGit &&
			ib.Spec.Source.Git.URL != "" &&
			ib.Spec.Source.Git.Revision != "" &&
			normalizeRepoURL(ib.Spec.Source.Git.URL) == normalizedRepoURL &&
			event.Ref == "refs/heads/"+ib.Spec.Source.Git.Revision

		if !isMatch {
			continue
		}
		matches = append(matches, ib)
	}

	if len(matches) == 0 {
		logger.Info("webhook ignored: no matching ImageBuild found", "repo", event.RepoURL, "ref", event.Ref)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	now := metav1.Now()
	var authenticatedCount int
	for _, ib := range matches {

		secret, err := getWebhookSecret(ctx, h.Client, &ib)
		if err != nil {
			logger.Error(err, "failed to resolve webhook secret", "name", ib.Name, "namespace", ib.Namespace)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if err := provider.Authenticate(r, body, secret); err != nil {
			continue
		}

		authenticatedCount++
		if err := h.patchOnCommitStatus(ctx, &ib, event, now); err != nil {
			logger.Error(err, "failed to update imagebuild status", "name", ib.Name, "namespace", ib.Namespace)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if h.EventRecorder != nil {
			h.EventRecorder.Eventf(&ib, corev1.EventTypeNormal, "WebhookAccepted", "git webhook accepted for %s/%s", ib.Namespace, ib.Name)
		}
	}

	if authenticatedCount == 0 {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) detectProvider(r *http.Request) ([]byte, webhookProvider, error) {
	if r.Method != http.MethodPost {
		return nil, nil, fmt.Errorf("%w: %s", errMethodNotAllowed, r.Method)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read body: %w", err)
	}

	for _, p := range []webhookProvider{&githubProvider{}, &gitlabProvider{}} {
		if p.Detect(r) {
			return body, p, nil
		}
	}

	return nil, nil, errUnsupportedWebhook
}

func (h *Handler) patchOnCommitStatus(ctx context.Context, ib *buildv1alpha1.ImageBuild, event *pushEvent, now metav1.Time) error {
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &buildv1alpha1.ImageBuild{}
		if err := h.Client.Get(ctx, types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}, latest); err != nil {
			return fmt.Errorf("failed to get ImageBuild %s/%s: %w", ib.Namespace, ib.Name, err)
		}
		if latest.Status.OnCommit == nil {
			latest.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{}
		}
		onCommitEvent := &buildv1alpha1.ImageBuildOnCommitEvent{
			Ref:        event.Ref,
			CommitSHA:  event.CommitSHA,
			ReceivedAt: now,
		}
		latest.Status.OnCommit.LastReceived = onCommitEvent
		latest.Status.OnCommit.Pending = onCommitEvent
		return h.Client.Status().Update(ctx, latest)
	}); err != nil {
		return fmt.Errorf("failed to patch on-commit status for %s/%s: %w", ib.Namespace, ib.Name, err)
	}

	return nil
}

func getWebhookSecret(ctx context.Context, c client.Client, ib *buildv1alpha1.ImageBuild) ([]byte, error) {
	if ib.Spec.OnCommit == nil {
		return nil, errMissingOnCommit
	}
	ref := ib.Spec.OnCommit.WebhookSecretRef
	sec := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      ref.Name,
		Namespace: ib.Namespace,
	}
	if err := c.Get(ctx, key, sec); err != nil {
		return nil, fmt.Errorf("failed to get webhook secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	val, ok := sec.Data[ref.Key]
	if !ok || len(val) == 0 {
		return nil, fmt.Errorf("%w: %q in secret %s/%s", errMissingWebhookSecretKey, ref.Key, key.Namespace, key.Name)
	}
	return val, nil
}

// newPushEvent constructs a pushEvent with URL fallback and field validation.
func newPushEvent(primaryURL, fallbackURL, ref, commitSHA string) (*pushEvent, error) {
	repo := strings.TrimSpace(primaryURL)
	if repo == "" {
		repo = strings.TrimSpace(fallbackURL)
	}
	if repo == "" || strings.TrimSpace(ref) == "" {
		return nil, errMissingPushEventFields
	}
	return &pushEvent{RepoURL: repo, Ref: ref, CommitSHA: commitSHA}, nil
}

func normalizeRepoURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	return s
}
