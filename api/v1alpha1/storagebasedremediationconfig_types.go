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
	"unicode"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Constants for StorageBasedRemediationConfig validation and defaults
const (
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"
	// DefaultSBRTimeoutSeconds is the default SBR timeout in seconds when sbrTimeoutSeconds is unset on the CR.
	DefaultSBRTimeoutSeconds = 30
	// DefaultMaxConsecutiveFailures is the runtime default when maxConsecutiveFailures is unset on the CR (no OpenAPI default).
	DefaultMaxConsecutiveFailures = 7

	DefaultIOTimeout                      time.Duration = 2 * time.Second
	DefaultBlockPerNodeIOTimeout          time.Duration = 15 * time.Second
	DefaultTotalOutageGracePeriod         time.Duration = 60 * time.Second
	DefaultAutonomousRebootRecoveryPeriod time.Duration = 5 * time.Minute
	// RelatedImageAgent when this env is set it contains the image of SBR agent
	RelatedImageAgent = "RELATED_IMAGE_AGENT"
)

// DetectOnlyModeType specifies whether SBR runs in detect-only mode (no remediation).
type DetectOnlyModeType string

const (
	// DetectOnlyModeDisabled is the default: SBR performs remediation (watchdog armed, fencing).
	DetectOnlyModeDisabled DetectOnlyModeType = "Disabled"
	// DetectOnlyModeEnabled disables all remediation: watchdog disarmed, no self-fence, no fence messages.
	DetectOnlyModeEnabled DetectOnlyModeType = "Enabled"
)

// SharedStorageVolumeModeType selects the SBR storage mode, not the Kubernetes PVC volumeMode.
type SharedStorageVolumeModeType string

