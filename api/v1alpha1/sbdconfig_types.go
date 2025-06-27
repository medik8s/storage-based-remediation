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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Constants for SBDConfig validation and defaults
const (
	// DefaultStaleNodeTimeout is the default timeout for considering nodes stale
	DefaultStaleNodeTimeout = 1 * time.Hour
	// MinStaleNodeTimeout is the minimum allowed stale node timeout
	MinStaleNodeTimeout = 1 * time.Minute
	// MaxStaleNodeTimeout is the maximum allowed stale node timeout
	MaxStaleNodeTimeout = 24 * time.Hour
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"
	// DefaultWatchdogTimeout is the default watchdog timeout duration
	DefaultWatchdogTimeout = 60 * time.Second
	// MinWatchdogTimeout is the minimum allowed watchdog timeout
	MinWatchdogTimeout = 10 * time.Second
	// MaxWatchdogTimeout is the maximum allowed watchdog timeout
	MaxWatchdogTimeout = 300 * time.Second
	// DefaultPetIntervalMultiple is the default multiple for calculating pet interval from watchdog timeout
	DefaultPetIntervalMultiple = 4
	// MinPetIntervalMultiple is the minimum allowed pet interval multiple
	MinPetIntervalMultiple = 3
	// MaxPetIntervalMultiple is the maximum allowed pet interval multiple
	MaxPetIntervalMultiple = 20
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
	// If not specified, defaults to sbd-agent from the same registry/org/tag as the operator
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy defines the pull policy for the SBD agent container image.
	// Valid values are Always, Never, and IfNotPresent.
	// Defaults to IfNotPresent for production stability.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default="IfNotPresent"
	// +optional
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"`

	// Namespace is the namespace where the SBD agent DaemonSet will be deployed
	// +kubebuilder:default="sbd-system"
	Namespace string `json:"namespace,omitempty"`

	// SharedStoragePVC is the name of a ReadWriteMany (RWX) PersistentVolumeClaim
	// that provides shared storage for coordination between SBD agents across nodes.
	// When specified, the controller will mount this PVC in the agent DaemonSet
	// and configure the sbd-agent to use it for cross-node coordination, slot assignment,
	// and shared configuration data. The PVC must exist in the same namespace as the SBDConfig.
	// +optional
	SharedStoragePVC string `json:"sharedStoragePVC,omitempty"`

	// SharedStorageMountPath is the path where the shared storage PVC will be mounted
	// in the sbd-agent containers. Defaults to "/shared-storage" if not specified.
	// This path will be passed to the sbd-agent via command line arguments.
	// +kubebuilder:default="/shared-storage"
	// +optional
	SharedStorageMountPath string `json:"sharedStorageMountPath,omitempty"`

	// StaleNodeTimeout defines how long to wait before considering a node stale and removing it from slot mapping
	// This timeout determines when inactive nodes are cleaned up from the shared SBD device slot assignments.
	// Nodes that haven't updated their heartbeat within this duration will be considered stale and their slots
	// will be freed for reuse by new nodes. The value must be at least 1 minute.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:default="1h"
	// +optional
	StaleNodeTimeout *metav1.Duration `json:"staleNodeTimeout,omitempty"`

	// WatchdogTimeout defines the watchdog timeout duration for the hardware/software watchdog device.
	// This determines how long the system will wait before triggering a reboot if the watchdog is not pet.
	// The pet interval is calculated as watchdog timeout divided by the pet interval multiple.
	// The value must be between 10 seconds and 300 seconds (5 minutes).
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:default="60s"
	// +optional
	WatchdogTimeout *metav1.Duration `json:"watchdogTimeout,omitempty"`

	// PetIntervalMultiple defines the multiple used to calculate the pet interval from the watchdog timeout.
	// Pet interval = watchdog timeout / pet interval multiple.
	// This ensures the pet interval is always shorter than the watchdog timeout with a safety margin.
	// The value must be between 3.0 and 20.0, with 4.0 providing a good balance of safety and efficiency.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:default=4
	// +optional
	PetIntervalMultiple *int32 `json:"petIntervalMultiple,omitempty"`
}

// GetSbdWatchdogPath returns the watchdog path with default fallback
func (s *SBDConfigSpec) GetSbdWatchdogPath() string {
	if s.SbdWatchdogPath != "" {
		return s.SbdWatchdogPath
	}
	return DefaultWatchdogPath
}

// GetImageWithOperatorImage returns the agent image with default fallback
// The default is constructed from the operator's image by replacing the image name with sbd-agent
func (s *SBDConfigSpec) GetImageWithOperatorImage(operatorImage string) string {
	if s.Image != "" {
		return s.Image
	}
	return deriveAgentImageFromOperator(operatorImage)
}

