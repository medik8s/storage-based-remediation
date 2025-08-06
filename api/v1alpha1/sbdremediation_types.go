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

// SBDRemediationConditionType represents the type of condition for SBDRemediation
type SBDRemediationConditionType string

const (
	// SBDRemediationConditionLeadershipAcquired indicates whether the operator has acquired leadership for fencing
	SBDRemediationConditionLeadershipAcquired SBDRemediationConditionType = "LeadershipAcquired"
	// SBDRemediationConditionFencingInProgress indicates whether fencing is currently in progress
	SBDRemediationConditionFencingInProgress SBDRemediationConditionType = "FencingInProgress"
	// SBDRemediationConditionFencingSucceeded indicates whether fencing completed successfully
	SBDRemediationConditionFencingSucceeded SBDRemediationConditionType = "FencingSucceeded"
	// SBDRemediationConditionReady indicates the overall readiness of the remediation
	SBDRemediationConditionReady SBDRemediationConditionType = "Ready"
)

// SBDRemediationReason represents the reason for the current remediation state
type SBDRemediationReason string

const (
	// SBDRemediationReasonHeartbeatTimeout indicates the node stopped sending heartbeats
	SBDRemediationReasonHeartbeatTimeout SBDRemediationReason = "HeartbeatTimeout"
	// SBDRemediationReasonNodeUnresponsive indicates the node is unresponsive
	SBDRemediationReasonNodeUnresponsive SBDRemediationReason = "NodeUnresponsive"
	// SBDRemediationReasonManualFencing indicates manual fencing was requested
	SBDRemediationReasonManualFencing SBDRemediationReason = "ManualFencing"
)

// SBDRemediationSpec defines the desired state of SBDRemediation.
type SBDRemediationSpec struct {
	// NodeName is the name of the Kubernetes node to be fenced
	// +kubebuilder:validation:Required
	NodeName string `json:"nodeName"`

	// Reason specifies why this node needs to be fenced
	// +kubebuilder:validation:Enum=HeartbeatTimeout;NodeUnresponsive;ManualFencing
	// +kubebuilder:default=NodeUnresponsive
	Reason SBDRemediationReason `json:"reason,omitempty"`

	// TimeoutSeconds specifies how long to wait before considering the fencing failed
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=60
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// SBDRemediationStatus defines the observed state of SBDRemediation.
type SBDRemediationStatus struct {
	// Conditions represent the latest available observations of the remediation's current state
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastUpdateTime is the time when this status was last updated
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`

	// NodeID is the numeric ID assigned to the target node for SBD operations
	NodeID *uint16 `json:"nodeID,omitempty"`

	// FenceMessageWritten indicates if the fence message was successfully written to the SBD device
	FenceMessageWritten bool `json:"fenceMessageWritten,omitempty"`

	// OperatorInstance identifies which operator instance is handling this remediation
	OperatorInstance string `json:"operatorInstance,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Fencing Succeeded",type="string",JSONPath=".status.conditions[?(@.type=='FencingSucceeded')].status"
// +kubebuilder:printcolumn:name="NodeID",type="integer",JSONPath=".status.nodeID"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SBDRemediation is the Schema for the sbdremediations API.
type SBDRemediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SBDRemediationSpec   `json:"spec,omitempty"`
	Status SBDRemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SBDRemediationList contains a list of SBDRemediation.
type SBDRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SBDRemediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SBDRemediation{}, &SBDRemediationList{})
}

// GetCondition returns the condition with the given type if it exists
func (r *SBDRemediation) GetCondition(conditionType SBDRemediationConditionType) *metav1.Condition {
	for i := range r.Status.Conditions {
		if r.Status.Conditions[i].Type == string(conditionType) {
			return &r.Status.Conditions[i]
		}
	}
	return nil
}

// SetCondition sets the given condition on the SBDRemediation
func (r *SBDRemediation) SetCondition(
	conditionType SBDRemediationConditionType,
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
func (r *SBDRemediation) IsConditionTrue(conditionType SBDRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse returns true if the condition is set to False
func (r *SBDRemediation) IsConditionFalse(conditionType SBDRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

// IsConditionUnknown returns true if the condition is set to Unknown or doesn't exist
func (r *SBDRemediation) IsConditionUnknown(conditionType SBDRemediationConditionType) bool {
	condition := r.GetCondition(conditionType)
	return condition == nil || condition.Status == metav1.ConditionUnknown
}

// IsFencingSucceeded returns true if fencing has completed successfully
func (r *SBDRemediation) IsFencingSucceeded() bool {
	return r.IsConditionTrue(SBDRemediationConditionFencingSucceeded)
}

// IsFencingInProgress returns true if fencing is currently in progress
func (r *SBDRemediation) IsFencingInProgress() bool {
	return r.IsConditionTrue(SBDRemediationConditionFencingInProgress)
}

// IsReady returns true if the remediation is ready (either succeeded or failed)
func (r *SBDRemediation) IsReady() bool {
	return r.IsConditionTrue(SBDRemediationConditionReady)
}

// HasLeadership returns true if leadership has been acquired
func (r *SBDRemediation) HasLeadership() bool {
	return r.IsConditionTrue(SBDRemediationConditionLeadershipAcquired)
}
