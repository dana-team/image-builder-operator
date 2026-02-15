package git

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGitLabSuccess(t *testing.T) {
	ib := newOnCommitImageBuild("https://gitlab.example/group/repo.git")
	c := newClient(t,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: webhookSecretName, Namespace: ib.Namespace}, Data: map[string][]byte{webhookSecretKey: []byte("token")}},
		ib,
	)
	h := &Handler{Client: c}

	body := `{"ref":"` + refHeadsMain + `","after":"abc","project":{"git_http_url":"https://gitlab.example/group/repo.git"}}`
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(body))
	req.Header.Set(headerGitlabEvent, "Push Hook")
	req.Header.Set(headerGitlabToken, "token")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req.WithContext(context.Background()))
	require.Equal(t, http.StatusAccepted, rr.Code)

	updated := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ib), updated))
	require.NotNil(t, updated.Status.OnCommit.Pending)
	require.Equal(t, "abc", updated.Status.OnCommit.Pending.CommitSHA)
}
