package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	distref "github.com/distribution/reference"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	shipwrightresources "github.com/shipwright-io/build/pkg/reconciler/buildrun/resources"
	"k8s.io/utils/ptr"
)

func (r *Reconciler) getLastBuildRun(ctx context.Context, ib *buildv1alpha1.ImageBuild) (*shipwright.BuildRun, error) {
	if ib.Status.LastBuildRunRef == "" {
		return nil, nil
	}

	br := &shipwright.BuildRun{}
	key := client.ObjectKey{Namespace: ib.Namespace, Name: ib.Status.LastBuildRunRef}
	if err := r.Get(ctx, key, br); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get BuildRun %q: %w", key.Name, err)
	}

	return br, nil
}

// buildInputs captures fields that trigger a new build when changed.
type buildInputs struct {
	Source    buildv1alpha1.ImageBuildSource `json:"source"`
	BuildFile buildv1alpha1.ImageBuildFile   `json:"buildFile"`
	Output    buildv1alpha1.ImageBuildOutput `json:"output"`
}

// getOrCreateBuildRun gets or creates a BuildRun owned by the given ImageBuild.
func (r *Reconciler) getOrCreateBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	desired *shipwright.BuildRun,
) (*shipwright.BuildRun, bool, error) {
	existing := &shipwright.BuildRun{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, existing); err == nil {
		if !metav1.IsControlledBy(existing, ib) {
			return nil, false, &controllerutil.AlreadyOwnedError{Object: existing}
		}
		return existing, false, nil
	} else if client.IgnoreNotFound(err) != nil {
		return nil, false, fmt.Errorf("failed to get BuildRun %q: %w", key.Name, err)
	}

	if err := controllerutil.SetControllerReference(ib, desired, r.Scheme); err != nil {
		return nil, false, fmt.Errorf("failed to set controller reference on BuildRun %q: %w", desired.Name, err)
	}
	if err := r.Create(ctx, desired); err != nil {
		return nil, false, fmt.Errorf("failed to create BuildRun %q: %w", desired.Name, err)
	}

	return desired, true, nil
}

func (r *Reconciler) ensureBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, error) {
	counter := nextCounter(ib.Status.BuildRunCounter)
	desired := newBuildRun(ib, counter)

	br, created, err := r.getOrCreateBuildRun(ctx, ib, desired)
	if err != nil {
		return nil, err
	}
	if !created {
		return br, nil
	}

	orig := ib.DeepCopy()
	ib.Status.BuildRunCounter = counter
	if err := r.Status().Patch(ctx, ib, client.MergeFrom(orig)); err != nil {
		return nil, fmt.Errorf("failed to patch BuildRunCounter status: %w", err)
	}

	return br, nil
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

// isSpecDrifted reports whether the observed state has drifted from the desired
// state, indicating a new BuildRun should be created.
func (r *Reconciler) isSpecDrifted(ctx context.Context, ib *buildv1alpha1.ImageBuild) bool {
	logger := log.FromContext(ctx)

	if ib.Status.LastBuildRunRef == "" {
		return true
	}

	lastSpecJSON, ok := ib.Annotations[buildv1alpha1.AnnotationKeyLastBuildSpec]
	if !ok {
		return true
	}

	var lastInputs buildInputs
	if err := json.Unmarshal([]byte(lastSpecJSON), &lastInputs); err != nil {
		logger.Error(err, "Failed to unmarshal last build spec annotation", "ImageBuild", ib.Name)
		return true
	}

	if !reflect.DeepEqual(ib.Spec.Source, lastInputs.Source) ||
		!reflect.DeepEqual(ib.Spec.BuildFile, lastInputs.BuildFile) ||
		!reflect.DeepEqual(ib.Spec.Output, lastInputs.Output) {
		return true
	}

	if r.isSecretRetryNeeded(ctx, ib) {
		logger.Info("Triggering automatic retry: referenced secret is now available",
			"ImageBuild", ib.Name,
			"LastBuildRun", ib.Status.LastBuildRunRef)
		return true
	}

	return false
}

// recordBuildSpec snapshots the build-relevant spec fields
// for detecting spec drift on subsequent reconciles.
func (r *Reconciler) recordBuildSpec(ib *buildv1alpha1.ImageBuild) error {
	if ib.Annotations == nil {
		ib.Annotations = make(map[string]string)
	}

	inputs := buildInputs{
		Source:    ib.Spec.Source,
		BuildFile: ib.Spec.BuildFile,
		Output:    ib.Spec.Output,
	}

	specJSON, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("failed to marshal build spec annotation: %w", err)
	}

	ib.Annotations[buildv1alpha1.AnnotationKeyLastBuildSpec] = string(specJSON)
	return nil
}

// isSecretRetryNeeded reports whether the last BuildRun failed due to a missing
// secret that has since become available.
func (r *Reconciler) isSecretRetryNeeded(ctx context.Context, ib *buildv1alpha1.ImageBuild) bool {
	logger := log.FromContext(ctx)

	lastBuildRun, err := r.getLastBuildRun(ctx, ib)
	if err != nil || lastBuildRun == nil {
		return false
	}

	succeededCond := lastBuildRun.Status.GetCondition(shipwright.Succeeded)
	if succeededCond == nil || succeededCond.GetStatus() != corev1.ConditionFalse {
		return false
	}

	if succeededCond.GetReason() != shipwrightresources.ConditionBuildRegistrationFailed {
		return false
	}

	var secretNames []string
	if ib.Spec.Output.PushSecret != nil {
		secretNames = append(secretNames, ib.Spec.Output.PushSecret.Name)
	}
	if ib.Spec.Source.Git.CloneSecret != nil {
		secretNames = append(secretNames, ib.Spec.Source.Git.CloneSecret.Name)
	}

	for _, name := range secretNames {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: ib.Namespace,
			Name:      name,
		}, secret); err == nil {
			logger.Info("Secret is now available, will retry build", "Secret", name)
			return true
		}
	}

	return false
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

// computeLatestImage returns the image reference for a successful BuildRun,
// preferring digest over tag; returns empty if neither is available.
func computeLatestImage(ib *buildv1alpha1.ImageBuild, br *shipwright.BuildRun) string {
	if br.Status.Output != nil && br.Status.Output.Digest != "" {
		return ib.Spec.Output.Image + "@" + br.Status.Output.Digest
	}
	if isTagOrDigestPresent(ib.Spec.Output.Image) {
		return ib.Spec.Output.Image
	}
	return ""
}

func newBuildRun(ib *buildv1alpha1.ImageBuild, counter int64) *shipwright.BuildRun {
	return &shipwright.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRunNameFor(ib, counter),
			Namespace: ib.Namespace,
			Labels: map[string]string{
				buildv1alpha1.LabelKeyParentImageBuild: ib.Name,
			},
		},
		Spec: shipwright.BuildRunSpec{
			Build: shipwright.ReferencedBuild{
				Name: ptr.To(buildNameFor(ib)),
			},
		},
	}
}

func buildRunNameFor(ib *buildv1alpha1.ImageBuild, counter int64) string {
	return fmt.Sprintf("%s-buildrun-%d", ib.Name, counter)
}

func nextCounter(current int64) int64 {
	if current < 0 {
		return 1
	}
	return current + 1
}

func isTagOrDigestPresent(image string) bool {
	parsed, err := distref.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	if _, ok := parsed.(distref.Digested); ok {
		return true
	}
	return !distref.IsNameOnly(parsed)
}
