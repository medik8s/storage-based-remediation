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
	"os"
	"strings"
	"time"
	"unicode"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/medik8s/storage-based-remediation/pkg/agent"
)

var typesLog = logf.Log.WithName("sbrconfig-types")

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Constants for StorageBasedRemediationConfig validation and defaults
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
	// DefaultSBRTimeoutSeconds is the default SBR timeout in seconds
	DefaultSBRTimeoutSeconds = 30
	// MinSBRTimeoutSeconds is the minimum allowed SBR timeout in seconds
	MinSBRTimeoutSeconds = 10
	// MaxSBRTimeoutSeconds is the maximum allowed SBR timeout in seconds
	MaxSBRTimeoutSeconds = 300
	// DefaultSBRUpdateInterval is the default interval for SBR device updates
	DefaultSBRUpdateInterval = 5 * time.Second
	// MinSBRUpdateInterval is the minimum allowed SBR update interval
	MinSBRUpdateInterval = 1 * time.Second
	// MaxSBRUpdateInterval is the maximum allowed SBR update interval
	MaxSBRUpdateInterval = 60 * time.Second
	// DefaultPeerCheckInterval is the default interval for peer check operations
	DefaultPeerCheckInterval = 5 * time.Second
	// MinPeerCheckInterval is the minimum allowed peer check interval
	MinPeerCheckInterval = 1 * time.Second
	// MaxPeerCheckInterval is the maximum allowed peer check interval
	MaxPeerCheckInterval = 60 * time.Second
	// DefaultMaxConsecutiveFailures is the runtime default when maxConsecutiveFailures is unset on the CR (no OpenAPI default).
	DefaultMaxConsecutiveFailures = 7
	// RelatedImageSbrAgent when this env is set it contains the image of SBR agent
	RelatedImageSbrAgent = "RELATED_IMAGE_SBR_AGENT"
)

// DetectOnlyModeType specifies whether SBR runs in detect-only mode (no remediation).
type DetectOnlyModeType string

const (
	// DetectOnlyModeDisabled is the default: SBR performs remediation (watchdog armed, fencing).
	DetectOnlyModeDisabled DetectOnlyModeType = "Disabled"
	// DetectOnlyModeEnabled disables all remediation: watchdog disarmed, no self-fence, no fence messages.
	DetectOnlyModeEnabled DetectOnlyModeType = "Enabled"
)

// SBRConfigConditionType represents the type of condition for StorageBasedRemediationConfig
type SBRConfigConditionType string

const (
	// SBRConfigConditionDaemonSetReady indicates whether the SBR agent DaemonSet is ready
	SBRConfigConditionDaemonSetReady SBRConfigConditionType = "DaemonSetReady"
	// SBRConfigConditionSharedStorageReady indicates whether shared storage is properly configured
	SBRConfigConditionSharedStorageReady SBRConfigConditionType = "SharedStorageReady"
	// SBRConfigConditionReady indicates the overall readiness of the StorageBasedRemediationConfig
	SBRConfigConditionReady SBRConfigConditionType = "Ready"
)

