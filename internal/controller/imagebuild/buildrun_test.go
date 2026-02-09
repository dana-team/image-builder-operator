package imagebuild

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	shipwrightresources "github.com/shipwright-io/build/pkg/reconciler/buildrun/resources"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestReconcileBuildRun(t *testing.T) {
	ctx := context.Background()

	t.Run("creates BuildRun when it does not exist", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, c := newReconciler(t, ib)

		br, err := r.reconcileBuildRun(ctx, ib)
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
		br, err := r.reconcileBuildRun(ctx, ib)
		require.NoError(t, err)
		require.Equal(t, existingBuildRun.Name, br.Name)
		require.Equal(t, existingBuildRun.UID, br.UID, "expected reconcileBuildRun to return the existing BuildRun object")
	})

	t.Run("fails when BuildRun owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.BuildRunCounter = 0
		conflict := newBuildRun(ib, 1)

		otherOwner := &buildv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: "someone-else",
				UID:  types.UID("other-uid"),
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflict, newScheme(t)))

		r, _ := newReconciler(t, ib, conflict)
		br, err := r.reconcileBuildRun(ctx, ib)
		require.Nil(t, br)
		require.Error(t, err)

		var alreadyOwned *controllerutil.AlreadyOwnedError
		require.ErrorAs(t, err, &alreadyOwned, "Should return AlreadyOwnedError when BuildRun has different owner")
	})

	t.Run("returns error when Get fails", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)
		r.Client = &getErrorClient{Client: r.Client, err: errFake}

		br, err := r.reconcileBuildRun(ctx, ib)
		require.Nil(t, br)
		require.Error(t, err)
	})
}

func TestPatchBuildSucceededCondition(t *testing.T) {
	ctx := context.Background()

	t.Run("Succeeded condition missing => Pending/Unknown", func(t *testing.T) {
		br := newTestBuildRun(t)
		requireBuildSucceeded(t, ctx, br, metav1.ConditionUnknown, ReasonBuildRunPending)
	})

	t.Run("Succeeded=True => Succeeded/True", func(t *testing.T) {
		br := newTestBuildRun(t)
		br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionTrue})
		requireBuildSucceeded(t, ctx, br, metav1.ConditionTrue, ReasonBuildRunSucceeded)
	})

	t.Run("Succeeded=False => Failed/False", func(t *testing.T) {
		br := newTestBuildRun(t)
		br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionFalse})
		requireBuildSucceeded(t, ctx, br, metav1.ConditionFalse, ReasonBuildRunFailed)
	})

	t.Run("Succeeded=Unknown => Running/Unknown", func(t *testing.T) {
		br := newTestBuildRun(t)
		br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionUnknown})
		requireBuildSucceeded(t, ctx, br, metav1.ConditionUnknown, ReasonBuildRunRunning)
	})
}

func TestDeriveBuildSucceededStatus(t *testing.T) {
	t.Run("Succeeded=False => includes BuildRun message", func(t *testing.T) {
		br := newTestBuildRun(t)
		br.Status.SetCondition(&shipwright.Condition{
			Type:    shipwright.Succeeded,
			Status:  corev1.ConditionFalse,
			Message: "step failed",
		})

		status, reason, message := deriveBuildSucceededStatus(br)

		require.Equal(t, metav1.ConditionFalse, status)
		require.Equal(t, ReasonBuildRunFailed, reason)
		require.True(t, strings.HasPrefix(message, "BuildRun failed"), "expected message to start with failure prefix")
		require.Contains(t, message, "step failed")
	})

	t.Run("Succeeded=Unknown => includes BuildRun message", func(t *testing.T) {
		br := newTestBuildRun(t)
		br.Status.SetCondition(&shipwright.Condition{
			Type:    shipwright.Succeeded,
			Status:  corev1.ConditionUnknown,
			Message: "waiting for pod",
		})

		status, reason, message := deriveBuildSucceededStatus(br)

		require.Equal(t, metav1.ConditionUnknown, status)
		require.Equal(t, ReasonBuildRunRunning, reason)
		require.True(t, strings.HasPrefix(message, "BuildRun is running"), "expected message to start with running prefix")
		require.Contains(t, message, "waiting for pod")
	})
}

