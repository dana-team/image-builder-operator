package imagebuild

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	shipwrightresources "github.com/shipwright-io/build/pkg/reconciler/buildrun/resources"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestEnsureBuildRun(t *testing.T) {
	ctx := context.Background()

	t.Run("creates BuildRun when it does not exist", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, c := newReconciler(t, ib)

		br, err := r.ensureBuildRun(ctx, ib)
		require.NoError(t, err)
		require.NotNil(t, br)

		actualBuildRun := &shipwright.BuildRun{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildRunNameFor(ib, ib.Status.BuildRunCounter), Namespace: ib.Namespace}, actualBuildRun))
		require.Equal(t, buildRunNameFor(ib, ib.Status.BuildRunCounter), actualBuildRun.Name)
		require.Equal(t, ib.Namespace, actualBuildRun.Namespace)

		require.True(t, metav1.IsControlledBy(actualBuildRun, ib), "BuildRun should be controller-owned by ImageBuild")
		require.NotNil(t, actualBuildRun.Spec.Build.Name)
		require.Equal(t, buildNameFor(ib), *actualBuildRun.Spec.Build.Name)
	})

	t.Run("reuses existing BuildRun", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.UID = types.UID("ib-uid")
		ib.Status.BuildRunCounter = 0
		existingBuildRun := newBuildRun(ib, 1)
		existingBuildRun.UID = types.UID("existing-buildrun-uid")

		require.NoError(t, controllerutil.SetControllerReference(ib, existingBuildRun, newScheme(t)))

		r, _ := newReconciler(t, ib, existingBuildRun)
		br, err := r.ensureBuildRun(ctx, ib)
		require.NoError(t, err)
		require.Equal(t, existingBuildRun.Name, br.Name)
		require.Equal(t, existingBuildRun.UID, br.UID, "expected ensureBuildRun to return the existing BuildRun object")
	})

	t.Run("fails when BuildRun owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.BuildRunCounter = 0
		conflict := newBuildRun(ib, 1)

		conflictingImageBuild := newConflictingImageBuild(ib.Namespace)
		require.NoError(t, controllerutil.SetControllerReference(conflictingImageBuild, conflict, newScheme(t)))

		r, _ := newReconciler(t, ib, conflict)
		br, err := r.ensureBuildRun(ctx, ib)
		require.Nil(t, br)
		require.Error(t, err)

		var alreadyOwned *controllerutil.AlreadyOwnedError
		require.ErrorAs(t, err, &alreadyOwned, "Should return AlreadyOwnedError when BuildRun has different owner")
	})

	t.Run("returns error when build run lookup fails", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)
		r.Client = &getErrorClient{Client: r.Client, err: errFake}

		br, err := r.ensureBuildRun(ctx, ib)
		require.Nil(t, br)
		require.Error(t, err)
	})
}

func TestComputeLatestImage(t *testing.T) {
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	t.Run("appends digest when build output has a digest", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		br.Status.Output = &shipwright.Output{Digest: "sha256:abc"}
		require.Equal(t, ib.Spec.Output.Image+"@sha256:abc", computeLatestImage(ib, br))
	})

	t.Run("keeps tagged image when no digest is present", func(t *testing.T) {
		ibCopy := ib.DeepCopy()
		ibCopy.Spec.Output.Image = "registry.example.com/team/app:v1"
		br := &shipwright.BuildRun{}
		require.Equal(t, "registry.example.com/team/app:v1", computeLatestImage(ibCopy, br))
	})

	t.Run("returns empty when image has no tag or digest", func(t *testing.T) {
		ibCopy := ib.DeepCopy()
		ibCopy.Spec.Output.Image = "registry.example.com/team/app"
		br := &shipwright.BuildRun{}
		require.Empty(t, computeLatestImage(ibCopy, br))
	})
}

func TestIsTagOrDigestPresent(t *testing.T) {
	digest := sha256.Sum256([]byte("digest"))

	tests := []struct {
		name     string
		image    string
		expected bool
	}{
		{name: "returns false for invalid image reference", image: "not a valid image@@@", expected: false},
		{name: "returns true when image has a tag", image: "registry.example.com/team/app:v1", expected: true},
		{name: "returns true when image has a digest", image: "registry.example.com/team/app@sha256:" + fmt.Sprintf("%x", digest), expected: true},
		{name: "returns false when image has no tag or digest", image: "registry.example.com/team/app", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, isTagOrDigestPresent(tt.image))
		})
	}
}

