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

package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/retry"
	storagev1 "k8s.io/api/storage/v1"
)

// Event types and reasons for SBDConfig controller
const (
	// Event types
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	// Event reasons for SBDConfig operations
	ReasonSBDConfigReconciled       = "SBDConfigReconciled"
	ReasonDaemonSetManaged          = "DaemonSetManaged"
	ReasonServiceAccountCreated     = "ServiceAccountCreated"
	ReasonClusterRoleBindingCreated = "ClusterRoleBindingCreated"
	ReasonSCCManaged                = "SCCManaged"
	ReasonReconcileError            = "ReconcileError"
	ReasonDaemonSetError            = "DaemonSetError"
	ReasonServiceAccountError       = "ServiceAccountError"
	ReasonSCCError                  = "SCCError"
	ReasonValidationError           = "ValidationError"
	ReasonCleanupCompleted          = "CleanupCompleted"
	ReasonCleanupError              = "CleanupError"
	ReasonPVCManaged                = "PVCManaged"
	ReasonPVCError                  = "PVCError"
	ReasonSBDDeviceInitialized      = "SBDDeviceInitialized"
	ReasonSBDDeviceInitError        = "SBDDeviceInitError"

	// Finalizer for cleanup operations
	SBDConfigFinalizerName = "sbd-operator.medik8s.io/cleanup"

	// OpenShift SCC constants
	SBDOperatorSCCName = "sbd-operator-sbd-agent-privileged"

	// Retry configuration constants for SBDConfig controller
	// MaxSBDConfigRetries is the maximum number of retry attempts for SBDConfig operations
	MaxSBDConfigRetries = 3
	// InitialSBDConfigRetryDelay is the initial delay between SBDConfig operation retries
	InitialSBDConfigRetryDelay = 500 * time.Millisecond
	// MaxSBDConfigRetryDelay is the maximum delay between SBDConfig operation retries
	MaxSBDConfigRetryDelay = 10 * time.Second
	// SBDConfigRetryBackoffFactor is the exponential backoff factor for SBDConfig operation retries
	SBDConfigRetryBackoffFactor = 2.0
)

// SBDConfigReconciler reconciles a SBDConfig object
type SBDConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Retry configuration for Kubernetes API operations
	retryConfig retry.Config

	// OpenShift detection cache
	isOpenShift *bool
}

// initializeRetryConfig initializes the retry configuration for SBDConfig operations
func (r *SBDConfigReconciler) initializeRetryConfig(logger logr.Logger) {
	r.retryConfig = retry.Config{
		MaxRetries:    MaxSBDConfigRetries,
		InitialDelay:  InitialSBDConfigRetryDelay,
		MaxDelay:      MaxSBDConfigRetryDelay,
		BackoffFactor: SBDConfigRetryBackoffFactor,
		Logger:        logger.WithName("sbdconfig-retry"),
	}
}

// isTransientKubernetesError determines if a Kubernetes API error is transient and should be retried
func (r *SBDConfigReconciler) isTransientKubernetesError(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific transient Kubernetes errors
	if errors.IsConflict(err) ||
		errors.IsServerTimeout(err) ||
		errors.IsServiceUnavailable(err) ||
		errors.IsTooManyRequests(err) ||
		errors.IsTimeout(err) {
		return true
	}

	// Check for temporary network issues
	errMsg := err.Error()
	transientPatterns := []string{
		"connection refused",
		"timeout",
		"temporary failure",
		"try again",
		"server is currently unable",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			return true
		}
	}

	return false
}

// performKubernetesAPIOperationWithRetry performs a Kubernetes API operation with retry logic
func (r *SBDConfigReconciler) performKubernetesAPIOperationWithRetry(ctx context.Context, operation string, fn func() error, logger logr.Logger) error {
	return retry.Do(ctx, r.retryConfig, operation, func() error {
		err := fn()
		if err != nil {
			// Wrap error with retry information
			return retry.NewRetryableError(err, r.isTransientKubernetesError(err), operation)
		}
		return nil
	})
}

// emitEvent is a helper function to emit Kubernetes events for the SBDConfig controller
func (r *SBDConfigReconciler) emitEvent(object client.Object, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(object, eventType, reason, message)
	}
}

// emitEventf is a helper function to emit formatted Kubernetes events for the SBDConfig controller
func (r *SBDConfigReconciler) emitEventf(object client.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(object, eventType, reason, messageFmt, args...)
	}
}

// getOperatorImage discovers the operator's own image by querying the current pod
// It uses environment variables (POD_NAME, POD_NAMESPACE) to find the current pod
// and extracts the image from the pod spec
func (r *SBDConfigReconciler) getOperatorImage(ctx context.Context, logger logr.Logger) (string, error) {
	// Try to get pod information from environment variables (set by Downward API)
	podName := os.Getenv("POD_NAME")
	podNamespace := os.Getenv("POD_NAMESPACE")

	if podName == "" || podNamespace == "" {
		logger.Error(nil, "POD_NAME or POD_NAMESPACE environment variables not set, using fallback")
		return "sbd-agent:latest", nil
	}

	// Get the current pod
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: podNamespace}, &pod)
	if err != nil {
		logger.Error(err, "Failed to get operator pod", "podName", podName, "podNamespace", podNamespace)
		return "sbd-agent:latest", nil // Fallback to default
	}

	// Find the manager container (operator container)
	for _, container := range pod.Spec.Containers {
		if container.Name == "manager" {
			logger.Info("Found operator image", "image", container.Image)
			return container.Image, nil
		}
	}

	// If manager container not found, use the first container's image
	if len(pod.Spec.Containers) > 0 {
		image := pod.Spec.Containers[0].Image
		logger.Error(nil, "Using first container image as operator image", "image", image)
		return image, nil
	}

	logger.Error(nil, "No containers found in operator pod, using fallback")
	return "sbd-agent:latest", nil
}

// isRunningOnOpenShift detects if the operator is running on OpenShift
// by checking for the presence of OpenShift-specific API resources
func (r *SBDConfigReconciler) isRunningOnOpenShift(ctx context.Context, logger logr.Logger) bool {
	// Use cached result if available
	if r.isOpenShift != nil {
		return *r.isOpenShift
	}

	// Check for OpenShift-specific API groups
	discoveryClient := r.Client.RESTMapper()

	// Try to find security.openshift.io API group
	_, err := discoveryClient.RESTMapping(schema.GroupKind{
		Group: "security.openshift.io",
		Kind:  "SecurityContextConstraints",
	})

	result := err == nil
	r.isOpenShift = &result

	if result {
		logger.Info("Detected OpenShift platform - SCC management enabled")
	} else {
		logger.V(1).Info("Standard Kubernetes platform detected - SCC management disabled")
	}

	return result
}