// StorageBasedRemediationConfigSpec defines the desired state of StorageBasedRemediationConfig.
type StorageBasedRemediationConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// WatchdogPath is the path to the watchdog device on the host
	// If not specified, defaults to "/dev/watchdog"
	// +kubebuilder:default="/dev/watchdog"
	// +optional
	WatchdogPath string `json:"watchdogPath,omitempty"`

	// Image is the container image for the SBR agent DaemonSet
	// If not specified, defaults to sbr-agent from the same registry/org/tag as the operator
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy defines the pull policy for the SBR agent container image.
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

	// NodeSelector is a selector which must be true for the SBR agent pod to fit on a node.
	// This allows users to control which nodes the SBR agent runs on by specifying node labels.
	// If not specified, defaults to worker nodes only (node-role.kubernetes.io/worker: "").
	// The selector is merged with the default requirement for kubernetes.io/os=linux.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// StaleNodeTimeout defines how long to wait before considering a node stale and removing it from slot mapping
	// This timeout determines when inactive nodes are cleaned up from the shared SBR device slot assignments.
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

	// LogLevel defines the logging level for the SBR agent pods.
	// Valid values are debug, info, warn, and error.
	// Debug provides the most verbose logging, while error only logs error messages.
	// +kubebuilder:validation:Enum=debug;info;warn;error
	// +kubebuilder:default="info"
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// IOTimeout defines the timeout for SBR I/O operations.
	// This determines how long the system will wait for SBR I/O operations to complete.
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

	// SBRTimeoutSeconds defines the SBR timeout in seconds, which determines the heartbeat interval.
	// The heartbeat interval is calculated as SBR timeout divided by 2.
	// This value controls how quickly the cluster can detect and respond to node failures.
	// The value must be between 10 and 300 seconds.
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=30
	// +optional
	SBRTimeoutSeconds *int32 `json:"sbrTimeoutSeconds,omitempty"`

	// MaxConsecutiveFailures is the maximum number of consecutive failures (SBR device, watchdog, or local
	// heartbeat writes) before the agent treats the node as failed and performs self-fencing (when not in
	// detect-only or otherwise disarmed). The same threshold scales how many peer heartbeat gaps are
	// required before a peer is considered unhealthy.
	// Increasing MaxConsecutiveFailures will increase time-to-detection proportionally to (maxConsecutiveFailures × heartbeatInterval) for both local and peer failures.
	// If omitted, DefaultMaxConsecutiveFailures is used
	// at runtime.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=32
	// +optional
	MaxConsecutiveFailures *int32 `json:"maxConsecutiveFailures,omitempty"`

	// SBRUpdateInterval defines the interval for updating the SBR device with node status information.
	// This determines how frequently each node writes its status to the shared SBR device.
	// More frequent updates provide faster failure detection but increase I/O load on the shared storage.
	// The value must be between 1 second and 60 seconds.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m))+$"
	// +kubebuilder:default="5s"
	// +optional
	SBRUpdateInterval *metav1.Duration `json:"sbrUpdateInterval,omitempty"`

	// PeerCheckInterval defines the interval for checking peer node heartbeats in the SBR device.
	// This determines how frequently each node reads and processes heartbeats from other nodes.
	// More frequent checks provide faster peer failure detection but increase I/O load on the shared storage.
	// The value must be between 1 second and 60 seconds.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(s|m))+$"
	// +kubebuilder:default="5s"
	// +optional
	PeerCheckInterval *metav1.Duration `json:"peerCheckInterval,omitempty"`

	// DetectOnlyMode when set to Enabled disables all remediation: the agent disarms the watchdog (no reboot)
	// and the controller does not write fence messages. SBR still sets node conditions (e.g. SBRStorageUnhealthy)
	// so NHC or other remediators can observe unhealthy nodes without SBR triggering a reboot.
	// +kubebuilder:validation:Enum=Disabled;Enabled
	// +optional
	DetectOnlyMode *DetectOnlyModeType `json:"detectOnlyMode,omitempty"`
}

// GetDetectOnlyMode returns whether detect-only mode is enabled (default false).
func (s *StorageBasedRemediationConfigSpec) GetDetectOnlyMode() bool {
	if s.DetectOnlyMode != nil {
		return *s.DetectOnlyMode == DetectOnlyModeEnabled
	}
	return false
}

// GetWatchdogPath returns the watchdog path with default fallback
func (s *StorageBasedRemediationConfigSpec) GetWatchdogPath() string {
	if s.WatchdogPath != "" {
		return s.WatchdogPath
	}
	return DefaultWatchdogPath
}

// GetImageWithOperatorImage returns the agent image with default fallback
// The default is constructed from the operator's image by replacing the image name with sbr-agent
func (s *StorageBasedRemediationConfigSpec) GetImageWithOperatorImage(operatorImage string) (string, error) {
	if s.Image != "" {
		return s.Image, nil
	}
	return deriveAgentImageFromOperator(operatorImage)
}

// GetImagePullPolicy returns the image pull policy with default fallback
func (s *StorageBasedRemediationConfigSpec) GetImagePullPolicy() string {
	if s.ImagePullPolicy != "" {
		return s.ImagePullPolicy
	}
	return "IfNotPresent"
}

