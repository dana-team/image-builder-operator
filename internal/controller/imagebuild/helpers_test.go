package imagebuild

import (
	"context"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const absentStrategy = "absent-strategy"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, buildv1alpha1.AddToScheme(s))
	require.NoError(t, shipwright.AddToScheme(s))
	return s
}

func newReconciler(t *testing.T, objs ...client.Object) (*ImageBuildReconciler, client.Client) {
	t.Helper()

	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&buildv1alpha1.ImageBuild{}).
		WithObjects(objs...).
		Build()

	return &ImageBuildReconciler{
		Client: c,
		Scheme: s,
	}, c
}

func newImageBuildPolicy() *buildv1alpha1.ImageBuildPolicy {
	return &buildv1alpha1.ImageBuildPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: ImageBuildPolicyName,
		},
		Spec: buildv1alpha1.ImageBuildPolicySpec{
			ClusterBuildStrategy: buildv1alpha1.ImageBuildClusterStrategy{
				BuildFile: buildv1alpha1.ImageBuildFileStrategy{
					Present: "present-strategy",
					Absent:  absentStrategy,
				},
			},
		},
	}
}

func newImageBuild(name, namespace string) *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: buildv1alpha1.ImageBuildSpec{
			BuildFile: buildv1alpha1.ImageBuildFile{Mode: buildv1alpha1.ImageBuildFileModeAbsent},
			Source: buildv1alpha1.ImageBuildSource{
				Type: buildv1alpha1.ImageBuildSourceTypeGit,
				Git:  buildv1alpha1.ImageBuildGitSource{URL: "https://example.invalid/repo.git"},
			},
			Output: buildv1alpha1.ImageBuildOutput{Image: "registry.example.com/team/app"},
		},
	}
}

func requireCondition(
	t *testing.T,
	conditions []metav1.Condition,
	condType string,
	status metav1.ConditionStatus,
	reason string,
) {
	t.Helper()

	cond := meta.FindStatusCondition(conditions, condType)
	require.NotNil(t, cond, "%s condition should be set", condType)
	require.Equal(t, status, cond.Status)
	require.Equal(t, reason, cond.Reason)
}

type getErrorClient struct {
	client.Client
	err error
}

func (c *getErrorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	// Inject a non-NotFound error for BuildRun fetches to exercise error handling.
	if _, ok := obj.(*shipwright.BuildRun); ok {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
