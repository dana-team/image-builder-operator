package imagebuild

import (
	"context"
	"testing"
	"time"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	testWebhookSecretName = "github-webhook-secret"
	testWebhookSecretKey  = "token"
)

func TestReconcileMissingPolicy(t *testing.T) {
	ctx := context.Background()

	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	r, c := newReconciler(t, ib)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)
	require.Greater(t, res.RequeueAfter, time.Duration(0))

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonMissingPolicy)
}

func TestReconcileStrategyNotFound(t *testing.T) {
	ctx := context.Background()

	policy := newImageBuildPolicy()
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	r, c := newReconciler(t, ib, policy)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)
	require.Greater(t, res.RequeueAfter, time.Duration(0))

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildStrategyNotFound)
}

func TestReconcileBuildConflict(t *testing.T) {
	ctx := context.Background()

	policy := newImageBuildPolicy()
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	clusterBuildStrategy := &shipwright.ClusterBuildStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
	}

	otherOwner := &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "someone-else",
			UID:  types.UID("other-uid"),
		},
	}

	conflictingBuild := &shipwright.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildNameFor(ib),
			Namespace: ib.Namespace,
		},
	}
	require.NoError(t, controllerutil.SetControllerReference(otherOwner, conflictingBuild, testScheme(t)))

	r, c := newReconciler(t, ib, policy, clusterBuildStrategy, conflictingBuild)

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), res.RequeueAfter)

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildConflict)
}

func TestReconcileCreatesBuild(t *testing.T) {
	ctx := context.Background()

	policy := newImageBuildPolicy()
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	clusterBuildStrategy := &shipwright.ClusterBuildStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
	}

	r, c := newReconciler(t, ib, policy, clusterBuildStrategy)
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	require.Equal(t, latest.Generation, latest.Status.ObservedGeneration)
	require.Equal(t, buildNameFor(ib), latest.Status.BuildRef)

	build := &shipwright.Build{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, build))
	require.Equal(t, buildNameFor(ib), build.Name)
	require.Equal(t, ib.Namespace, build.Namespace)
	require.True(t, metav1.IsControlledBy(build, latest), "Build should be controller-owned by ImageBuild")
	require.Equal(t, absentStrategy, build.Spec.Strategy.Name)
}

func TestReconcileUpdatesBuild(t *testing.T) {
	ctx := context.Background()

	policy := newImageBuildPolicy()
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

	clusterBuildStrategy := &shipwright.ClusterBuildStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
	}

	r, c := newReconciler(t, ib, policy, clusterBuildStrategy)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))

	build := &shipwright.Build{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, build))
	require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL)

	build.Spec.Source.Git.URL = "https://drifted-url.com"
	require.NoError(t, c.Update(ctx, build))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: buildNameFor(ib), Namespace: ib.Namespace}, build))
	require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL)
	require.Equal(t, absentStrategy, build.Spec.Strategy.Name)
}

func TestEnsureWebhookSecretMissing(t *testing.T) {
	ctx := context.Background()

	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
	ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{
		Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit,
	}
	ib.Spec.OnCommit = &buildv1alpha1.ImageBuildOnCommit{
		WebhookSecretRef: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: testWebhookSecretName},
			Key:                  testWebhookSecretKey,
		},
	}

	r, c := newReconciler(t, ib)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)
	require.Greater(t, res.RequeueAfter, time.Duration(0))

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretMissing)
}

func TestEnsureWebhookSecretMissingKey(t *testing.T) {
	ctx := context.Background()

	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
	ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{
		Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit,
	}
	ib.Spec.OnCommit = &buildv1alpha1.ImageBuildOnCommit{
		WebhookSecretRef: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: testWebhookSecretName},
			Key:                  testWebhookSecretKey,
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWebhookSecretName,
			Namespace: ib.Namespace,
		},
		Data: map[string][]byte{
			"wrong-key": []byte("my-token"),
		},
	}

	r, c := newReconciler(t, ib, secret)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ib.Name, Namespace: ib.Namespace}})
	require.NoError(t, err)
	require.Greater(t, res.RequeueAfter, time.Duration(0))

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretInvalidKey)
}

