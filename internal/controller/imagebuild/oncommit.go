package imagebuild

import (
	"context"
	"fmt"
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
)

// reconcileOnCommitBuildRun handles the on-commit rebuild flow: debounce, duplicate
// commit detection, active-run check, and BuildRun creation.
func (r *Reconciler) reconcileOnCommitBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, *time.Duration, error) {
	if !isRebuildEnabled(ib) {
		return nil, nil, nil
	}

	if remaining := remainingWait(ib); remaining != nil {
		return nil, remaining, nil
	}

	pendingCommit := ib.Status.OnCommit.Pending.CommitSHA
	if isDuplicateCommit(ib, pendingCommit) {
		orig := ib.DeepCopy()
		ib.Status.OnCommit.Pending = nil
		if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
			return nil, nil, fmt.Errorf("failed to clear pending commit status: %w", err)
		}
		return nil, nil, nil
	}

	activeBuildRun, err := r.getActiveBuildRun(ctx, ib)
	if err != nil || activeBuildRun != nil {
		return activeBuildRun, nil, err
	}

	return r.ensureOnCommitBuildRun(ctx, ib, pendingCommit)
}

func (r *Reconciler) ensureOnCommitLabel(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	desired := "false"
	if ib.Spec.Rebuild != nil && ib.Spec.Rebuild.Mode == buildv1alpha1.ImageBuildRebuildModeOnCommit {
		desired = "true"
	}

	if ib.Labels == nil {
		ib.Labels = map[string]string{}
	}
	if ib.Labels[buildv1alpha1.LabelKeyOnCommitEnabled] == desired {
		return nil
	}

	orig := ib.DeepCopy()
	ib.Labels[buildv1alpha1.LabelKeyOnCommitEnabled] = desired

	if err := r.Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch oncommit label: %w", err)
	}

	return nil
}

func isRebuildEnabled(ib *buildv1alpha1.ImageBuild) bool {
	return ib.Spec.Rebuild != nil &&
		ib.Spec.Rebuild.Mode == buildv1alpha1.ImageBuildRebuildModeOnCommit &&
		ib.Status.OnCommit != nil &&
		ib.Status.OnCommit.Pending != nil
}

// remainingWait returns the remaining wait time if the pending commit is still
// within the debounce window or the minimum interval since the last trigger.
func remainingWait(ib *buildv1alpha1.ImageBuild) *time.Duration {
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

func isDuplicateCommit(ib *buildv1alpha1.ImageBuild, commitSHA string) bool {
	if ib.Status.OnCommit.LastTriggeredBuildRun == nil {
		return false
	}
	return ib.Status.OnCommit.LastTriggeredBuildRun.CommitSHA == commitSHA
}

func (r *Reconciler) getActiveBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, error) {
	if ib.Status.LastBuildRunRef == "" {
		return nil, nil
	}

	active := &shipwright.BuildRun{}
	key := client.ObjectKey{Namespace: ib.Namespace, Name: ib.Status.LastBuildRunRef}
	if err := r.Get(ctx, key, active); err != nil {
		if notFoundErr := client.IgnoreNotFound(err); notFoundErr != nil {
			return nil, fmt.Errorf("failed to get active BuildRun %q: %w", key.Name, notFoundErr)
		}
		return nil, nil
	}

	if metav1.IsControlledBy(active, ib) && isActiveBuildRun(active) {
		return active, nil
	}
	return nil, nil
}

// isActiveBuildRun reports whether the BuildRun is still in progress.
func isActiveBuildRun(br *shipwright.BuildRun) bool {
	cond := br.Status.GetCondition(shipwright.Succeeded)
	if cond == nil {
		return true
	}
	status := cond.GetStatus()
	return status != corev1.ConditionTrue && status != corev1.ConditionFalse
}

func (r *Reconciler) ensureOnCommitBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	commitSHA string,
) (*shipwright.BuildRun, *time.Duration, error) {
	counter := nextCounter(ib.Status.OnCommit.TriggerCounter)
	desired := newBuildRun(ib, counter)
	desired.Name = fmt.Sprintf("%s-buildrun-oncommit-%d", ib.Name, counter)
	desired.Labels[buildv1alpha1.LabelKeyRebuildMode] = string(buildv1alpha1.ImageBuildRebuildModeOnCommit)

	br, created, err := r.getOrCreateBuildRun(ctx, ib, desired)
	if err != nil {
		return nil, nil, err
	}
	if !created {
		return br, nil, nil
	}

	if err := r.patchOnCommitTriggered(ctx, ib, desired, counter, commitSHA); err != nil {
		return nil, nil, fmt.Errorf("failed to mark triggered for BuildRun %q: %w", desired.Name, err)
	}

	return br, nil, nil
}

func (r *Reconciler) patchOnCommitTriggered(ctx context.Context, ib *buildv1alpha1.ImageBuild, br *shipwright.BuildRun, triggerCounter int64, commitSHA string) error {
	orig := ib.DeepCopy()
	ib.Status.OnCommit.TriggerCounter = triggerCounter
	ib.Status.OnCommit.LastTriggeredBuildRun = &buildv1alpha1.ImageBuildOnCommitLastTriggered{
		Name:        br.Name,
		CommitSHA:   commitSHA,
		TriggeredAt: metav1.Now(),
	}
	ib.Status.OnCommit.Pending = nil

	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch on-commit triggered status: %w", err)
	}

	return nil
}