// GetImagePullPolicy returns the image pull policy with default fallback
func (s *SBDConfigSpec) GetImagePullPolicy() string {
	if s.ImagePullPolicy != "" {
		return s.ImagePullPolicy
	}
	return "IfNotPresent"
}

// GetStaleNodeTimeout returns the stale node timeout with default fallback
func (s *SBDConfigSpec) GetStaleNodeTimeout() time.Duration {
	if s.StaleNodeTimeout != nil {
		return s.StaleNodeTimeout.Duration
	}
	return DefaultStaleNodeTimeout
}

// GetWatchdogTimeout returns the watchdog timeout with default fallback
func (s *SBDConfigSpec) GetWatchdogTimeout() time.Duration {
	if s.WatchdogTimeout != nil {
		return s.WatchdogTimeout.Duration
	}
	return DefaultWatchdogTimeout
}

// GetPetIntervalMultiple returns the pet interval multiple with default fallback
func (s *SBDConfigSpec) GetPetIntervalMultiple() int32 {
	if s.PetIntervalMultiple != nil {
		return *s.PetIntervalMultiple
	}
	return DefaultPetIntervalMultiple
}

// GetPetInterval calculates the pet interval based on watchdog timeout and multiple
func (s *SBDConfigSpec) GetPetInterval() time.Duration {
	watchdogTimeout := s.GetWatchdogTimeout()
	multiple := s.GetPetIntervalMultiple()
	petInterval := watchdogTimeout / time.Duration(multiple)

	// Ensure minimum pet interval of 1 second
	if petInterval < time.Second {
		petInterval = time.Second
	}

	return petInterval
}

// GetSharedStoragePVC returns the shared storage PVC name
func (s *SBDConfigSpec) GetSharedStoragePVC() string {
	return s.SharedStoragePVC
}

// GetSharedStorageMountPath returns the shared storage mount path with default fallback
func (s *SBDConfigSpec) GetSharedStorageMountPath() string {
	if s.SharedStorageMountPath != "" {
		return s.SharedStorageMountPath
	}
	return "/shared-storage"
}

