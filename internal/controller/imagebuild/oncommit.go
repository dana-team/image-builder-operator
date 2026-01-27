package imagebuild

import (
	"context"
	"time"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	onCommitDebounce    = 10 * time.Second
	onCommitMinInterval = 30 * time.Second
	onCommitLabelKey    = "build.dana.io/oncommit-enabled"
)

// ensureOnCommitLabel maintains the label required for the webhook handler to filter ImageBuilds.
func (r *ImageBuildReconciler) ensureOnCommitLabel(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	desired := "false"
	if ib.Spec.Rebuild != nil && ib.Spec.Rebuild.Mode == buildv1alpha1.ImageBuildRebuildModeOnCommit {
		desired = "true"
	}

	if ib.Labels == nil {
		ib.Labels = map[string]string{}
	}
	if ib.Labels[onCommitLabelKey] == desired {
		return nil
	}

	orig := ib.DeepCopy()
	ib.Labels[onCommitLabelKey] = desired
	return r.Patch(ctx, ib, client.MergeFrom(orig))
}

// triggerBuildRun enforces debounce/rate-limit/one-active-build and creates a BuildRun
// when a pending trigger is ready.
//
// Returns:
// - selected BuildRun to use for status mapping (may be an existing active run)
// - optional requeueAfter for debounce/rate-limit timers
func (r *ImageBuildReconciler) triggerBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, *time.Duration, error) {
	if ib.Spec.Rebuild == nil || ib.Spec.Rebuild.Mode != buildv1alpha1.ImageBuildRebuildModeOnCommit {
		return nil, nil, nil
	}

	if ib.Status.OnCommit == nil || ib.Status.OnCommit.Pending == nil {
		return nil, nil, nil
	}

	if requeueAfter := requeueAfter(ib); requeueAfter != nil {
		return nil, requeueAfter, nil
	}

	pendingCommit := ib.Status.OnCommit.Pending.CommitSHA
	if ib.Status.OnCommit.LastTriggeredBuildRun != nil &&
		ib.Status.OnCommit.LastTriggeredBuildRun.CommitSHA == pendingCommit {
		orig := ib.DeepCopy()
		ib.Status.OnCommit.Pending = nil
		_ = r.Status().Patch(ctx, ib, client.MergeFrom(orig))
		return nil, nil, nil
	}

	if ib.Status.LastBuildRunRef != "" {
		active := &shipwright.BuildRun{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ib.Namespace, Name: ib.Status.LastBuildRunRef}, active); err == nil {
			if metav1.IsControlledBy(active, ib) {
				cond := active.Status.GetCondition(shipwright.Succeeded)
				if cond == nil || (cond.GetStatus() != corev1.ConditionTrue && cond.GetStatus() != corev1.ConditionFalse) {
					return active, nil, nil
				}
			}
		} else if client.IgnoreNotFound(err) != nil {
			return nil, nil, err
		}
	}

	counter := nextTrigger(ib)
	br, err := r.ensureBuildRunOnCommit(ctx, ib, counter)
	if err != nil {
		return nil, nil, err
	}

	if err := r.markTriggered(ctx, ib, br, counter, pendingCommit); err != nil {
		return nil, nil, err
	}

	return br, nil, nil
}

func requeueAfter(ib *buildv1alpha1.ImageBuild) *time.Duration {
	receivedAt := ib.Status.OnCommit.Pending.ReceivedAt.Time
	if !receivedAt.IsZero() {
		if remaining := time.Until(receivedAt.Add(onCommitDebounce)); remaining > 0 {
			return &remaining
		}
	}

	if ib.Status.OnCommit.LastTriggeredBuildRun != nil && !ib.Status.OnCommit.LastTriggeredBuildRun.TriggeredAt.IsZero() {
		last := ib.Status.OnCommit.LastTriggeredBuildRun.TriggeredAt.Time
		if remaining := time.Until(last.Add(onCommitMinInterval)); remaining > 0 {
			return &remaining
		}
	}

	return nil
}

func nextTrigger(ib *buildv1alpha1.ImageBuild) int64 {
	counter := ib.Status.OnCommit.TriggerCounter
	if counter < 0 {
		counter = 0
	}
	return counter + 1
}

func (r *ImageBuildReconciler) markTriggered(ctx context.Context, ib *buildv1alpha1.ImageBuild, br *shipwright.BuildRun, triggerCounter int64, commitSHA string) error {
	orig := ib.DeepCopy()
	ib.Status.OnCommit.TriggerCounter = triggerCounter
	ib.Status.OnCommit.LastTriggeredBuildRun = &buildv1alpha1.ImageBuildOnCommitLastTriggered{
		Name:        br.Name,
		CommitSHA:   commitSHA,
		TriggeredAt: metav1.Now(),
	}
	ib.Status.OnCommit.Pending = nil
	return r.Status().Patch(ctx, ib, client.MergeFrom(orig))
}
