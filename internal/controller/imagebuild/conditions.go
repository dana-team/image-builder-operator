package imagebuild

const (
	TypeReady          = "Ready"
	TypeBuildSucceeded = "BuildSucceeded"

	ReasonBuildReconcileFailed  = "BuildReconcileFailed"
	ReasonBuildConflict         = "BuildConflict"
	ReasonBuildStrategyNotFound = "BuildStrategyNotFound"
	ReasonMissingPolicy         = "MissingPolicy"
	ReasonReconciled            = "Reconciled"

	ReasonBuildRunReconcileFailed = "BuildRunReconcileFailed"
	ReasonBuildRunConflict        = "BuildRunConflict"
	ReasonBuildRunPending         = "BuildRunPending"
	ReasonBuildRunRunning         = "BuildRunRunning"
	ReasonBuildRunSucceeded       = "BuildRunSucceeded"
	ReasonBuildRunFailed          = "BuildRunFailed"

	ReasonOnCommitDisabled        = "OnCommitDisabled"
	ReasonWebhookSecretMissing    = "WebhookSecretMissing"
	ReasonWebhookSecretInvalidKey = "WebhookSecretInvalidKey"
	ReasonOnCommitBuildTriggered  = "OnCommitBuildTriggered"
)