const (
	// SharedStorageVolumeModeFilesystem uses a filesystem-backed RWX PVC (default).
	SharedStorageVolumeModeFilesystem SharedStorageVolumeModeType = "Filesystem"
	// SharedStorageVolumeModeBlock uses a raw block device RWX PVC.
	SharedStorageVolumeModeBlock SharedStorageVolumeModeType = "Block"
	// SharedStorageVolumeModeBlockPerNode uses a private RWO Block PVC for each node.
	SharedStorageVolumeModeBlockPerNode SharedStorageVolumeModeType = "BlockPerNode"
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
// +kubebuilder:validation:XValidation:rule="!has(self.sharedStorageVolumeMode) || !(self.sharedStorageVolumeMode in ['Block', 'BlockPerNode']) || (has(self.sharedStorageClass) && size(self.sharedStorageClass) > 0)",message="sharedStorageClass is required for Block and BlockPerNode"
// +kubebuilder:validation:XValidation:rule="!has(self.totalOutageGracePeriod) || (has(self.sharedStorageVolumeMode) && self.sharedStorageVolumeMode == 'BlockPerNode')",message="totalOutageGracePeriod requires BlockPerNode"
// +kubebuilder:validation:XValidation:rule="!has(self.autonomousRebootRecoveryPeriod) || (has(self.sharedStorageVolumeMode) && self.sharedStorageVolumeMode == 'BlockPerNode')",message="autonomousRebootRecoveryPeriod requires BlockPerNode"
// +kubebuilder:validation:XValidation:rule="!has(self.ioTimeout) || (duration(self.ioTimeout) >= duration('1s') && duration(self.ioTimeout) <= duration('10m'))",message="ioTimeout must be between 1s and 10m"
// +kubebuilder:validation:XValidation:rule="!has(self.totalOutageGracePeriod) || (duration(self.totalOutageGracePeriod) >= duration('0s') && duration(self.totalOutageGracePeriod) <= duration('1h'))",message="totalOutageGracePeriod must be between 0s and 1h"
// +kubebuilder:validation:XValidation:rule="!has(self.autonomousRebootRecoveryPeriod) || duration(self.autonomousRebootRecoveryPeriod) > duration('0s')",message="autonomousRebootRecoveryPeriod must be positive"
type StorageBasedRemediationConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// WatchdogPath is the path to the watchdog device on the host
	// If not specified, defaults to "/dev/watchdog"
	// +kubebuilder:default="/dev/watchdog"
	// +optional
	WatchdogPath string `json:"watchdogPath,omitempty"`

	// SharedStorageClass names the StorageClass for SBR storage.
	// Filesystem and Block require ReadWriteMany (RWX) storage.
	// BlockPerNode requires private ReadWriteOnce (RWO) Block storage for each node.
	// BlockPerNode currently defines the API only. The operator does not implement this storage mode.
	// +optional
	SharedStorageClass string `json:"sharedStorageClass,omitempty"`

	// SharedStorageVolumeMode selects the SBR storage mode.
	// Filesystem (default) uses an RWX filesystem. Block uses an RWX raw block device.
	// BlockPerNode defines private RWO Block storage. It is not a Kubernetes volumeMode value.
	// Block and BlockPerNode require sharedStorageClass.
	// This field is immutable after creation.
	// +kubebuilder:validation:Enum=Filesystem;Block;BlockPerNode
	// +optional
	SharedStorageVolumeMode *SharedStorageVolumeModeType `json:"sharedStorageVolumeMode,omitempty"`

	// IOTimeout limits a storage I/O operation. The API default is 15s for BlockPerNode and 2s for existing modes.
	// This API field does not change the running agent timeout.
	// +optional
	IOTimeout *metav1.Duration `json:"ioTimeout,omitempty"`

	// TotalOutageGracePeriod defines the BlockPerNode outage grace period, with an effective default of 60s.
	// Zero disables the grace period. The effective value is immutable after creation.
	// +optional
	TotalOutageGracePeriod *metav1.Duration `json:"totalOutageGracePeriod,omitempty"`

	// AutonomousRebootRecoveryPeriod defines the BlockPerNode recovery period, with an effective default of 5m.
	// The duration must be positive. The effective value is immutable after creation.
	// +optional
	AutonomousRebootRecoveryPeriod *metav1.Duration `json:"autonomousRebootRecoveryPeriod,omitempty"`

	// NodeSelector is a selector which must be true for the SBR agent pod to fit on a node.
	// This allows users to control which nodes the SBR agent runs on by specifying node labels.
	// If not specified, defaults to worker nodes only (node-role.kubernetes.io/worker: "").
	// The selector is merged with the default requirement for kubernetes.io/os=linux.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// SBRTimeoutSeconds configures the base timing for failure detection.
	// The heartbeat is sent every (sbrTimeoutSeconds/2) seconds.
	// A node is considered unhealthy after maxConsecutiveFailures missed heartbeats.
	// The operator also uses (sbrTimeoutSeconds/6) for update and peer-check intervals
	// (~3 scans per heartbeat) so shared-storage jitter is less likely to look like a missed heartbeat.
	// Time-to-detection scales with maxConsecutiveFailures × heartbeatInterval.
	// Allowed range is enforced by CRD validation (10-300 seconds).
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=300
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

// GetSBRTimeoutSeconds returns the SBR timeout in seconds with default fallback.
func (s *StorageBasedRemediationConfigSpec) GetSBRTimeoutSeconds() int32 {
	if s.SBRTimeoutSeconds != nil {
		return *s.SBRTimeoutSeconds
	}
	return DefaultSBRTimeoutSeconds
}

// GetHeartbeatInterval returns the agent heartbeat interval (sbrTimeoutSeconds / 2).
func (s *StorageBasedRemediationConfigSpec) GetHeartbeatInterval() time.Duration {
	interval := time.Duration(s.GetSBRTimeoutSeconds()) * time.Second / 2
	if interval < time.Second {
		return time.Second
	}
	return interval
}

// GetSBRUpdateInterval returns the sbr-update-interval passed to the agent (sbrTimeoutSeconds / 6).
// Heartbeat timing for failure detection uses GetHeartbeatInterval instead.
func (s *StorageBasedRemediationConfigSpec) GetSBRUpdateInterval() time.Duration {
	return s.derivedTimingInterval()
}

// GetPeerCheckInterval returns the peer-check-interval passed to the agent (sbrTimeoutSeconds / 6).
// Heartbeat timing for failure detection uses GetHeartbeatInterval instead.
func (s *StorageBasedRemediationConfigSpec) GetPeerCheckInterval() time.Duration {
	return s.derivedTimingInterval()
}

// derivedTimingInterval is timeout/6: poll peers ~3× per heartbeat period (timeout/2).
func (s *StorageBasedRemediationConfigSpec) derivedTimingInterval() time.Duration {
	interval := time.Duration(s.GetSBRTimeoutSeconds()) * time.Second / 6
	if interval < time.Second {
		return time.Second
	}
	return interval
}

// GetMaxConsecutiveFailures returns max consecutive failures when unset uses DefaultMaxConsecutiveFailures.
func (s *StorageBasedRemediationConfigSpec) GetMaxConsecutiveFailures() int32 {
	if s.MaxConsecutiveFailures != nil {
		return *s.MaxConsecutiveFailures
	}
	return int32(DefaultMaxConsecutiveFailures)
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

// GetSharedStorageVolumeMode returns the volume mode, defaulting to Filesystem when nil.
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageVolumeMode() SharedStorageVolumeModeType {
	if s.SharedStorageVolumeMode != nil {
		return *s.SharedStorageVolumeMode
	}
	return SharedStorageVolumeModeFilesystem
}

// IsBlockMode returns true if shared storage uses raw block device mode.
func (s *StorageBasedRemediationConfigSpec) IsBlockMode() bool {
	return s.GetSharedStorageVolumeMode() == SharedStorageVolumeModeBlock
}

func (s *StorageBasedRemediationConfigSpec) IsBlockPerNodeMode() bool {
	return s.GetSharedStorageVolumeMode() == SharedStorageVolumeModeBlockPerNode
}

func (s *StorageBasedRemediationConfigSpec) GetIOTimeout() time.Duration {
	if s.IOTimeout != nil {
		return s.IOTimeout.Duration
	}
	if s.IsBlockPerNodeMode() {
		return DefaultBlockPerNodeIOTimeout
	}
	return DefaultIOTimeout
}

func (s *StorageBasedRemediationConfigSpec) GetTotalOutageGracePeriod() time.Duration {
	if s.TotalOutageGracePeriod != nil {
		return s.TotalOutageGracePeriod.Duration
	}
	if s.IsBlockPerNodeMode() {
		return DefaultTotalOutageGracePeriod
	}
	return 0
}

func (s *StorageBasedRemediationConfigSpec) GetAutonomousRebootRecoveryPeriod() time.Duration {
	if s.AutonomousRebootRecoveryPeriod != nil {
		return s.AutonomousRebootRecoveryPeriod.Duration
	}
	if s.IsBlockPerNodeMode() {
		return DefaultAutonomousRebootRecoveryPeriod
	}
	return 0
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

// ValidateAll validates configuration values not covered by CRD OpenAPI schema.
func (s *StorageBasedRemediationConfigSpec) ValidateAll() error {
	if err := s.ValidateSharedStorageClass(); err != nil {
		return fmt.Errorf("shared storage PVC validation failed: %w", err)
	}

	switch mode := s.GetSharedStorageVolumeMode(); mode {
	case SharedStorageVolumeModeFilesystem:
	case SharedStorageVolumeModeBlock, SharedStorageVolumeModeBlockPerNode:
		if s.SharedStorageClass == "" {
			return fmt.Errorf("sharedStorageClass is required when sharedStorageVolumeMode is %s", mode)
		}
	default:
		return fmt.Errorf("sharedStorageVolumeMode %q is invalid", mode)
	}
	if s.IOTimeout != nil && (s.IOTimeout.Duration < time.Second || s.IOTimeout.Duration > 10*time.Minute) {
		return fmt.Errorf("ioTimeout must be between 1s and 10m")
	}
	if s.TotalOutageGracePeriod != nil {
		if !s.IsBlockPerNodeMode() {
			return fmt.Errorf("totalOutageGracePeriod requires BlockPerNode")
		}
		if s.TotalOutageGracePeriod.Duration < 0 || s.TotalOutageGracePeriod.Duration > time.Hour {
			return fmt.Errorf("totalOutageGracePeriod must be between 0s and 1h")
		}
	}
	if s.AutonomousRebootRecoveryPeriod != nil {
		if !s.IsBlockPerNodeMode() {
			return fmt.Errorf("autonomousRebootRecoveryPeriod requires BlockPerNode")
		}
		if s.AutonomousRebootRecoveryPeriod.Duration <= 0 {
			return fmt.Errorf("autonomousRebootRecoveryPeriod must be positive")
		}
	}

	return nil
}

// StorageValidationStatus caches the result of the storage validation checks
type StorageValidationStatus struct {
	// ConcurrentWriteable records whether every node was confirmed able to write to the real
	// disk at the same time. nil means not checked yet.
	// +optional
	ConcurrentWriteable *bool `json:"concurrentWriteable,omitempty"`

	// ProbedNodeCount is the node count when ConcurrentWriteable was last confirmed. If the node
	// count grows past this, the check must run again.
	// +optional
	ProbedNodeCount int32 `json:"probedNodeCount,omitempty"`

	// LastProbeTime is when the checks last ran.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// Message holds the latest detail, for operators to read.
	// +optional
	Message string `json:"message,omitempty"`
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

	// StorageValidation caches the result of the storage validation checks (StorageClass RWX
	// check and the concurrent write check).
	// +optional
	StorageValidation *StorageValidationStatus `json:"storageValidation,omitempty"`
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

// IsStorageWriteable returns true only when storage validation has confirmed write capability.
func (c *StorageBasedRemediationConfig) IsStorageWriteable() bool {
	sv := c.Status.StorageValidation
	return sv != nil && sv.ConcurrentWriteable != nil && *sv.ConcurrentWriteable
}