func TestComputeLatestImage(t *testing.T) {
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	t.Run("digest present => image@digest", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		br.Status.Output = &shipwright.Output{Digest: "sha256:abc"}
		require.Equal(t, ib.Spec.Output.Image+"@sha256:abc", computeLatestImage(ib, br))
	})

	t.Run("no digest, image already tagged => keep spec.output.image", func(t *testing.T) {
		ibCopy := ib.DeepCopy()
		ibCopy.Spec.Output.Image = "registry.example.com/team/app:v1"
		br := &shipwright.BuildRun{}
		require.Equal(t, "registry.example.com/team/app:v1", computeLatestImage(ibCopy, br))
	})

	t.Run("repo-only image => no-op (empty)", func(t *testing.T) {
		ibCopy := ib.DeepCopy()
		ibCopy.Spec.Output.Image = "registry.example.com/team/app"
		br := &shipwright.BuildRun{}
		require.Equal(t, "", computeLatestImage(ibCopy, br))
	})
}

func TestHasTagOrDigest(t *testing.T) {
	t.Run("invalid image name => false", func(t *testing.T) {
		require.False(t, hasTagOrDigest("not a valid image@@@"))
	})

	t.Run("tagged image => true", func(t *testing.T) {
		require.True(t, hasTagOrDigest("registry.example.com/team/app:v1"))
	})

	t.Run("digest image => true", func(t *testing.T) {
		digest := sha256.Sum256([]byte("digest"))
		require.True(t, hasTagOrDigest("registry.example.com/team/app@sha256:"+fmt.Sprintf("%x", digest)))
	})

	t.Run("name-only image => false", func(t *testing.T) {
		require.False(t, hasTagOrDigest("registry.example.com/team/app"))
	})
}

func TestPatchLatestImage(t *testing.T) {
	ctx := context.Background()

	t.Run("empty latest image => no-op", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.patchLatestImage(ctx, ib, ""))

		require.Empty(t, ib.Status.LatestImage)
		require.Zero(t, ib.Status.ObservedGeneration)
	})

	t.Run("latest image set => patch status", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)
		latest := "registry.example.com/team/app@sha256:abc"

		require.NoError(t, r.patchLatestImage(ctx, ib, latest))

		require.Equal(t, latest, ib.Status.LatestImage)
		require.Equal(t, ib.Generation, ib.Status.ObservedGeneration)
	})
}

func TestIsNewBuildRequired(t *testing.T) {
	ctx := context.Background()
	const testBuildRunName = "some-buildrun"
	const testSecretName = "push-secret"

	t.Run("no previous build => required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("no annotation => required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("build inputs unchanged => not required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("git URL changed => required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Source.Git.URL = "https://github.com/other/repo"

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("git revision changed => required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Source.Git.Revision = "develop"

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("output image changed => required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Output.Image = "registry.example.com/other/image"

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("onCommit field added => not required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.OnCommit = &buildv1alpha1.ImageBuildOnCommit{
			WebhookSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "webhook-secret"},
				Key:                  "token",
			},
		}

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("rebuild mode changed => not required", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("secret now available => retry", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: testSecretName}
		ib.Status.LastBuildRunRef = testBuildRunName

		failedBR := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testBuildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBR.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testSecretName,
				Namespace: ib.Namespace,
			},
		}

		r, _ := newReconciler(t, ib, failedBR, secret)
		require.NoError(t, r.recordBuildSpec(ib))

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("secret still missing => no retry", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: testSecretName}
		ib.Status.LastBuildRunRef = testBuildRunName

		failedBR := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testBuildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBR.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: shipwrightresources.ConditionBuildRegistrationFailed,
		})

		r, _ := newReconciler(t, ib, failedBR)
		require.NoError(t, r.recordBuildSpec(ib))

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("other error => no retry", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName

		failedBR := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testBuildRunName,
				Namespace: ib.Namespace,
			},
		}
		failedBR.Status.SetCondition(&shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
			Reason: "BuildRunTimeout",
		})

		r, _ := newReconciler(t, ib, failedBR)
		require.NoError(t, r.recordBuildSpec(ib))

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})
}

func newTestBuildRun(t *testing.T) *shipwright.BuildRun {
	t.Helper()
	return &shipwright.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "br",
			Namespace: "ns-" + t.Name(),
		},
	}
}

func requireBuildSucceeded(t *testing.T, ctx context.Context, br *shipwright.BuildRun, expectedStatus metav1.ConditionStatus, expectedReason string) {
	t.Helper()

	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
	r, _ := newReconciler(t, ib)

	require.NoError(t, r.patchBuildSucceededCondition(ctx, ib, br))

	requireCondition(t, ib.Status.Conditions, TypeBuildSucceeded, expectedStatus, expectedReason)
	buildSucceededCond := meta.FindStatusCondition(ib.Status.Conditions, TypeBuildSucceeded)
	require.Equal(t, ib.Generation, buildSucceededCond.ObservedGeneration)
}
