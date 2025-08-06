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
	"unicode"

	"github.com/medik8s/sbd-operator/pkg/agent"
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
	// DefaultIOTimeout is the default timeout for I/O operations
	DefaultIOTimeout = 2 * time.Second
	// MinIOTimeout is the minimum allowed I/O timeout
	MinIOTimeout = 100 * time.Millisecond
	// MaxIOTimeout is the maximum allowed I/O timeout
	MaxIOTimeout = 5 * time.Minute
	// DefaultRebootMethod is the default reboot method for self-fencing
	DefaultRebootMethod = "systemctl-reboot"
	// DefaultSBDTimeoutSeconds is the default SBD timeout in seconds
	DefaultSBDTimeoutSeconds = 30
	// MinSBDTimeoutSeconds is the minimum allowed SBD timeout in seconds
	MinSBDTimeoutSeconds = 10
	// MaxSBDTimeoutSeconds is the maximum allowed SBD timeout in seconds
	MaxSBDTimeoutSeconds = 300
	// DefaultSBDUpdateInterval is the default interval for SBD device updates
	DefaultSBDUpdateInterval = 5 * time.Second
	// MinSBDUpdateInterval is the minimum allowed SBD update interval
	MinSBDUpdateInterval = 1 * time.Second
	// MaxSBDUpdateInterval is the maximum allowed SBD update interval
	MaxSBDUpdateInterval = 60 * time.Second
	// DefaultPeerCheckInterval is the default interval for peer check operations
	DefaultPeerCheckInterval = 5 * time.Second
	// MinPeerCheckInterval is the minimum allowed peer check interval
	MinPeerCheckInterval = 1 * time.Second
	// MaxPeerCheckInterval is the maximum allowed peer check interval
	MaxPeerCheckInterval = 60 * time.Second
)

// SBDConfigConditionType represents the type of condition for SBDConfig
type SBDConfigConditionType string

const (
	// SBDConfigConditionDaemonSetReady indicates whether the SBD agent DaemonSet is ready
	SBDConfigConditionDaemonSetReady SBDConfigConditionType = "DaemonSetReady"
	// SBDConfigConditionSharedStorageReady indicates whether shared storage is properly configured
	SBDConfigConditionSharedStorageReady SBDConfigConditionType = "SharedStorageReady"
	// SBDConfigConditionReady indicates the overall readiness of the SBDConfig
	SBDConfigConditionReady SBDConfigConditionType = "Ready"
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

	// SharedStorageClass is the name of a StorageClass to use for creating shared storage.
	// When specified, the controller will create a PVC using this StorageClass and mount it
	// in the agent DaemonSet for cross-node coordination, slot assignment, and shared configuration data.
	// The StorageClass must support ReadWriteMany (RWX) access mode.
	// +optional
	SharedStorageClass string `json:"sharedStorageClass,omitempty"`

	// NodeSelector is a selector which must be true for the SBD agent pod to fit on a node.
	// This allows users to control which nodes the SBD agent runs on by specifying node labels.
	// If not specified, defaults to worker nodes only (node-role.kubernetes.io/worker: "").
	// The selector is merged with the default requirement for kubernetes.io/os=linux.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// StaleNodeTimeout defines how long to wait before considering a node stale and removing it from slot mapping
	// This timeout determines when inactive nodes are cleaned up from the shared SBD device slot assignments.
	// Nodes that haven't updated their heartbeat within this duration will be considered stale and their slots
	// will be freed for reuse by new nodes. The value must be at least 1 minute.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m|h))+$"
	// +kubebuilder:default="1h"
	// +optional
	StaleNodeTimeout *metav1.Duration `json:"staleNodeTimeout,omitempty"`

	// WatchdogTimeout defines the watchdog timeout duration for the hardware/software watchdog device.
	// This determines how long the system will wait before triggering a reboot if the watchdog is not pet.
	// The pet interval is calculated as watchdog timeout divided by the pet interval multiple.
	// The value must be between 10 seconds and 300 seconds (5 minutes).
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m|h))+$"
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

	// LogLevel defines the logging level for the SBD agent pods.
	// Valid values are debug, info, warn, and error.
	// Debug provides the most verbose logging, while error only logs error messages.
	// +kubebuilder:validation:Enum=debug;info;warn;error
	// +kubebuilder:default="info"
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// IOTimeout defines the timeout for SBD I/O operations.
	// This determines how long the system will wait for SBD I/O operations to complete.
	// The value must be between 100 milliseconds and 5 minutes.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m|h))+$"
	// +kubebuilder:default="2s"
	// +optional
	IOTimeout *metav1.Duration `json:"iotimeout,omitempty"`

	// RebootMethod defines the method to use for self-fencing when a node needs to be rebooted.
	// Valid values are "panic" (immediate kernel panic), "systemctl-reboot" (graceful systemctl reboot),
	// and "none" (disable self-fencing, rely only on watchdog hardware timeout).
	// Panic provides the fastest failover but less graceful shutdown, while systemctl-reboot allows
	// for graceful service shutdown but may be slower. The "none" option disables agent-initiated
	// self-fencing and relies solely on hardware watchdog timeout for node fencing.
	// +kubebuilder:validation:Enum=panic;systemctl-reboot;none
	// +kubebuilder:default="panic"
	// +optional
	RebootMethod string `json:"rebootMethod,omitempty"`

	// SBDTimeoutSeconds defines the SBD timeout in seconds, which determines the heartbeat interval.
	// The heartbeat interval is calculated as SBD timeout divided by 2.
	// This value controls how quickly the cluster can detect and respond to node failures.
	// The value must be between 10 and 300 seconds.
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=30
	// +optional
	SBDTimeoutSeconds *int32 `json:"sbdTimeoutSeconds,omitempty"`

	// SBDUpdateInterval defines the interval for updating the SBD device with node status information.
	// This determines how frequently each node writes its status to the shared SBD device.
	// More frequent updates provide faster failure detection but increase I/O load on the shared storage.
	// The value must be between 1 second and 60 seconds.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m))+$"
	// +kubebuilder:default="5s"
	// +optional
	SBDUpdateInterval *metav1.Duration `json:"sbdUpdateInterval,omitempty"`

	// PeerCheckInterval defines the interval for checking peer node heartbeats in the SBD device.
	// This determines how frequently each node reads and processes heartbeats from other nodes.
	// More frequent checks provide faster peer failure detection but increase I/O load on the shared storage.
	// The value must be between 1 second and 60 seconds.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m))+$"
	// +kubebuilder:default="5s"
	// +optional
	PeerCheckInterval *metav1.Duration `json:"peerCheckInterval,omitempty"`
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

