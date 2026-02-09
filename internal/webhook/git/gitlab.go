package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-playground/webhooks/v6/gitlab"
)

const (
	headerGitlabEvent = "X-Gitlab-Event"
	headerGitlabToken = "X-Gitlab-Token"
)

type gitlabProvider struct{}

// Name returns the provider identifier.
func (p *gitlabProvider) Name() string { return "gitlab" }

// Detect reports whether the request is a GitLab push event.
func (p *gitlabProvider) Detect(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get(headerGitlabEvent)), string(gitlab.PushEvents))
}

// ReadPushEvent parses a GitLab push webhook payload into a pushEvent.
func (p *gitlabProvider) ReadPushEvent(body []byte) (*pushEvent, error) {
	var payload gitlab.PushEventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse GitLab push event: %w", err)
	}

	repo := strings.TrimSpace(payload.Project.GitHTTPURL)
	if repo == "" {
		repo = strings.TrimSpace(payload.Project.WebURL)
	}
	if repo == "" || strings.TrimSpace(payload.Ref) == "" {
		return nil, errMissingPushEventFields
	}
	return &pushEvent{RepoURL: repo, Ref: payload.Ref, CommitSHA: payload.After}, nil
}

// Authenticate validates the GitLab webhook token against the shared secret.
func (p *gitlabProvider) Authenticate(r *http.Request, body []byte, secret []byte) error {
	hook, err := gitlab.New(gitlab.Options.Secret(string(secret)))
	if err != nil {
		return fmt.Errorf("failed to create GitLab webhook validator: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if _, err = hook.Parse(r, gitlab.PushEvents); err != nil {
		return fmt.Errorf("failed to validate GitLab webhook token: %w", err)
	}

	return nil
}