// ensureSCCPermissions ensures that the service account has the required SCC permissions
// This function updates the existing SCC to include the service account from the target namespace
func (r *SBDConfigReconciler) ensureSCCPermissions(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, namespaceName string, logger logr.Logger) error {
	if !r.isRunningOnOpenShift(ctx, logger) {
		logger.V(1).Info("Not running on OpenShift, skipping SCC management")
		return nil
	}

	sccLogger := logger.WithValues("scc.name", SBDOperatorSCCName, "namespace", namespaceName)
	serviceAccountUser := fmt.Sprintf("system:serviceaccount:%s:sbd-agent", namespaceName)

	// Get the existing SCC
	scc := &unstructured.Unstructured{}
	scc.SetAPIVersion("security.openshift.io/v1")
	scc.SetKind("SecurityContextConstraints")

	err := r.Get(ctx, types.NamespacedName{Name: SBDOperatorSCCName}, scc)
	if err != nil {
		if errors.IsNotFound(err) {
			sccLogger.Error(err, "Required SCC not found - ensure OpenShift installer was used",
				"scc", SBDOperatorSCCName,
				"requiredServiceAccount", serviceAccountUser)
			return fmt.Errorf("required SCC '%s' not found - ensure the operator was installed using the OpenShift installer: %w", SBDOperatorSCCName, err)
		}
		sccLogger.Error(err, "Failed to get SCC")
		return fmt.Errorf("failed to get SCC '%s': %w", SBDOperatorSCCName, err)
	}

	// Check if the service account is already in the users list
	users, found, err := unstructured.NestedStringSlice(scc.Object, "users")
	if err != nil {
		sccLogger.Error(err, "Failed to get users from SCC")
		return fmt.Errorf("failed to get users from SCC: %w", err)
	}

	if !found {
		users = []string{}
	}

	// Check if service account is already present
	for _, user := range users {
		if user == serviceAccountUser {
			sccLogger.V(1).Info("Service account already has SCC permissions", "serviceAccount", serviceAccountUser)
			return nil
		}
	}

	// Add the service account to the users list
	users = append(users, serviceAccountUser)

	// Update the SCC
	err = unstructured.SetNestedStringSlice(scc.Object, users, "users")
	if err != nil {
		sccLogger.Error(err, "Failed to set users in SCC")
		return fmt.Errorf("failed to set users in SCC: %w", err)
	}

	// Perform the update with retry logic
	err = r.performKubernetesAPIOperationWithRetry(ctx, "update SCC", func() error {
		return r.Update(ctx, scc)
	}, sccLogger)

	if err != nil {
		sccLogger.Error(err, "Failed to update SCC after retries", "serviceAccount", serviceAccountUser)
		r.emitEventf(sbdConfig, EventTypeWarning, ReasonSCCError,
			"Failed to update SCC '%s' to grant permissions to service account '%s': %v", SBDOperatorSCCName, serviceAccountUser, err)
		return fmt.Errorf("failed to update SCC '%s': %w", SBDOperatorSCCName, err)
	}

	sccLogger.Info("Successfully updated SCC to grant permissions to service account",
		"serviceAccount", serviceAccountUser,
		"scc", SBDOperatorSCCName)

	r.emitEventf(sbdConfig, EventTypeNormal, ReasonSCCManaged,
		"SCC '%s' updated to grant permissions to service account '%s'", SBDOperatorSCCName, serviceAccountUser)

	return nil
}

// validateStorageClass validates that the specified storage class supports ReadWriteMany access mode
func (r *SBDConfigReconciler) validateStorageClass(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, logger logr.Logger) error {
	if !sbdConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to validate
		return nil
	}

	storageClassName := sbdConfig.Spec.GetSharedStorageStorageClass()
	logger = logger.WithValues("storageClass", storageClassName)

	// Get the StorageClass object
	storageClass := &storagev1.StorageClass{}
	err := r.Get(ctx, types.NamespacedName{Name: storageClassName}, storageClass)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("StorageClass '%s' not found", storageClassName)
		}
		return fmt.Errorf("failed to get StorageClass '%s': %w", storageClassName, err)
	}

	// Check if the provisioner is known to support ReadWriteMany
	provisioner := storageClass.Provisioner
	if r.isRWXCompatibleProvisioner(provisioner) {
		logger.Info("StorageClass validation passed", "provisioner", provisioner)
		return nil
	}

	// Check if the provisioner is known to be incompatible
	if r.isRWXIncompatibleProvisioner(provisioner) {
		return fmt.Errorf("StorageClass '%s' uses provisioner '%s' that does not support ReadWriteMany access mode required for SBD shared storage", storageClassName, provisioner)
	}

	// For unknown provisioners, test with a temporary PVC
	logger.Info("StorageClass uses unknown provisioner, testing ReadWriteMany support", "provisioner", provisioner)
	if err := r.testRWXSupport(ctx, sbdConfig, storageClassName, logger); err != nil {
		return fmt.Errorf("StorageClass '%s' does not support ReadWriteMany access mode required for SBD shared storage: %w", storageClassName, err)
	}

	logger.Info("StorageClass validation passed", "provisioner", provisioner)
	return nil
}

// isRWXCompatibleProvisioner checks if a CSI provisioner is known to support ReadWriteMany
func (r *SBDConfigReconciler) isRWXCompatibleProvisioner(provisioner string) bool {
	// Known RWX-compatible provisioners
	rwxProvisioners := map[string]bool{
		// AWS
		"efs.csi.aws.com": true,

		// Azure
		"file.csi.azure.com": true,

		// GCP
		"filestore.csi.storage.gke.io": true,

		// NFS
		"nfs.csi.k8s.io": true,
		"cluster.local/nfs-subdir-external-provisioner": true,
		"k8s-sigs.io/nfs-subdir-external-provisioner":   true,

		// CephFS
		"cephfs.csi.ceph.com": true,

		// GlusterFS
		"gluster.org/glusterfs": true,

		// Other known RWX provisioners
		"nfs-provisioner": true,
		"csi-nfsplugin":   true,
	}

	return rwxProvisioners[provisioner]
}

