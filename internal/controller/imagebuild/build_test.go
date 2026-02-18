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
	imageBuildName = "test-ib"
	namespace      = "test-ns"
)

func TestNewBuild(t *testing.T) {
	t.Run("produces Build from ImageBuild spec", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategyName)

		require.Equal(t, buildNameFor(ib), build.Name)
		require.Equal(t, ib.Namespace, build.Namespace)
		require.Equal(t, absentStrategyName, build.Spec.Strategy.Name)
		require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL)
		require.Equal(t, shipwright.GitType, build.Spec.Source.Type)
		require.Equal(t, ib.Spec.Output.Image, build.Spec.Output.Image)
		require.NotNil(t, build.Labels)
		require.Equal(t, ib.Name, build.Labels[buildv1alpha1.LabelKeyParentImageBuild])
	})

	t.Run("includes optional fields when specified", func(t *testing.T) {
		const (
			revision        = "v1.2.3"
			contextDir      = "backend/api"
			cloneSecretName = "git-clone-secret"
			pushSecretName  = "registry-push-secret"
		)

		ib := newImageBuild(imageBuildName, namespace)
		ib.Spec.Source.Git.Revision = revision
		ib.Spec.Source.ContextDir = contextDir
		ib.Spec.Source.Git.CloneSecret = &corev1.LocalObjectReference{Name: cloneSecretName}
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategyName)

		require.NotNil(t, build.Spec.Source.Git.Revision)
		require.Equal(t, revision, *build.Spec.Source.Git.Revision)
		require.NotNil(t, build.Spec.Source.ContextDir)
		require.Equal(t, contextDir, *build.Spec.Source.ContextDir)
		require.NotNil(t, build.Spec.Source.Git.CloneSecret)
		require.Equal(t, cloneSecretName, *build.Spec.Source.Git.CloneSecret)
		require.NotNil(t, build.Spec.Output.PushSecret)
		require.Equal(t, pushSecretName, *build.Spec.Output.PushSecret)
	})

	t.Run("excludes optional fields when not specified", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		r, _ := newReconciler(t, ib)
		build := r.newBuild(ib, absentStrategyName)

		require.Nil(t, build.Spec.Source.Git.Revision)
		require.Nil(t, build.Spec.Source.ContextDir)
		require.Nil(t, build.Spec.Source.Git.CloneSecret)
		require.Nil(t, build.Spec.Output.PushSecret)
	})
}

func TestEnsureBuild(t *testing.T) {
	ctx := context.Background()

	t.Run("creates Build when it does not exist", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategyName},
		}
		r, c := newReconciler(t, ib, strategy)

		err := r.ensureBuild(ctx, ib, absentStrategyName)
		require.NoError(t, err)

		build := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, build))
		require.True(t, metav1.IsControlledBy(build, ib), "Build should be controller-owned by ImageBuild")
		require.Equal(t, absentStrategyName, build.Spec.Strategy.Name)
	})

	t.Run("updates existing Build when spec drifts", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategyName},
		}

		kind := shipwright.ClusterBuildStrategyKind
		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    map[string]string{buildv1alpha1.LabelKeyParentImageBuild: ib.Name},
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategyName, Kind: &kind},
				Source:   &shipwright.Source{Type: shipwright.GitType, Git: &shipwright.Git{URL: "https://old-url.com"}},
				Output:   shipwright.Image{Image: ib.Spec.Output.Image},
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, existingBuild, newScheme(t)))

		r, c := newReconciler(t, ib, strategy, existingBuild)

		err := r.ensureBuild(ctx, ib, absentStrategyName)
		require.NoError(t, err)

		updated := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, updated))
		require.Equal(t, ib.Spec.Source.Git.URL, updated.Spec.Source.Git.URL, "Build spec should be corrected")
	})

	t.Run("ensures Build has required labels", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategyName},
		}

		kind := shipwright.ClusterBuildStrategyKind
		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    nil,
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategyName, Kind: &kind},
				Source:   &shipwright.Source{Type: shipwright.GitType, Git: &shipwright.Git{URL: ib.Spec.Source.Git.URL}},
				Output:   shipwright.Image{Image: ib.Spec.Output.Image},
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, existingBuild, newScheme(t)))

		r, c := newReconciler(t, ib, strategy, existingBuild)

		err := r.ensureBuild(ctx, ib, absentStrategyName)
		require.NoError(t, err)

		updated := &shipwright.Build{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, updated))
		require.NotNil(t, updated.Labels)
		require.Equal(t, ib.Name, updated.Labels[buildv1alpha1.LabelKeyParentImageBuild], "Label should be added to existing Build")
	})

	t.Run("fails when Build owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		strategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategyName},
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

		err := r.ensureBuild(ctx, ib, absentStrategyName)
		require.Error(t, err)

		var alreadyOwned *controllerutil.AlreadyOwnedError
		require.ErrorAs(t, err, &alreadyOwned, "Should return AlreadyOwnedError when Build has different owner")
	})

	t.Run("fails when ClusterBuildStrategy not found", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		r, _ := newReconciler(t, ib)

		err := r.ensureBuild(ctx, ib, "nonexistent-strategy")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to get ClusterBuildStrategy")
	})
}

