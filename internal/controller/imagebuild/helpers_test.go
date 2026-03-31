package imagebuild

import (
	"context"
	"errors"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const absentStrategyName = "absent-strategy"

var errFake = errors.New("fake error")

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, buildv1alpha1.AddToScheme(s))
	require.NoError(t, shipwright.AddToScheme(s))
	return s
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()

	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&buildv1alpha1.ImageBuild{}).
		WithObjects(objs...).
		Build()

	return &Reconciler{
		Client: c,
		Scheme: s,
	}, c
}

func newImageBuildPolicy() *buildv1alpha1.ImageBuildPolicy {
	return &buildv1alpha1.ImageBuildPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyName,
		},
		Spec: buildv1alpha1.ImageBuildPolicySpec{
			ClusterBuildStrategy: buildv1alpha1.ImageBuildClusterStrategy{
				BuildFile: buildv1alpha1.ImageBuildFileStrategy{
					Present: "present-strategy",
					Absent:  absentStrategyName,
				},
			},
		},
	}
}

func newClusterBuildStrategy(name string) *shipwright.ClusterBuildStrategy {
	return &shipwright.ClusterBuildStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func newConflictingImageBuild(namespace string) *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-owner",
			Namespace: namespace,
			UID:       types.UID("other-uid"),
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

// buildCreateErrorClient injects errors when creating Build objects,
// used to exercise the generic error fallthrough in reconcileBuild.
type buildCreateErrorClient struct {
	client.Client
}

func (c *buildCreateErrorClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*shipwright.Build); ok {
		return errFake
	}
	return c.Client.Create(ctx, obj, opts...)
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

type patchErrorClient struct {
	client.Client
	err error
}

func (c *patchErrorClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*buildv1alpha1.ImageBuild); ok {
		return c.err
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// statusPatchErrorClient injects errors on status sub-resource Patch calls.
type statusPatchErrorClient struct {
	client.Client
	err error
}

func (c *statusPatchErrorClient) Status() client.SubResourceWriter {
	return &errorStatusWriter{SubResourceWriter: c.Client.Status(), err: c.err}
}

type errorStatusWriter struct {
	client.SubResourceWriter
	err error
}

func (w *errorStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return w.err
}
