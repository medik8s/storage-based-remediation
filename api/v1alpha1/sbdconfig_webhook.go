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
var sbdconfiglog = logf.Log.WithName("sbdconfig-resource")

// +kubebuilder:webhook:path=/validate-medik8s-medik8s-io-v1alpha1-sbdconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=medik8s.medik8s.io,resources=sbdconfigs,verbs=create;update,versions=v1alpha1,name=vsbdconfig.kb.io,admissionReviewVersions=v1

// SBDConfigValidator implements admission webhook validation for SBDConfig
type SBDConfigValidator struct{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (v *SBDConfigValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	sbdConfig := obj.(*SBDConfig)
	sbdconfiglog.Info("validate create", "name", sbdConfig.Name, "namespace", sbdConfig.Namespace)

	// Validate the SBDConfig spec
	if err := sbdConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("SBDConfig spec validation failed: %w", err)
	}

	// TODO: Add node selector overlap validation once we can access the client
	// For now, the validation logic is in the controller
	sbdconfiglog.V(1).Info("Admission webhook validation passed", "name", sbdConfig.Name)

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (v *SBDConfigValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	sbdConfig := newObj.(*SBDConfig)
	sbdconfiglog.Info("validate update", "name", sbdConfig.Name, "namespace", sbdConfig.Namespace)

	// Validate the SBDConfig spec
	if err := sbdConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("SBDConfig spec validation failed: %w", err)
	}

	// TODO: Add node selector overlap validation once we can access the client
	// For now, the validation logic is in the controller
	sbdconfiglog.V(1).Info("Admission webhook validation passed", "name", sbdConfig.Name)

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (v *SBDConfigValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	sbdConfig := obj.(*SBDConfig)
	sbdconfiglog.Info("validate delete", "name", sbdConfig.Name, "namespace", sbdConfig.Namespace)

	// No validation needed for deletion
	return nil, nil
}

// SetupWithManager sets up the webhook with the Manager.
func (v *SBDConfigValidator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&SBDConfig{}).
		WithValidator(v).
		Complete()
}
