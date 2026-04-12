package imagebuild

import (
	"testing"
	"time"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestHasRetentionParams(t *testing.T) {
	t.Run("reports false when retention is nil", func(t *testing.T) {
		require.False(t, hasRetentionParams(nil))
	})

	t.Run("reports false when no fields are set", func(t *testing.T) {
		require.False(t, hasRetentionParams(&buildv1alpha1.Retention{}))
	})

	t.Run("reports true when a field is set", func(t *testing.T) {
		cases := []struct {
			name string
			r    *buildv1alpha1.Retention
		}{
			{"failedLimit", &buildv1alpha1.Retention{FailedLimit: ptr.To(int32(1))}},
			{"succeededLimit", &buildv1alpha1.Retention{SucceededLimit: ptr.To(int32(2))}},
			{"ttlAfterFailed", &buildv1alpha1.Retention{TTLAfterFailed: &metav1.Duration{Duration: time.Minute}}},
			{"ttlAfterSucceeded", &buildv1alpha1.Retention{TTLAfterSucceeded: &metav1.Duration{Duration: time.Hour}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				require.True(t, hasRetentionParams(tc.r))
			})
		}
	})
}

func TestResolveRetention(t *testing.T) {
	policyRetention := &buildv1alpha1.Retention{SucceededLimit: ptr.To(int32(5))}

	t.Run("yields nil when policy is nil and ImageBuild omits retention", func(t *testing.T) {
		require.Nil(t, resolveRetention(newImageBuild("ib", "ns"), nil))
	})

	t.Run("yields nil when policy omits retention and ImageBuild omits retention", func(t *testing.T) {
		require.Nil(t, resolveRetention(newImageBuild("ib", "ns"), newImageBuildPolicy()))
	})

	t.Run("yields nil when policy retention is empty and ImageBuild omits retention", func(t *testing.T) {
		policy := newImageBuildPolicy()
		policy.Spec.Retention = &buildv1alpha1.Retention{}
		require.Nil(t, resolveRetention(newImageBuild("ib", "ns"), policy))
	})

	t.Run("yields nil when ImageBuild has empty retention object despite policy defaults", func(t *testing.T) {
		ib := newImageBuild("ib", "ns")
		ib.Spec.Retention = &buildv1alpha1.Retention{}
		policy := newImageBuildPolicy()
		policy.Spec.Retention = policyRetention
		require.Nil(t, resolveRetention(ib, policy))
	})

	t.Run("uses policy retention when ImageBuild omits retention", func(t *testing.T) {
		policy := newImageBuildPolicy()
		policy.Spec.Retention = policyRetention
		resolved := resolveRetention(newImageBuild("ib", "ns"), policy)
		require.Same(t, policy.Spec.Retention, resolved)
	})

	t.Run("uses ImageBuild retention over policy when both set", func(t *testing.T) {
		ib := newImageBuild("ib", "ns")
		ib.Spec.Retention = &buildv1alpha1.Retention{FailedLimit: ptr.To(int32(9))}
		policy := newImageBuildPolicy()
		policy.Spec.Retention = policyRetention
		resolved := resolveRetention(ib, policy)
		require.Same(t, ib.Spec.Retention, resolved)
	})
}

func TestNewBuildRetention(t *testing.T) {
	t.Run("returns nil when retention is nil", func(t *testing.T) {
		require.Nil(t, newBuildRetention(nil))
	})

	t.Run("maps set fields and leaves others unset", func(t *testing.T) {
		d := &metav1.Duration{Duration: 30 * time.Minute}
		buildRetention := newBuildRetention(&buildv1alpha1.Retention{
			FailedLimit:       ptr.To(int32(3)),
			TTLAfterSucceeded: d,
		})
		require.NotNil(t, buildRetention)
		require.NotNil(t, buildRetention.FailedLimit)
		require.Equal(t, uint(3), *buildRetention.FailedLimit)
		require.Nil(t, buildRetention.SucceededLimit)
		require.Nil(t, buildRetention.TTLAfterFailed)
		require.NotNil(t, buildRetention.TTLAfterSucceeded)
		require.Equal(t, *d, *buildRetention.TTLAfterSucceeded)
	})
}
