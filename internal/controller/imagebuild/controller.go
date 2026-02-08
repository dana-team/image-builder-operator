package imagebuild

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
)

const (
	imageBuildControllerName = "ImageBuildController"
	imageBuildPolicyName     = "image-build-policy"

	indexPushSecret    = "pushSecret"
	indexCloneSecret   = "cloneSecret"
	indexWebhookSecret = "webhookSecret"
)

// Reconciler reconciles ImageBuild resources.
type Reconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	EventRecorder record.EventRecorder
}

// +kubebuilder:rbac:groups=build.dana.io,resources=imagebuilds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=build.dana.io,resources=imagebuilds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=build.dana.io,resources=imagebuilds/finalizers,verbs=update
// +kubebuilder:rbac:groups=build.dana.io,resources=imagebuildpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch;update
// +kubebuilder:rbac:groups="events.k8s.io",resources=events,verbs=get;list;watch;create;patch;update
// +kubebuilder:rbac:groups=shipwright.io,resources=builds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=shipwright.io,resources=clusterbuildstrategies,verbs=get;list;watch
// +kubebuilder:rbac:groups=shipwright.io,resources=buildruns,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// SetupWithManager registers the controller and its watches with the given manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, indexPushSecret, func(obj client.Object) []string {
		ib := obj.(*buildv1alpha1.ImageBuild)
		if ib.Spec.Output.PushSecret != nil {
			return []string{ib.Spec.Output.PushSecret.Name}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, indexCloneSecret, func(obj client.Object) []string {
		ib := obj.(*buildv1alpha1.ImageBuild)
		if ib.Spec.Source.Git.CloneSecret != nil {
			return []string{ib.Spec.Source.Git.CloneSecret.Name}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, indexWebhookSecret, func(obj client.Object) []string {
		ib := obj.(*buildv1alpha1.ImageBuild)
		if ib.Spec.OnCommit != nil {
			return []string{ib.Spec.OnCommit.WebhookSecretRef.Name}
		}
		return nil
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1alpha1.ImageBuild{}).
		Owns(&shipwright.Build{}).
		Owns(&shipwright.BuildRun{}).
		Watches(&corev1.Secret{}, r.mapSecretToImageBuilds(), builder.WithPredicates(r.secretWatchPredicate())).
		Named(imageBuildControllerName).
		Complete(r)
}

// secretWatchPredicate returns a predicate that only accepts Secret create
// events, so that a newly available secret can trigger a retry for failed builds.
func (r *Reconciler) secretWatchPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

// mapSecretToImageBuilds returns an event handler that enqueues reconcile
// requests for every ImageBuild referencing the Secret.
func (r *Reconciler) mapSecretToImageBuilds() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}

		var requests []reconcile.Request

		for _, indexKey := range []string{indexPushSecret, indexCloneSecret, indexWebhookSecret} {
			imageBuilds := &buildv1alpha1.ImageBuildList{}
			if err := r.List(ctx, imageBuilds,
				client.InNamespace(secret.Namespace),
				client.MatchingFields{indexKey: secret.Name},
			); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list ImageBuilds by secret index",
					"Secret", secret.Name, "Index", indexKey)
				continue
			}

			for _, ib := range imageBuilds.Items {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&ib),
				})
			}
		}

		return requests
	})
}

