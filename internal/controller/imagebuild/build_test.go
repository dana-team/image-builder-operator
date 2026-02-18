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
	"k8s.io/utils/ptr"
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
		strategy := newClusterBuildStrategy(absentStrategyName)
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
		strategy := newClusterBuildStrategy(absentStrategyName)

		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    map[string]string{buildv1alpha1.LabelKeyParentImageBuild: ib.Name},
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategyName, Kind: ptr.To(shipwright.ClusterBuildStrategyKind)},
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
		strategy := newClusterBuildStrategy(absentStrategyName)

		existingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
				Labels:    nil,
			},
			Spec: shipwright.BuildSpec{
				Strategy: shipwright.Strategy{Name: absentStrategyName, Kind: ptr.To(shipwright.ClusterBuildStrategyKind)},
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
		strategy := newClusterBuildStrategy(absentStrategyName)

		conflictingImageBuild := newConflictingImageBuild(ib.Namespace)

		conflictingBuild := &shipwright.Build{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildNameFor(ib),
				Namespace: ib.Namespace,
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(conflictingImageBuild, conflictingBuild, newScheme(t)))

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
