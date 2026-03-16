package imagebuild

import (
	"context"
	"strings"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
