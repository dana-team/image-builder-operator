package imagebuild

import (
	"context"
	"fmt"
	"testing"
	"time"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestTriggerNaming(t *testing.T) {
	ib := newImageBuild("ib", "ns")
	ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
	ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
		Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: "refs/heads/main", CommitSHA: "abc"},
	}

	policy := newImageBuildPolicy()
	r, _ := newReconciler(t, policy, ib)

	br, requeue, err := r.triggerBuildRun(context.Background(), ib)
	require.NoError(t, err)
	require.Nil(t, requeue)
	require.NotNil(t, br)
	require.Equal(t, fmt.Sprintf("%s-buildrun-oncommit-1", ib.Name), br.Name)
	require.Equal(t, "oncommit", br.Labels["build.dana.io/build-trigger"])
}

func TestTriggerActiveBuild(t *testing.T) {
	ib := newImageBuild("ib", "ns")
	ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
	ib.Status.LastBuildRunRef = "active-br"
	ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
		Pending: &buildv1alpha1.ImageBuildOnCommitEvent{Ref: "refs/heads/main", CommitSHA: "abc"},
	}

	activeBR := &shipwright.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "active-br", Namespace: ib.Namespace},
	}
	require.NoError(t, controllerutil.SetControllerReference(ib, activeBR, testScheme(t)))

	policy := newImageBuildPolicy()
	r, _ := newReconciler(t, policy, ib, activeBR)

	br, requeue, err := r.triggerBuildRun(context.Background(), ib)
	require.NoError(t, err)
	require.Nil(t, requeue)
	require.NotNil(t, br, "should return the active BuildRun for status mapping")
	require.Equal(t, activeBR.Name, br.Name)
}

func TestTriggerDebounce(t *testing.T) {
	now := time.Now()
	ib := newImageBuild("ib", "ns")
	ib.Spec.Rebuild = &buildv1alpha1.ImageBuildRebuild{Mode: buildv1alpha1.ImageBuildRebuildModeOnCommit}
	ib.Status.OnCommit = &buildv1alpha1.ImageBuildOnCommitStatus{
		Pending: &buildv1alpha1.ImageBuildOnCommitEvent{
			Ref:        "refs/heads/main",
			CommitSHA:  "abc",
			ReceivedAt: metav1.NewTime(now),
		},
	}

	policy := newImageBuildPolicy()
	r, _ := newReconciler(t, policy, ib)

	br, requeue, err := r.triggerBuildRun(context.Background(), ib)
	require.NoError(t, err)
	require.NotNil(t, requeue, "should requeue for debounce")
	require.Nil(t, br)
	require.True(t, *requeue > 0)
}