// GetLogLevel returns the log level with default fallback
func (s *SBDConfigSpec) GetLogLevel() string {
	if s.LogLevel != "" {
		return s.LogLevel
	}
	return "warn"
}

// GetIOTimeout returns the SBD I/O timeout with default fallback
func (s *SBDConfigSpec) GetIOTimeout() time.Duration {
	if s.IOTimeout != nil {
		return s.IOTimeout.Duration
	}
	return DefaultIOTimeout
}

// GetRebootMethod returns the reboot method with default fallback
func (s *SBDConfigSpec) GetRebootMethod() string {
	if s.RebootMethod != "" {
		return s.RebootMethod
	}
	return DefaultRebootMethod
}

// GetSBDTimeoutSeconds returns the SBD timeout in seconds with default fallback
func (s *SBDConfigSpec) GetSBDTimeoutSeconds() int32 {
	if s.SBDTimeoutSeconds != nil {
		return *s.SBDTimeoutSeconds
	}
	return DefaultSBDTimeoutSeconds
}

// GetSBDUpdateInterval returns the SBD update interval with default fallback
func (s *SBDConfigSpec) GetSBDUpdateInterval() time.Duration {
	if s.SBDUpdateInterval != nil {
		return s.SBDUpdateInterval.Duration
	}
	return DefaultSBDUpdateInterval
}

// GetPeerCheckInterval returns the peer check interval with default fallback
func (s *SBDConfigSpec) GetPeerCheckInterval() time.Duration {
	if s.PeerCheckInterval != nil {
		return s.PeerCheckInterval.Duration
	}
	return DefaultPeerCheckInterval
}

// GetSharedStoragePVCName returns the generated PVC name for shared storage
func (s *SBDConfigSpec) GetSharedStoragePVCName(sbdConfigName string) string {
	if s.SharedStorageClass == "" {
		return ""
	}
	return fmt.Sprintf("%s-shared-storage", sbdConfigName)
}

// GetSharedStorageStorageClass returns the storage class name for shared storage
func (s *SBDConfigSpec) GetSharedStorageStorageClass() string {
	return s.SharedStorageClass
}

// GetSharedStorageSize returns the fixed storage size
func (s *SBDConfigSpec) GetSharedStorageSize() string {
	if s.SharedStorageClass == "" {
		return ""
	}
	return "10Mi"
}

// GetSharedStorageAccessModes returns the fixed access modes
func (s *SBDConfigSpec) GetSharedStorageAccessModes() []string {
	if s.SharedStorageClass == "" {
		return nil
	}
	return []string{"ReadWriteMany"}
}

// GetSharedStorageMountPath returns the shared storage mount path
// The controller automatically chooses a sensible path for mounting shared storage
func (s *SBDConfigSpec) GetSharedStorageMountPath() string {
	return agent.SharedStorageSBDDeviceDirectory
}

