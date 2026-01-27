package git

import (
	"bytes"
	"context"
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

func testClient(t *testing.T, objs ...client.Object) client.Client {
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

func newOnCommitImageBuild(url, revision string) *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ib",
			Namespace: "ns",
			Labels:    map[string]string{"build.dana.io/oncommit-enabled": "true"},
		},
		Spec: buildv1alpha1.ImageBuildSpec{
			OnCommit: &buildv1alpha1.ImageBuildOnCommit{
				WebhookSecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "wh"},
					Key:                  "k",
				},
			},
			Source:    buildv1alpha1.ImageBuildSource{Type: buildv1alpha1.ImageBuildSourceTypeGit, Git: buildv1alpha1.ImageBuildGitSource{URL: url, Revision: revision}},
			BuildFile: buildv1alpha1.ImageBuildFile{Mode: buildv1alpha1.ImageBuildFileModeAbsent},
			Output:    buildv1alpha1.ImageBuildOutput{Image: "registry.example.com/team/app:v1"},
			Rebuild:   &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
		},
	}
}

func TestWebhookNoMatch(t *testing.T) {
	c := testClient(t)
	h := &Handler{Client: c}

	body := `{"ref":"refs/heads/main","after":"abc","project":{"git_http_url":"https://example.com/none.git"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/git", bytes.NewBufferString(body))
	req.Header.Set(headerGitlabEvent, "Push Hook")
	req.Header.Set(headerGitlabToken, "any")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req.WithContext(context.Background()))
	require.Equal(t, http.StatusAccepted, rr.Code)
}
