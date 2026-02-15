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

		otherOwner := &buildv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: "someone-else",
				UID:  types.UID("other-uid"),
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflict, newScheme(t)))

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

func TestPatchBuildSucceededCondition(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		conditionSetup func(br *shipwright.BuildRun)
		expectedStatus metav1.ConditionStatus
		expectedReason string
	}{
		{
			name:           "reports pending when build run has no status",
			conditionSetup: nil,
			expectedStatus: metav1.ConditionUnknown,
			expectedReason: ReasonBuildRunPending,
		},
		{
			name: "reports succeeded when build run completes",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionTrue})
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: ReasonBuildRunSucceeded,
		},
		{
			name: "reports failed when build run fails",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionFalse})
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: ReasonBuildRunFailed,
		},
		{
			name: "reports running when build run is in progress",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.SetCondition(&shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionUnknown})
			},
			expectedStatus: metav1.ConditionUnknown,
			expectedReason: ReasonBuildRunRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := newTestBuildRun(t)
			if tt.conditionSetup != nil {
				tt.conditionSetup(br)
			}
			requireBuildSucceeded(t, ctx, br, tt.expectedStatus, tt.expectedReason)
		})
	}
}

func TestDeriveBuildSucceededStatus(t *testing.T) {
	t.Run("includes failure details when build run fails", func(t *testing.T) {
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

	t.Run("includes status details when build run is in progress", func(t *testing.T) {
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
		require.Equal(t, "", computeLatestImage(ibCopy, br))
	})
}

func TestHasTagOrDigest(t *testing.T) {
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
			require.Equal(t, tt.expected, hasTagOrDigest(tt.image))
		})
	}
}

func TestPatchLatestImage(t *testing.T) {
	ctx := context.Background()

	t.Run("skips patch when latest image is empty", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.patchLatestImage(ctx, ib, ""))

		require.Empty(t, ib.Status.LatestImage)
		require.Zero(t, ib.Status.ObservedGeneration)
	})

	t.Run("patches status when latest image is set", func(t *testing.T) {
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

	t.Run("required when no previous build exists", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		r, _ := newReconciler(t, ib)

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("required when previous build spec is not recorded", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.True(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("not required when build inputs are unchanged", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		require.False(t, r.isNewBuildRequired(ctx, ib))
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
			ib.Status.LastBuildRunRef = testBuildRunName
			r, _ := newReconciler(t, ib)

			require.NoError(t, r.recordBuildSpec(ib))
			tt.mutate(ib)

			require.True(t, r.isNewBuildRequired(ctx, ib))
		})
	}

	t.Run("not required when only onCommit field is added", func(t *testing.T) {
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

	t.Run("not required when only rebuild mode changes", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = testBuildRunName
		r, _ := newReconciler(t, ib)

		require.NoError(t, r.recordBuildSpec(ib))

		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}

		require.False(t, r.isNewBuildRequired(ctx, ib))
	})

	t.Run("retries when previously missing secret becomes available", func(t *testing.T) {
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

	t.Run("does not retry when secret is still missing", func(t *testing.T) {
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

	t.Run("does not retry for non-registration errors", func(t *testing.T) {
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
