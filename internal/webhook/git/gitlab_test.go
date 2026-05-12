package git

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitLabReadPushEvent(t *testing.T) {
	p := &gitlabProvider{}

	t.Run("parses valid push payload", func(t *testing.T) {
		ssh := "git@gitlab.example:group/repo.git"
		body := []byte(fmt.Sprintf(`{"ref":%q,"after":"abc","project":{"git_http_url":%q,"git_ssh_url":%q}}`,
			refHeadsMain, gitlabRepoURL, ssh))
		event, err := p.ReadPushEvent(body)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{ssh, gitlabRepoURL}, event.cloneURLs)
		require.Equal(t, refHeadsMain, event.ref)
		require.Equal(t, "abc", event.commitSHA)
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
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, string(secret))

		require.NoError(t, p.Authenticate(req, body, secret))
	})

	t.Run("rejects invalid token", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, "wrong")

		require.Error(t, p.Authenticate(req, body, secret))
	})
}
