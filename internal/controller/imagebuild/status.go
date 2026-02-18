package imagebuild

import (
	"context"
	"fmt"
	"strings"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *Reconciler) setNotReady(ctx context.Context, ib *buildv1alpha1.ImageBuild, reason, message string) {
	if err := r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, reason, message); err != nil {
		log.FromContext(ctx).Error(err, "failed to patch Ready condition")
	}
}

func (r *Reconciler) patchReadyCondition(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	orig := ib.DeepCopy()

	ib.Status.ObservedGeneration = ib.Generation

	meta.SetStatusCondition(&ib.Status.Conditions, metav1.Condition{
		Type:               TypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ib.Generation,
	})

	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch Ready condition status: %w", err)
	}

	return nil
}

func (r *Reconciler) patchBuildRef(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	buildRef := buildNameFor(ib)
	if ib.Status.BuildRef == buildRef && ib.Status.ObservedGeneration == ib.Generation {
		return nil
	}

	orig := ib.DeepCopy()
	ib.Status.ObservedGeneration = ib.Generation
	ib.Status.BuildRef = buildRef

	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch ImageBuild status: %w", err)
	}

	return nil
}

func (r *Reconciler) patchBuildSucceededCondition(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	br *shipwright.BuildRun,
) error {
	orig := ib.DeepCopy()

	ib.Status.ObservedGeneration = ib.Generation
	ib.Status.LastBuildRunRef = br.Name

	status, reason, message := deriveBuildSucceededStatus(br)
	meta.SetStatusCondition(&ib.Status.Conditions, metav1.Condition{
		Type:               TypeBuildSucceeded,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ib.Generation,
	})

	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch BuildSucceeded condition status: %w", err)
	}

	return nil
}

func (r *Reconciler) patchLatestImage(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	latestImage string,
) error {
	if latestImage == "" {
		return nil
	}

	orig := ib.DeepCopy()
	ib.Status.ObservedGeneration = ib.Generation
	ib.Status.LatestImage = latestImage

	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to patch latest image status: %w", err)
	}

	return nil
}

// deriveBuildSucceededStatus maps the Shipwright BuildRun's Succeeded condition
// to an ImageBuild-level (status, reason, message) tuple.
func deriveBuildSucceededStatus(br *shipwright.BuildRun) (metav1.ConditionStatus, string, string) {
	succeededCondition := br.Status.GetCondition(shipwright.Succeeded)
	if succeededCondition == nil {
		return metav1.ConditionUnknown, ReasonBuildRunPending, "BuildRun has not reported status yet"
	}

	switch succeededCondition.GetStatus() {
	case corev1.ConditionTrue:
		return metav1.ConditionTrue, ReasonBuildRunSucceeded, "BuildRun succeeded"
	case corev1.ConditionFalse:
		return metav1.ConditionFalse, ReasonBuildRunFailed, deriveConditionMessage("BuildRun failed", succeededCondition)
	default:
		return metav1.ConditionUnknown, ReasonBuildRunRunning, deriveConditionMessage("BuildRun is running", succeededCondition)
	}
}

func deriveConditionMessage(prefix string, cond *shipwright.Condition) string {
	if msg := strings.TrimSpace(cond.GetMessage()); msg != "" {
		return prefix + ": " + msg
	}
	return prefix
}
