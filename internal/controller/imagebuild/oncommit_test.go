package imagebuild

import (
	"context"
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
		require.Equal(t, "oncommit", br.Labels[labelKeyBuildTrigger])
	})

	t.Run("returns active BuildRun when present", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
		activeBuildRunName := "active-br"
		ib.Status.LastBuildRunRef = activeBuildRunName
		ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
			Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: refName, CommitSHA: testCommitSHA},
		}

		activeBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: activeBuildRunName, Namespace: ib.Namespace},
		}
		require.NoError(t, controllerutil.SetControllerReference(ib, activeBuildRun, newScheme(t)))

		policy := newImageBuildPolicy()
		r, _ := newReconciler(t, policy, ib, activeBuildRun)

		br, requeue, err := r.reconcileRebuild(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, requeue)
		require.NotNil(t, br, "should return the active BuildRun for status mapping")
		require.Equal(t, activeBuildRun.Name, br.Name)
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
		require.NoError(t, controllerutil.SetControllerReference(ib, doneBuildRun, newScheme(t)))
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
		require.NoError(t, controllerutil.SetControllerReference(ib, failedBuildRun, newScheme(t)))
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
		r.Client = &getErrorClient{Client: r.Client, err: errFake}

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
		require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflict, newScheme(t)))

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
	tests := []struct {
		name     string
		ib       *buildv1alpha1.ImageBuild
		expected bool
	}{
		{
			name:     "disabled when rebuild not configured",
			ib:       &buildv1alpha1.ImageBuild{},
			expected: false,
		},
		{
			name: "disabled when mode is not oncommit",
			ib: &buildv1alpha1.ImageBuild{
				Spec: buildv1alpha1.ImageBuildSpec{
					Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: "manual"},
				},
			},
			expected: false,
		},
		{
			name: "disabled when no commit is pending",
			ib: &buildv1alpha1.ImageBuild{
				Spec: buildv1alpha1.ImageBuildSpec{
					Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
				},
				Status: buildv1alpha1.ImageBuildStatus{
					OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{},
				},
			},
			expected: false,
		},
		{
			name: "enabled when oncommit mode set and commit pending",
			ib: &buildv1alpha1.ImageBuild{
				Spec: buildv1alpha1.ImageBuildSpec{
					Rebuild: &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit},
				},
				Status: buildv1alpha1.ImageBuildStatus{
					OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
						Pending: &buildv1alpha1.ImageBuildOnCommitEvent{CommitSHA: testCommitSHA},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, isRebuildEnabled(tt.ib))
		})
	}
}

func TestIsDuplicateCommit(t *testing.T) {
	tests := []struct {
		name     string
		ib       *buildv1alpha1.ImageBuild
		expected bool
	}{
		{
			name: "not duplicate when no previous build was triggered",
			ib: &buildv1alpha1.ImageBuild{
				Status: buildv1alpha1.ImageBuildStatus{
					OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{},
				},
			},
			expected: false,
		},
		{
			name: "not duplicate when commit SHA differs",
			ib: &buildv1alpha1.ImageBuild{
				Status: buildv1alpha1.ImageBuildStatus{
					OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
						LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{CommitSHA: differentCommitSHA},
					},
				},
			},
			expected: false,
		},
		{
			name: "duplicate when commit SHA matches",
			ib: &buildv1alpha1.ImageBuild{
				Status: buildv1alpha1.ImageBuildStatus{
					OnCommit: &buildv1alpha1.ImageBuildOnCommitStatus{
						LastTriggeredBuildRun: &buildv1alpha1.ImageBuildOnCommitLastTriggered{CommitSHA: testCommitSHA},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, isDuplicateCommit(tt.ib, testCommitSHA))
		})
	}
}

func TestIsActiveBuildRun(t *testing.T) {
	tests := []struct {
		name           string
		conditionSetup func(br *shipwright.BuildRun)
		expected       bool
	}{
		{
			name:           "active when build run has no status yet",
			conditionSetup: nil,
			expected:       true,
		},
		{
			name: "inactive after build run succeeds",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionTrue})
			},
			expected: false,
		},
		{
			name: "inactive after build run fails",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionFalse})
			},
			expected: false,
		},
		{
			name: "active while build run is in progress",
			conditionSetup: func(br *shipwright.BuildRun) {
				br.Status.Conditions = append(br.Status.Conditions, shipwright.Condition{Type: shipwright.Succeeded, Status: corev1.ConditionUnknown})
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := &shipwright.BuildRun{}
			if tt.conditionSetup != nil {
				tt.conditionSetup(br)
			}
			require.Equal(t, tt.expected, isActiveBuildRun(br))
		})
	}
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
		require.Equal(t, "true", ib.Labels[labelKeyOnCommitEnabled])

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.NotNil(t, latest.Labels)
		require.Equal(t, "true", latest.Labels[labelKeyOnCommitEnabled])
	})

	t.Run("clears label when on-commit rebuild disabled", func(t *testing.T) {
		ib := newImageBuild(imageBuildName, imageBuildNamespace)
		ib.Labels = map[string]string{labelKeyOnCommitEnabled: "true"}

		r, c := newReconciler(t, ib)

		require.NoError(t, r.ensureOnCommitLabel(ctx, ib))
		require.NotNil(t, ib.Labels)
		require.Equal(t, "false", ib.Labels[labelKeyOnCommitEnabled])

		latest := &buildv1alpha1.ImageBuild{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
		require.NotNil(t, latest.Labels)
		require.Equal(t, "false", latest.Labels[labelKeyOnCommitEnabled])
	})
}