func TestPatchBuildRef(t *testing.T) {
	ctx := context.Background()

	t.Run("updates status on first reconcile", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		ib.Generation = 3
		r, c := newReconciler(t, ib)

		err := r.patchBuildRef(ctx, ib)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.Equal(t, buildNameFor(ib), latest.Status.BuildRef)
		require.Equal(t, int64(3), latest.Status.ObservedGeneration)
	})

	t.Run("updates status when generation changes", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		ib.Generation = 2
		ib.Status.BuildRef = buildNameFor(ib)
		ib.Status.ObservedGeneration = 1
		r, c := newReconciler(t, ib)

		err := r.patchBuildRef(ctx, ib)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.Equal(t, int64(2), latest.Status.ObservedGeneration)
	})

	t.Run("skips update when status is current", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		ib.Generation = 1
		ib.Status.BuildRef = buildNameFor(ib)
		ib.Status.ObservedGeneration = 1
		r, c := newReconciler(t, ib)

		err := r.patchBuildRef(ctx, ib)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.Equal(t, buildNameFor(ib), latest.Status.BuildRef)
		require.Equal(t, int64(1), latest.Status.ObservedGeneration)
	})

	t.Run("returns error when status update fails", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)

		baseReconciler, baseClient := newReconciler(t, ib)
		r := &Reconciler{
			Client: &statusPatchErrorClient{
				Client: baseClient,
				err:    errFake,
			},
			Scheme: baseReconciler.Scheme,
		}

		err := r.patchBuildRef(ctx, ib)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to patch ImageBuild status")
	})
}

func TestPatchReadyCondition(t *testing.T) {
	const readyMessage = "All good"

	ctx := context.Background()

	t.Run("sets Ready condition with reason and message", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		r, c := newReconciler(t, ib)

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, readyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionTrue, ReasonReconciled)
		require.Equal(t, readyMessage, latest.Status.Conditions[0].Message)
	})

	t.Run("updates status observed generation", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
		ib.Generation = 5
		r, c := newReconciler(t, ib)

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, readyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		require.Equal(t, int64(5), latest.Status.ObservedGeneration)
		require.Equal(t, int64(5), latest.Status.Conditions[0].ObservedGeneration)
	})

	t.Run("replaces existing Ready condition", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, namespace)
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

		err := r.patchReadyCondition(ctx, ib, metav1.ConditionTrue, ReasonReconciled, readyMessage)
		require.NoError(t, err)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

		require.Equal(t, int64(3), latest.Status.ObservedGeneration)
		require.Len(t, latest.Status.Conditions, 1)
		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionTrue, ReasonReconciled)
	})
}
