package imagebuild

import (
	"context"
	"testing"
	"time"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

func TestReconcileBuild(t *testing.T) {
	ctx := context.Background()

	t.Run("with missing dependencies", func(t *testing.T) {
		t.Run("reports not ready when policy missing", func(t *testing.T) {
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			r, c := newReconciler(t, ib)

			res := requireReconcile(t, ctx, r, ib)
			require.Greater(t, res.RequeueAfter, time.Duration(0))

			latest := requireImageBuild(t, c, ctx, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonMissingPolicy)
		})

		t.Run("requeues when strategy unavailable", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			r, c := newReconciler(t, ib, policy)

			res := requireReconcile(t, ctx, r, ib)
			require.Greater(t, res.RequeueAfter, time.Duration(0))

			latest := requireImageBuild(t, c, ctx, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildStrategyNotFound)
		})

		t.Run("fails gracefully when Build owned by another ImageBuild", func(t *testing.T) {
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

			res := requireReconcile(t, ctx, r, ib)
			require.Equal(t, time.Duration(0), res.RequeueAfter)

			latest := requireImageBuild(t, c, ctx, ib)
			requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonBuildConflict)
		})
	})

	t.Run("with all dependencies available", func(t *testing.T) {
		t.Run("reconciles new ImageBuild to Ready", func(t *testing.T) {
			ctx, r, c, ib := setupReconciler(t)

			requireReconcile(t, ctx, r, ib)

			latest := requireStatus(t, c, ctx, ib, ib.Generation, buildNameFor(ib))
			requireBuild(t, c, ctx, latest, absentStrategy)
		})

		t.Run("corrects Build spec drift on reconcile", func(t *testing.T) {
			ctx, r, c, ib := setupReconciler(t)

			requireReconcile(t, ctx, r, ib)

			build := requireBuild(t, c, ctx, ib, absentStrategy)
			require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL)

			build.Spec.Source.Git.URL = "https://drifted-url.com"
			require.NoError(t, c.Update(ctx, build))

			requireReconcile(t, ctx, r, ib)

			build = requireBuild(t, c, ctx, ib, absentStrategy)
			require.Equal(t, ib.Spec.Source.Git.URL, build.Spec.Source.Git.URL, "Build spec should be corrected")
		})

		t.Run("propagates all source and output configuration to Build", func(t *testing.T) {
			const (
				testRevision    = "v1.2.3"
				testContextDir  = "backend/api"
				testCloneSecret = "git-clone-secret"
				testPushSecret  = "registry-push-secret"
			)

			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
			ib.Spec.Source.Git.Revision = testRevision
			ib.Spec.Source.ContextDir = testContextDir
			ib.Spec.Source.Git.CloneSecret = &corev1.LocalObjectReference{Name: testCloneSecret}
			ib.Spec.Output.PushSecret = &corev1.LocalObjectReference{Name: testPushSecret}

			strategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}
			r, c := newReconciler(t, ib, policy, strategy)

			requireReconcile(t, ctx, r, ib)

			build := requireBuild(t, c, ctx, ib, absentStrategy)

			require.NotNil(t, build.Spec.Source.Git.Revision)
			require.Equal(t, testRevision, *build.Spec.Source.Git.Revision)

			require.NotNil(t, build.Spec.Source.ContextDir)
			require.Equal(t, testContextDir, *build.Spec.Source.ContextDir)

			require.NotNil(t, build.Spec.Source.Git.CloneSecret)
			require.Equal(t, testCloneSecret, *build.Spec.Source.Git.CloneSecret)

			require.NotNil(t, build.Spec.Output.PushSecret)
			require.Equal(t, testPushSecret, *build.Spec.Output.PushSecret)
		})

		t.Run("ensures labels exist on existing Build", func(t *testing.T) {
			policy := newImageBuildPolicy()
			ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())

			clusterBuildStrategy := &shipwright.ClusterBuildStrategy{
				ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
			}

			kind := shipwright.ClusterBuildStrategyKind
			existingBuild := &shipwright.Build{
				ObjectMeta: metav1.ObjectMeta{
					Name:      buildNameFor(ib),
					Namespace: ib.Namespace,
					Labels:    nil,
				},
				Spec: shipwright.BuildSpec{
					Strategy: shipwright.Strategy{
						Name: absentStrategy,
						Kind: &kind,
					},
					Source: &shipwright.Source{
						Type: shipwright.GitType,
						Git:  &shipwright.Git{URL: ib.Spec.Source.Git.URL},
					},
					Output: shipwright.Image{Image: ib.Spec.Output.Image},
				},
			}
			require.NoError(t, controllerutil.SetControllerReference(ib, existingBuild, testScheme(t)))

			r, c := newReconciler(t, ib, policy, clusterBuildStrategy, existingBuild)

			requireReconcile(t, ctx, r, ib)

			build := requireBuild(t, c, ctx, ib, absentStrategy)
			require.NotNil(t, build.Labels)
			require.Equal(t, ib.Name, build.Labels["build.dana.io/parent-imagebuild"])
		})
	})
}

