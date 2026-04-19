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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var sbrConfigLog = logf.Log.WithName("sbrconfig-resource")

// +kubebuilder:webhook:path=/validate-storage-based-remediation-medik8s-io-v1alpha1-storagebasedremediationconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs,verbs=create;update,versions=v1alpha1,name=vstoragebasedremediationconfig.kb.io,admissionReviewVersions=v1

// StorageBasedRemediationConfigValidator implements admission webhook validation for StorageBasedRemediationConfig
type StorageBasedRemediationConfigValidator struct{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	sbrConfig := obj.(*StorageBasedRemediationConfig)
	sbrConfigLog.Info("validate create", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// Validate the StorageBasedRemediationConfig spec
	if err := sbrConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig spec validation failed: %w", err)
	}

	// TODO: Add node selector overlap validation once we can access the client
	// For now, the validation logic is in the controller
	sbrConfigLog.V(1).Info("Admission webhook validation passed", "name", sbrConfig.Name)

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateUpdate(
	ctx context.Context,
	oldObj, newObj runtime.Object,
) (admission.Warnings, error) {
	sbrConfig := newObj.(*StorageBasedRemediationConfig)
	sbrConfigLog.Info("validate update", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// Validate the StorageBasedRemediationConfig spec
	if err := sbrConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig spec validation failed: %w", err)
	}

	// TODO: Add node selector overlap validation once we can access the client
	// For now, the validation logic is in the controller
	sbrConfigLog.V(1).Info("Admission webhook validation passed", "name", sbrConfig.Name)

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	sbrConfig := obj.(*StorageBasedRemediationConfig)
	sbrConfigLog.Info("validate delete", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// No validation needed for deletion
	return nil, nil
}

// SetupWithManager sets up the webhook with the Manager.
func (v *StorageBasedRemediationConfigValidator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&StorageBasedRemediationConfig{}).
		WithValidator(v).
		Complete()
}
