package imagebuild

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
	testCommitSHA      = "abc123"
	differentCommitSHA = "xyz789"
)

func TestReconcileRebuild(t *testing.T) {
	ctx := context.Background()
	imageBuildName := "ib"
	imageBuildNamespace := "ns"
	refName := "refs/heads/main"
	expectedOnCommitBuildRunName := fmt.Sprintf("%s-buildrun-oncommit-1", imageBuildName)
	failedBuildRunName := "failed-br"

	t.Run("creates BuildRun with oncommit naming", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.NotNil(t, br)
		require.Equal(t, expectedOnCommitBuildRunName, br.Name)
		require.Equal(t, "oncommit", br.Labels["build.dana.io/build-trigger"])
	})

	t.Run("returns active BuildRun when present", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		activeBuildRunName := "active-br"
		ib.Status.LastBuildRunRef = activeBuildRunName
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		activeBR := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: activeBuildRunName, Namespace: ib.Namespace},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, activeBR, testScheme(t)))

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib, activeBR)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.NotNil(t, br, "should return the active BuildRun for status mapping")
		require.Equal(t, activeBR.Name, br.Name)
	})

	t.Run("clears pending when commit already triggered", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
			LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{
				Name:      expectedOnCommitBuildRunName,
				CommitSHA: testCommitSHA,
			},
		}

		policy := newImageBuildPolicy()
		r, c := newReconciler(t, policy, ib)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.Nil(t, br)
		require.Nil(t, ib.Status.OnCommit.Pending)

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.NotNil(t, latest.Status.OnCommit)
		require.Nil(t, latest.Status.OnCommit.Pending)
	})

	t.Run("requeues for debounce when event recently received", func(t *testing.T) {
		now := time.Now()
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{
				Ref:        refName,
				CommitSHA:  testCommitSHA,
				ReceivedAt: metav1.NewTime(now),
			},
		}

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.NotNil(t, requeue, "should requeue for debounce")
		require.Nil(t, br)
		require.True(t, *requeue > 0)
	})

	t.Run("requeues for on-commit min interval", func(t *testing.T) {
		now := time.Now()
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
			LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{
				Name:        expectedOnCommitBuildRunName,
				CommitSHA:   testCommitSHA,
				TriggeredAt: metav1.NewTime(now),
			},
		}

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.NotNil(t, requeue)
		require.Nil(t, br)
		require.True(t, *requeue > 0)
	})

	t.Run("creates new BuildRun when last BuildRun succeeded", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		doneBuildRunName := "done-br"
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.LastBuildRunRef = doneBuildRunName
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		doneBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: doneBuildRunName, Namespace: ib.Namespace},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, doneBuildRun, testScheme(t)))
		doneBuildRun.Status.Conditions = append(doneBuildRun.Status.Conditions, shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionTrue,
		})

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib, doneBuildRun)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.NotNil(t, br)
		require.Equal(t, fmt.Sprintf("%s-buildrun-oncommit-1", ib.Name), br.Name)
	})

	t.Run("creates new BuildRun when last BuildRun failed", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.LastBuildRunRef = failedBuildRunName
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		failedBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: failedBuildRunName, Namespace: ib.Namespace},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, failedBuildRun, testScheme(t)))
		failedBuildRun.Status.Conditions = append(failedBuildRun.Status.Conditions, shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
		})

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib, failedBuildRun)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.NotNil(t, br)
		require.Equal(t, fmt.Sprintf("%s-buildrun-oncommit-1", ib.Name), br.Name)
	})

	t.Run("returns error when last BuildRun fetch fails", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.LastBuildRunRef = "missing-br"
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib)
		r.Client = &getErrorClient{Client: r.Client, err: errors.New("boom")}

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.Error(t, err)
		require.Nil(t, requeue)
		require.Nil(t, br)
	})

	t.Run("returns conflict when BuildRun owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}
		counter := int64(1)

		conflict := newBuildRun(ib, 0)
		conflict.Name = fmt.Sprintf("%s-buildrun-oncommit-%d", ib.Name, counter)

		otherOwner := &buildv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "someone-else",
				Namespace: ib.Namespace,
				UID:       types.UID("other-uid"),
			},
		}
		require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflict, testScheme(t)))

		r, _ := newReconciler(t, ib, conflict)
		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.Nil(t, br)
		require.Nil(t, requeue)
		require.Error(t, err)

		var alreadyOwned *controllerutil.AlreadyOwnedError
		require.ErrorAs(t, err, &alreadyOwned, "Should return AlreadyOwnedError when BuildRun has different owner")
	})
}

