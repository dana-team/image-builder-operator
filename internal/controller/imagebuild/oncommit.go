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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	onCommitDebounce    = 10 * time.Second
	onCommitMinInterval = 30 * time.Second
	onCommitLabelKey    = "build.dana.io/oncommit-enabled"
)

func (r *Reconciler) reconcileRebuild(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, *time.Duration, error) {
	if !isRebuildEnabled(ib) {
		return nil, nil, nil
	}

	if requeueAfter := requeueAfter(ib); requeueAfter != nil {
		return nil, requeueAfter, nil
	}

	pendingCommit := ib.Status.OnCommit.Pending.CommitSHA
	if isDuplicateCommit(ib, pendingCommit) {
		if err := r.clearPendingCommit(ctx, ib); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}

	activeBR, err := r.getActiveBuildRun(ctx, ib)
	if err != nil || activeBR != nil {
		return activeBR, nil, err
	}

	return r.createBuildRun(ctx, ib, pendingCommit)
}

func (r *Reconciler) ensureOnCommitLabel(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
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

func isRebuildEnabled(ib *buildv1alpha1.ImageBuild) bool {
	return ib.Spec.Rebuild != nil &&
		ib.Spec.Rebuild.Mode == buildv1alpha1.ImageBuildRebuildModeOnCommit &&
		ib.Status.OnCommit != nil &&
		ib.Status.OnCommit.Pending != nil
}

func isDuplicateCommit(ib *buildv1alpha1.ImageBuild, commitSHA string) bool {
	if ib.Status.OnCommit.LastTriggeredBuildRun == nil {
		return false
	}
	return ib.Status.OnCommit.LastTriggeredBuildRun.CommitSHA == commitSHA
}

func (r *Reconciler) clearPendingCommit(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	orig := ib.DeepCopy()
	ib.Status.OnCommit.Pending = nil
	return r.Status().Patch(ctx, ib, client.MergeFrom(orig))
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
		return nil, client.IgnoreNotFound(err)
	}

	if metav1.IsControlledBy(active, ib) && isActiveBuildRun(active) {
		return active, nil
	}
	return nil, nil
}

func isActiveBuildRun(br *shipwright.BuildRun) bool {
	cond := br.Status.GetCondition(shipwright.Succeeded)
	if cond == nil {
		return true
	}
	status := cond.GetStatus()
	return status != corev1.ConditionTrue && status != corev1.ConditionFalse
}

func (r *Reconciler) createBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	commitSHA string,
) (*shipwright.BuildRun, *time.Duration, error) {
	counter := nextTrigger(ib)
	br := newBuildRun(ib, counter)
	br.Name = fmt.Sprintf("%s-buildrun-oncommit-%d", ib.Name, counter)
	br.Labels["build.dana.io/build-trigger"] = "oncommit"

	existing := &shipwright.BuildRun{}
	key := client.ObjectKeyFromObject(br)
	if err := r.Get(ctx, key, existing); err == nil {
		if !metav1.IsControlledBy(existing, ib) {
			return nil, nil, &controllerutil.AlreadyOwnedError{Object: existing}
		}
		return existing, nil, nil
	} else if client.IgnoreNotFound(err) != nil {
		return nil, nil, err
	}

	if err := controllerutil.SetControllerReference(ib, br, r.Scheme); err != nil {
		return nil, nil, err
	}
	if err := r.Create(ctx, br); err != nil {
		return nil, nil, err
	}
	if err := r.markTriggered(ctx, ib, br, counter, commitSHA); err != nil {
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

func (r *Reconciler) markTriggered(ctx context.Context, ib *buildv1alpha1.ImageBuild, br *shipwright.BuildRun, triggerCounter int64, commitSHA string) error {
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