// isRWXIncompatibleProvisioner checks if a CSI provisioner is known to NOT support ReadWriteMany
func (r *SBDConfigReconciler) isRWXIncompatibleProvisioner(provisioner string) bool {
	// Known RWX-incompatible provisioners (block storage that only supports RWO)
	rwxIncompatibleProvisioners := map[string]bool{
		// AWS
		"ebs.csi.aws.com": true,
		"aws-ebs":         true,

		// Azure
		"disk.csi.azure.com": true,
		"azure-disk":         true,

		// GCP
		"pd.csi.storage.gke.io": true,
		"gce-pd":                true,

		// VMware
		"csi.vsphere.vmware.com": true,

		// OpenStack
		"cinder.csi.openstack.org": true,

		// Other known block storage provisioners
		"csi.trident.netapp.io": true, // NetApp Trident (when configured for block)
		"iscsi.csi.k8s.io":      true, // iSCSI CSI driver
	}

	return rwxIncompatibleProvisioners[provisioner]
}

// testRWXSupport tests if a storage class actually supports ReadWriteMany by creating a temporary PVC
func (r *SBDConfigReconciler) testRWXSupport(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, storageClassName string, logger logr.Logger) error {
	// Create a temporary PVC with ReadWriteMany to test compatibility
	testPVCName := fmt.Sprintf("%s-rwx-test", sbdConfig.Name)

	testPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPVCName,
			Namespace: sbdConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "sbd-operator",
				"app.kubernetes.io/component": "storage-validation",
				"sbdconfig":                   sbdConfig.Name,
				"temp-test":                   "true",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"), // Minimal size for testing
				},
			},
			StorageClassName: &storageClassName,
		},
	}

	// Set controller reference for cleanup
	if err := controllerutil.SetControllerReference(sbdConfig, testPVC, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on test PVC: %w", err)
	}

	// Create the test PVC
	logger.Info("Creating temporary PVC to test ReadWriteMany support")
	err := r.Create(ctx, testPVC)
	if err != nil {
		return fmt.Errorf("failed to create test PVC: %w", err)
	}

	// Schedule cleanup regardless of test outcome
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if deleteErr := r.Delete(cleanupCtx, testPVC); deleteErr != nil {
			logger.Error(deleteErr, "Failed to cleanup test PVC", "testPVC", testPVCName)
		} else {
			logger.Info("Successfully cleaned up test PVC", "testPVC", testPVCName)
		}
	}()

	// Wait a short time and check if the PVC was rejected due to unsupported access mode
	time.Sleep(5 * time.Second)

	// Get the PVC to check its status
	err = r.Get(ctx, types.NamespacedName{Name: testPVCName, Namespace: sbdConfig.Namespace}, testPVC)
	if err != nil {
		return fmt.Errorf("failed to get test PVC status: %w", err)
	}

	// Check for events that indicate RWX incompatibility
	events := &corev1.EventList{}
	err = r.List(ctx, events, client.InNamespace(sbdConfig.Namespace), client.MatchingFields{"involvedObject.name": testPVCName})
	if err != nil {
		logger.Error(err, "Failed to list events for test PVC")
	} else {
		for _, event := range events.Items {
			message := strings.ToLower(event.Message)
			if strings.Contains(message, "readwritemany") &&
				(strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") || strings.Contains(message, "invalid")) {
				return fmt.Errorf("storage class does not support ReadWriteMany access mode: %s", event.Message)
			}
			if strings.Contains(message, "access mode") && strings.Contains(message, "not supported") {
				return fmt.Errorf("storage class does not support required access mode: %s", event.Message)
			}
		}
	}

	logger.Info("ReadWriteMany test passed", "testPVC", testPVCName)
	return nil
}

// ensurePVC ensures that a PVC exists for shared storage when SharedStorageClass is specified
func (r *SBDConfigReconciler) ensurePVC(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, logger logr.Logger) error {
	if !sbdConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to do
		return nil
	}

	pvcName := sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name)
	storageClassName := sbdConfig.Spec.GetSharedStorageStorageClass()

	logger = logger.WithValues(
		"pvc.name", pvcName,
		"pvc.namespace", sbdConfig.Namespace,
		"storageClass", storageClassName,
	)

	// Define the desired PVC
	desiredPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: sbdConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sbd-operator",
				"app.kubernetes.io/component":  "shared-storage",
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbdconfig":                    sbdConfig.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(sbdConfig.Spec.GetSharedStorageSize()),
				},
			},
			StorageClassName: &storageClassName,
		},
	}

	// Set controller reference
	if err := controllerutil.SetControllerReference(sbdConfig, desiredPVC, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on PVC: %w", err)
	}

	// Use CreateOrUpdate to manage the PVC
	actualPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: sbdConfig.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, actualPVC, func() error {
		// Update the PVC spec with the desired configuration
		// Note: Most PVC fields are immutable after creation, so we only update what we can
		actualPVC.Labels = desiredPVC.Labels

		// Only set spec if PVC is being created (empty ResourceVersion means it's new)
		if actualPVC.ResourceVersion == "" {
			actualPVC.Spec = desiredPVC.Spec
		}

		// Set the controller reference
		return controllerutil.SetControllerReference(sbdConfig, actualPVC, r.Scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to create or update PVC")
		return fmt.Errorf("failed to create or update PVC '%s': %w", pvcName, err)
	}

	logger.Info("PVC operation completed",
		"operation", result,
		"pvc.generation", actualPVC.Generation,
		"pvc.resourceVersion", actualPVC.ResourceVersion,
		"pvc.status.phase", actualPVC.Status.Phase)

	// Emit event for PVC management
	if result == controllerutil.OperationResultCreated {
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonPVCManaged,
			"PVC '%s' for shared storage created successfully using StorageClass '%s'", pvcName, storageClassName)
		logger.Info("PVC created successfully")
	} else if result == controllerutil.OperationResultUpdated {
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonPVCManaged,
			"PVC '%s' for shared storage updated successfully", pvcName)
		logger.Info("PVC updated successfully")
	} else {
		logger.V(1).Info("PVC unchanged")
	}

	return nil
}

