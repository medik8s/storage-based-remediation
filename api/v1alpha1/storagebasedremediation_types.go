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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SBRRemediationConditionType represents the type of condition for StorageBasedRemediation
type SBRRemediationConditionType string

const (
	// SBRRemediationConditionLeadershipAcquired indicates whether the operator has acquired leadership for fencing
	SBRRemediationConditionLeadershipAcquired SBRRemediationConditionType = "LeadershipAcquired"
	// SBRRemediationConditionFencingInProgress indicates whether fencing is currently in progress
	SBRRemediationConditionFencingInProgress SBRRemediationConditionType = "FencingInProgress"
	// SBRRemediationConditionFencingSucceeded indicates whether fencing completed successfully
	SBRRemediationConditionFencingSucceeded SBRRemediationConditionType = "FencingSucceeded"
	// SBRRemediationConditionReady indicates the overall readiness of the remediation
	SBRRemediationConditionReady SBRRemediationConditionType = "Ready"
)

// Node condition type set by SBR when it detects a node as unhealthy (e.g. heartbeat timeout).
// NHC or other remediators watch this condition and create StorageBasedRemediation when True.
const NodeConditionSBRStorageUnhealthy = "SBRStorageUnhealthy"

// SBRStorageUnhealthyReasonHeartbeatTimeout is the Kubernetes Node condition.reason value
// the SBR agent uses with condition type SBRStorageUnhealthy when a peer is unhealthy due to
// heartbeat timeout (pre-remediation signaling for remediators such as NHC).
const SBRStorageUnhealthyReasonHeartbeatTimeout = "HeartbeatTimeout"

// StorageBasedRemediationSpec defines the desired state of StorageBasedRemediation.
// It is intentionally empty: the node to remediate is identified by metadata.name.
//
// Before a StorageBasedRemediation exists, the SBR agent may set the Node condition
// SBRStorageUnhealthy (including condition reason/message) so remediators such as NHC
// can decide whether to create this CR. That condition context is not carried in spec
// and is not re-read from the Node when the operator writes the fence message.
type StorageBasedRemediationSpec struct {
}

// StorageBasedRemediationStatus defines the observed state of StorageBasedRemediation.
type StorageBasedRemediationStatus struct {
	// Conditions represent the latest available observations of the remediation's current state
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastUpdateTime is the time when this status was last updated
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`

	// NodeID is the numeric ID assigned to the target node for SBR operations
	NodeID *uint16 `json:"nodeID,omitempty"`

	// FenceMessageWritten indicates if the fence message was successfully written to the SBR device
	FenceMessageWritten bool `json:"fenceMessageWritten,omitempty"`

	// OperatorInstance identifies which operator instance is handling this remediation
	OperatorInstance string `json:"operatorInstance,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".metadata.name"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Fencing Succeeded",type="string",JSONPath=".status.conditions[?(@.type=='FencingSucceeded')].status"
// +kubebuilder:printcolumn:name="NodeID",type="integer",JSONPath=".status.nodeID"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// StorageBasedRemediation is the Schema for the storagebasedremediations API.
type StorageBasedRemediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StorageBasedRemediationSpec   `json:"spec,omitempty"`
	Status StorageBasedRemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StorageBasedRemediationList contains a list of StorageBasedRemediation.
type StorageBasedRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageBasedRemediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageBasedRemediation{}, &StorageBasedRemediationList{})
}

// GetCondition returns the condition with the given type if it exists
func (r *StorageBasedRemediation) GetCondition(conditionType SBRRemediationConditionType) *metav1.Condition {
	for i := range r.Status.Conditions {
		if r.Status.Conditions[i].Type == string(conditionType) {
			return &r.Status.Conditions[i]
		}
	}
	return nil
}

// SetCondition sets the given condition on the StorageBasedRemediation
func (r *StorageBasedRemediation) SetCondition(
	conditionType SBRRemediationConditionType,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()

	// Find existing condition
	for i := range r.Status.Conditions {
		if r.Status.Conditions[i].Type == string(conditionType) {
			// Update existing condition
			condition := &r.Status.Conditions[i]

			// Only update LastTransitionTime if status changed
			if condition.Status != status {
				condition.LastTransitionTime = now
			}

			condition.Status = status
			condition.Reason = reason
			condition.Message = message
			condition.ObservedGeneration = r.Generation
			return
		}
	}

	// Add new condition
	r.Status.Conditions = append(r.Status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: r.Generation,
	})
}

// IsConditionTrue returns true if the condition is set to True
func (r *StorageBasedRemediation) IsConditionTrue(conditionType SBRRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse returns true if the condition is set to False
func (r *StorageBasedRemediation) IsConditionFalse(conditionType SBRRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

// IsConditionUnknown returns true if the condition is set to Unknown or doesn't exist
func (r *StorageBasedRemediation) IsConditionUnknown(conditionType SBRRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition == nil || condition.Status == metav1.ConditionUnknown
}

// IsFencingSucceeded returns true if fencing has completed successfully
func (r *StorageBasedRemediation) IsFencingSucceeded() bool {
	return r.IsConditionTrue(SBRRemediationConditionFencingSucceeded)
}

// IsFencingInProgress returns true if fencing is currently in progress
func (r *StorageBasedRemediation) IsFencingInProgress() bool {
	return r.IsConditionTrue(SBRRemediationConditionFencingInProgress)
}

// IsReady returns true if the remediation is ready (either succeeded or failed)
func (r *StorageBasedRemediation) IsReady() bool {
	return r.IsConditionTrue(SBRRemediationConditionReady)
}

// HasLeadership returns true if leadership has been acquired
func (r *StorageBasedRemediation) HasLeadership() bool {
	return r.IsConditionTrue(SBRRemediationConditionLeadershipAcquired)
}
