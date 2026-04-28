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
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"
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

// ValidateAll validates all configuration values
func (s *StorageBasedRemediationConfigSpec) ValidateAll() error {
	if err := s.ValidateSharedStorageClass(); err != nil {
		return fmt.Errorf("shared storage PVC validation failed: %w", err)
	}

	return nil
}

// DeriveAgentImageFromOperator derives the sbr-agent image from the operator image
func DeriveAgentImageFromOperator(operatorImage string) (string, error) {
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
