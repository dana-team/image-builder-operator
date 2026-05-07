package git

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	"github.com/google/go-github/v69/github"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var errFake = errors.New("fake error")

const (
	refHeadsMain      = "refs/heads/main"
	revisionMain      = "main"
	webhookSecretName = "webhook-secret"
	webhookSecretKey  = "token"

	gitlabRepoURL  = "https://gitlab.example/group/repo.git"
	githubRepoURL  = "https://github.com/Org/Repo.git/"
	gitlabPushHook = "Push Hook"
	fakeToken      = "fake-token"
)

func TestServeHTTP(t *testing.T) {
	t.Run("no match", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload("https://example.com/none.git")))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, fakeToken)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusAccepted, rr.Code)
	})

	t.Run("rejects invalid payload", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString("{"))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, fakeToken)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("fails on list error", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: &listErrorClient{Client: c, err: errFake}}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload("https://example.com/repo.git")))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, fakeToken)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("rejects unauthenticated webhook", func(t *testing.T) {
		ib := newOnCommitImageBuild(gitlabRepoURL)
		c := newClient(t,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{webhookSecretKey: []byte("expected")}},
			ib,
		)
		h := &Handler{Client: c}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload(gitlabRepoURL)))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, "wrong")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("fails when webhook secret key missing", func(t *testing.T) {
		ib := newOnCommitImageBuild(gitlabRepoURL)
		c := newClient(t,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{}},
			ib,
		)
		h := &Handler{Client: c}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload(gitlabRepoURL)))
		req.Header.Set(gitlabEventHeader, gitlabPushHook)
		req.Header.Set(gitlabAuthHeader, fakeToken)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("method not allowed for non-POST requests", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, WebhookPath, nil)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("rejects unsupported provider", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","repository":{"html_url":"https://example.com/repo"}}`
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	for _, tc := range []struct {
		name     string
		repoURL  string
		secret   []byte
		buildReq func(secret []byte) *http.Request
	}{
		{
			name:    "accepts valid GitLab push",
			repoURL: gitlabRepoURL,
			secret:  []byte("token"),
			buildReq: func(secret []byte) *http.Request {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload(gitlabRepoURL)))
				req.Header.Set(gitlabEventHeader, gitlabPushHook)
				req.Header.Set(gitlabAuthHeader, string(secret))
				return req
			},
		},
		{
			name:    "accepts valid GitHub push",
			repoURL: githubRepoURL,
			secret:  []byte("s3cr3t"),
			buildReq: func(secret []byte) *http.Request {
				body := []byte(`{"ref":"` + refHeadsMain + `","after":"abc","repository":{"clone_url":"https://github.com/org/repo.git"}}`)
				return newGitHubPushRequest(body, secret)
			},
		},
		{
			name:    "GitHub push records pending commit when source uses SSH clone URL",
			repoURL: "git@github.com:Org/Repo.git",
			secret:  []byte("s3cr3t"),
			buildReq: func(secret []byte) *http.Request {
				body := []byte(`{"ref":"` + refHeadsMain + `","after":"abc","repository":{"clone_url":"https://github.com/org/repo.git"}}`)
				return newGitHubPushRequest(body, secret)
			},
		},
		{
			name:    "GitLab push records pending commit when source uses SSH clone URL",
			repoURL: "git@gitlab.example:group/repo.git",
			secret:  []byte("token"),
			buildReq: func(secret []byte) *http.Request {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewBufferString(gitlabPushPayload(gitlabRepoURL)))
				req.Header.Set(gitlabEventHeader, gitlabPushHook)
				req.Header.Set(gitlabAuthHeader, string(secret))
				return req
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ib := newOnCommitImageBuild(tc.repoURL)
			c := newClient(t,
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{webhookSecretKey: tc.secret}},
				ib,
			)
			h := &Handler{Client: c}
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, tc.buildReq(tc.secret).WithContext(context.Background()))
			require.Equal(t, http.StatusAccepted, rr.Code)

			updated := &buildv1alpha1.ImageBuild{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ib), updated))
			require.NotNil(t, updated.Status.OnCommit, "OnCommit status should be set")
			require.NotNil(t, updated.Status.OnCommit.Pending, "Pending commit should be set")
			require.Equal(t, "abc", updated.Status.OnCommit.Pending.CommitSHA)
		})
	}
}

func TestRepoIdentity(t *testing.T) {
	t.Run("repo identity matches normalized URL when remote does not parse as vcs", func(t *testing.T) {
		remoteURL := "foo"
		require.Equal(t, normalizeRepoURL(remoteURL), repoIdentity(remoteURL))
	})
}

func newGitHubPushRequest(body, secret []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, WebhookPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(github.EventTypeHeader, "push")
	req.Header.Set(github.SHA256SignatureHeader, sig)
	return req
}

func gitlabPushPayload(repoURL string) string {
	return fmt.Sprintf(`{"ref":"%s","after":"abc","project":{"git_http_url":"%s"}}`, refHeadsMain, repoURL)
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, buildv1alpha1.AddToScheme(s))
	require.NoError(t, shipwright.AddToScheme(s))
	return fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&buildv1alpha1.ImageBuild{}).
		WithObjects(objs...).
		Build()
}

func newOnCommitImageBuild(url string) *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ib",
			Namespace: "ns",
			Labels:    map[string]string{buildv1alpha1.LabelKeyOnCommitEnabled: "true"},
		},
		Spec: buildv1alpha1.ImageBuildSpec{
			OnCommit: &buildv1alpha1.ImageBuildOnCommit{
				WebhookSecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: webhookSecretName},
					Key:                  webhookSecretKey,
				},
			},
			Source:    buildv1alpha1.ImageBuildSource{Type: buildv1alpha1.ImageBuildSourceTypeGit, Git: buildv1alpha1.ImageBuildGitSource{URL: url, Revision: revisionMain}},
			BuildFile: buildv1alpha1.ImageBuildFile{Mode: buildv1alpha1.ImageBuildFileModeAbsent},
			Output:    buildv1alpha1.ImageBuildOutput{Image: "registry.example.com/team/app:v1"},
			Rebuild:   &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
		},
	}
}

type listErrorClient struct {
	client.Client
	err error
}

func (c *listErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.err
}