func TestEnsureWebhookSecret(t *testing.T) {
	ctx := context.Background()

	setupWebhookImageBuild := func(t *testing.T) *buildv1alpha1.ImageBuild {
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

	t.Run("reports not ready when webhook secret missing", func(t *testing.T) {
		ib := setupWebhookImageBuild(t)
		r, c := newReconciler(t, ib)

		res := requireReconcile(t, ctx, r, ib)
		require.Greater(t, res.RequeueAfter, time.Duration(0))

		latest := requireImageBuild(t, c, ctx, ib)
		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretMissing)
	})

	t.Run("reports not ready when webhook secret missing required key", func(t *testing.T) {
		ib := setupWebhookImageBuild(t)

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

		res := requireReconcile(t, ctx, r, ib)
		require.Greater(t, res.RequeueAfter, time.Duration(0))

		latest := requireImageBuild(t, c, ctx, ib)
		requireCondition(t, latest.Status.Conditions, TypeReady, metav1.ConditionFalse, ReasonWebhookSecretInvalidKey)
	})
}

func TestObservedGeneration(t *testing.T) {
	ctx := context.Background()

	t.Run("tracks spec updates across reconciliations", func(t *testing.T) {
		policy := newImageBuildPolicy()
		ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
		ib.Generation = 1

		clusterBuildStrategy := &shipwright.ClusterBuildStrategy{
			ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
		}

		r, c := newReconciler(t, ib, policy, clusterBuildStrategy)

		requireReconcile(t, ctx, r, ib)

		latest := requireStatus(t, c, ctx, ib, 1, buildNameFor(ib))

		latest.Generation = 2
		latest.Spec.Source.Git.Revision = "v2.0.0"
		require.NoError(t, c.Update(ctx, latest))

		requireReconcile(t, ctx, r, latest)

		latest = requireStatus(t, c, ctx, latest, 2, buildNameFor(latest))

		readyCond := meta.FindStatusCondition(latest.Status.Conditions, TypeReady)
		require.NotNil(t, readyCond)
		require.Equal(t, int64(2), readyCond.ObservedGeneration)
	})
}

func setupReconciler(t *testing.T) (context.Context, *ImageBuildReconciler, client.Client, *buildv1alpha1.ImageBuild) {
	t.Helper()

	ctx := context.Background()
	policy := newImageBuildPolicy()
	ib := newImageBuild("ib-"+t.Name(), "ns-"+t.Name())
	strategy := &shipwright.ClusterBuildStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: absentStrategy},
	}
	r, c := newReconciler(t, ib, policy, strategy)
	return ctx, r, c, ib
}

func requireReconcile(t *testing.T, ctx context.Context, r *ImageBuildReconciler, ib *buildv1alpha1.ImageBuild) ctrl.Result {
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

func requireStatus(
	t *testing.T,
	c client.Client,
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	wantObservedGen int64,
	wantBuildRef string,
) *buildv1alpha1.ImageBuild {
	t.Helper()

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	require.Equal(t, wantObservedGen, latest.Status.ObservedGeneration)
	require.Equal(t, wantBuildRef, latest.Status.BuildRef)
	return latest
}

func requireBuild(
	t *testing.T,
	c client.Client,
	ctx context.Context,
	ib *buildv1alpha1.ImageBuild,
	wantStrategy string,
) *shipwright.Build {
	t.Helper()

	build := &shipwright.Build{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{
		Name:      buildNameFor(ib),
		Namespace: ib.Namespace,
	}, build))
	require.True(t, metav1.IsControlledBy(build, ib))
	require.Equal(t, wantStrategy, build.Spec.Strategy.Name)
	return build
}

func requireImageBuild(t *testing.T, c client.Client, ctx context.Context, ib *buildv1alpha1.ImageBuild) *buildv1alpha1.ImageBuild {
	t.Helper()

	latest := &buildv1alpha1.ImageBuild{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ib), latest))
	return latest
}