// ensureSBDDevice ensures that the SBD device file exists in shared storage when SharedStorageClass is specified
func (r *SBDConfigReconciler) ensureSBDDevice(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, logger logr.Logger) error {
	if !sbdConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to do
		return nil
	}

	pvcName := sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name)
	jobName := fmt.Sprintf("%s-sbd-device-init", sbdConfig.Name)

	logger = logger.WithValues(
		"job.name", jobName,
		"job.namespace", sbdConfig.Namespace,
		"pvc.name", pvcName,
	)

	// Define the desired Job for SBD device initialization
	desiredJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: sbdConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sbd-operator",
				"app.kubernetes.io/component":  "sbd-device-init",
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbdconfig":                    sbdConfig.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":       "sbd-operator",
						"app.kubernetes.io/component":  "sbd-device-init",
						"app.kubernetes.io/managed-by": "sbd-operator",
						"sbdconfig":                    sbdConfig.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "sbd-device-init",
							Image:   "registry.access.redhat.com/ubi8/ubi-minimal:latest",
							Command: []string{"sh", "-c"},
							Args: []string{
								fmt.Sprintf(`
set -e
SBD_DEVICE_PATH="/sbd-shared/%s"
SBD_DEVICE_SIZE_KB=1024
NODE_MAP_FILE="$SBD_DEVICE_PATH.nodemap"
CLUSTER_NAME="${CLUSTER_NAME:-default-cluster}"

echo "Initializing SBD device and node mapping at $SBD_DEVICE_PATH"

# Function to create initial node mapping file
create_initial_node_mapping() {
    local node_map_file="$1"
    local cluster_name="$2"
    
    echo "Creating initial node mapping file: $node_map_file"
    
    # Create minimal valid node mapping table JSON
    local json_data="{
  \"magic\": [83, 66, 68, 78, 77, 65, 80, 49],
  \"version\": 1,
  \"cluster_name\": \"$cluster_name\",
  \"entries\": {},
  \"slot_usage\": {},
  \"last_update\": \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%S.%%3NZ)\"
}"
    
    # Calculate simple checksum (simplified for shell)
    local checksum=$(echo -n "$json_data" | cksum | cut -d' ' -f1)
    
    # Write checksum (4 bytes little-endian) followed by JSON
    printf "\\$(printf '%%02x' $((checksum & 0xFF)))" > "$node_map_file"
    printf "\\$(printf '%%02x' $(((checksum >> 8) & 0xFF)))" >> "$node_map_file"
    printf "\\$(printf '%%02x' $(((checksum >> 16) & 0xFF)))" >> "$node_map_file"
    printf "\\$(printf '%%02x' $(((checksum >> 24) & 0xFF)))" >> "$node_map_file"
    echo -n "$json_data" >> "$node_map_file"
    
    chmod 644 "$node_map_file"
    echo "Created initial node mapping file: $node_map_file ($(wc -c < "$node_map_file") bytes)"
}

# Check if both SBD device and node mapping file exist
if [ -f "$SBD_DEVICE_PATH" ] && [ -f "$NODE_MAP_FILE" ]; then
    echo "SBD device and node mapping file already exist"
    # Verify both files have content
    if [ -s "$SBD_DEVICE_PATH" ] && [ -s "$NODE_MAP_FILE" ]; then
        echo "Both files are non-empty, initialization complete"
        exit 0
    else
        echo "One or both files are empty, re-initializing..."
    fi
elif [ -f "$SBD_DEVICE_PATH" ]; then
    echo "SBD device exists but node mapping file is missing, creating node mapping..."
    create_initial_node_mapping "$NODE_MAP_FILE" "$CLUSTER_NAME"
    exit 0
else
    echo "SBD device does not exist, creating both SBD device and node mapping..."
fi

# Create the SBD device file with specific size (1MB = 1024KB)
# This provides space for multiple node slots and metadata
dd if=/dev/zero of="$SBD_DEVICE_PATH" bs=1024 count=$SBD_DEVICE_SIZE_KB

# Set appropriate permissions for the SBD device
chmod 664 "$SBD_DEVICE_PATH"

# Create the initial node mapping file
create_initial_node_mapping "$NODE_MAP_FILE" "$CLUSTER_NAME"

# Verify both files were created successfully
if [ -f "$SBD_DEVICE_PATH" ] && [ -s "$SBD_DEVICE_PATH" ] && [ -f "$NODE_MAP_FILE" ] && [ -s "$NODE_MAP_FILE" ]; then
    echo "SBD device successfully initialized at $SBD_DEVICE_PATH"
    echo "Device size: $(ls -lh "$SBD_DEVICE_PATH" | awk '{print $5}')"
    echo "Node mapping file created at $NODE_MAP_FILE"
    echo "Node mapping size: $(ls -lh "$NODE_MAP_FILE" | awk '{print $5}')"
else
    echo "ERROR: Failed to create SBD device or node mapping file"
    exit 1
fi

echo "SBD device and node mapping initialization completed successfully"
`, agent.SharedStorageSBDDeviceFile),
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER_NAME",
									Value: sbdConfig.Name,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "shared-storage",
									MountPath: sbdConfig.Spec.GetSharedStorageMountPath(),
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: mustParseQuantity("64Mi"),
									corev1.ResourceCPU:    mustParseQuantity("10m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: mustParseQuantity("128Mi"),
									corev1.ResourceCPU:    mustParseQuantity("100m"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "shared-storage",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
				},
			},
			// Clean up completed jobs after 1 hour to avoid accumulation
			TTLSecondsAfterFinished: func() *int32 { i := int32(3600); return &i }(),
		},
	}

	// Set controller reference
	if err := controllerutil.SetControllerReference(sbdConfig, desiredJob, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on SBD device init job: %w", err)
	}

	// Check if job already exists and is completed
	existingJob := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: sbdConfig.Namespace}, existingJob)
	if err == nil {
		// Job exists, check if it's completed successfully
		if existingJob.Status.Succeeded > 0 {
			logger.V(1).Info("SBD device initialization job already completed successfully")
			return nil
		}

		// If job failed, delete it so it can be recreated
		if existingJob.Status.Failed > 0 {
			logger.Info("SBD device initialization job failed, recreating...",
				"failedCount", existingJob.Status.Failed)
			if err := r.Delete(ctx, existingJob); err != nil {
				logger.Error(err, "Failed to delete failed SBD device init job")
				return fmt.Errorf("failed to delete failed SBD device init job: %w", err)
			}
			// Wait a bit for deletion to complete
			return fmt.Errorf("recreating failed SBD device init job, will retry")
		}

		// Job is still running - prevent DaemonSet creation until job completes
		logger.Info("SBD device initialization job is still running, waiting for completion before creating DaemonSet",
			"job.name", jobName,
			"job.active", existingJob.Status.Active,
			"job.conditions", len(existingJob.Status.Conditions))
		return fmt.Errorf("SBD device initialization job '%s' is still running, DaemonSet creation will be delayed until job completes", jobName)
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get SBD device init job: %w", err)
	}

	// Create the job
	logger.Info("Creating SBD device initialization job")
	if err := r.Create(ctx, desiredJob); err != nil {
		logger.Error(err, "Failed to create SBD device initialization job")
		r.emitEventf(sbdConfig, EventTypeWarning, ReasonSBDDeviceInitError,
			"Failed to create SBD device initialization job: %v", err)
		return fmt.Errorf("failed to create SBD device initialization job: %w", err)
	}

	logger.Info("SBD device initialization job created successfully, waiting for completion before creating DaemonSet")
	r.emitEventf(sbdConfig, EventTypeNormal, ReasonSBDDeviceInitialized,
		"SBD device initialization job '%s' created successfully", jobName)

	// Return error to trigger requeue and wait for job completion
	return fmt.Errorf("SBD device initialization job '%s' was just created, DaemonSet creation will be delayed until job completes", jobName)
}

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For SBDConfig, this implementation deploys and manages the SBD Agent DaemonSet.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *SBDConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("sbdconfig-controller").WithValues(
		"request", req.NamespacedName,
		"controller", "SBDConfig",
	)

	// Initialize retry configuration if not already done
	if r.retryConfig.MaxRetries == 0 {
		r.initializeRetryConfig(logger)
	}

	// Retrieve the SBDConfig object with retry logic
	var sbdConfig medik8sv1alpha1.SBDConfig
	err := r.performKubernetesAPIOperationWithRetry(ctx, "get SBDConfig", func() error {
		return r.Get(ctx, req.NamespacedName, &sbdConfig)
	}, logger)

	if err != nil {
		if errors.IsNotFound(err) {
			// The SBDConfig resource was deleted
			logger.Info("SBDConfig resource not found, it may have been deleted",
				"name", req.Name,
				"namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue with backoff for transient errors
		logger.Error(err, "Failed to get SBDConfig after retries",
			"name", req.Name,
			"namespace", req.Namespace)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Add resource-specific context to logger
	logger = logger.WithValues(
		"sbdconfig.name", sbdConfig.Name,
		"sbdconfig.namespace", sbdConfig.Namespace,
		"sbdconfig.generation", sbdConfig.Generation,
		"sbdconfig.resourceVersion", sbdConfig.ResourceVersion,
	)

	// Handle deletion with finalizer
	if sbdConfig.DeletionTimestamp != nil {
		logger.Info("SBDConfig is being deleted, performing cleanup")
		return r.handleDeletion(ctx, &sbdConfig, logger)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&sbdConfig, SBDConfigFinalizerName) {
		logger.Info("Adding finalizer to SBDConfig")
		controllerutil.AddFinalizer(&sbdConfig, SBDConfigFinalizerName)
		return ctrl.Result{}, r.Update(ctx, &sbdConfig)
	}

	// Get the operator image first for logging and DaemonSet creation
	operatorImage, err := r.getOperatorImage(ctx, logger)
	if err != nil {
		logger.Error(err, "Failed to get operator image")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Starting SBDConfig reconciliation",
		"spec.image", sbdConfig.Spec.GetImageWithOperatorImage(operatorImage),
		"namespace", sbdConfig.Namespace,
		"spec.sbdWatchdogPath", sbdConfig.Spec.GetSbdWatchdogPath(),
		"spec.staleNodeTimeout", sbdConfig.Spec.GetStaleNodeTimeout(),
		"spec.watchdogTimeout", sbdConfig.Spec.GetWatchdogTimeout(),
		"spec.petIntervalMultiple", sbdConfig.Spec.GetPetIntervalMultiple(),
		"spec.calculatedPetInterval", sbdConfig.Spec.GetPetInterval())

	// Validate the SBDConfig spec
	if err := sbdConfig.Spec.ValidateAll(); err != nil {
		logger.Error(err, "SBDConfig validation failed")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonValidationError,
			"SBDConfig validation failed: %v", err)
		// Don't requeue on validation errors - user needs to fix the configuration
		return ctrl.Result{}, fmt.Errorf("SBDConfig validation failed: %w", err)
	}

	// Note: Node selector overlap validation is now handled by the admission webhook
	// to provide immediate feedback and prevent invalid configurations from being created

	// Ensure the service account and RBAC resources exist with retry logic
	// Deploy in the same namespace as the SBDConfig CR
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure service account", func() error {
		return r.ensureServiceAccount(ctx, &sbdConfig, sbdConfig.Namespace, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure service account exists after retries",
			"namespace", sbdConfig.Namespace,
			"operation", "serviceaccount-creation")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonServiceAccountError,
			"Failed to ensure service account 'sbd-agent' exists in namespace '%s': %v", sbdConfig.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Ensure SCC permissions are configured for OpenShift
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure SCC permissions", func() error {
		return r.ensureSCCPermissions(ctx, &sbdConfig, sbdConfig.Namespace, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure SCC permissions after retries",
			"namespace", sbdConfig.Namespace,
			"operation", "scc-permissions")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonSCCError,
			"Failed to ensure SCC permissions for service account 'sbd-agent' in namespace '%s': %v", sbdConfig.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Validate storage class compatibility
	err = r.performKubernetesAPIOperationWithRetry(ctx, "validate storage class", func() error {
		return r.validateStorageClass(ctx, &sbdConfig, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to validate storage class compatibility after retries",
			"namespace", sbdConfig.Namespace,
			"operation", "storage-class-validation")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonPVCError,
			"Failed to validate storage class compatibility for PVC '%s': %v", sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name), err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Ensure PVC exists for shared storage
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure PVC", func() error {
		return r.ensurePVC(ctx, &sbdConfig, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure PVC after retries",
			"namespace", sbdConfig.Namespace,
			"operation", "pvc-creation")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonPVCError,
			"Failed to ensure PVC for shared storage in namespace '%s': %v", sbdConfig.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Ensure SBD device exists in shared storage
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure SBD device", func() error {
		return r.ensureSBDDevice(ctx, &sbdConfig, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure SBD device after retries",
			"namespace", sbdConfig.Namespace,
			"operation", "sbd-device-init")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonSBDDeviceInitError,
			"Failed to ensure SBD device in shared storage in namespace '%s': %v", sbdConfig.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Define the desired DaemonSet
	desiredDaemonSet := r.buildDaemonSet(&sbdConfig, operatorImage)
	daemonSetLogger := logger.WithValues(
		"daemonset.name", desiredDaemonSet.Name,
		"daemonset.namespace", desiredDaemonSet.Namespace,
	)

	// Use CreateOrUpdate to manage the DaemonSet with retry logic
	actualDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desiredDaemonSet.Name,
			Namespace: desiredDaemonSet.Namespace,
		},
	}

	var result controllerutil.OperationResult
	err = r.performKubernetesAPIOperationWithRetry(ctx, "create or update DaemonSet", func() error {
		var err error
		result, err = controllerutil.CreateOrUpdate(ctx, r.Client, actualDaemonSet, func() error {
			// Update the DaemonSet spec with the desired configuration
			actualDaemonSet.Spec = desiredDaemonSet.Spec
			actualDaemonSet.Labels = desiredDaemonSet.Labels
			actualDaemonSet.Annotations = desiredDaemonSet.Annotations

			// Set the controller reference
			return controllerutil.SetControllerReference(&sbdConfig, actualDaemonSet, r.Scheme)
		})
		return err
	}, daemonSetLogger)

	if err != nil {
		daemonSetLogger.Error(err, "Failed to create or update DaemonSet after retries",
			"operation", "daemonset-create-or-update",
			"desired.image", desiredDaemonSet.Spec.Template.Spec.Containers[0].Image)
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonDaemonSetError,
			"Failed to create or update DaemonSet '%s': %v", desiredDaemonSet.Name, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	daemonSetLogger.Info("DaemonSet operation completed",
		"operation", result,
		"daemonset.generation", actualDaemonSet.Generation,
		"daemonset.resourceVersion", actualDaemonSet.ResourceVersion)

	// Emit event for DaemonSet management
	if result == controllerutil.OperationResultCreated {
		r.emitEventf(&sbdConfig, EventTypeNormal, ReasonDaemonSetManaged,
			"DaemonSet '%s' for SBD Agent created successfully", actualDaemonSet.Name)
		daemonSetLogger.Info("DaemonSet created successfully")
	} else if result == controllerutil.OperationResultUpdated {
		r.emitEventf(&sbdConfig, EventTypeNormal, ReasonDaemonSetManaged,
			"DaemonSet '%s' for SBD Agent updated successfully", actualDaemonSet.Name)
		daemonSetLogger.Info("DaemonSet updated successfully")
	} else {
		r.emitEventf(&sbdConfig, EventTypeNormal, ReasonDaemonSetManaged,
			"DaemonSet '%s' for SBD Agent managed", actualDaemonSet.Name)
		daemonSetLogger.V(1).Info("DaemonSet unchanged")
	}

	// Update the SBDConfig status with retry logic
	err = r.performKubernetesAPIOperationWithRetry(ctx, "update SBDConfig status", func() error {
		return r.updateStatus(ctx, &sbdConfig, actualDaemonSet, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to update SBDConfig status after retries",
			"operation", "status-update",
			"daemonset.name", actualDaemonSet.Name)
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonReconcileError,
			"Failed to update SBDConfig status: %v", err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled SBDConfig",
		"operation", "reconcile-complete",
		"daemonset.name", actualDaemonSet.Name,
		"result", result)

	// Emit success event for SBDConfig reconciliation
	r.emitEventf(&sbdConfig, EventTypeNormal, ReasonSBDConfigReconciled,
		"SBDConfig '%s' successfully reconciled", sbdConfig.Name)

	return ctrl.Result{}, nil
}

// handleDeletion handles the cleanup when an SBDConfig is being deleted
func (r *SBDConfigReconciler) handleDeletion(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Starting SBDConfig cleanup")

	// Clean up the ClusterRoleBinding specific to this SBDConfig
	clusterRoleBindingName := fmt.Sprintf("sbd-agent-%s-%s", sbdConfig.Namespace, sbdConfig.Name)
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleBindingName,
		},
	}

	err := r.Delete(ctx, clusterRoleBinding)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete ClusterRoleBinding during cleanup", "clusterRoleBinding", clusterRoleBindingName)
		r.emitEventf(sbdConfig, EventTypeWarning, ReasonCleanupError,
			"Failed to delete ClusterRoleBinding '%s': %v", clusterRoleBindingName, err)
		return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
	}

	if err == nil {
		logger.Info("ClusterRoleBinding deleted successfully", "clusterRoleBinding", clusterRoleBindingName)
	}

	// Note: We don't delete the shared service account because other SBDConfigs in the same namespace might be using it
	// The service account will be cleaned up when the namespace is deleted or manually by administrators

	// Note: DaemonSet cleanup is handled automatically by Kubernetes garbage collection due to OwnerReference

	// Remove the finalizer to allow deletion
	controllerutil.RemoveFinalizer(sbdConfig, SBDConfigFinalizerName)
	err = r.Update(ctx, sbdConfig)
	if err != nil {
		logger.Error(err, "Failed to remove finalizer from SBDConfig")
		r.emitEventf(sbdConfig, EventTypeWarning, ReasonCleanupError,
			"Failed to remove finalizer: %v", err)
		return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
	}

	logger.Info("SBDConfig cleanup completed successfully")
	r.emitEventf(sbdConfig, EventTypeNormal, ReasonCleanupCompleted,
		"SBDConfig cleanup completed successfully")

	return ctrl.Result{}, nil
}

