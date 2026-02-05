package imagebuild

import (
	"context"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	testImageBuildName = "test-ib"
	testNamespace      = "test-ns"
)

func TestNewBuild(t *testing.T) {
	t.Run("produces Build from ImageBuild spec", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategy)

		require.Equal(t, buildNameFor(ib), build.Name)
		require.Equal(t, ib.Namespace, build.Namespace)
		require.Equal(t, absentStrategy, build.Spec.Strategy.Name)
		require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL)
		require.Equal(t, shipwright.GitType, build.Spec.Source.Type)
		require.Equal(t, ib.Spec.Output.Image, build.Spec.Output.Image)
		require.NotNil(t, build.Labels)
		require.Equal(t, ib.Name, build.Labels["build.dana.io/parent-imagebuild"])
	})

	t.Run("includes optional fields when specified", func(t *testing.T) {
		const (
			testRevision    = "v1.2.3"
			testContextDir  = "backend/api"
			testCloneSecret = "git-clone-secret"
			testPushSecret  = "registry-push-secret"
		)

		ib := newImageBuild(testImageBuildName, testNamespace)
		ib.Spec.Source.Git.Revision = testRevision
		ib.Spec.Source.ContextDir = testContextDir
		ib.Spec.Source.Git.CloneSecret = &corev1.LocalObjectReference{Name: testCloneSecret}
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: testPushSecret}
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategy)

		require.NotNil(t, build.Spec.Source.Git.Revision)
		require.Equal(t, testRevision, *build.Spec.Source.Git.Revision)
		require.NotNil(t, build.Spec.Source.ContextDir)
		require.Equal(t, testContextDir, *build.Spec.Source.ContextDir)
		require.NotNil(t, build.Spec.Source.Git.CloneSecret)
		require.Equal(t, testCloneSecret, *build.Spec.Source.Git.CloneSecret)
		require.NotNil(t, build.Spec.Output.PushSecret)
		require.Equal(t, testPushSecret, *build.Spec.Output.PushSecret)
	})

	t.Run("excludes optional fields when not specified", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategy)

		require.Nil(t, build.Spec.Source.Git.Revision)
		require.Nil(t, build.Spec.Source.ContextDir)
		require.Nil(t, build.Spec.Source.Git.CloneSecret)
		require.Nil(t, build.Spec.Output.PushSecret)
	})
}

func TestReconcileBuild(t *testing.T) {
	ctx := context.Background()

	t.Run("creates Build when it does not exist", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
		}
		r, c := newReconciler(t, ib, strategy)

		err := r.reconcileBuild(ctx, ib, absentStrategy)
		require.NoError(t, err)

		build := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, build))
		require.True(t, metav1.IsControlledBy(build, ib), "Build should be controller-owned by ImageBuild")
		require.Equal(t, absentStrategy, build.Spec.Strategy.Name)
	})

	t.Run("updates existing Build when spec drifts", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
		}

		kind := shipwright.ClusterBuildStrategyKind
		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    map[string]string{"build.dana.io/parent-imagebuild": ib.Name},
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategy, Kind: &kind},
				Source:   &shipwright.Source{Type: shipwright.GitType, Git: &shipwright.Git{URL: "https://old-url.com"}},
				Output:   shipwright.Image{Image: ib.Spec.Output.Image},
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, existingBuild, newScheme(t)))

		r, c := newReconciler(t, ib, strategy, existingBuild)

		err := r.reconcileBuild(ctx, ib, absentStrategy)
		require.NoError(t, err)

		updated := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, updated))
		require.Equal(t, ib.Spec.Source.Git.URL, updated.Spec.Source.Git.URL, "Build spec should be corrected")
	})

	t.Run("ensures Build has required labels", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
		}

		kind := shipwright.ClusterBuildStrategyKind
		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    nil,
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategy, Kind: &kind},
				Source:   &shipwright.Source{Type: shipwright.GitType, Git: &shipwright.Git{URL: ib.Spec.Source.Git.URL}},
				Output:   shipwright.Image{Image: ib.Spec.Output.Image},
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, existingBuild, newScheme(t)))

		r, c := newReconciler(t, ib, strategy, existingBuild)

		err := r.reconcileBuild(ctx, ib, absentStrategy)
		require.NoError(t, err)

		updated := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, updated))
		require.NotNil(t, updated.Labels)
		require.Equal(t, ib.Name, updated.Labels["build.dana.io/parent-imagebuild"], "Label should be added to existing Build")
	})

	t.Run("fails when Build owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
		}

		otherOwner := &buildv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: "someone-else",
				UID:  types.UID("other-uid"),
			},
		}

		conflictingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflictingBuild, newScheme(t)))

		r, _ := newReconciler(t, ib, strategy, conflictingBuild)

		err := r.reconcileBuild(ctx, ib, absentStrategy)
		require.Error(t, err)

		var alreadyOwned *controllerutil.AlreadyOwnedError
		require.ErrorAs(t, err, &alreadyOwned, "Should return AlreadyOwnedError when Build has different owner")
	})

	t.Run("fails when ClusterBuildStrategy not found", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		r, _ := newReconciler(t, ib)

		err := r.reconcileBuild(ctx, ib, "nonexistent-strategy")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to get ClusterBuildStrategy")
	})
}

func TestPatchReadyCondition(t *testing.T) {
	const testReadyMessage = "All good"

	ctx := context.Background()

	t.Run("sets Ready condition with reason and message", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		r, c := newReconciler(t, ib)

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, testReadyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionTrue, ReasonReconciled)
		require.Equal(t, testReadyMessage, latest.Status.Conditions[0].Message)
	})

	t.Run("updates status observed generation", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		ib.Generation = 5
		r, c := newReconciler(t, ib)

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, testReadyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		require.Equal(t, int64(5), latest.Status.ObservedGeneration)
		require.Equal(t, int64(5), latest.Status.Conditions[0].ObservedGeneration)
	})

	t.Run("replaces existing Ready condition", func(t *testing.T) {
		ib := newImageBuild(testImageBuildName, testNamespace)
		ib.Generation = 3
		ib.Status.Conditions = []metav1.Condition{
			{
				Type:               TypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             ReasonMissingPolicy,
				Message:            "Policy missing",
				ObservedGeneration: 2,
			},
		}
		r, c := newReconciler(t, ib)

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, testReadyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		require.Equal(t, int64(3), latest.Status.ObservedGeneration)
		require.Len(t, latest.Status.Conditions, 1)
		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionTrue, ReasonReconciled)
	})
}
