package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/go-github/v69/github"
)

var errUnexpectedGitHubEvent = errors.New("unexpected GitHub event type")

type githubProvider struct{}

// Name returns the provider identifier.
func (p *githubProvider) Name() string { return "github" }

// Detect reports whether the request is a GitHub push event.
func (p *githubProvider) Detect(r *http.Request) bool {
	return github.WebHookType(r) == "push"
}

// ReadPushEvent parses a GitHub push webhook payload into a pushEvent.
func (p *githubProvider) ReadPushEvent(body []byte) (*pushEvent, error) {
	webhookEvent, err := github.ParseWebHook("push", body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GitHub push event: %w", err)
	}
	payload, ok := webhookEvent.(*github.PushEvent)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errUnexpectedGitHubEvent, webhookEvent)
	}

	return newPushEvent(payload.GetRepo().GetCloneURL(), payload.GetRepo().GetHTMLURL(), payload.GetRef(), payload.GetAfter())
}

// Authenticate validates the GitHub webhook signature against the shared secret.
func (p *githubProvider) Authenticate(r *http.Request, body []byte, secret []byte) error {
	r.Body = io.NopCloser(bytes.NewReader(body))
	if _, err := github.ValidatePayload(r, secret); err != nil {
		return fmt.Errorf("failed to validate GitHub webhook signature: %w", err)
	}

	return nil
}
