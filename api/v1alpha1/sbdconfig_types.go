/*
Copyright 2025.

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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Constants for SBDConfig validation and defaults
const (
	// DefaultStaleNodeTimeout is the default timeout for considering nodes stale
	DefaultStaleNodeTimeout = 10 * time.Minute
	// MinStaleNodeTimeout is the minimum allowed stale node timeout
	MinStaleNodeTimeout = 1 * time.Minute
	// MaxStaleNodeTimeout is the maximum allowed stale node timeout
	MaxStaleNodeTimeout = 24 * time.Hour
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"
)

// SBDConfigSpec defines the desired state of SBDConfig.
type SBDConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// SbdWatchdogPath is the path to the watchdog device on the host
	// If not specified, defaults to "/dev/watchdog"
	// +kubebuilder:default="/dev/watchdog"
	// +optional
	SbdWatchdogPath string `json:"sbdWatchdogPath,omitempty"`

	// Image is the container image for the SBD agent DaemonSet
	// +kubebuilder:default="sbd-agent:latest"
	Image string `json:"image,omitempty"`

	// Namespace is the namespace where the SBD agent DaemonSet will be deployed
	// +kubebuilder:default="sbd-system"
	Namespace string `json:"namespace,omitempty"`

	// StaleNodeTimeout defines how long to wait before considering a node stale and removing it from slot mapping
	// This timeout determines when inactive nodes are cleaned up from the shared SBD device slot assignments.
	// Nodes that haven't updated their heartbeat within this duration will be considered stale and their slots
	// will be freed for reuse by new nodes. The value must be at least 1 minute.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:default="10m"
	// +optional
	StaleNodeTimeout *metav1.Duration `json:"staleNodeTimeout,omitempty"`
}

// GetSbdWatchdogPath returns the watchdog path with default fallback
func (s *SBDConfigSpec) GetSbdWatchdogPath() string {
	if s.SbdWatchdogPath != "" {
		return s.SbdWatchdogPath
	}
	return DefaultWatchdogPath
}

// GetStaleNodeTimeout returns the stale node timeout with default fallback
func (s *SBDConfigSpec) GetStaleNodeTimeout() time.Duration {
	if s.StaleNodeTimeout != nil {
		return s.StaleNodeTimeout.Duration
	}
	return DefaultStaleNodeTimeout
}

// ValidateStaleNodeTimeout validates the stale node timeout value
func (s *SBDConfigSpec) ValidateStaleNodeTimeout() error {
	timeout := s.GetStaleNodeTimeout()

	if timeout < MinStaleNodeTimeout {
		return fmt.Errorf("stale node timeout %v is less than minimum %v", timeout, MinStaleNodeTimeout)
	}

	if timeout > MaxStaleNodeTimeout {
		return fmt.Errorf("stale node timeout %v is greater than maximum %v", timeout, MaxStaleNodeTimeout)
	}

	return nil
}

// SBDConfigStatus defines the observed state of SBDConfig.
type SBDConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// DaemonSetReady indicates whether the SBD agent DaemonSet is ready
	DaemonSetReady bool `json:"daemonSetReady,omitempty"`

	// ReadyNodes is the number of nodes where the SBD agent is ready
	ReadyNodes int32 `json:"readyNodes,omitempty"`

	// TotalNodes is the total number of nodes where the SBD agent should be deployed
	TotalNodes int32 `json:"totalNodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// SBDConfig is the Schema for the sbdconfigs API.
type SBDConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SBDConfigSpec   `json:"spec,omitempty"`
	Status SBDConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SBDConfigList contains a list of SBDConfig.
type SBDConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SBDConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SBDConfig{}, &SBDConfigList{})
}
