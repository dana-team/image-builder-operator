package imagebuild

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"k8s.io/utils/ptr"
)

// ensureBuild ensures the Shipwright Build exists and matches desired state.
func (r *Reconciler) ensureBuild(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	strategyName string,
) error {
	logger := log.FromContext(ctx)

	clusterBuildStrategy := &shipwright.ClusterBuildStrategy{}
	if err := r.Get(ctx, types.NamespacedName{Name: strategyName}, clusterBuildStrategy); err != nil {
		return fmt.Errorf("failed to get ClusterBuildStrategy %q: %w", strategyName, err)
	}

	desired := r.newBuild(ib, strategyName)

	actual := &shipwright.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, actual, func() error {
		if err := controllerutil.SetControllerReference(ib, actual, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference on Build %q: %w", actual.Name, err)
		}
		if actual.Labels == nil {
			actual.Labels = make(map[string]string, len(desired.Labels))
		}
		for k, v := range desired.Labels {
			actual.Labels[k] = v
		}
		actual.Spec = desired.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or patch Build %q: %w", actual.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Reconciled Shipwright Build", "name", actual.Name, "operation", string(op))
	}

	return nil
}

func (r *Reconciler) newBuild(
	ib *buildv1alpha1.ImageBuild,
	strategyName string,
) *shipwright.Build {
	build := &shipwright.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildNameFor(ib),
			Namespace: ib.Namespace,
			Labels: map[string]string{
				buildv1alpha1.LabelKeyParentImageBuild: ib.Name,
			},
		},
		Spec: shipwright.BuildSpec{
			Strategy: shipwright.Strategy{
				Name: strategyName,
				Kind: ptr.To(shipwright.ClusterBuildStrategyKind),
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
		build.Spec.Source.Git.Revision = ptr.To(ib.Spec.Source.Git.Revision)
	}
	if ib.Spec.Source.ContextDir != "" {
		build.Spec.Source.ContextDir = ptr.To(ib.Spec.Source.ContextDir)
	}
	if ib.Spec.Source.Git.CloneSecret != nil && ib.Spec.Source.Git.CloneSecret.Name != "" {
		build.Spec.Source.Git.CloneSecret = ptr.To(ib.Spec.Source.Git.CloneSecret.Name)
	}
	if ib.Spec.Output.PushSecret != nil && ib.Spec.Output.PushSecret.Name != "" {
		build.Spec.Output.PushSecret = ptr.To(ib.Spec.Output.PushSecret.Name)
	}

	return build
}

func buildNameFor(ib *buildv1alpha1.ImageBuild) string {
	return ib.Name + "-build"
}