// ensureServiceAccount creates the service account and RBAC resources if they don't exist
func (r *SBDConfigReconciler) ensureServiceAccount(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, namespaceName string, logger logr.Logger) error {
	// Create the service account - SHARED across multiple SBDConfigs in the same namespace
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sbd-agent",
			Namespace: namespaceName,
			Labels: map[string]string{
				"app":                          "sbd-agent",
				"app.kubernetes.io/name":       "sbd-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbd-operator",
				"app.kubernetes.io/managed-by": "sbd-operator",
			},
			Annotations: map[string]string{
				"sbd-operator/shared-resource": "true",
				"sbd-operator/managed-by":      "sbd-operator",
			},
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		// DO NOT set controller reference - allow multiple SBDConfigs to share this service account
		// Instead, use labels and annotations to track management
		if serviceAccount.Labels == nil {
			serviceAccount.Labels = make(map[string]string)
		}
		if serviceAccount.Annotations == nil {
			serviceAccount.Annotations = make(map[string]string)
		}

		// Update management labels
		serviceAccount.Labels["app"] = "sbd-agent"
		serviceAccount.Labels["app.kubernetes.io/name"] = "sbd-agent"
		serviceAccount.Labels["app.kubernetes.io/component"] = "agent"
		serviceAccount.Labels["app.kubernetes.io/part-of"] = "sbd-operator"
		serviceAccount.Labels["app.kubernetes.io/managed-by"] = "sbd-operator"
		serviceAccount.Annotations["sbd-operator/shared-resource"] = "true"
		serviceAccount.Annotations["sbd-operator/managed-by"] = "sbd-operator"

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update service account: %w", err)
	}

	if result == controllerutil.OperationResultCreated {
		logger.Info("Service account created for SBD agent", "serviceAccount", "sbd-agent", "namespace", namespaceName)
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonServiceAccountCreated,
			"Service account 'sbd-agent' created in namespace '%s'", namespaceName)
	}

	// Create the ClusterRoleBinding to use the existing sbd-agent-role
	// Use SBDConfig-specific name to avoid conflicts
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("sbd-agent-%s-%s", namespaceName, sbdConfig.Name),
			Labels: map[string]string{
				"app":                          "sbd-agent",
				"app.kubernetes.io/name":       "sbd-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbd-operator",
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbdconfig":                    sbdConfig.Name,
				"sbdconfig-namespace":          namespaceName,
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "sbd-agent",
				Namespace: namespaceName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "sbd-agent-role", // Use the generated cluster role with proper permissions
		},
	}

	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, clusterRoleBinding, func() error {
		// Do not set owner reference on cluster-scoped resources when owner is namespace-scoped
		// This is not allowed in Kubernetes and will cause the operation to fail
		// The ClusterRoleBinding will be managed by the operator without ownership
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update cluster role binding: %w", err)
	}

	if result == controllerutil.OperationResultCreated {
		logger.Info("ClusterRoleBinding created for SBD agent", "clusterRoleBinding", fmt.Sprintf("sbd-agent-%s-%s", namespaceName, sbdConfig.Name))
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonClusterRoleBindingCreated,
			"ClusterRoleBinding 'sbd-agent-%s-%s' created", namespaceName, sbdConfig.Name)
	}

	return nil
}