// HasSharedStorage returns true if shared storage is configured
func (s *SBDConfigSpec) HasSharedStorage() bool {
	return s.SharedStorageClass != ""
}

// GetNodeSelector returns the node selector with default fallback to worker nodes only
func (s *SBDConfigSpec) GetNodeSelector() map[string]string {
	if len(s.NodeSelector) > 0 {
		return s.NodeSelector
	}

	// Default to worker nodes only
	return map[string]string{
		"node-role.kubernetes.io/worker": "",
	}
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

// ValidateIOTimeout validates the SBD I/O timeout value
func (s *SBDConfigSpec) ValidateIOTimeout() error {
	timeout := s.GetIOTimeout()

	if timeout < MinIOTimeout {
		return fmt.Errorf("I/O timeout %v is less than minimum %v", timeout, MinIOTimeout)
	}

	if timeout > MaxIOTimeout {
		return fmt.Errorf("I/O timeout %v is greater than maximum %v", timeout, MaxIOTimeout)
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

// ValidateSharedStorageClass validates the shared storage class configuration
func (s *SBDConfigSpec) ValidateSharedStorageClass() error {
	storageClassName := s.SharedStorageClass
	if storageClassName == "" {
		return nil // Optional field
	}

	// Validate storage class name follows Kubernetes naming conventions
	if len(storageClassName) > 253 {
		return fmt.Errorf("shared storage class name must be no more than 253 characters")
	}

	// Must start and end with alphanumeric character
	if len(storageClassName) > 0 {
		if !unicode.IsLetter(rune(storageClassName[0])) && !unicode.IsDigit(rune(storageClassName[0])) {
			return fmt.Errorf("shared storage class name must start with alphanumeric character")
		}
		lastChar := rune(storageClassName[len(storageClassName)-1])
		if !unicode.IsLetter(lastChar) && !unicode.IsDigit(lastChar) {
			return fmt.Errorf("shared storage class name must end with alphanumeric character")
		}
	}

	// Check for valid characters (lowercase letters, numbers, hyphens, dots)
	for _, char := range storageClassName {
		if !unicode.IsLower(char) && !unicode.IsDigit(char) && char != '-' && char != '.' {
			return fmt.Errorf("shared storage class name must contain only lowercase letters, numbers, hyphens, and dots")
		}
	}

	return nil
}

// ValidateRebootMethod validates the reboot method value
func (s *SBDConfigSpec) ValidateRebootMethod() error {
	method := s.GetRebootMethod()

	switch method {
	case "panic", "systemctl-reboot", "none":
		return nil
	default:
		return fmt.Errorf("invalid reboot method %q. Valid values are: panic, systemctl-reboot, none", method)
	}
}

// ValidateSBDTimeoutSeconds validates the SBD timeout value
func (s *SBDConfigSpec) ValidateSBDTimeoutSeconds() error {
	timeout := s.GetSBDTimeoutSeconds()

	if timeout < MinSBDTimeoutSeconds {
		return fmt.Errorf("SBD timeout %d seconds is less than minimum %d seconds", timeout, MinSBDTimeoutSeconds)
	}

	if timeout > MaxSBDTimeoutSeconds {
		return fmt.Errorf("SBD timeout %d seconds is greater than maximum %d seconds", timeout, MaxSBDTimeoutSeconds)
	}

	return nil
}

// ValidateSBDUpdateInterval validates the SBD update interval value
func (s *SBDConfigSpec) ValidateSBDUpdateInterval() error {
	interval := s.GetSBDUpdateInterval()

	if interval < MinSBDUpdateInterval {
		return fmt.Errorf("SBD update interval %v is less than minimum %v", interval, MinSBDUpdateInterval)
	}

	if interval > MaxSBDUpdateInterval {
		return fmt.Errorf("SBD update interval %v is greater than maximum %v", interval, MaxSBDUpdateInterval)
	}

	return nil
}

// ValidatePeerCheckInterval validates the peer check interval value
func (s *SBDConfigSpec) ValidatePeerCheckInterval() error {
	interval := s.GetPeerCheckInterval()

	if interval < MinPeerCheckInterval {
		return fmt.Errorf("peer check interval %v is less than minimum %v", interval, MinPeerCheckInterval)
	}

	if interval > MaxPeerCheckInterval {
		return fmt.Errorf("peer check interval %v is greater than maximum %v", interval, MaxPeerCheckInterval)
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

	if err := s.ValidateIOTimeout(); err != nil {
		return fmt.Errorf("I/O timeout validation failed: %w", err)
	}

	if err := s.ValidateImagePullPolicy(); err != nil {
		return fmt.Errorf("image pull policy validation failed: %w", err)
	}

	if err := s.ValidateSharedStorageClass(); err != nil {
		return fmt.Errorf("shared storage PVC validation failed: %w", err)
	}

	if err := s.ValidateRebootMethod(); err != nil {
		return fmt.Errorf("reboot method validation failed: %w", err)
	}

	if err := s.ValidateSBDTimeoutSeconds(); err != nil {
		return fmt.Errorf("SBD timeout seconds validation failed: %w", err)
	}

	if err := s.ValidateSBDUpdateInterval(); err != nil {
		return fmt.Errorf("SBD update interval validation failed: %w", err)
	}

	if err := s.ValidatePeerCheckInterval(); err != nil {
		return fmt.Errorf("peer check interval validation failed: %w", err)
	}

	return nil
}

// deriveAgentImageFromOperator derives the sbd-agent image from the operator image
func deriveAgentImageFromOperator(operatorImage string) string {
	// Handle empty operator image
	if operatorImage == "" {
		return "sbd-agent:latest"
	}

	// Replace the image name with sbd-agent while preserving registry/org/tag
	// Example: registry.io/org/sbd-operator:v1.0.0 -> registry.io/org/sbd-agent:v1.0.0
	lastSlash := strings.LastIndex(operatorImage, "/")
	if lastSlash == -1 {
		// No slash found, handle simple image names like "sbd-operator:v1.0.0" or "sbd-operator"
		agentImage := strings.Replace(operatorImage, "sbd-operator", "sbd-agent", 1)
		if agentImage == operatorImage {
			// If no replacement happened, default to sbd-agent
			agentImage = "sbd-agent"
		}

		// Add :latest tag if no tag is present
		if !strings.Contains(agentImage, ":") {
			agentImage += ":latest"
		}

		return agentImage
	}

	prefix := operatorImage[:lastSlash+1]
	suffix := operatorImage[lastSlash+1:]

	// Replace operator with agent in the image name
	agentSuffix := strings.Replace(suffix, "sbd-operator", "sbd-agent", 1)
	if agentSuffix == suffix {
		// If no replacement happened, default to sbd-agent
		agentSuffix = "sbd-agent"
	}

	// Add :latest tag if no tag is present
	if !strings.Contains(agentSuffix, ":") {
		agentSuffix += ":latest"
	}

	return prefix + agentSuffix
}

// SBDConfigStatus defines the observed state of SBDConfig.
type SBDConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Conditions represent the latest available observations of the SBDConfig's current state
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ReadyNodes is the number of nodes where the SBD agent is ready
	ReadyNodes int32 `json:"readyNodes,omitempty"`

	// TotalNodes is the total number of nodes where the SBD agent should be deployed
	TotalNodes int32 `json:"totalNodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

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

// GetCondition returns the condition with the given type if it exists
func (c *SBDConfig) GetCondition(conditionType SBDConfigConditionType) *metav1.Condition {
	for i := range c.Status.Conditions {
		if c.Status.Conditions[i].Type == string(conditionType) {
			return &c.Status.Conditions[i]
		}
	}
	return nil
}

// SetCondition sets the given condition on the SBDConfig
func (c *SBDConfig) SetCondition(
	conditionType SBDConfigConditionType,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()

	// Find existing condition
	for i := range c.Status.Conditions {
		if c.Status.Conditions[i].Type == string(conditionType) {
			// Update existing condition
			condition := &c.Status.Conditions[i]

			// Only update LastTransitionTime if status changed
			if condition.Status != status {
				condition.LastTransitionTime = now
			}

			condition.Status = status
			condition.Reason = reason
			condition.Message = message
			condition.ObservedGeneration = c.Generation
			return
		}
	}

	// Add new condition
	c.Status.Conditions = append(c.Status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: c.Generation,
	})
}

// IsConditionTrue returns true if the condition is set to True
func (c *SBDConfig) IsConditionTrue(conditionType SBDConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse returns true if the condition is set to False
func (c *SBDConfig) IsConditionFalse(conditionType SBDConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

// IsConditionUnknown returns true if the condition is set to Unknown or doesn't exist
func (c *SBDConfig) IsConditionUnknown(conditionType SBDConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition == nil || condition.Status == metav1.ConditionUnknown
}

// IsDaemonSetReady returns true if the DaemonSet is ready
func (c *SBDConfig) IsDaemonSetReady() bool {
	return c.IsConditionTrue(SBDConfigConditionDaemonSetReady)
}

// IsSharedStorageReady returns true if shared storage is ready
func (c *SBDConfig) IsSharedStorageReady() bool {
	return c.IsConditionTrue(SBDConfigConditionSharedStorageReady)
}

// IsReady returns true if the SBDConfig is ready overall
func (c *SBDConfig) IsReady() bool {
	return c.IsConditionTrue(SBDConfigConditionReady)
}
