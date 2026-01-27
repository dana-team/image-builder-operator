package imagebuild

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
)

var ErrBuildStrategyNotFound = errors.New("clusterbuildstrategy not found")

func buildNameFor(ib *buildv1alpha1.ImageBuild) string {
	return ib.Name + "-build"
}

func (r *ImageBuildReconciler) patchReadyCondition(
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

	return r.Status().Patch(ctx, ib, client.MergeFrom(orig))
}

func (r *ImageBuildReconciler) newBuild(
	ib *buildv1alpha1.ImageBuild,
	selectedStrategyName string,
) *shipwright.Build {
	kind := shipwright.ClusterBuildStrategyKind

	build := &shipwright.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildNameFor(ib),
			Namespace: ib.Namespace,
			Labels: map[string]string{
				"build.dana.io/parent-imagebuild": ib.Name,
			},
		},
		Spec: shipwright.BuildSpec{
			Strategy: shipwright.Strategy{
				Name: selectedStrategyName,
				Kind: &kind,
			},
			Source: &shipwright.Source{
				Type: shipwright.GitType,
				Git: &shipwright.Git{
					URL: ib.Spec.Source.Git.URL,
				},
			},
			Output: shipwright.Image{
				Image: ib.Spec.Output.Image,
			},
		},
	}

	if ib.Spec.Source.Git.Revision != "" {
		rev := ib.Spec.Source.Git.Revision
		build.Spec.Source.Git.Revision = &rev
	}
	if ib.Spec.Source.ContextDir != "" {
		cd := ib.Spec.Source.ContextDir
		build.Spec.Source.ContextDir = &cd
	}
	if ib.Spec.Source.Git.CloneSecret != nil && ib.Spec.Source.Git.CloneSecret.Name != "" {
		sec := ib.Spec.Source.Git.CloneSecret.Name
		build.Spec.Source.Git.CloneSecret = &sec
	}
	if ib.Spec.Output.PushSecret != nil && ib.Spec.Output.PushSecret.Name != "" {
		ps := ib.Spec.Output.PushSecret.Name
		build.Spec.Output.PushSecret = &ps
	}

	return build
}

// reconcileBuild ensures the Shipwright Build exists and matches desired state.
func (r *ImageBuildReconciler) reconcileBuild(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	selectedStrategyName string,
) error {
	logger := log.FromContext(ctx)

	clusterBuildStrategy := &shipwright.ClusterBuildStrategy{}
	if err := r.Get(ctx, types.NamespacedName{Name: selectedStrategyName}, clusterBuildStrategy); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrBuildStrategyNotFound, selectedStrategyName, err)
	}

	desired := r.newBuild(ib, selectedStrategyName)

	actual := &shipwright.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, actual, func() error {
		if err := controllerutil.SetControllerReference(ib, actual, r.Scheme); err != nil {
			return err
		}
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			actual.Labels[k] = v
		}
		actual.Spec = desired.Spec
		return nil
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Reconciled Shipwright Build", "name", actual.Name, "operation", string(op))
	}

	return nil
}