// buildDaemonSet constructs the desired DaemonSet based on the SBDConfig
func (r *SBDConfigReconciler) buildDaemonSet(sbdConfig *medik8sv1alpha1.SBDConfig, operatorImage string) *appsv1.DaemonSet {
	daemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfig.Name)
	labels := map[string]string{
		"app":        "sbd-agent",
		"component":  "sbd-agent",
		"version":    "latest",
		"managed-by": "sbd-operator",
		"sbdconfig":  sbdConfig.Name,
	}

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonSetName,
			Namespace: sbdConfig.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       "sbd-agent",
					"sbdconfig": sbdConfig.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"prometheus.io/scrape": "false",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "sbd-agent",
					HostNetwork:        true,
					HostPID:            true,
					DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
					PriorityClassName:  "system-node-critical",
					RestartPolicy:      corev1.RestartPolicyAlways,
					NodeSelector:       r.buildNodeSelector(sbdConfig),
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "kubernetes.io/os",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"linux"},
											},
										},
									},
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
						{Key: "CriticalAddonsOnly", Operator: corev1.TolerationOpExists},
						{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule},
						{Key: "node-role.kubernetes.io/master", Effect: corev1.TaintEffectNoSchedule},
					},
					Containers: []corev1.Container{
						{
							Name:            "sbd-agent",
							Image:           sbdConfig.Spec.GetImageWithOperatorImage(operatorImage),
							ImagePullPolicy: corev1.PullPolicy(sbdConfig.Spec.GetImagePullPolicy()),
							SecurityContext: &corev1.SecurityContext{
								Privileged:               &[]bool{true}[0],
								RunAsUser:                &[]int64{0}[0],
								RunAsGroup:               &[]int64{0}[0],
								RunAsNonRoot:             &[]bool{false}[0],
								ReadOnlyRootFilesystem:   &[]bool{false}[0],
								AllowPrivilegeEscalation: &[]bool{true}[0],
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{
										"SYS_ADMIN",
										"SYS_MODULE", // Required for loading kernel modules like softdog
									},
									Drop: []corev1.Capability{"ALL"},
								},
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeUnconfined,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: mustParseQuantity("128Mi"),
									corev1.ResourceCPU:    mustParseQuantity("50m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: mustParseQuantity("256Mi"),
									corev1.ResourceCPU:    mustParseQuantity("100m"),
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
									},
								},
								{
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
									},
								},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
									},
								},
								{
									Name: "POD_IP",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
									},
								},
							},
							Args:         r.buildSBDAgentArgs(sbdConfig),
							VolumeMounts: r.buildVolumeMounts(sbdConfig),
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "grep -l sbd-agent /proc/*/cmdline 2>/dev/null"},
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       30,
								TimeoutSeconds:      10,
								FailureThreshold:    3,
								SuccessThreshold:    1,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", fmt.Sprintf("test -c %s && grep -l sbd-agent /proc/*/cmdline 2>/dev/null", sbdConfig.Spec.GetSbdWatchdogPath())},
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
								TimeoutSeconds:      5,
								FailureThreshold:    3,
								SuccessThreshold:    1,
							},
							StartupProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "grep -l sbd-agent /proc/*/cmdline 2>/dev/null"},
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      5,
								FailureThreshold:    6,
								SuccessThreshold:    1,
							},
						},
					},
					Volumes:                       r.buildVolumes(sbdConfig),
					TerminationGracePeriodSeconds: &[]int64{30}[0],
				},
			},
		},
	}
}

