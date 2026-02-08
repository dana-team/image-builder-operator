/*
Copyright 2026.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageBuildPolicySpec defines the desired state of an ImageBuildPolicy.
type ImageBuildPolicySpec struct {
	// ClusterBuildStrategy holds platform defaults for selecting a build strategy.
	ClusterBuildStrategy ImageBuildClusterStrategy `json:"clusterBuildStrategy"`
}

// ImageBuildClusterStrategy holds platform defaults for selecting a ClusterBuildStrategy.
type ImageBuildClusterStrategy struct {
	// BuildFile holds strategy defaults for selecting a strategy based on whether a
	// build file indicator is present (e.g. Dockerfile, Containerfile).
	BuildFile ImageBuildFileStrategy `json:"buildFile"`
}

// ImageBuildFileStrategy defines which ClusterBuildStrategy to use based on the presence or absence of a build file.
type ImageBuildFileStrategy struct {
	// Present is the strategy name to use when the source indicates a file-based build.
	// +kubebuilder:validation:MinLength=1
	Present string `json:"present"`

	// Absent is the strategy name to use when the source does not indicate a file-based build.
	// +kubebuilder:validation:MinLength=1
	Absent string `json:"absent"`
}

// ImageBuildPolicyStatus defines the observed state of ImageBuildPolicy
type ImageBuildPolicyStatus struct{}

// ImageBuildPolicy is the Schema for the ImageBuildPolicies API.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ibp
type ImageBuildPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImageBuildPolicySpec   `json:"spec,omitempty"`
	Status ImageBuildPolicyStatus `json:"status,omitempty"`
}

// ImageBuildPolicyList contains a list of ImageBuildPolicy resources.
// +kubebuilder:object:root=true
type ImageBuildPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageBuildPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageBuildPolicy{}, &ImageBuildPolicyList{})
}
