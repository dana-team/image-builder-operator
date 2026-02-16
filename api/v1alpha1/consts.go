package v1alpha1

// Label and annotation keys for resources managed by this API group.
var (
	LabelKeyParentImageBuild = GroupVersion.Group + "/parent-imagebuild"
	LabelKeyOnCommitEnabled  = GroupVersion.Group + "/oncommit-enabled"
	LabelKeyBuildTrigger     = GroupVersion.Group + "/build-trigger"

	AnnotationKeyLastBuildSpec = GroupVersion.Group + "/last-build-spec"
)

// Predefined label values.
const (
	LabelValueBuildTriggerOnCommit = "oncommit"
)