// buildSBDAgentArgs builds the command line arguments for the sbd-agent container
func (r *SBDConfigReconciler) buildSBDAgentArgs(sbdConfig *medik8sv1alpha1.SBDConfig) []string {
	// Get configured watchdog timeout and calculate pet interval
	watchdogTimeout := sbdConfig.Spec.GetWatchdogTimeout()
	petInterval := sbdConfig.Spec.GetPetInterval()
	ioTimeout := sbdConfig.Spec.GetIOTimeout()
	rebootMethod := sbdConfig.Spec.GetRebootMethod()
	sbdTimeoutSeconds := sbdConfig.Spec.GetSBDTimeoutSeconds()
	sbdUpdateInterval := sbdConfig.Spec.GetSBDUpdateInterval()
	peerCheckInterval := sbdConfig.Spec.GetPeerCheckInterval()

	// Base arguments using shared flag constants
	args := []string{
		fmt.Sprintf("--%s=%s", agent.FlagWatchdogPath, sbdConfig.Spec.GetSbdWatchdogPath()),
		fmt.Sprintf("--%s=%s", agent.FlagWatchdogTimeout, watchdogTimeout.String()),
		fmt.Sprintf("--%s=%s", agent.FlagPetInterval, petInterval.String()),
		fmt.Sprintf("--%s=%s", agent.FlagLogLevel, sbdConfig.Spec.GetLogLevel()),
		fmt.Sprintf("--%s=%s", agent.FlagClusterName, sbdConfig.Name),
		fmt.Sprintf("--%s=%s", agent.FlagStaleNodeTimeout, sbdConfig.Spec.GetStaleNodeTimeout().String()),
		fmt.Sprintf("--io-timeout=%s", ioTimeout.String()),
		fmt.Sprintf("--%s=%s", agent.FlagRebootMethod, rebootMethod),
		fmt.Sprintf("--%s=%d", agent.FlagSBDTimeoutSeconds, sbdTimeoutSeconds),
		fmt.Sprintf("--%s=%s", agent.FlagSBDUpdateInterval, sbdUpdateInterval.String()),
		fmt.Sprintf("--%s=%s", agent.FlagPeerCheckInterval, peerCheckInterval.String()),
	}

	// Add shared storage arguments if configured
	if sbdConfig.Spec.HasSharedStorage() {
		// Set SBD device to a file within the shared storage mount
		sbdDevicePath := fmt.Sprintf("%s/%s", sbdConfig.Spec.GetSharedStorageMountPath(), agent.SharedStorageSBDDeviceFile)
		args = append(args, fmt.Sprintf("--%s=%s", agent.FlagSBDDevice, sbdDevicePath))

		// Enable file locking for shared storage safety
		args = append(args, fmt.Sprintf("--%s=true", agent.FlagSBDFileLocking))
	}

	return args
}

