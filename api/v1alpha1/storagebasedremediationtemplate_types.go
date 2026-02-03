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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type StorageBasedRemediationTemplateResource struct {
	Spec StorageBasedRemediationSpec `json:"spec"`
}

// StorageBasedRemediationTemplateSpec defines the desired state of StorageBasedRemediationTemplate.
type StorageBasedRemediationTemplateSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Template defines the desired state of StorageBasedRemediationTemplate
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	Template StorageBasedRemediationTemplateResource `json:"template"`
}

// StorageBasedRemediationTemplateStatus defines the observed state of StorageBasedRemediationTemplate.
type StorageBasedRemediationTemplateStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// StorageBasedRemediationTemplate is the Schema for the sbdremediationtemplates API.
// +operator-sdk:csv:customresourcedefinitions:resources={{"StorageBasedRemediationTemplate","v1alpha1","storagebasedremediationtemplates"}}
type StorageBasedRemediationTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StorageBasedRemediationTemplateSpec   `json:"spec,omitempty"`
	Status StorageBasedRemediationTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StorageBasedRemediationTemplateList contains a list of StorageBasedRemediationTemplate.
type StorageBasedRemediationTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageBasedRemediationTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageBasedRemediationTemplate{}, &StorageBasedRemediationTemplateList{})
}
