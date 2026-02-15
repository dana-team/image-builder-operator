// Package imagebuild implements the ImageBuild controller.
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
	controllerName = "ImageBuildController"
	policyName     = "image-build-policy"

	indexPushSecret    = "pushSecret"
	indexCloneSecret   = "cloneSecret"
	indexWebhookSecret = "webhookSecret"

	buildRunPollInterval = 10 * time.Second
	errorRequeueInterval = 30 * time.Second
)

var (
	errWebhookSecretMissing    = errors.New("webhook secret not found")
	errWebhookSecretInvalidKey = errors.New("webhook secret key not found")
	errBuildRunFailed          = errors.New("buildrun reconciliation failed")
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
		return fmt.Errorf("failed to index field %q: %w", indexPushSecret, err)
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, indexCloneSecret, func(obj client.Object) []string {
		ib := obj.(*buildv1alpha1.ImageBuild)
		if ib.Spec.Source.Git.CloneSecret != nil {
			return []string{ib.Spec.Source.Git.CloneSecret.Name}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to index field %q: %w", indexCloneSecret, err)
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &buildv1alpha1.ImageBuild{}, indexWebhookSecret, func(obj client.Object) []string {
		ib := obj.(*buildv1alpha1.ImageBuild)
		if ib.Spec.OnCommit != nil {
			return []string{ib.Spec.OnCommit.WebhookSecretRef.Name}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to index field %q: %w", indexWebhookSecret, err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&buildv1alpha1.ImageBuild{}).
		Owns(&shipwright.Build{}).
		Owns(&shipwright.BuildRun{}).
		Watches(&corev1.Secret{}, r.mapSecretToImageBuilds(), builder.WithPredicates(r.secretWatchPredicate())).
		Named(controllerName).
		Complete(r); err != nil {
		return fmt.Errorf("failed to build controller: %w", err)
	}

	return nil
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

	if stop, err := r.reconcilePrerequisites(ctx, imageBuild); stop {
		return ctrl.Result{RequeueAfter: errorRequeueInterval}, err
	}

	if err := r.ensureBuild(ctx, imageBuild); err != nil {
		var alreadyOwned *controllerutil.AlreadyOwnedError
		if errors.As(err, &alreadyOwned) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: errorRequeueInterval}, nil
	}

	if err := r.patchBuildRef(ctx, imageBuild); err != nil {
		return ctrl.Result{}, err
	}

	buildRun, requeueAfter, err := r.resolveBuildRun(ctx, imageBuild)
	if err != nil {
		if !errors.Is(err, errBuildRunFailed) {
			return ctrl.Result{}, err
		}
		requeue := errorRequeueInterval
		var alreadyOwned *controllerutil.AlreadyOwnedError
		if errors.As(err, &alreadyOwned) {
			requeue = 0
		}
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	if requeueAfter != nil {
		return ctrl.Result{RequeueAfter: *requeueAfter}, nil
	}

	poll, err := r.reconcileStatus(ctx, imageBuild, buildRun)
	if err != nil {
		return ctrl.Result{}, err
	}
	if poll {
		return ctrl.Result{RequeueAfter: buildRunPollInterval}, nil
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) reconcilePrerequisites(ctx context.Context, imageBuild *buildv1alpha1.ImageBuild) (bool, error) {
	if err := r.ensureOnCommitLabel(ctx, imageBuild); err != nil {
		return true, err
	}
	if err := r.validateWebhookSecret(ctx, imageBuild); err != nil {
		return true, nil
	}
	return false, nil
}

func (r *Reconciler) reconcileStatus(
	ctx context.Context,
	imageBuild *buildv1alpha1.ImageBuild,
	buildRun *shipwright.BuildRun,
) (bool, error) {
	if buildRun != nil {
		if err := r.patchBuildSucceededCondition(ctx, imageBuild, buildRun); err != nil {
			return false, err
		}
	}

	ready := meta.FindStatusCondition(imageBuild.Status.Conditions, TypeReady)
	if ready == nil ||
		ready.Status != metav1.ConditionTrue ||
		ready.ObservedGeneration != imageBuild.Generation ||
		ready.Reason != ReasonReconciled {
		if err := r.patchReadyCondition(ctx, imageBuild, metav1.ConditionTrue, ReasonReconciled, "ImageBuild is reconciled"); err != nil {
			return false, err
		}
	}

	if buildRun != nil && buildRun.IsSuccessful() {
		if err := r.patchLatestImage(ctx, imageBuild, computeLatestImage(imageBuild, buildRun)); err != nil {
			return false, err
		}
	}

	cond := meta.FindStatusCondition(imageBuild.Status.Conditions, TypeBuildSucceeded)
	if cond != nil && cond.Status == metav1.ConditionUnknown {
		return true, nil
	}

	return false, nil
}

// ensureBuild reconciles the Build for the given ImageBuild
// based on the cluster-wide ImageBuildPolicy.
func (r *Reconciler) ensureBuild(ctx context.Context, imageBuild *buildv1alpha1.ImageBuild) error {
	logger := log.FromContext(ctx)

	policy := &buildv1alpha1.ImageBuildPolicy{}
	if err := r.Get(ctx, client.ObjectKey{Name: policyName}, policy); err != nil {
		if patchErr := r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, ReasonMissingPolicy, "ImageBuildPolicy is missing"); patchErr != nil {
			logger.Error(patchErr, "failed to patch Ready condition")
		}
		return fmt.Errorf("failed to get ImageBuildPolicy: %w", err)
	}

	present := policy.Spec.ClusterBuildStrategy.BuildFile.Present
	absent := policy.Spec.ClusterBuildStrategy.BuildFile.Absent

	selectedStrategyName := absent
	if imageBuild.Spec.BuildFile.Mode == buildv1alpha1.ImageBuildFileModePresent {
		selectedStrategyName = present
	}

	if err := r.reconcileBuild(ctx, imageBuild, selectedStrategyName); err != nil {
		var (
			reason       = ReasonBuildReconcileFailed
			alreadyOwned *controllerutil.AlreadyOwnedError
		)

		switch {
		case apierrors.IsNotFound(err):
			reason = ReasonBuildStrategyNotFound
		case errors.As(err, &alreadyOwned):
			reason = ReasonBuildConflict
		}

		if patchErr := r.patchReadyCondition(ctx, imageBuild, metav1.ConditionFalse, reason, err.Error()); patchErr != nil {
			logger.Error(patchErr, "failed to patch Ready condition")
		}

		return err
	}

	return nil
}

// ensureBuildRun creates a new BuildRun when the spec has changed,
// or returns the existing one otherwise.
func (r *Reconciler) ensureBuildRun(
	ctx context.Context,
	imageBuild *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, error) {
	if r.isNewBuildRequired(ctx, imageBuild) {
		br, err := r.reconcileBuildRun(ctx, imageBuild)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBuildRunFailed, err)
		}

		if err := r.recordBuildSpec(imageBuild); err != nil {
			return nil, fmt.Errorf("failed to record build spec: %w", err)
		}
		if err := r.Update(ctx, imageBuild); err != nil {
			return nil, fmt.Errorf("failed to update ImageBuild: %w", err)
		}

		return br, nil
	}

	if imageBuild.Status.LastBuildRunRef != "" {
		existingBR := &shipwright.BuildRun{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: imageBuild.Namespace, Name: imageBuild.Status.LastBuildRunRef}, existingBR); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to fetch last BuildRun %q: %w", imageBuild.Status.LastBuildRunRef, err)
			}
		} else {
			return existingBR, nil
		}
	}

	return nil, nil
}