func TestIsRebuildEnabled(t *testing.T) {
	t.Run("returns false when rebuild nil", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{}
		require.False(t, isRebuildEnabled(ib))
	})

	t.Run("returns false when mode not oncommit", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Spec: buildv1alpha1.ImageBuildSpec{
				Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: "manual"},
			},
		}
		require.False(t, isRebuildEnabled(ib))
	})

	t.Run("returns false when no pending commit", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Spec: buildv1alpha1.ImageBuildSpec{
				Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
			},
			Status: buildv1alpha1.ImageBuildStatus{
				OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{},
			},
		}
		require.False(t, isRebuildEnabled(ib))
	})

	t.Run("returns true when enabled and pending", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Spec: buildv1alpha1.ImageBuildSpec{
				Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
			},
			Status: buildv1alpha1.ImageBuildStatus{
				OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
					Pending: &buildv1alpha1.ImageBuildOnCommitEvent{CommitSHA: testCommitSHA},
				},
			},
		}
		require.True(t, isRebuildEnabled(ib))
	})
}

func TestIsDuplicateCommit(t *testing.T) {
	t.Run("returns false when no last triggered", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Status: buildv1alpha1.ImageBuildStatus{
				OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{},
			},
		}
		require.False(t, isDuplicateCommit(ib, testCommitSHA))
	})

	t.Run("returns false when different commit", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Status: buildv1alpha1.ImageBuildStatus{
				OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
					LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{CommitSHA: differentCommitSHA},
				},
			},
		}
		require.False(t, isDuplicateCommit(ib, testCommitSHA))
	})

	t.Run("returns true when same commit", func(t *testing.T) {
		ib := &buildv1alpha1.ImageBuild{
			Status: buildv1alpha1.ImageBuildStatus{
				OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
					LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{CommitSHA: testCommitSHA},
				},
			},
		}
		require.True(t, isDuplicateCommit(ib, testCommitSHA))
	})
}

func TestIsActiveBuildRun(t *testing.T) {
	t.Run("returns true when condition nil", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		require.True(t, isActiveBuildRun(br))
	})

	t.Run("returns false when succeeded", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionTrue,
		})
		require.False(t, isActiveBuildRun(br))
	})

	t.Run("returns false when failed", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionFalse,
		})
		require.False(t, isActiveBuildRun(br))
	})

	t.Run("returns true when running", func(t *testing.T) {
		br := &shipwright.BuildRun{}
		br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{
			Type:   shipwright.Succeeded,
			Status: corev1.ConditionUnknown,
		})
		require.True(t, isActiveBuildRun(br))
	})
}

func TestClearPendingCommit(t *testing.T) {
	ctx := context.Background()
	imageBuildName := "ib"
	imageBuildNamespace := "ns"

	ib := newImageBuild(imageBuildName, imageBuildNamespace)
	ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
		Pending: &buildv1alpha1.ImageBuildOnCommitEvent{CommitSHA: testCommitSHA},
	}

	r, c := newReconciler(t, ib)
	require.NoError(t, r.clearPendingCommit(ctx, ib))

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	require.Nil(t, latest.Status.OnCommit.Pending)
}

func TestEnsureOnCommitLabel(t *testing.T) {
	ctx := context.Background()
	imageBuildName := "ib"
	imageBuildNamespace := "ns"

	t.Run("sets label when on-commit rebuild enabled", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}

		r, c := newReconciler(t, ib)

		require.NoError(t, r.ensureOnCommitLabel(ctx, ib))
		require.NotNil(t, ib.Labels)
		require.Equal(t, "true", ib.Labels[onCommitLabelKey])

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.NotNil(t, latest.Labels)
		require.Equal(t, "true", latest.Labels[onCommitLabelKey])
	})

	t.Run("clears label when on-commit rebuild disabled", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Labels = map[string]string{onCommitLabelKey: "true"}

		r, c := newReconciler(t, ib)

		require.NoError(t, r.ensureOnCommitLabel(ctx, ib))
		require.NotNil(t, ib.Labels)
		require.Equal(t, "false", ib.Labels[onCommitLabelKey])

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.NotNil(t, latest.Labels)
		require.Equal(t, "false", latest.Labels[onCommitLabelKey])
	})
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