// Reconcile reconciles the desired state of an ImageBuild with the cluster state.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("ImageBuildName", req.Name, "ImageBuildNamespace", req.Namespace)
	logger.Info("Starting Reconcile")

	imageBuild := &buildv1alpha1.ImageBuild{}
	if err := r.Get(ctx, req.NamespacedName, imageBuild); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ImageBuild: %w", err)
	}

	if err := r.ensureOnCommitLabel(ctx, imageBuild); err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	if err := r.ensureWebhookSecret(ctx, imageBuild); err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	var alreadyOwned *controllerutil.AlreadyOwnedError

	policy := &buildv1alpha1.ImageBuildPolicy{}
	if err := r.Get(ctx, client.ObjectKey{Name: imageBuildPolicyName}, policy); err != nil {
		_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonMissingPolicy, "ImageBuildPolicy is missing")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	present := policy.Spec.ClusterBuildStrategy.BuildFile.Present
	absent := policy.Spec.ClusterBuildStrategy.BuildFile.Absent

	selectedStrategyName := absent
	if imageBuild.Spec.BuildFile.Mode == buildv1alpha1.ImageBuildFileModePresent {
		selectedStrategyName = present
	}

	if err := r.reconcileBuild(ctx, imageBuild, selectedStrategyName); err != nil {
		if apierrors.IsNotFound(err) {
			_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildStrategyNotFound, err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if errors.As(err, &alreadyOwned) {
			_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildConflict, err.Error())
			return ctrl.Result{}, nil
		}
		_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildReconcileFailed, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	buildRef := buildNameFor(imageBuild)
	if imageBuild.Status.BuildRef != buildRef || imageBuild.Status.ObservedGeneration != imageBuild.Generation {
		orig := imageBuild.DeepCopy()
		imageBuild.Status.ObservedGeneration = imageBuild.Generation
		imageBuild.Status.BuildRef = buildRef
		if err := r.Status().Patch(ctx, imageBuild, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}

	var buildRun *shipwright.BuildRun

	if br, requeueAfter, err := r.reconcileRebuild(ctx, imageBuild); err != nil {
		_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildRunReconcileFailed, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	} else if requeueAfter != nil {
		return ctrl.Result{RequeueAfter: *requeueAfter}, nil
	} else if br != nil {
		buildRun = br
	}

	if buildRun == nil {
		br, result, err := r.ensureBuildRun(ctx, imageBuild)
		if err != nil || result != nil {
			return *result, err
		}
		buildRun = br
	}

	if buildRun != nil {
		if err := r.patchBuildSucceededCondition(ctx, imageBuild, buildRun); err != nil {
			return ctrl.Result{}, err
		}
	}

	ready := meta.FindStatusCondition(imageBuild.Status.Conditions, TypeReady)
	if ready == nil ||
		ready.Status != metav1.ConditionTrue ||
		ready.ObservedGeneration != imageBuild.Generation ||
		ready.Reason != ReasonReconciled {
		if err := r.patchReadyCondition(ctx, imageBuild, metav1.ConditionTrue, ReasonReconciled, "ImageBuild is reconciled"); err != nil {
			return ctrl.Result{}, err
		}
	}

	if buildRun.IsSuccessful() {
		if err := r.patchLatestImage(ctx, imageBuild, computeLatestImage(imageBuild, buildRun)); err != nil {
			return ctrl.Result{}, err
		}
	}

	cond := meta.FindStatusCondition(imageBuild.Status.Conditions, TypeBuildSucceeded)
	if cond != nil && cond.Status == metav1.ConditionUnknown {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// ensureBuildRun creates a new BuildRun when the spec has changed,
// or returns the existing one otherwise.
func (r *Reconciler) ensureBuildRun(
	ctx context.Context,
	imageBuild *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, *ctrl.Result, error) {
	var alreadyOwned *controllerutil.AlreadyOwnedError

	if r.isNewBuildRequired(ctx, imageBuild) {
		br, err := r.reconcileBuildRun(ctx, imageBuild)
		if err != nil {
			if errors.As(err, &alreadyOwned) {
				_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildRunConflict, err.Error())
				return nil, &ctrl.Result{}, nil
			}
			_ = r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonBuildRunReconcileFailed, err.Error())
			return nil, &ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if err := r.recordBuildSpec(imageBuild); err != nil {
			return nil, &ctrl.Result{}, err
		}
		if err := r.Update(ctx, imageBuild); err != nil {
			return nil, &ctrl.Result{}, err
		}

		return br, nil, nil
	}

	if imageBuild.Status.LastBuildRunRef != "" {
		existingBR := &shipwright.BuildRun{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: imageBuild.Namespace, Name: imageBuild.Status.LastBuildRunRef}, existingBR); err != nil {
			if !apierrors.IsNotFound(err) {
				log.FromContext(ctx).Error(err, "Failed to fetch last BuildRun", "BuildRun", imageBuild.Status.LastBuildRunRef)
			}
		} else {
			return existingBR, nil, nil
		}
	}

	return nil, nil, nil
}

func (r *Reconciler) ensureWebhookSecret(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	if ib.Spec.OnCommit == nil {
		return nil
	}

	secretName := ib.Spec.OnCommit.WebhookSecretRef.Name
	secretKey := ib.Spec.OnCommit.WebhookSecretRef.Key

	secret := &corev1.Secret{}
	key := client.ObjectKey{
		Namespace: ib.Namespace,
		Name:      secretName,
	}

	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("WebhookSecret %q not found", secretName)
			_ = r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, ReasonWebhookSecretMissing, msg)
			return errors.New(msg)
		}
		return err
	}

	if _, ok := secret.Data[secretKey]; !ok {
		msg := fmt.Sprintf("WebhookSecret %q missing key %q", secretName, secretKey)
		_ = r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, ReasonWebhookSecretInvalidKey, msg)
		return errors.New(msg)
	}

	return nil
}
