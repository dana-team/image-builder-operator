/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ImageBuildSourceType string

const (
	ImageBuildSourceTypeGit ImageBuildSourceType = "Git"
)

type ImageBuildRebuildMode string

const (
	ImageBuildRebuildModeInitial  ImageBuildRebuildMode = "Initial"
	ImageBuildRebuildModeOnCommit ImageBuildRebuildMode = "OnCommit"
)

type ImageBuildFileMode string

const (
	ImageBuildFileModePresent ImageBuildFileMode = "Present"
	ImageBuildFileModeAbsent  ImageBuildFileMode = "Absent"
)

type ImageBuildFile struct {
	// +kubebuilder:validation:Enum=Present;Absent
	// Mode selects whether the source is expected to contain a Dockerfile/Containerfile.
	// Present: use a buildfile-based strategy; Absent: use a non-buildfile-based strategy.
	Mode ImageBuildFileMode `json:"mode"`
}

type ImageBuildSpec struct {
	// Source refers to the location where the source code is.
	Source ImageBuildSource `json:"source"`

	// BuildFile indicates whether the source should be built using a buildfile-based or non-buildfile-based strategy.
	BuildFile ImageBuildFile `json:"buildFile"`

	// +optional
	// Rebuild controls rebuild behavior.
	Rebuild *ImageBuildRebuild `json:"rebuild,omitempty"`

	// Output refers to the location where the built image would be pushed.
	Output ImageBuildOutput `json:"output"`

	// +optional
	// OnCommit configures webhook-triggered rebuilds.
	OnCommit *ImageBuildOnCommit `json:"onCommit,omitempty"`
}

type ImageBuildSource struct {
	// +kubebuilder:validation:Enum=Git
	// Type is the type of source code used as input for the build.
	// Supported values: "Git".
	Type ImageBuildSourceType `json:"type"`

	// Git contains the details for obtaining source code from a git repository.
	Git ImageBuildGitSource `json:"git"`

	// +optional
	// ContextDir is a path to a subdirectory within the source code that should
	// be used as the build root directory.
	ContextDir string `json:"contextDir,omitempty"`
}

type ImageBuildGitSource struct {
	// +kubebuilder:validation:MinLength=1
	// URL describes the URL of the Git repository.
	URL string `json:"url"`

	// +optional
	// Revision describes the Git revision (e.g., branch, tag, commit SHA, etc.)
	// to fetch.
	Revision string `json:"revision,omitempty"`

	// +optional
	// CloneSecret references a Secret that contains credentials to access the
	// repository.
	CloneSecret *corev1.LocalObjectReference `json:"cloneSecret,omitempty"`
}

type ImageBuildRebuild struct {
	// +kubebuilder:validation:Enum=Initial;OnCommit
	// Mode selects the rebuild strategy.
	Mode ImageBuildRebuildMode `json:"mode"`
}

type ImageBuildOutput struct {
	// Image is the reference of the image.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// +optional
	// Describes the secret name for pushing a container image.
	PushSecret *corev1.LocalObjectReference `json:"pushSecret,omitempty"`
}

type ImageBuildOnCommit struct {
	// WebhookSecretRef references the Secret used to verify webhook requests.
	WebhookSecretRef corev1.SecretKeySelector `json:"webhookSecretRef"`
}

type ImageBuildStatus struct {
	// +optional
	// ObservedGeneration is the .metadata.generation last processed by the
	// controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// BuildRef references the associated Build.
	BuildRef string `json:"buildRef,omitempty"`

	// +optional
	// Conditions represent the latest available observations of the ImageBuild's
	// state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// LatestImage is the last successfully produced image reference.
	LatestImage string `json:"latestImage,omitempty"`

	// +optional
	// LastBuildRunRef is a reference to the last BuildRun
	// created for this ImageBuild.
	LastBuildRunRef string `json:"lastBuildRunRef,omitempty"`

	// +optional
	// BuildRunCounter is incremented each time a BuildRun is created and is
	// used to generate deterministic BuildRun names.
	BuildRunCounter int64 `json:"buildRunCounter,omitempty"`

	// +optional
	// OnCommit stores on-commit trigger state.
	OnCommit *ImageBuildOnCommitStatus `json:"onCommit,omitempty"`
}

type ImageBuildOnCommitEvent struct {
	// Ref is the git ref from the webhook payload.
	// +optional
	Ref string `json:"ref,omitempty"`

	// CommitSHA is the commit SHA from the webhook payload.
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// ReceivedAt is when the webhook was received.
	// +optional
	ReceivedAt metav1.Time `json:"receivedAt,omitempty"`
}

type ImageBuildOnCommitLastTriggered struct {
	// Name is the name of the last BuildRun created from an on-commit trigger.
	// +optional
	Name string `json:"name,omitempty"`

	// CommitSHA is the commit SHA that triggered the last BuildRun.
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// TriggeredAt is when the last BuildRun was created from an on-commit trigger.
	// +optional
	TriggeredAt metav1.Time `json:"triggeredAt,omitempty"`
}

type ImageBuildOnCommitStatus struct {
	// LastReceived is the last received webhook event.
	// +optional
	LastReceived *ImageBuildOnCommitEvent `json:"lastReceived,omitempty"`

	// Pending is the latest pending on-commit trigger.
	// +optional
	Pending *ImageBuildOnCommitEvent `json:"pending,omitempty"`

	// LastTriggeredBuildRun references the last BuildRun created from an on-commit trigger.
	// +optional
	LastTriggeredBuildRun *ImageBuildOnCommitLastTriggered `json:"lastTriggeredBuildRun,omitempty"`

	// TriggerCounter is used to derive deterministic BuildRun names for on-commit triggers.
	// +optional
	TriggerCounter int64 `json:"triggerCounter,omitempty"`
}

// ImageBuild is the Schema for the ImageBuilds API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ib
type ImageBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the ImageBuild.
	Spec ImageBuildSpec `json:"spec,omitempty"`
	// Status defines the observed state of the ImageBuild.
	Status ImageBuildStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ImageBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageBuild `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageBuild{}, &ImageBuildList{})
}