func TestIsSpecDrifted(t *testing.T) {
	ctx := context.Background()
	const buildRunName = "some-buildrun"

	t.Run("required when no previous build exists", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)

		require.True(t, r.isSpecDrifted(ctx, ib))
	})

	t.Run("required when previous build spec is not recorded", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = buildRunName
		r, _ := newReconciler(t, ib)

		require.True(t, r.isSpecDrifted(ctx, ib))
	})

	t.Run("not required when build inputs are unchanged", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = buildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		require.False(t, r.isSpecDrifted(ctx, ib))
	})

	fieldChangeTests := []struct {
		name   string
		mutate func(ib *buildv1alpha1.ImageBuild)
	}{
		{name: "requires new build when git URL changes", mutate: func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Source.Git.URL = "https://github.com/other/repo" }},
		{name: "requires new build when git revision changes", mutate: func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Source.Git.Revision = "develop" }},
		{name: "requires new build when output image changes", mutate: func(ib *buildv1alpha1.ImageBuild) { ib.Spec.Output.Image = "registry.example.com/other/image" }},
	}

	for _, tt := range fieldChangeTests {
		t.Run(tt.name, func(t *testing.T) {
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			ib.Status.LastBuildRunRef = buildRunName
			r, _ := newReconciler(t, ib)

			require.NoError(t, r.recordBuildSpec(ib))
			tt.mutate(ib)

			require.True(t, r.isSpecDrifted(ctx, ib))
		})
	}

	t.Run("not required when only onCommit field is added", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = buildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.OnCommit = &buildv1alpha1.ImageBuildOnCommit{
			WebhookSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "webhook-secret"},
				Key:                  "token",
			},
		}

		require.False(t, r.isSpecDrifted(ctx, ib))
	})

	t.Run("not required when only rebuild mode changes", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = buildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}

		require.False(t, r.isSpecDrifted(ctx, ib))
	})

}

func TestIsSecretRetryNeeded(t *testing.T) {
	ctx := context.Background()
	const buildRunName = "some-buildrun"

	t.Run("retries when all referenced secrets become available", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		ib.Spec.Source.Git.CloneSecret = &corev1.LocalObjectReference{Name: "clone-secret"}
		ib.Status.LastBuildRunRef = buildRunName

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBuildRun.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		pushSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: pushSecretName, Namespace: ib.Namespace},
		}
		cloneSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "clone-secret", Namespace: ib.Namespace},
		}

		r, _ := newReconciler(t, ib, failedBuildRun, pushSecret, cloneSecret)

		require.True(t, r.isSecretRetryNeeded(ctx, ib))
	})

	t.Run("does not retry when only some secrets exist", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		ib.Spec.Source.Git.CloneSecret = &corev1.LocalObjectReference{Name: "clone-secret"}
		ib.Status.LastBuildRunRef = buildRunName

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBuildRun.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		pushSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: pushSecretName, Namespace: ib.Namespace},
		}

		r, _ := newReconciler(t, ib, failedBuildRun, pushSecret)

		require.False(t, r.isSecretRetryNeeded(ctx, ib))
	})

	t.Run("does not retry when secret is still missing", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		ib.Status.LastBuildRunRef = buildRunName

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBuildRun.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		r, _ := newReconciler(t, ib, failedBuildRun)

		require.False(t, r.isSecretRetryNeeded(ctx, ib))
	})

	t.Run("does not retry for non-registration errors", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		ib.Status.LastBuildRunRef = buildRunName

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBuildRun.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: "BuildRunTimeout",
		})

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: pushSecretName, Namespace: ib.Namespace},
		}

		r, _ := newReconciler(t, ib, failedBuildRun, secret)

		require.False(t, r.isSecretRetryNeeded(ctx, ib))
	})

	t.Run("blocked after retry already attempted", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: pushSecretName}
		ib.Status.LastBuildRunRef = buildRunName
		ib.Annotations = map[string]string{
			buildv1alpha1.AnnotationKeyRetryAttempted: "true",
		}

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBuildRun.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: pushSecretName, Namespace: ib.Namespace},
		}

		r, _ := newReconciler(t, ib, failedBuildRun, secret)

		require.False(t, r.isSecretRetryNeeded(ctx, ib))
	})
}
