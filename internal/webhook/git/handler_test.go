package git

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
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
)

func TestServeHTTP(t *testing.T) {
	t.Run("no match", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","project":{"git_http_url":"https://example.com/none.git"}}`
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		req.Header.Set(headerGitlabEvent, "Push Hook")
		req.Header.Set(headerGitlabToken, "any")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusAccepted, rr.Code)
	})

	t.Run("rejects invalid payload", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString("{"))
		req.Header.Set(headerGitlabEvent, "Push Hook")
		req.Header.Set(headerGitlabToken, "any")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("fails on list error", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: &listErrorClient{Client: c, err: errFake}}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","project":{"git_http_url":"https://example.com/repo.git"}}`
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		req.Header.Set(headerGitlabEvent, "Push Hook")
		req.Header.Set(headerGitlabToken, "any")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("rejects unauthenticated webhook", func(t *testing.T) {
		ib := newOnCommitImageBuild("https://gitlab.example/group/repo.git")
		c := newClient(t,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{webhookSecretKey: []byte("expected")}},
			ib,
		)
		h := &Handler{Client: c}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","project":{"git_http_url":"https://gitlab.example/group/repo.git"}}`
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		req.Header.Set(headerGitlabEvent, "Push Hook")
		req.Header.Set(headerGitlabToken, "wrong")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("fails when webhook secret key missing", func(t *testing.T) {
		ib := newOnCommitImageBuild("https://gitlab.example/group/repo.git")
		c := newClient(t,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{}},
			ib,
		)
		h := &Handler{Client: c}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","project":{"git_http_url":"https://gitlab.example/group/repo.git"}}`
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		req.Header.Set(headerGitlabEvent, "Push Hook")
		req.Header.Set(headerGitlabToken, "any")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("rejects non-POST", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		req := httptest.NewRequest(http.MethodGet, WebhookPath, nil)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("rejects unsupported provider", func(t *testing.T) {
		c := newClient(t)
		h := &Handler{Client: c}

		body := `{"ref":"` + refHeadsMain + `","after":"abc","repository":{"html_url":"https://example.com/repo"}}`
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req.WithContext(context.Background()))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
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
