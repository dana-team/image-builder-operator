package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	distref "github.com/distribution/reference"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
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

	return !reflect.DeepEqual(ib.Spec.Source, lastInputs.Source) ||
		!reflect.DeepEqual(ib.Spec.BuildFile, lastInputs.BuildFile) ||
		!reflect.DeepEqual(ib.Spec.Output, lastInputs.Output)
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
