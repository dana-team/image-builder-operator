package git

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v69/github"
	"github.com/stretchr/testify/require"
)

const githubCloneURL = githubRepoURL + ".git"

func TestGitHubReadPushEvent(t *testing.T) {
	p := &githubProvider{}

	t.Run("parses valid push payload", func(t *testing.T) {
		body := []byte(`{"ref":"` + refHeadsMain + `","after":"abc123","repository":{"clone_url":"` + githubCloneURL + `","html_url":"` + githubRepoURL + `"}}`)
		event, err := p.ReadPushEvent(body)
		require.NoError(t, err)
		require.Equal(t, githubCloneURL, event.RepoURL)
		require.Equal(t, refHeadsMain, event.Ref)
		require.Equal(t, "abc123", event.CommitSHA)
	})

	t.Run("falls back to HTML URL when clone URL is empty", func(t *testing.T) {
		body := []byte(`{"ref":"` + refHeadsMain + `","after":"abc","repository":{"clone_url":"","html_url":"` + githubRepoURL + `"}}`)
		event, err := p.ReadPushEvent(body)
		require.NoError(t, err)
		require.Equal(t, githubRepoURL, event.RepoURL)
	})

	t.Run("rejects malformed JSON", func(t *testing.T) {
		_, err := p.ReadPushEvent([]byte("{"))
		require.Error(t, err)
	})
}

func TestGitHubAuthenticate(t *testing.T) {
	p := &githubProvider{}
	secret := []byte("s3cr3t")
	body := []byte(`{"ref":"refs/heads/main"}`)

	t.Run("succeeds with valid HMAC signature", func(t *testing.T) {
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(github.EventTypeHeader, "push")
		req.Header.Set(github.SHA256SignatureHeader, sig)

		require.NoError(t, p.Authenticate(req, body, secret))
	})

	t.Run("rejects invalid HMAC signature", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(github.EventTypeHeader, "push")
		req.Header.Set(github.SHA256SignatureHeader, "sha256=invalid")

		require.Error(t, p.Authenticate(req, body, secret))
	})
}