// HasSharedStorage returns true if shared storage is configured
func (s *SBDConfigSpec) HasSharedStorage() bool {
	return s.SharedStoragePVC != ""
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

// ValidateWatchdogTimeout validates the watchdog timeout value
func (s *SBDConfigSpec) ValidateWatchdogTimeout() error {
	timeout := s.GetWatchdogTimeout()

	if timeout < MinWatchdogTimeout {
		return fmt.Errorf("watchdog timeout %v is less than minimum %v", timeout, MinWatchdogTimeout)
	}

	if timeout > MaxWatchdogTimeout {
		return fmt.Errorf("watchdog timeout %v is greater than maximum %v", timeout, MaxWatchdogTimeout)
	}

	return nil
}

// ValidatePetIntervalMultiple validates the pet interval multiple value
func (s *SBDConfigSpec) ValidatePetIntervalMultiple() error {
	multiple := s.GetPetIntervalMultiple()

	if multiple < MinPetIntervalMultiple {
		return fmt.Errorf("pet interval multiple %d is less than minimum %d", multiple, MinPetIntervalMultiple)
	}

	if multiple > MaxPetIntervalMultiple {
		return fmt.Errorf("pet interval multiple %d is greater than maximum %d", multiple, MaxPetIntervalMultiple)
	}

	return nil
}

// ValidatePetIntervalTiming validates that the calculated pet interval is appropriate
func (s *SBDConfigSpec) ValidatePetIntervalTiming() error {
	watchdogTimeout := s.GetWatchdogTimeout()
	petInterval := s.GetPetInterval()

	// Pet interval must be shorter than watchdog timeout
	if petInterval >= watchdogTimeout {
		return fmt.Errorf("pet interval %v must be shorter than watchdog timeout %v", petInterval, watchdogTimeout)
	}

	// Pet interval should be at least 3 times shorter than watchdog timeout for safety
	maxPetInterval := watchdogTimeout / 3
	if petInterval > maxPetInterval {
		return fmt.Errorf("pet interval %v is too long for watchdog timeout %v. "+
			"Pet interval should be at least 3 times shorter than watchdog timeout. "+
			"Maximum recommended pet interval: %v",
			petInterval, watchdogTimeout, maxPetInterval)
	}

	// Pet interval should be at least 1 second
	if petInterval < time.Second {
		return fmt.Errorf("pet interval %v is too short. Minimum recommended: 1s", petInterval)
	}

	return nil
}

// ValidateImagePullPolicy validates the image pull policy value
func (s *SBDConfigSpec) ValidateImagePullPolicy() error {
	policy := s.GetImagePullPolicy()

	switch policy {
	case "Always", "Never", "IfNotPresent":
		return nil
	default:
		return fmt.Errorf("invalid image pull policy %q. Valid values are: Always, Never, IfNotPresent", policy)
	}
}

// ValidateSharedStoragePVC validates the shared storage PVC name
func (s *SBDConfigSpec) ValidateSharedStoragePVC() error {
	pvcName := s.GetSharedStoragePVC()

	// Empty PVC name is valid (shared storage is optional)
	if pvcName == "" {
		return nil
	}

	// Validate PVC name follows Kubernetes naming conventions
	// Names must be lowercase alphanumeric characters or '-', and must start and end with an alphanumeric character
	if len(pvcName) == 0 {
		return fmt.Errorf("shared storage PVC name cannot be empty when specified")
	}

	if len(pvcName) > 253 {
		return fmt.Errorf("shared storage PVC name %q is too long (max 253 characters)", pvcName)
	}

	// Basic validation - more comprehensive validation would require regex
	// but keeping it simple for now as Kubernetes API server will validate it
	if strings.HasPrefix(pvcName, "-") || strings.HasSuffix(pvcName, "-") {
		return fmt.Errorf("shared storage PVC name %q cannot start or end with '-'", pvcName)
	}

	return nil
}

// ValidateSharedStorageMountPath validates the shared storage mount path
func (s *SBDConfigSpec) ValidateSharedStorageMountPath() error {
	mountPath := s.GetSharedStorageMountPath()

	// Mount path must be an absolute path
	if !strings.HasPrefix(mountPath, "/") {
		return fmt.Errorf("shared storage mount path %q must be an absolute path", mountPath)
	}

	// Mount path cannot be just "/"
	if mountPath == "/" {
		return fmt.Errorf("shared storage mount path cannot be root directory '/'")
	}

	// Mount path should not conflict with common system paths
	systemPaths := []string{"/dev", "/proc", "/sys", "/etc", "/var", "/usr", "/bin", "/sbin", "/lib", "/lib64"}
	for _, sysPath := range systemPaths {
		if strings.HasPrefix(mountPath, sysPath+"/") || mountPath == sysPath {
			return fmt.Errorf("shared storage mount path %q conflicts with system path %q", mountPath, sysPath)
		}
	}

	return nil
}

// ValidateAll validates all configuration values
func (s *SBDConfigSpec) ValidateAll() error {
	if err := s.ValidateStaleNodeTimeout(); err != nil {
		return fmt.Errorf("stale node timeout validation failed: %w", err)
	}

	if err := s.ValidateWatchdogTimeout(); err != nil {
		return fmt.Errorf("watchdog timeout validation failed: %w", err)
	}

	if err := s.ValidatePetIntervalMultiple(); err != nil {
		return fmt.Errorf("pet interval multiple validation failed: %w", err)
	}

	if err := s.ValidatePetIntervalTiming(); err != nil {
		return fmt.Errorf("pet interval timing validation failed: %w", err)
	}

	if err := s.ValidateImagePullPolicy(); err != nil {
		return fmt.Errorf("image pull policy validation failed: %w", err)
	}

	if err := s.ValidateSharedStoragePVC(); err != nil {
		return fmt.Errorf("shared storage PVC validation failed: %w", err)
	}

	if err := s.ValidateSharedStorageMountPath(); err != nil {
		return fmt.Errorf("shared storage mount path validation failed: %w", err)
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

// deriveAgentImageFromOperator constructs the agent image from the operator's image by replacing the image name with sbd-agent
func deriveAgentImageFromOperator(operatorImage string) string {
	// Handle the format: registry/org/image:tag or registry/org/image
	// Examples:
	// - quay.io/medik8s/sbd-operator:v1.2.3 -> quay.io/medik8s/sbd-agent:v1.2.3
	// - quay.io/medik8s/sbd-operator -> quay.io/medik8s/sbd-agent:latest
	// - sbd-operator:latest -> sbd-agent:latest

	if operatorImage == "" {
		return "sbd-agent:latest"
	}

	// Split by ':' to separate image from tag
	var imageWithoutTag, tag string
	if colonIndex := strings.LastIndex(operatorImage, ":"); colonIndex != -1 {
		imageWithoutTag = operatorImage[:colonIndex]
		tag = operatorImage[colonIndex+1:]
	} else {
		imageWithoutTag = operatorImage
		tag = "latest"
	}

	// Split by '/' to get registry/org/image parts
	parts := strings.Split(imageWithoutTag, "/")
	if len(parts) == 0 {
		return "sbd-agent:" + tag
	}

	// Replace the last part (image name) with sbd-agent
	parts[len(parts)-1] = "sbd-agent"

	// Reconstruct the image
	return strings.Join(parts, "/") + ":" + tag
}
