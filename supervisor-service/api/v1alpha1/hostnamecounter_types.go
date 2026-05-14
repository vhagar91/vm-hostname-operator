package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// HostnameCounter is the Schema for tracking hostname generation counters.
//
// Each HostnameCounter tracks the index for a specific template pattern,
// ensuring unique sequential hostnames across VM creations.
type HostnameCounter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostnameCounterSpec   `json:"spec,omitempty"`
	Status HostnameCounterStatus `json:"status,omitempty"`
}

// HostnameCounterSpec defines the desired state of HostnameCounter.
type HostnameCounterSpec struct {
	// Template is the hostname template pattern.
	// '#' characters are replaced with zero-padded digits.
	// Example: "vm-###" -> "vm-001", "vm-002"
	// Example: "node#" -> "node1", "node2"
	// The number of '#' characters determines the digit width.
	Template string `json:"template"`

	// CurrentIndex is the current counter value.
	// The next generated hostname will use this index.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	CurrentIndex int `json:"currentIndex,omitempty"`

	// NamespaceScope restricts this counter to a specific namespace.
	// If empty, the counter applies cluster-wide.
	// +optional
	NamespaceScope string `json:"namespaceScope,omitempty"`

	// Locked prevents concurrent counter updates.
	// Set to true while a hostname generation is in progress.
	// +kubebuilder:default=false
	Locked bool `json:"locked,omitempty"`

	// LockedAt is the timestamp when the counter was locked.
	// +optional
	LockedAt *metav1.Time `json:"lockedAt,omitempty"`
}

// HostnameCounterStatus defines the observed state of HostnameCounter.
type HostnameCounterStatus struct {
	// LastGenerated is the timestamp of the last hostname generation.
	// +optional
	LastGenerated *metav1.Time `json:"lastGenerated,omitempty"`

	// LastHostname is the last hostname that was generated.
	// +optional
	LastHostname string `json:"lastHostname,omitempty"`

	// GenerationCount is the total number of hostnames generated.
	// +optional
	GenerationCount int `json:"generationCount,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// HostnameCounterList contains a list of HostnameCounter.
type HostnameCounterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostnameCounter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostnameCounter{}, &HostnameCounterList{})
}