// resolveBuildRun resolves the active or required BuildRun for the given ImageBuild.
// It tries on-commit rebuild first, then falls back to spec-change BuildRun creation.
// On failure it patches the Ready condition with the appropriate reason.
func (r *Reconciler) resolveBuildRun(
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
) (*shipwright.BuildRun, *time.Duration, error) {
	logger := log.FromContext(ctx)

	if buildRun, requeueAfter, err := r.reconcileRebuild(ctx, ib); err != nil {
		if patchErr := r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, ReasonBuildRunReconcileFailed, err.Error()); patchErr != nil {
			logger.Error(patchErr, "failed to patch Ready condition")
		}
		return nil, nil, fmt.Errorf("%w: %w", errBuildRunFailed, err)
	} else if requeueAfter != nil || buildRun != nil {
		return buildRun, requeueAfter, nil
	}

	buildRun, err := r.ensureBuildRun(ctx, ib)
	if err != nil {
		if !errors.Is(err, errBuildRunFailed) {
			return nil, nil, err
		}
		reason := ReasonBuildRunReconcileFailed
		var alreadyOwned *controllerutil.AlreadyOwnedError
		if errors.As(err, &alreadyOwned) {
			reason = ReasonBuildRunConflict
		}
		if patchErr := r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, reason, err.Error()); patchErr != nil {
			logger.Error(patchErr, "failed to patch Ready condition")
		}
		return nil, nil, err
	}

	return buildRun, nil, nil
}

func (r *Reconciler) validateWebhookSecret(ctx context.Context, ib *buildv1alpha1.ImageBuild) error {
	logger := log.FromContext(ctx)

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
			if patchErr := r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, ReasonWebhookSecretMissing,
				fmt.Sprintf("WebhookSecret %q not found", secretName)); patchErr != nil {
				logger.Error(patchErr, "failed to patch Ready condition")
			}
			return fmt.Errorf("%w: %q", errWebhookSecretMissing, secretName)
		}
		return fmt.Errorf("failed to get webhook secret %q: %w", secretName, err)
	}

	if _, ok := secret.Data[secretKey]; !ok {
		if patchErr := r.patchReadyCondition(ctx, ib, metav1.ConditionFalse, ReasonWebhookSecretInvalidKey,
			fmt.Sprintf("WebhookSecret %q missing key %q", secretName, secretKey)); patchErr != nil {
			logger.Error(patchErr, "failed to patch Ready condition")
		}
		return fmt.Errorf("%w: key %q in secret %q", errWebhookSecretInvalidKey, secretKey, secretName)
	}

	return nil
}
