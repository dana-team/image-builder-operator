package imagebuild

import (
	"context"
	"fmt"
	"testing"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testWebhookSecretName = "github-webhook-secret"
	testWebhookSecretKey  = "token"
	testWrongSecretKey    = "wrong-key"
	testTokenValue        = "my-token"
	testRevisionV2        = "v2.0.0"
)

func TestReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("dependency validation", func(t *testing.T) {
		t.Run("reports not ready when policy missing", func(t *testing.T) {
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			r, c := newReconciler(t, ib)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, errorRequeueInterval, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonMissingPolicy)
		})

		t.Run("retries when strategy unavailable", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			r, c := newReconciler(t, ib, policy)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, errorRequeueInterval, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildStrategyNotFound)
		})
	})

	t.Run("webhook validation", func(t *testing.T) {
		t.Run("reports not ready when secret missing", func(t *testing.T) {
			ib := newWebhookImageBuild(t)
			r, c := newReconciler(t, ib)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, errorRequeueInterval, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretMissing)
		})

		t.Run("reports not ready when secret missing required key", func(t *testing.T) {
			ib := newWebhookImageBuild(t)

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testWebhookSecretName,
					Namespace: ib.Namespace,
				},
				Data: map[string][]byte{
					testWrongSecretKey: []byte(testTokenValue),
				},
			}

			r, c := newReconciler(t, ib, secret)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, errorRequeueInterval, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretInvalidKey)
		})
	})

	t.Run("status tracking", func(t *testing.T) {
		t.Run("tracks observed generation and associated build", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}
			r, c := newReconciler(t, ib, policy, strategy)

			requireReconcile(t, ctx, r, ib)

			latest := requireImageBuild(t, ctx, c, ib)
			require.Equal(t, ib.Generation, latest.Status.ObservedGeneration)
			require.Equal(t, buildNameFor(ib), latest.Status.BuildRef)
		})

		t.Run("reflects spec changes across reconciliations", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			ib.Generation = 1

			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			r, c := newReconciler(t, ib, policy, strategy)

			requireReconcile(t, ctx, r, ib)

			latest := requireImageBuild(t, ctx, c, ib)
			require.Equal(t, int64(1), latest.Status.ObservedGeneration)
			require.Equal(t, buildNameFor(ib), latest.Status.BuildRef)

			latest.Generation = 2
			latest.Spec.Source.Git.Revision = testRevisionV2
			require.NoError(t, c.Update(ctx, latest))

			requireReconcile(t, ctx, r, latest)

			latest = requireImageBuild(t, ctx, c, latest)
			require.Equal(t, int64(2), latest.Status.ObservedGeneration)
			require.Equal(t, buildNameFor(latest), latest.Status.BuildRef)

			readyCond := meta.FindStatusCondition(latest.Status.Conditions, TypeReady)
			require.NotNil(t, readyCond)
			require.Equal(t, int64(2), readyCond.ObservedGeneration)
		})
	})

	t.Run("failure scenarios", func(t *testing.T) {
		t.Run("no-ops when ImageBuild is deleted", func(t *testing.T) {
			r, _ := newReconciler(t)

			res, err := r.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "ib-" + t.Name(), Namespace: "ns-" + t.Name()},
			})
			require.NoError(t, err)
			require.Equal(t, ctrl.Result{}, res)
		})

		t.Run("reports not ready when Build owned by another ImageBuild", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			otherImageBuild := &buildv1alpha1.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-ib",
					Namespace: ib.Namespace,
					UID:       types.UID("other-uid"),
				},
			}
			conflictingBuild := &shipwright.Build{
				ObjectMeta: metav1.ObjectMeta{
					Name:      buildNameFor(ib),
					Namespace: ib.Namespace,
				},
			}
			require.NoError(t, controllerutil.SetControllerReference(otherImageBuild, conflictingBuild, newScheme(t)))

			r, c := newReconciler(t, ib, policy, strategy, conflictingBuild)

			res := requireReconcile(t, ctx, r, ib)
			require.Zero(t, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildConflict)
		})

		t.Run("reports not ready when on-commit rebuild fails", func(t *testing.T) {
			policy := newImageBuildPolicy()
			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
			ib.Spec.OnCommit = &buildv1alpha1.ImageBuildOnCommit{
				WebhookSecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: testWebhookSecretName},
					Key:                  testWebhookSecretKey,
				},
			}
			ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
				Pending: &buildv1alpha1.ImageBuildOnCommitEvent{CommitSHA: "abc123"},
			}

			webhookSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testWebhookSecretName,
					Namespace: ib.Namespace,
				},
				Data: map[string][]byte{
					testWebhookSecretKey: []byte(testTokenValue),
				},
			}

			otherImageBuild := &buildv1alpha1.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-ib",
					Namespace: ib.Namespace,
					UID:       types.UID("other-uid"),
				},
			}
			conflictingBuildRun := &shipwright.BuildRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-buildrun-oncommit-%d", ib.Name, 1),
					Namespace: ib.Namespace,
				},
			}
			require.NoError(t, controllerutil.SetControllerReference(otherImageBuild, conflictingBuildRun, newScheme(t)))

			r, c := newReconciler(t, ib, policy, strategy, webhookSecret, conflictingBuildRun)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, errorRequeueInterval, res.RequeueAfter)

			latest := requireImageBuild(t, ctx, c, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildRunReconcileFailed)
		})
	})

	t.Run("build progression", func(t *testing.T) {
		t.Run("records built image after successful build", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			successfulBuildRun := &shipwright.BuildRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "successful-br",
					Namespace: ib.Namespace,
				},
			}
			successfulBuildRun.Status.SetCondition(&shipwright.Condition{
				Type:   shipwright.Succeeded,
				Status: corev1.ConditionTrue,
			})
			successfulBuildRun.Status.Output = &shipwright.Output{Digest: "sha256:abc123"}
			require.NoError(t, controllerutil.SetControllerReference(ib, successfulBuildRun, newScheme(t)))

			ib.Status.LastBuildRunRef = successfulBuildRun.Name
			require.NoError(t, (&Reconciler{}).recordBuildSpec(ib))

			r, c := newReconciler(t, ib, policy, strategy, successfulBuildRun)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, ctrl.Result{}, res)

			latest := requireImageBuild(t, ctx, c, ib)
			require.Equal(t, ib.Spec.Output.Image+"@sha256:abc123", latest.Status.LatestImage)
		})

		t.Run("requeues while build is running", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			runningBuildRun := &shipwright.BuildRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "running-br",
					Namespace: ib.Namespace,
				},
			}
			runningBuildRun.Status.SetCondition(&shipwright.Condition{
				Type:   shipwright.Succeeded,
				Status: corev1.ConditionUnknown,
			})
			require.NoError(t, controllerutil.SetControllerReference(ib, runningBuildRun, newScheme(t)))

			ib.Status.LastBuildRunRef = runningBuildRun.Name
			require.NoError(t, (&Reconciler{}).recordBuildSpec(ib))

			r, _ := newReconciler(t, ib, policy, strategy, runningBuildRun)

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, buildRunPollInterval, res.RequeueAfter)
		})
	})
}