// buildNodeSelector builds the node selector for the DaemonSet, merging user-specified selectors with OS requirement
func (r *SBDConfigReconciler) buildNodeSelector(sbdConfig *medik8sv1alpha1.SBDConfig) map[string]string {
	// Start with the user-specified node selector (defaults to worker nodes only)
	nodeSelector := make(map[string]string)
	for k, v := range sbdConfig.Spec.GetNodeSelector() {
		nodeSelector[k] = v
	}

	// Always require Linux OS
	nodeSelector["kubernetes.io/os"] = "linux"

	return nodeSelector
}

// buildVolumeMounts builds the volume mounts for the sbd-agent container
func (r *SBDConfigReconciler) buildVolumeMounts(sbdConfig *medik8sv1alpha1.SBDConfig) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: "dev", MountPath: "/dev"},
		{Name: "sys", MountPath: "/sys", ReadOnly: true},
		{Name: "proc", MountPath: "/proc", ReadOnly: true},
	}

	// Add shared storage mount if configured
	if sbdConfig.Spec.HasSharedStorage() {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "shared-storage",
			MountPath: sbdConfig.Spec.GetSharedStorageMountPath(),
		})
	}

	return mounts
}

// buildVolumes builds the volumes for the DaemonSet pod spec
func (r *SBDConfigReconciler) buildVolumes(sbdConfig *medik8sv1alpha1.SBDConfig) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: "dev",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/dev",
					Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
				},
			},
		},
		{
			Name: "sys",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/sys",
					Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
				},
			},
		},
		{
			Name: "proc",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc",
					Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
				},
			},
		},
	}

	// Add shared storage volume if configured
	if sbdConfig.Spec.HasSharedStorage() {
		volumes = append(volumes, corev1.Volume{
			Name: "shared-storage",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name),
				},
			},
		})
	}

	return volumes
}

// updateStatus updates the SBDConfig status based on the DaemonSet state
func (r *SBDConfigReconciler) updateStatus(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, daemonSet *appsv1.DaemonSet, logger logr.Logger) error {
	// Check if we need to fetch the latest DaemonSet status
	latestDaemonSet := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      daemonSet.Name,
		Namespace: daemonSet.Namespace,
	}, latestDaemonSet)
	if err != nil {
		return err
	}

	// Update numeric status fields
	sbdConfig.Status.TotalNodes = latestDaemonSet.Status.DesiredNumberScheduled
	sbdConfig.Status.ReadyNodes = latestDaemonSet.Status.NumberReady

	// Determine DaemonSet readiness status
	daemonSetReady := latestDaemonSet.Status.NumberReady == latestDaemonSet.Status.DesiredNumberScheduled && latestDaemonSet.Status.DesiredNumberScheduled > 0

	// Set DaemonSet readiness condition
	if daemonSetReady {
		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionDaemonSetReady,
			metav1.ConditionTrue,
			"DaemonSetReady",
			fmt.Sprintf("All %d SBD agent pods are ready", latestDaemonSet.Status.NumberReady),
		)
	} else {
		var reason, message string
		if latestDaemonSet.Status.DesiredNumberScheduled == 0 {
			reason = "NoNodesScheduled"
			message = "No nodes are scheduled to run SBD agent pods"
		} else {
			reason = "PodsNotReady"
			message = fmt.Sprintf("%d of %d SBD agent pods are ready",
				latestDaemonSet.Status.NumberReady,
				latestDaemonSet.Status.DesiredNumberScheduled)
		}
		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionDaemonSetReady,
			metav1.ConditionFalse,
			reason,
			message,
		)
	}

	// Set shared storage readiness condition
	if sbdConfig.Spec.HasSharedStorage() {
		// For now, we'll assume shared storage is ready if the PVC name is specified
		// In the future, we could add more sophisticated checks
		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionSharedStorageReady,
			metav1.ConditionTrue,
			"SharedStorageConfigured",
			fmt.Sprintf("Shared storage PVC '%s' is configured", sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name)),
		)
	} else {
		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionSharedStorageReady,
			metav1.ConditionTrue,
			"SharedStorageNotRequired",
			"Shared storage is not configured and not required",
		)
	}

	// Set overall readiness condition
	if daemonSetReady && (sbdConfig.IsConditionTrue(medik8sv1alpha1.SBDConfigConditionSharedStorageReady)) {
		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionReady,
			metav1.ConditionTrue,
			"Ready",
			"SBDConfig is ready and all components are operational",
		)
	} else {
		var reasons []string
		if !daemonSetReady {
			reasons = append(reasons, "DaemonSet not ready")
		}
		if !sbdConfig.IsConditionTrue(medik8sv1alpha1.SBDConfigConditionSharedStorageReady) {
			reasons = append(reasons, "Shared storage not ready")
		}

		sbdConfig.SetCondition(
			medik8sv1alpha1.SBDConfigConditionReady,
			metav1.ConditionFalse,
			"NotReady",
			fmt.Sprintf("SBDConfig is not ready: %s", strings.Join(reasons, ", ")),
		)
	}

	// Update the status
	return r.Status().Update(ctx, sbdConfig)
}

// mustParseQuantity is a helper function for parsing resource quantities
func mustParseQuantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return q
}

// SetupWithManager sets up the controller with the Manager.
func (r *SBDConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("setup").WithValues("controller", "SBDConfig")

	logger.Info("Setting up SBDConfig controller")

	err := ctrl.NewControllerManagedBy(mgr).
		For(&medik8sv1alpha1.SBDConfig{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Named("sbdconfig").
		Complete(r)

	if err != nil {
		logger.Error(err, "Failed to setup SBDConfig controller")
		return err
	}

	logger.Info("SBDConfig controller setup completed successfully")
	return nil
}
