package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	distref "github.com/distribution/reference"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
)

const annotationKeyLastBuildSpec = "build.dana.io/last-build-spec"

// buildInputs captures fields that trigger a new build when changed.
type buildInputs struct {
	Source    buildv1alpha1.ImageBuildSource `json:"source"`
	BuildFile buildv1alpha1.ImageBuildFile   `json:"buildFile"`
	Output    buildv1alpha1.ImageBuildOutput `json:"output"`
}

func buildRunNameFor(ib *buildv1alpha1.ImageBuild, counter int64) string {
	return fmt.Sprintf("%s-buildrun-%d", ib.Name, counter)
}

func newBuildRun(ib *buildv1alpha1.ImageBuild, counter int64) *shipwright.BuildRun {
	buildName := buildNameFor(ib)

	return &shipwright.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRunNameFor(ib, counter),
			Namespace: ib.Namespace,
			Labels: map[string]string{
				"build.dana.io/parent-imagebuild": ib.Name,
			},
		},
		Spec: shipwright.BuildRunSpec{
			Build: shipwright.ReferencedBuild{
				Name: &buildName,
			},
		},
	}
}

func (r *ImageBuildReconciler) reconcileBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, error) {
	counter := nextBuildRunCounter(ib)
	desired := newBuildRun(ib, counter)

	existing := &shipwright.BuildRun{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, existing); err == nil {
		if !metav1.IsControlledBy(existing, ib) {
			return nil, &controllerutil.AlreadyOwnedError{Object: existing}
		}
		return existing, nil
	} else if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	if err := controllerutil.SetControllerReference(ib, desired, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, desired); err != nil {
		return nil, err
	}

	orig := ib.DeepCopy()
	ib.Status.BuildRunCounter = counter
	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return nil, err
	}

	return desired, nil
}

func nextBuildRunCounter(ib *buildv1alpha1.ImageBuild) int64 {
	counter := ib.Status.BuildRunCounter
	if counter < 0 {
		counter = 0
	}
	return counter + 1
}

func deriveBuildSucceededStatus(br *shipwright.BuildRun) (metav1.ConditionStatus, string, string) {
	succeededCondition := br.Status.GetCondition(shipwright.Succeeded)
	if succeededCondition == nil {
		return metav1.ConditionUnknown, ReasonBuildRunPending, "BuildRun has not reported status yet"
	}

	switch succeededCondition.GetStatus() {
	case corev1.ConditionTrue:
		return metav1.ConditionTrue, ReasonBuildRunSucceeded, "BuildRun succeeded"
	case corev1.ConditionFalse:
		msg := "BuildRun failed"
		if buildRunMessage := strings.TrimSpace(succeededCondition.GetMessage()); buildRunMessage != "" {
			msg = fmt.Sprintf("BuildRun failed: %s", buildRunMessage)
		}
		return metav1.ConditionFalse, ReasonBuildRunFailed, strings.TrimSpace(msg)
	default:
		msg := "BuildRun is running"
		if buildRunMessage := strings.TrimSpace(succeededCondition.GetMessage()); buildRunMessage != "" {
			msg = fmt.Sprintf("BuildRun is running: %s", buildRunMessage)
		}
		return metav1.ConditionUnknown, ReasonBuildRunRunning, strings.TrimSpace(msg)
	}
}

func hasTagOrDigest(image string) bool {
	parsed, err := distref.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	if _, ok := parsed.(distref.Digested); ok {
		return true
	}
	return !distref.IsNameOnly(parsed)
}

func computeLatestImage(ib *buildv1alpha1.ImageBuild, br *shipwright.BuildRun) string {
	if br.Status.Output != nil && br.Status.Output.Digest != "" {
		return ib.Spec.Output.Image + "@" + br.Status.Output.Digest
	}
	if hasTagOrDigest(ib.Spec.Output.Image) {
		return ib.Spec.Output.Image
	}
	return ""
}

func (r *ImageBuildReconciler) patchBuildSucceededCondition(
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

	return r.Status().Patch(ctx, ib, client.MergeFrom(orig))
}

func (r *ImageBuildReconciler) patchLatestImage(
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
	return r.Status().Patch(ctx, ib, client.MergeFrom(orig))
}

func (r *ImageBuildReconciler) ensureBuildRunOnCommit(ctx context.Context, ib *buildv1alpha1.ImageBuild, counter int64) (*shipwright.BuildRun, error) {
	desired := newBuildRun(ib, 0)
	desired.Name = fmt.Sprintf("%s-buildrun-oncommit-%d", ib.Name, counter)
	desired.Labels["build.dana.io/build-trigger"] = "oncommit"

	existing := &shipwright.BuildRun{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, existing); err == nil {
		if !metav1.IsControlledBy(existing, ib) {
			return nil, &controllerutil.AlreadyOwnedError{Object: existing}
		}
		return existing, nil
	} else if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	if err := controllerutil.SetControllerReference(ib, desired, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, desired); err != nil {
		return nil, err
	}
	return desired, nil
}

func (r *ImageBuildReconciler) isNewBuildRequired(ctx context.Context, ib *buildv1alpha1.ImageBuild) bool {
	if ib.Status.LastBuildRunRef == "" {
		return true
	}

	lastSpecJson, ok := ib.Annotations[annotationKeyLastBuildSpec]
	if !ok {
		return true
	}

	var lastInputs buildInputs
	if err := json.Unmarshal([]byte(lastSpecJson), &lastInputs); err != nil {
		log.FromContext(ctx).Error(err, "Failed to unmarshal last build spec annotation", "ImageBuild", ib.Name)
		return true
	}

	return !reflect.DeepEqual(ib.Spec.Source, lastInputs.Source) ||
		!reflect.DeepEqual(ib.Spec.BuildFile, lastInputs.BuildFile) ||
		!reflect.DeepEqual(ib.Spec.Output, lastInputs.Output)
}

func (r *ImageBuildReconciler) recordBuildSpec(ib *buildv1alpha1.ImageBuild) error {
	if ib.Annotations == nil {
		ib.Annotations = make(map[string]string)
	}

	inputs := buildInputs{
		Source:    ib.Spec.Source,
		BuildFile: ib.Spec.BuildFile,
		Output:    ib.Spec.Output,
	}

	specJson, err := json.Marshal(inputs)
	if err != nil {
		return err
	}

	ib.Annotations[annotationKeyLastBuildSpec] = string(specJson)
	return nil
}