func newWebhookImageBuild(t *testing.T) *buildv1alpha1.ImageBuild {
	t.Helper()

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
	return ib
}

func requireReconcile(t *testing.T, ctx context.Context, r *Reconciler, ib *buildv1alpha1.ImageBuild) ctrl.Result {
	t.Helper()

	res, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      ib.Name,
			Namespace: ib.Namespace,
		},
	})
	require.NoError(t, err)
	return res
}

func requireImageBuild(t *testing.T, ctx context.Context, c client.Client, ib *buildv1alpha1.ImageBuild) *buildv1alpha1.ImageBuild {
	t.Helper()

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	return latest
}

func TestEnsureBuildRun(t *testing.T) {
	ctx := context.Background()

	t.Run("reports not ready when BuildRun owned by another ImageBuild", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

		otherImageBuild := &buildv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-ib",
				Namespace: ib.Namespace,
				UID:       types.UID("other-uid"),
			},
		}
		conflictingBuildRun := newBuildRun(ib, nextBuildRunCounter(ib))
		require.NoError(t, controllerutil.SetControllerReference(otherImageBuild, conflictingBuildRun, newScheme(t)))

		r, c := newReconciler(t, ib, conflictingBuildRun)

		br, result, err := r.ensureBuildRun(ctx, ib)
		require.Nil(t, br)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Zero(t, result.RequeueAfter)

		latest := requireImageBuild(t, ctx, c, ib)
		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildRunConflict)
	})

	t.Run("reuses last BuildRun when spec unchanged", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = "existing-br"

		lastBuildRun := &shipwright.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-br",
				Namespace: ib.Namespace,
			},
		}

		r, _ := newReconciler(t, ib, lastBuildRun)
		require.NoError(t, r.recordBuildSpec(ib))

		br, result, err := r.ensureBuildRun(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, result)
		require.NotNil(t, br)
		require.Equal(t, lastBuildRun.Name, br.Name)
	})

	t.Run("tolerates deleted BuildRun gracefully", func(t *testing.T) {
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Status.LastBuildRunRef = "deleted-br"

		r, _ := newReconciler(t, ib)
		require.NoError(t, r.recordBuildSpec(ib))

		br, result, err := r.ensureBuildRun(ctx, ib)
		require.NoError(t, err)
		require.Nil(t, result)
		require.Nil(t, br)
	})
}

func TestMapSecretToImageBuilds(t *testing.T) {
	ctx := context.Background()

	t.Run("ignores secrets not referenced by any ImageBuild", func(t *testing.T) {
		c := newClientWithSecretIndexes(t)
		r := &Reconciler{Client: c, Scheme: newScheme(t)}
		handler := r.mapSecretToImageBuilds()
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()

		unreferencedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unreferenced-secret",
				Namespace: "default",
			},
		}
		handler.Create(ctx, event.CreateEvent{Object: unreferencedSecret}, queue)

		require.Zero(t, queue.Len())
	})

	t.Run("enqueues ImageBuild matching secret index", func(t *testing.T) {
		const secretName = "push-secret"
		const namespace = "ns"
		const imageBuildName = "ib"

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
		}

		ibPush := newImageBuild(imageBuildName, namespace)
		ibPush.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: secretName}

		c := newClientWithSecretIndexes(t, secret, ibPush)
		r := &Reconciler{Client: c, Scheme: newScheme(t)}
		handler := r.mapSecretToImageBuilds()
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()

		handler.Create(ctx, event.CreateEvent{Object: secret}, queue)

		require.Equal(t, 1, queue.Len())
		req, _ := queue.Get()
		queue.Done(req)
		require.Equal(t, namespace+"/"+imageBuildName, req.String(), "expected ImageBuild to be enqueued")
	})
}