// GetStaleNodeTimeout returns the stale node timeout with default fallback
func (s *StorageBasedRemediationConfigSpec) GetStaleNodeTimeout() time.Duration {
	if s.StaleNodeTimeout != nil {
		return s.StaleNodeTimeout.Duration
	}
	return DefaultStaleNodeTimeout
}

// GetWatchdogTimeout returns the watchdog timeout with default fallback
func (s *StorageBasedRemediationConfigSpec) GetWatchdogTimeout() time.Duration {
	if s.WatchdogTimeout != nil {
		return s.WatchdogTimeout.Duration
	}
	return DefaultWatchdogTimeout
}

// GetPetIntervalMultiple returns the pet interval multiple with default fallback
func (s *StorageBasedRemediationConfigSpec) GetPetIntervalMultiple() int32 {
	if s.PetIntervalMultiple != nil {
		return *s.PetIntervalMultiple
	}
	return DefaultPetIntervalMultiple
}

// GetPetInterval calculates the pet interval based on watchdog timeout and multiple
func (s *StorageBasedRemediationConfigSpec) GetPetInterval() time.Duration {
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
func (s *StorageBasedRemediationConfigSpec) GetLogLevel() string {
	if s.LogLevel != "" {
		return s.LogLevel
	}
	return "warn"
}

// GetIOTimeout returns the SBR I/O timeout with default fallback
func (s *StorageBasedRemediationConfigSpec) GetIOTimeout() time.Duration {
	if s.IOTimeout != nil {
		return s.IOTimeout.Duration
	}
	return DefaultIOTimeout
}

// GetRebootMethod returns the reboot method with default fallback
func (s *StorageBasedRemediationConfigSpec) GetRebootMethod() string {
	if s.RebootMethod != "" {
		return s.RebootMethod
	}
	return DefaultRebootMethod
}

// GetSBRTimeoutSeconds returns the SBR timeout in seconds with default fallback
func (s *StorageBasedRemediationConfigSpec) GetSBRTimeoutSeconds() int32 {
	if s.SBRTimeoutSeconds != nil {
		return *s.SBRTimeoutSeconds
	}
	return DefaultSBRTimeoutSeconds
}

// GetMaxConsecutiveFailures returns max consecutive failures when unset uses DefaultMaxConsecutiveFailures.
func (s *StorageBasedRemediationConfigSpec) GetMaxConsecutiveFailures() int32 {
	if s.MaxConsecutiveFailures != nil {
		return *s.MaxConsecutiveFailures
	}
	return int32(DefaultMaxConsecutiveFailures)
}

// GetSBRUpdateInterval returns the SBR update interval with default fallback
func (s *StorageBasedRemediationConfigSpec) GetSBRUpdateInterval() time.Duration {
	if s.SBRUpdateInterval != nil {
		return s.SBRUpdateInterval.Duration
	}
	return DefaultSBRUpdateInterval
}

// GetPeerCheckInterval returns the peer check interval with default fallback
func (s *StorageBasedRemediationConfigSpec) GetPeerCheckInterval() time.Duration {
	if s.PeerCheckInterval != nil {
		return s.PeerCheckInterval.Duration
	}
	return DefaultPeerCheckInterval
}

// GetSharedStoragePVCName returns the generated PVC name for shared storage
func (s *StorageBasedRemediationConfigSpec) GetSharedStoragePVCName(sbrConfigName string) string {
	if s.SharedStorageClass == "" {
		return ""
	}
	return fmt.Sprintf("%s-shared-storage", sbrConfigName)
}

// GetSharedStorageStorageClass returns the storage class name for shared storage
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageStorageClass() string {
	return s.SharedStorageClass
}

// GetSharedStorageSize returns the fixed storage size
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageSize() string {
	if s.SharedStorageClass == "" {
		return ""
	}
	return "10Mi"
}

// GetSharedStorageAccessModes returns the fixed access modes
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageAccessModes() []string {
	if s.SharedStorageClass == "" {
		return nil
	}
	return []string{"ReadWriteMany"}
}

// GetSharedStorageMountPath returns the shared storage mount path
// The controller automatically chooses a sensible path for mounting shared storage
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageMountPath() string {
	return agent.SharedStorageSBRDeviceDirectory
}

// HasSharedStorage returns true if shared storage is configured
func (s *StorageBasedRemediationConfigSpec) HasSharedStorage() bool {
	return s.SharedStorageClass != ""
}

// GetNodeSelector returns the node selector with default fallback to worker nodes only
func (s *StorageBasedRemediationConfigSpec) GetNodeSelector() map[string]string {
	if len(s.NodeSelector) > 0 {
		return s.NodeSelector
	}

	// Default to worker nodes only
	return map[string]string{
		"node-role.kubernetes.io/worker": "",
	}
}

// ValidateStaleNodeTimeout validates the stale node timeout value
func (s *StorageBasedRemediationConfigSpec) ValidateStaleNodeTimeout() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidateWatchdogTimeout() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidatePetIntervalMultiple() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidatePetIntervalTiming() error {
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

// ValidateIOTimeout validates the SBR I/O timeout value
func (s *StorageBasedRemediationConfigSpec) ValidateIOTimeout() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidateImagePullPolicy() error {
	policy := s.GetImagePullPolicy()

	switch policy {
	case "Always", "Never", "IfNotPresent":
		return nil
	default:
		return fmt.Errorf("invalid image pull policy %q. Valid values are: Always, Never, IfNotPresent", policy)
	}
}

// ValidateSharedStorageClass validates the shared storage class configuration
func (s *StorageBasedRemediationConfigSpec) ValidateSharedStorageClass() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidateRebootMethod() error {
	method := s.GetRebootMethod()

	switch method {
	case "panic", "systemctl-reboot", "none":
		return nil
	default:
		return fmt.Errorf("invalid reboot method %q. Valid values are: panic, systemctl-reboot, none", method)
	}
}

// ValidateSBRTimeoutSeconds validates the SBR timeout value
func (s *StorageBasedRemediationConfigSpec) ValidateSBRTimeoutSeconds() error {
	timeout := s.GetSBRTimeoutSeconds()

	if timeout < MinSBRTimeoutSeconds {
		return fmt.Errorf("SBR timeout %d seconds is less than minimum %d seconds", timeout, MinSBRTimeoutSeconds)
	}

	if timeout > MaxSBRTimeoutSeconds {
		return fmt.Errorf("SBR timeout %d seconds is greater than maximum %d seconds", timeout, MaxSBRTimeoutSeconds)
	}

	return nil
}

// ValidateSBRUpdateInterval validates the SBR update interval value
func (s *StorageBasedRemediationConfigSpec) ValidateSBRUpdateInterval() error {
	interval := s.GetSBRUpdateInterval()

	if interval < MinSBRUpdateInterval {
		return fmt.Errorf("SBR update interval %v is less than minimum %v", interval, MinSBRUpdateInterval)
	}

	if interval > MaxSBRUpdateInterval {
		return fmt.Errorf("SBR update interval %v is greater than maximum %v", interval, MaxSBRUpdateInterval)
	}

	return nil
}

// ValidatePeerCheckInterval validates the peer check interval value
func (s *StorageBasedRemediationConfigSpec) ValidatePeerCheckInterval() error {
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
func (s *StorageBasedRemediationConfigSpec) ValidateAll() error {
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

	if err := s.ValidateSBRTimeoutSeconds(); err != nil {
		return fmt.Errorf("SBR timeout seconds validation failed: %w", err)
	}

	if err := s.ValidateSBRUpdateInterval(); err != nil {
		return fmt.Errorf("SBR update interval validation failed: %w", err)
	}

	if err := s.ValidatePeerCheckInterval(); err != nil {
		return fmt.Errorf("peer check interval validation failed: %w", err)
	}

	return nil
}

// deriveAgentImageFromOperator derives the sbr-agent image from the operator image
func deriveAgentImageFromOperator(operatorImage string) (string, error) {
	// Handle empty operator image
	if operatorImage == "" {
		return "", fmt.Errorf("invalid empty operator image")
	}

	// In CI, RELATED_IMAGE_SBR_AGENT is injected via `oc set env` after bundle installation,
	// pointing to the CI-built agent image. In non-CI runs this env var is not set,
	// so we fall through to pod image discovery and derivation.
	if img, found := os.LookupEnv(RelatedImageSbrAgent); found {
		typesLog.Info("Using RELATED_IMAGE_SBR_AGENT for agent image", "image", img)
		return img, nil
	}

	lastSlash := strings.LastIndex(operatorImage, "/")
	var prefix, suffix string
	if lastSlash == -1 {
		prefix = ""
		suffix = operatorImage
	} else {
		prefix = operatorImage[:lastSlash+1]
		suffix = operatorImage[lastSlash+1:]
	}

	// If already an agent image (e.g. controller fallback in tests), return as-is
	if suffix == "sbr-agent" || strings.HasPrefix(suffix, "sbr-agent:") {
		agentSuffix := suffix
		if !strings.Contains(agentSuffix, ":") {
			agentSuffix += ":latest"
		}
		return prefix + agentSuffix, nil
	}

	// Replace operator with agent in the image name. Two naming schemes are supported:
	// 1) storage-based-remediation-*-operator -> storage-based-remediation-agent-* (then strip "-operator")
	// 2) sbr-operator -> sbr-agent
	agentSuffix := strings.Replace(suffix, "storage-based-remediation", "storage-based-remediation-agent", 1)
	if agentSuffix != suffix {
		agentSuffix = strings.Replace(agentSuffix, "-operator", "", 1)
	} else {
		agentSuffix = strings.Replace(suffix, "sbr-operator", "sbr-agent", 1)
	}
	if agentSuffix == suffix {
		return "", fmt.Errorf("invalid operator image %q", operatorImage)
	}

	// Add :latest tag if no tag is present
	if !strings.Contains(agentSuffix, ":") {
		agentSuffix += ":latest"
	}

	return prefix + agentSuffix, nil
}

// StorageBasedRemediationConfigStatus defines the observed state of StorageBasedRemediationConfig.
type StorageBasedRemediationConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Conditions represent the latest available observations of the StorageBasedRemediationConfig's current state
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ReadyNodes is the number of nodes where the SBR agent is ready
	ReadyNodes int32 `json:"readyNodes,omitempty"`

	// TotalNodes is the total number of nodes where the SBR agent should be deployed
	TotalNodes int32 `json:"totalNodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// StorageBasedRemediationConfig is the Schema for the storagebasedremediationconfigs API.
type StorageBasedRemediationConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StorageBasedRemediationConfigSpec   `json:"spec,omitempty"`
	Status StorageBasedRemediationConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StorageBasedRemediationConfigList contains a list of StorageBasedRemediationConfig.
type StorageBasedRemediationConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageBasedRemediationConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageBasedRemediationConfig{}, &StorageBasedRemediationConfigList{})
}

// GetCondition returns the condition with the given type if it exists
func (c *StorageBasedRemediationConfig) GetCondition(conditionType SBRConfigConditionType) *metav1.Condition {
	for i := range c.Status.Conditions {
		if c.Status.Conditions[i].Type == string(conditionType) {
			return &c.Status.Conditions[i]
		}
	}
	return nil
}

// SetCondition sets the given condition on the StorageBasedRemediationConfig
func (c *StorageBasedRemediationConfig) SetCondition(
	conditionType SBRConfigConditionType,
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
func (c *StorageBasedRemediationConfig) IsConditionTrue(conditionType SBRConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse returns true if the condition is set to False
func (c *StorageBasedRemediationConfig) IsConditionFalse(conditionType SBRConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

// IsConditionUnknown returns true if the condition is set to Unknown or doesn't exist
func (c *StorageBasedRemediationConfig) IsConditionUnknown(conditionType SBRConfigConditionType) bool {
	condition := c.GetCondition(conditionType)
	return condition == nil || condition.Status == metav1.ConditionUnknown
}

// IsDaemonSetReady returns true if the DaemonSet is ready
func (c *StorageBasedRemediationConfig) IsDaemonSetReady() bool {
	return c.IsConditionTrue(SBRConfigConditionDaemonSetReady)
}

// IsSharedStorageReady returns true if shared storage is ready
func (c *StorageBasedRemediationConfig) IsSharedStorageReady() bool {
	return c.IsConditionTrue(SBRConfigConditionSharedStorageReady)
}

// IsReady returns true if the StorageBasedRemediationConfig is ready overall
func (c *StorageBasedRemediationConfig) IsReady() bool {
	return c.IsConditionTrue(SBRConfigConditionReady)
}
