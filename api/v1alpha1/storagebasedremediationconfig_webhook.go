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
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var sbrConfigLog = logf.Log.WithName("sbrconfig-resource")

var (
	// ErrNodeSelectorOverlap means the configuration selects a node that another configuration already selects.
	ErrNodeSelectorOverlap = errors.New("node selector overlaps an existing configuration")

	// ErrStorageClassConflict means a BlockPerNode configuration uses a different StorageClass than an existing one.
	ErrStorageClassConflict = errors.New("StorageClass differs from an existing BlockPerNode configuration")

	// ErrConflictCheckUnavailable means the conflict check could not read the cluster state.
	ErrConflictCheckUnavailable = errors.New("configuration conflict check unavailable")
)

// +kubebuilder:webhook:path=/validate-storage-based-remediation-medik8s-io-v1alpha1-storagebasedremediationconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs,verbs=create;update,versions=v1alpha1,name=vstoragebasedremediationconfig.kb.io,admissionReviewVersions=v1

// StorageBasedRemediationConfigValidator implements admission webhook validation for StorageBasedRemediationConfig
type StorageBasedRemediationConfigValidator struct {
	// Client reads the StorageBasedRemediationConfigs already stored in the cluster, across all
	// namespaces, for the overlap and single-StorageClass checks. It is required: without it the
	// validator rejects every create and update rather than admitting unchecked configurations.
	Client client.Reader
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateCreate(ctx context.Context, sbrConfig *StorageBasedRemediationConfig) (admission.Warnings, error) {
	sbrConfigLog.Info("validate create", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// Validate the StorageBasedRemediationConfig spec
	if err := sbrConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig spec validation failed: %w", err)
	}

	// Reject a configuration that selects nodes already owned by a BlockPerNode configuration,
	// or that introduces a second BlockPerNode StorageClass.
	if err := CheckBlockPerNodeConflicts(ctx, v.Client, sbrConfig); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig overlap validation failed: %w", err)
	}

	sbrConfigLog.V(1).Info("Admission webhook validation passed", "name", sbrConfig.Name)

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateUpdate(
	ctx context.Context,
	oldSbrConfig, sbrConfig *StorageBasedRemediationConfig,
) (admission.Warnings, error) {
	sbrConfigLog.Info("validate update", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// Validate the StorageBasedRemediationConfig spec
	if err := sbrConfig.Spec.ValidateAll(); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig spec validation failed: %w", err)
	}

	// SharedStorageVolumeMode is immutable after creation.
	// nil resolves to Filesystem for comparison: nil→Filesystem is allowed, nil→Block is rejected.
	oldMode := oldSbrConfig.Spec.GetSharedStorageVolumeMode()
	newMode := sbrConfig.Spec.GetSharedStorageVolumeMode()
	if oldMode != newMode {
		return nil, fmt.Errorf("sharedStorageVolumeMode is immutable: cannot change from %q to %q", oldMode, newMode)
	}
	if oldSbrConfig.Spec.GetTotalOutageGracePeriod() != sbrConfig.Spec.GetTotalOutageGracePeriod() {
		return nil, fmt.Errorf("totalOutageGracePeriod is immutable")
	}
	if oldSbrConfig.Spec.GetAutonomousRebootRecoveryPeriod() != sbrConfig.Spec.GetAutonomousRebootRecoveryPeriod() {
		return nil, fmt.Errorf("autonomousRebootRecoveryPeriod is immutable")
	}

	// Re-run the overlap checks: the selector or StorageClass may have changed, and so may the
	// other configurations in the cluster since this one was created.
	if err := CheckBlockPerNodeConflicts(ctx, v.Client, sbrConfig); err != nil {
		return nil, fmt.Errorf("StorageBasedRemediationConfig overlap validation failed: %w", err)
	}

	sbrConfigLog.V(1).Info("Admission webhook validation passed", "name", sbrConfig.Name)

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (v *StorageBasedRemediationConfigValidator) ValidateDelete(ctx context.Context, sbrConfig *StorageBasedRemediationConfig) (admission.Warnings, error) {
	sbrConfigLog.Info("validate delete", "name", sbrConfig.Name, "namespace", sbrConfig.Namespace)

	// No validation needed for deletion
	return nil, nil
}

// IntersectSelectedNodes returns the sorted names of the nodes that both configurations select.
func IntersectSelectedNodes(a, b *StorageBasedRemediationConfigSpec, nodes []corev1.Node) []string {
	shared := make([]string, 0)
	for i := range nodes {
		if a.SelectsNode(&nodes[i]) && b.SelectsNode(&nodes[i]) {
			shared = append(shared, nodes[i].Name)
		}
	}
	sort.Strings(shared)
	return shared
}

// FormatNodeSelector renders a selector map as a stable, sorted "key=value,key=value" string so
// error messages and events are deterministic.
func FormatNodeSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return "<empty>"
	}
	parts := make([]string, 0, len(selector))
	for key, value := range selector {
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// requiresExclusiveNodes reports whether a pair of configurations may not share a node.
//
// BlockPerNode arms a watchdog per node from a per-node probe volume, so two configurations
// selecting the same node would mean two watchdog owners on that host. The exclusivity applies to
// pairs involving BlockPerNode: shared-storage configurations keep their existing behaviour.
func requiresExclusiveNodes(a, b *StorageBasedRemediationConfig) bool {
	return a.Spec.IsBlockPerNodeMode() || b.Spec.IsBlockPerNodeMode()
}

// isSameConfig reports whether existing is the object being admitted, so an update is not
// compared against its own stored copy.
func isSameConfig(candidate, existing *StorageBasedRemediationConfig) bool {
	if candidate.UID != "" && existing.UID != "" {
		return candidate.UID == existing.UID
	}
	return candidate.Namespace == existing.Namespace && candidate.Name == existing.Name
}

// checkConflicts rejects a configuration that conflicts with an already-stored one.
//
// Two rules are enforced, across all namespaces:
//
//   - Node exclusivity: a configuration may not select a node that a BlockPerNode configuration
//     also selects (in either direction). Disjoint node sets coexist, including a shared-storage
//     configuration alongside a BlockPerNode one. Overlap is decided against the real nodes, not
//     against selector shape, so configurations split on unrelated label keys are allowed as long
//     as no node carries both label sets.
//   - Single StorageClass: every BlockPerNode configuration in the cluster must name the same
//     sharedStorageClass. Per-node-group StorageClass mapping is deferred (design 6.5).
//
// nodes is the current node list; existing is every stored configuration, including (harmlessly)
// the candidate itself.
func checkConflicts(
	candidate *StorageBasedRemediationConfig,
	existing []StorageBasedRemediationConfig,
	nodes []corev1.Node,
) error {
	// Stable order so a configuration with several conflicts always reports the same one.
	ordered := make([]*StorageBasedRemediationConfig, 0, len(existing))
	for i := range existing {
		ordered = append(ordered, &existing[i])
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Namespace != ordered[j].Namespace {
			return ordered[i].Namespace < ordered[j].Namespace
		}
		return ordered[i].Name < ordered[j].Name
	})

	for _, other := range ordered {
		if isSameConfig(candidate, other) {
			continue
		}

		if candidate.Spec.IsBlockPerNodeMode() && other.Spec.IsBlockPerNodeMode() &&
			candidate.Spec.SharedStorageClass != other.Spec.SharedStorageClass {
			return fmt.Errorf(
				"%w: StorageBasedRemediationConfig %q in namespace %q uses "+
					"BlockPerNode with sharedStorageClass %q, this one uses %q: all BlockPerNode "+
					"configurations must use the same StorageClass",
				ErrStorageClassConflict,
				other.Name, other.Namespace,
				other.Spec.SharedStorageClass, candidate.Spec.SharedStorageClass)
		}

		if !requiresExclusiveNodes(candidate, other) {
			continue
		}
		if shared := IntersectSelectedNodes(&candidate.Spec, &other.Spec, nodes); len(shared) > 0 {
			return fmt.Errorf(
				"%w: StorageBasedRemediationConfig %q in namespace %q (mode %s, "+
					"selector %s) already selects node(s) %s: a node may be managed by only one "+
					"BlockPerNode configuration, otherwise two agents would own its watchdog",
				ErrNodeSelectorOverlap,
				other.Name, other.Namespace,
				other.Spec.GetSharedStorageVolumeMode(),
				FormatNodeSelector(other.Spec.EffectiveNodeSelector()),
				strings.Join(shared, ", "))
		}
	}

	return nil
}

// CheckBlockPerNodeConflicts reads the cluster state the conflict rules need and applies them.
//
// reader must be able to list StorageBasedRemediationConfigs cluster-wide and list Nodes. A failed
// list is returned as an error so admission fails closed: unreadable cluster state must not let a
// conflicting configuration in.
func CheckBlockPerNodeConflicts(
	ctx context.Context,
	reader client.Reader,
	candidate *StorageBasedRemediationConfig,
) error {
	if reader == nil {
		return fmt.Errorf("%w: no client configured for the validating webhook", ErrConflictCheckUnavailable)
	}

	var existingConfigs StorageBasedRemediationConfigList
	if err := reader.List(ctx, &existingConfigs); err != nil {
		return fmt.Errorf("%w: failed to list StorageBasedRemediationConfigs: %w", ErrConflictCheckUnavailable, err)
	}

	var nodes corev1.NodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return fmt.Errorf("%w: failed to list Nodes: %w", ErrConflictCheckUnavailable, err)
	}

	return checkConflicts(candidate, existingConfigs.Items, nodes.Items)
}

// SetupWithManager sets up the webhook with the Manager.
func (v *StorageBasedRemediationConfigValidator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &StorageBasedRemediationConfig{}).
		WithValidator(v).
		Complete()
}
