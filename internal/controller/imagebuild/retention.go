package imagebuild

import (
	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	"k8s.io/utils/ptr"
)

func hasRetentionParams(r *buildv1alpha1.Retention) bool {
	if r == nil {
		return false
	}
	return r.FailedLimit != nil || r.SucceededLimit != nil ||
		r.TTLAfterFailed != nil || r.TTLAfterSucceeded != nil
}

// resolveRetention chooses retention for the Build reconciled from the ImageBuild and policy, or nil when none applies.
func resolveRetention(ib *buildv1alpha1.ImageBuild, policy *buildv1alpha1.ImageBuildPolicy) *buildv1alpha1.Retention {
	if ib.Spec.Retention != nil {
		if !hasRetentionParams(ib.Spec.Retention) {
			return nil
		}
		return ib.Spec.Retention
	}
	if policy != nil && hasRetentionParams(policy.Spec.Retention) {
		return policy.Spec.Retention
	}
	return nil
}

// newBuildRetention translates API retention into the reconciled Build's retention spec, or nil when r is nil.
func newBuildRetention(r *buildv1alpha1.Retention) *shipwright.BuildRetention {
	if r == nil {
		return nil
	}
	retention := &shipwright.BuildRetention{}
	if r.FailedLimit != nil {
		retention.FailedLimit = ptr.To(uintFromNonNegativeInt32(*r.FailedLimit))
	}
	if r.SucceededLimit != nil {
		retention.SucceededLimit = ptr.To(uintFromNonNegativeInt32(*r.SucceededLimit))
	}
	if r.TTLAfterFailed != nil {
		retention.TTLAfterFailed = ptr.To(*r.TTLAfterFailed)
	}
	if r.TTLAfterSucceeded != nil {
		retention.TTLAfterSucceeded = ptr.To(*r.TTLAfterSucceeded)
	}
	return retention
}

func uintFromNonNegativeInt32(v int32) uint {
	if v < 0 {
		return 0
	}
	return uint(v)
}
