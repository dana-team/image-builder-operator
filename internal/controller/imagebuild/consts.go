package imagebuild

import buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"

var apiGroup = buildv1alpha1.GroupVersion.Group

// Label and annotation keys used on resources managed by this controller.
var (
	labelKeyParentImageBuild = apiGroup + "/parent-imagebuild"
	labelKeyOnCommitEnabled  = apiGroup + "/oncommit-enabled"
	labelKeyBuildTrigger     = apiGroup + "/build-trigger"

	annotationKeyLastBuildSpec = apiGroup + "/last-build-spec"
)
