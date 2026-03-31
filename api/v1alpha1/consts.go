package v1alpha1

// Label and annotation keys for resources managed by this API group.
var (
	LabelKeyParentImageBuild = GroupVersion.Group + "/parent-imagebuild"
	LabelKeyOnCommitEnabled  = GroupVersion.Group + "/oncommit-enabled"
	LabelKeyRebuildMode      = GroupVersion.Group + "/rebuild-mode"

	AnnotationKeyLastBuildSpec  = GroupVersion.Group + "/last-build-spec"
	AnnotationKeyRetryAttempted = GroupVersion.Group + "/retry-attempted"
)
