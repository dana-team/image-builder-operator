package git

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitLabReadPushEvent(t *testing.T) {
	p := &gitlabProvider{}

	t.Run("parses valid push payload", func(t *testing.T) {
		body := []byte(gitlabPushPayload(gitlabRepoURL))
		event, err := p.ReadPushEvent(body)
		require.NoError(t, err)
		require.Equal(t, gitlabRepoURL, event.RepoURL)
		require.Equal(t, refHeadsMain, event.Ref)
		require.Equal(t, "abc", event.CommitSHA)
	})

	t.Run("rejects malformed JSON", func(t *testing.T) {
		_, err := p.ReadPushEvent([]byte("{"))
		require.Error(t, err)
	})
}

func TestGitLabAuthenticate(t *testing.T) {
	p := &gitlabProvider{}
	secret := []byte("correct")
	body := []byte(gitlabPushPayload(gitlabRepoURL))

	t.Run("succeeds with valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set(headerGitlabEvent, gitlabPushHook)
		req.Header.Set(headerGitlabToken, string(secret))

		require.NoError(t, p.Authenticate(req, body, secret))
	})

	t.Run("rejects invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set(headerGitlabEvent, gitlabPushHook)
		req.Header.Set(headerGitlabToken, "wrong")

		require.Error(t, p.Authenticate(req, body, secret))
	})
}
