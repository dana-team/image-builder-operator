package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Retention contains information about retention params.
type Retention struct {
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	// FailedLimit defines the maximum number of failed buildruns that should exist.
	FailedLimit *int32 `json:"failedLimit,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	// SucceededLimit defines the maximum number of succeeded buildruns that should exist.
	SucceededLimit *int32 `json:"succeededLimit,omitempty"`
	// +optional
	// +kubebuilder:validation:Format=duration
	// TTLAfterFailed defines the maximum duration of time the failed buildrun should exist.
	TTLAfterFailed *metav1.Duration `json:"ttlAfterFailed,omitempty"`
	// +optional
	// +kubebuilder:validation:Format=duration
	// TTLAfterSucceeded defines the maximum duration of time the succeeded buildrun should exist.
	TTLAfterSucceeded *metav1.Duration `json:"ttlAfterSucceeded,omitempty"`
}
