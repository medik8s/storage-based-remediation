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

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/pkg/agent"
	"github.com/medik8s/storage-based-remediation/pkg/retry"
)

// Event types and reasons for StorageBasedRemediationConfig controller
const (
	// Event types
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	// Event reasons for StorageBasedRemediationConfig operations
	ReasonStorageBasedRemediationConfigReconciled = "StorageBasedRemediationConfigReconciled"
	ReasonDaemonSetManaged                        = "DaemonSetManaged"
	ReasonServiceAccountCreated                   = "ServiceAccountCreated"
	ReasonClusterRoleBindingCreated               = "ClusterRoleBindingCreated"
	ReasonSCCManaged                              = "SCCManaged"
	ReasonReconcileError                          = "ReconcileError"
	ReasonDaemonSetError                          = "DaemonSetError"
	ReasonServiceAccountError                     = "ServiceAccountError"
	ReasonSCCError                                = "SCCError"
	ReasonValidationError                         = "ValidationError"
	ReasonCleanupCompleted                        = "CleanupCompleted"
	ReasonCleanupError                            = "CleanupError"
	ReasonPVCManaged                              = "PVCManaged"
	ReasonPVCError                                = "PVCError"
	ReasonSBRDeviceInitialized                    = "SBRDeviceInitialized"
	ReasonSBRDeviceInitError                      = "SBRDeviceInitWaiting"

	// Finalizer for cleanup operations
	StorageBasedRemediationConfigFinalizerName = "sbr-operator.medik8s.io/cleanup"

	// Default image constants
	DefaultSBRAgentImage = "sbr-agent:latest"
	SBROperatorName      = "sbr-operator"

	// Retry configuration constants for StorageBasedRemediationConfig controller
	// MaxStorageBasedRemediationConfigRetries is the maximum number of retry attempts for StorageBasedRemediationConfig operations
	MaxStorageBasedRemediationConfigRetries = 3
	// InitialStorageBasedRemediationConfigRetryDelay is the initial delay between StorageBasedRemediationConfig operation retries
	InitialStorageBasedRemediationConfigRetryDelay = 500 * time.Millisecond
	// MaxStorageBasedRemediationConfigRetryDelay is the maximum delay between StorageBasedRemediationConfig operation retries
	MaxStorageBasedRemediationConfigRetryDelay = 10 * time.Second
	// StorageBasedRemediationConfigRetryBackoffFactor is the exponential backoff factor for StorageBasedRemediationConfig operation retries
	StorageBasedRemediationConfigRetryBackoffFactor = 2.0
)

// StorageBasedRemediationConfigReconciler reconciles a StorageBasedRemediationConfig object
type StorageBasedRemediationConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Retry configuration for Kubernetes API operations
	retryConfig retry.Config

	// OpenShift detection cache
	isOpenShift *bool

	// Filter log
	FilterLog logr.Logger
}

// initializeRetryConfig initializes the retry configuration for StorageBasedRemediationConfig operations
func (r *StorageBasedRemediationConfigReconciler) initializeRetryConfig(logger logr.Logger) {
	r.retryConfig = retry.Config{
		MaxRetries:    MaxStorageBasedRemediationConfigRetries,
		InitialDelay:  InitialStorageBasedRemediationConfigRetryDelay,
		MaxDelay:      MaxStorageBasedRemediationConfigRetryDelay,
		BackoffFactor: StorageBasedRemediationConfigRetryBackoffFactor,
		Logger:        logger.WithName("sbrconfig-retry"),
	}
}

// isTransientKubernetesError determines if a Kubernetes API error is transient and should be retried
func (r *StorageBasedRemediationConfigReconciler) isTransientKubernetesError(err error) bool {
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
func (r *StorageBasedRemediationConfigReconciler) performKubernetesAPIOperationWithRetry(
	ctx context.Context, operation string, fn func() error, logger logr.Logger) error {
	return retry.Do(ctx, r.retryConfig, operation, func() error {
		err := fn()
		if err != nil {
			// Log retry attempt
			logger.V(1).Info("Retrying Kubernetes API operation", "operation", operation, "error", err)
			// Wrap error with retry information
			return retry.NewRetryableError(err, r.isTransientKubernetesError(err), operation)
		}
		return nil
	})
}

// emitEvent is a helper function to emit Kubernetes events for the StorageBasedRemediationConfig controller
func (r *StorageBasedRemediationConfigReconciler) emitEvent(object client.Object, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(object, eventType, reason, message)
	}
}

// emitEventf is a helper function to emit formatted Kubernetes events for the StorageBasedRemediationConfig controller
func (r *StorageBasedRemediationConfigReconciler) emitEventf(
	object client.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(object, eventType, reason, messageFmt, args...)
	}
}

// getOperatorImage discovers the operator's own image by querying the current pod
// It uses environment variables (POD_NAME, POD_NAMESPACE) to find the current pod
// and extracts the image from the pod spec
func (r *StorageBasedRemediationConfigReconciler) getOperatorImage(ctx context.Context, logger logr.Logger) string {
	// In CI, RELATED_IMAGE_SBR_AGENT is injected via `oc set env` after bundle installation,
	// pointing to the CI-built agent image. In non-CI runs this env var is not set,
	// so we fall through to pod image discovery and derivation.
	if img := os.Getenv("RELATED_IMAGE_SBR_AGENT"); img != "" {
		logger.Info("Using RELATED_IMAGE_SBR_AGENT for agent image", "image", img)
		return img
	}

	// Try to get pod information from environment variables (set by Downward API)
	podName := os.Getenv("POD_NAME")
	podNamespace := os.Getenv("POD_NAMESPACE")

	if podName == "" || podNamespace == "" {
		logger.Error(nil, "POD_NAME or POD_NAMESPACE environment variables not set, using fallback")
		return DefaultSBRAgentImage
	}

	// Get the current pod
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: podNamespace}, &pod)
	if err != nil {
		logger.Error(err, "Failed to get operator pod", "podName", podName, "podNamespace", podNamespace)
		return DefaultSBRAgentImage // Fallback to default
	}

	// Find the manager container (operator container)
	for _, container := range pod.Spec.Containers {
		if container.Name == "manager" {
			logger.Info("Found operator image", "image", container.Image)
			return container.Image
		}
	}

	// If manager container not found, use the first container's image
	if len(pod.Spec.Containers) > 0 {
		image := pod.Spec.Containers[0].Image
		logger.Error(nil, "Using first container image as operator image", "image", image)
		return image
	}

	logger.Error(nil, "No containers found in operator pod, using fallback")
	return DefaultSBRAgentImage
}

// isRunningOnOpenShift detects if the operator is running on OpenShift
// by checking for the presence of OpenShift-specific API resources
func (r *StorageBasedRemediationConfigReconciler) isRunningOnOpenShift(logger logr.Logger) bool {
	// Use cached result if available
	if r.isOpenShift != nil {
		return *r.isOpenShift
	}

	// Check for OpenShift-specific API groups
	discoveryClient := r.RESTMapper()

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

// ensureSCCPermissions ensures that the service account can use the required SCC on OpenShift.
// We use the built-in "privileged" SCC (Option A / SNR-style): sbr-agent gets "use" via the
// ClusterRole sbr-operator-sbr-agent-privileged-scc and a ClusterRoleBinding created in ensureServiceAccount.
// No custom SCC is required; this function is a no-op on OpenShift.
func (r *StorageBasedRemediationConfigReconciler) ensureSCCPermissions(
	ctx context.Context,
	sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig,
	namespaceName string,
	logger logr.Logger,
) (controllerutil.OperationResult, error) {
	if !r.isRunningOnOpenShift(logger) {
		logger.V(1).Info("Not running on OpenShift, skipping SCC management")
		return controllerutil.OperationResultNone, nil
	}
	// Option A: use built-in privileged SCC; permission is granted by ClusterRoleBinding in ensureServiceAccount.
	logger.V(1).Info("OpenShift detected; sbr-agent uses built-in privileged SCC via ClusterRoleBinding")
	return controllerutil.OperationResultNone, nil
}

// validateStorageClass validates that the specified storage class supports ReadWriteMany access mode
func (r *StorageBasedRemediationConfigReconciler) validateStorageClass(
	ctx context.Context, sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig, logger logr.Logger) error {
	if !sbrConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to validate
		return fmt.Errorf("no shared storage configured")
	}

	storageClassName := sbrConfig.Spec.GetSharedStorageStorageClass()
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

		// For NFS-based storage, validate mount options for SBR cache coherency
		if r.isNFSBasedProvisioner(provisioner) {
			if err := r.validateNFSMountOptions(storageClass, logger); err != nil {
				logger.Info("StorageClass mount options validation failed", "error", err)
				return fmt.Errorf("StorageClass '%s' mount options are not configured for SBR cache coherency: %w",
					storageClassName, err)
			}
			logger.Info("StorageClass mount options validation passed")
		}

		return nil
	}

	// Check if the provisioner is known to be incompatible
	if r.isRWXIncompatibleProvisioner(provisioner) {
		return fmt.Errorf(
			"StorageClass '%s' uses provisioner '%s' that does not support "+
				"ReadWriteMany access mode required for SBR shared storage",
			storageClassName, provisioner)
	}

	// For unknown provisioners, test with a temporary PVC
	logger.Info("StorageClass uses unknown provisioner, testing ReadWriteMany support", "provisioner", provisioner)
	if err := r.testRWXSupport(ctx, sbrConfig, storageClassName, logger); err != nil {
		return fmt.Errorf("StorageClass '%s' does not support ReadWriteMany access mode required for SBR shared storage: %w",
			storageClassName, err)
	}

	logger.Info("StorageClass validation passed", "provisioner", provisioner)
	return nil
}

// isRWXCompatibleProvisioner checks if a CSI provisioner is known to support ReadWriteMany
func (r *StorageBasedRemediationConfigReconciler) isRWXCompatibleProvisioner(provisioner string) bool {
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
		"cephfs.csi.ceph.com":                   true,
		"openshift-storage.cephfs.csi.ceph.com": true,

		// GlusterFS
		"gluster.org/glusterfs": true,

		// Other known RWX provisioners
		"nfs-provisioner": true,
		"csi-nfsplugin":   true,
	}

	return rwxProvisioners[provisioner]
}

// isRWXIncompatibleProvisioner checks if a CSI provisioner is known to NOT support ReadWriteMany
func (r *StorageBasedRemediationConfigReconciler) isRWXIncompatibleProvisioner(provisioner string) bool {
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

// isNFSBasedProvisioner checks if a provisioner uses NFS and requires mount option validation
func (r *StorageBasedRemediationConfigReconciler) isNFSBasedProvisioner(provisioner string) bool {
	// NFS-based provisioners that use standard NFS mount options
	nfsProvisioners := map[string]bool{
		// AWS EFS uses NFS4
		"efs.csi.aws.com": true,

		// Standard NFS provisioners
		"nfs.csi.k8s.io": true,
		"cluster.local/nfs-subdir-external-provisioner": true,
		"k8s-sigs.io/nfs-subdir-external-provisioner":   true,
		"nfs-provisioner": true,
		"csi-nfsplugin":   true,
	}

	return nfsProvisioners[provisioner]
}

// validateNFSMountOptions validates that NFS mount options include cache coherency settings for SBR
func (r *StorageBasedRemediationConfigReconciler) validateNFSMountOptions(storageClass *storagev1.StorageClass, logger logr.Logger) error {
	if storageClass != nil {
		logger.Info("Skipping NFS mount options validation - TODO: Until we find some storage that actually works with SBR")
		return nil
	}
	mountOptions := storageClass.MountOptions
	logger = logger.WithValues("mountOptions", mountOptions)

	// Required mount options for SBR cache coherency
	requiredOptions := []string{"cache=none", "sync"}
	recommendedOptions := []string{"local_lock=none"}

	// Check for required options
	missingRequired := []string{}
	for _, required := range requiredOptions {
		found := false
		for _, option := range mountOptions {
			if option == required {
				found = true
				break
			}
		}
		if !found {
			missingRequired = append(missingRequired, required)
		}
	}

	if len(missingRequired) > 0 {
		return fmt.Errorf("missing required NFS mount options for SBR cache coherency: %v. "+
			"These options are required to prevent NFS client-side caching issues "+
			"that can cause SBR heartbeat coordination failures",
			missingRequired)
	}

	// Check for recommended options (warning only)
	missingRecommended := []string{}
	for _, recommended := range recommendedOptions {
		found := false
		for _, option := range mountOptions {
			if option == recommended {
				found = true
				break
			}
		}
		if !found {
			missingRecommended = append(missingRecommended, recommended)
		}
	}

	if len(missingRecommended) > 0 {
		logger.Info("StorageClass is missing recommended NFS mount options for optimal SBR operation",
			"missingOptions", missingRecommended)
	}

	logger.Info("NFS mount options validation passed", "checkedOptions", requiredOptions)
	return nil
}

// testRWXSupport tests if a storage class actually supports ReadWriteMany by creating a temporary PVC
func (r *StorageBasedRemediationConfigReconciler) testRWXSupport(
	ctx context.Context, sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig, storageClassName string, logger logr.Logger) error {
	// Create a temporary PVC with ReadWriteMany to test compatibility
	testPVCName := fmt.Sprintf("%s-rwx-test", sbrConfig.Name)

	testPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPVCName,
			Namespace: sbrConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "sbr-operator",
				"app.kubernetes.io/component": "storage-validation",
				"sbrconfig":                   sbrConfig.Name,
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
	if err := controllerutil.SetControllerReference(sbrConfig, testPVC, r.Scheme); err != nil {
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
	err = r.Get(ctx, types.NamespacedName{Name: testPVCName, Namespace: sbrConfig.Namespace}, testPVC)
	if err != nil {
		return fmt.Errorf("failed to get test PVC status: %w", err)
	}

	// Check for events that indicate RWX incompatibility
	events := &corev1.EventList{}
	err = r.List(ctx, events, client.InNamespace(sbrConfig.Namespace),
		client.MatchingFields{"involvedObject.name": testPVCName})
	if err != nil {
		logger.Error(err, "Failed to list events for test PVC")
	} else {
		for _, evt := range events.Items {
			message := strings.ToLower(evt.Message)
			if strings.Contains(message, "readwritemany") &&
				(strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") ||
					strings.Contains(message, "invalid")) {
				return fmt.Errorf("storage class does not support ReadWriteMany access mode: %s", evt.Message)
			}
			if strings.Contains(message, "access mode") && strings.Contains(message, "not supported") {
				return fmt.Errorf("storage class does not support required access mode: %s", evt.Message)
			}
		}
	}

	logger.Info("ReadWriteMany test passed", "testPVC", testPVCName)
	return nil
}

// ensurePVC ensures that a PVC exists for shared storage when SharedStorageClass is specified
func (r *StorageBasedRemediationConfigReconciler) ensurePVC(
	ctx context.Context,
	sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig,
	logger logr.Logger,
) (
	controllerutil.OperationResult, error,
) {
	if !sbrConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to do
		return controllerutil.OperationResultNone, nil
	}

	pvcName := sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name)
	storageClassName := sbrConfig.Spec.GetSharedStorageStorageClass()

	logger = logger.WithValues(
		"pvc.name", pvcName,
		"pvc.namespace", sbrConfig.Namespace,
		"storageClass", storageClassName,
	)

	// Define the desired PVC
	desiredPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: sbrConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sbr-operator",
				"app.kubernetes.io/component":  "shared-storage",
				"app.kubernetes.io/managed-by": "sbr-operator",
				"sbrconfig":                    sbrConfig.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(sbrConfig.Spec.GetSharedStorageSize()),
				},
			},
			StorageClassName: &storageClassName,
		},
	}

	// Set controller reference
	if err := controllerutil.SetControllerReference(sbrConfig, desiredPVC, r.Scheme); err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to set controller reference on PVC: %w", err)
	}

	// Use CreateOrUpdate to manage the PVC
	actualPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: sbrConfig.Namespace,
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
		return controllerutil.SetControllerReference(sbrConfig, actualPVC, r.Scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to create or update PVC")
		return result, fmt.Errorf("failed to create or update PVC '%s': %w", pvcName, err)
	}

	logger.Info("PVC operation completed",
		"operation", result,
		"pvc.generation", actualPVC.Generation,
		"pvc.resourceVersion", actualPVC.ResourceVersion,
		"pvc.status.phase", actualPVC.Status.Phase)

	// Emit event for PVC management
	switch result {
	case controllerutil.OperationResultCreated:
		r.emitEventf(sbrConfig, EventTypeNormal, ReasonPVCManaged,
			"PVC '%s' for shared storage created successfully using StorageClass '%s'", pvcName, storageClassName)
		logger.Info("PVC created successfully")
	case controllerutil.OperationResultUpdated:
		r.emitEventf(sbrConfig, EventTypeNormal, ReasonPVCManaged,
			"PVC '%s' for shared storage updated successfully", pvcName)
		logger.Info("PVC updated successfully")
	default:
		logger.V(1).Info("PVC unchanged")
	}

	return result, nil
}

// ensureSBRDevice ensures that the SBR device file exists in shared storage when SharedStorageClass is specified
func (r *StorageBasedRemediationConfigReconciler) ensureSBRDevice(
	ctx context.Context,
	sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig,
	logger logr.Logger,
) (
	controllerutil.OperationResult, error,
) {
	if !sbrConfig.Spec.HasSharedStorage() {
		// No shared storage configured, nothing to do
		return controllerutil.OperationResultNone, nil
	}

	pvcName := sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name)
	jobName := fmt.Sprintf("%s-sbr-device-init", sbrConfig.Name)

	logger = logger.WithValues(
		"job.name", jobName,
		"job.namespace", sbrConfig.Namespace,
		"pvc.name", pvcName,
	)

	// Define the desired Job for SBR device initialization
	desiredJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: sbrConfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sbr-operator",
				"app.kubernetes.io/component":  "sbr-device-init",
				"app.kubernetes.io/managed-by": "sbr-operator",
				"sbrconfig":                    sbrConfig.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":       "sbr-operator",
						"app.kubernetes.io/component":  "sbr-device-init",
						"app.kubernetes.io/managed-by": "sbr-operator",
						"sbrconfig":                    sbrConfig.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "sbr-device-init",
							Image:   "registry.access.redhat.com/ubi8/ubi-minimal:latest",
							Command: []string{"sh", "-c"},
							Args: []string{
								fmt.Sprintf(`
set -e
SBR_DEVICE_SIZE_KB=1024
SBR_DEVICE_PREFIX="%s"
HEARTBEAT_DEVICE_PATH="$SBR_DEVICE_PREFIX/%s"
FENCE_DEVICE_PATH="$HEARTBEAT_DEVICE_PATH%s"
NODE_MAP_FILE="$HEARTBEAT_DEVICE_PATH%s"
CLUSTER_NAME="${CLUSTER_NAME:-default-cluster}"

echo "Initializing SBR devices: heartbeat at $HEARTBEAT_DEVICE_PATH, fence at $FENCE_DEVICE_PATH"
echo "Using shared node mapping file: $NODE_MAP_FILE"

# Function to create initial node mapping file
create_initial_node_mapping() {
    local node_map_file="$1"
    local cluster_name="$2"
    
    echo "Creating initial shared node mapping file: $node_map_file"
    
    # Create minimal valid node mapping table JSON
    local json_data="{
  \"magic\": [83, 66, 68, 78, 77, 65, 80, 49],
  \"version\": 1,
  \"cluster_name\": \"$cluster_name\",
  \"device_type\": \"shared\",
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

# Function to create SBR device file
create_sbr_device() {
    local device_path="$1"
    local device_type="$2"
    
    echo "Creating $device_type SBR device at $device_path"
    
    # Create the SBR device file with specific size (1MB = 1024KB)
    # This provides space for multiple node slots and metadata
    dd if=/dev/zero of="$device_path" bs=1024 count=$SBR_DEVICE_SIZE_KB
    
    # Set appropriate permissions for the SBR device
    chmod 664 "$device_path"
    
    echo "$device_type SBR device created successfully at $device_path"
}

# Check if both heartbeat and fence devices and the shared node mapping file exist
HEARTBEAT_EXISTS=false
FENCE_EXISTS=false
NODE_MAP_EXISTS=false

if [ -f "$HEARTBEAT_DEVICE_PATH" ] && [ -s "$HEARTBEAT_DEVICE_PATH" ]; then
    echo "Heartbeat SBR device already exists and is non-empty"
    HEARTBEAT_EXISTS=true
fi

if [ -f "$FENCE_DEVICE_PATH" ] && [ -s "$FENCE_DEVICE_PATH" ]; then
    echo "Fence SBR device already exists and is non-empty"
    FENCE_EXISTS=true
fi

if [ -f "$NODE_MAP_FILE" ] && [ -s "$NODE_MAP_FILE" ]; then
    echo "Shared node mapping file already exists and is non-empty"
    NODE_MAP_EXISTS=true
fi

if [ "$HEARTBEAT_EXISTS" = true ] && [ "$FENCE_EXISTS" = true ] && [ "$NODE_MAP_EXISTS" = true ]; then
    echo "Both SBR devices and shared node mapping are properly initialized"
    exit 0
fi

# Create heartbeat device if needed
if [ "$HEARTBEAT_EXISTS" = false ]; then
    echo "Creating heartbeat SBR device..."
    create_sbr_device "$HEARTBEAT_DEVICE_PATH" "heartbeat"
fi

# Create fence device if needed
if [ "$FENCE_EXISTS" = false ]; then
    echo "Creating fence SBR device..."
    create_sbr_device "$FENCE_DEVICE_PATH" "fence"
fi

# Create shared node mapping file if needed
if [ "$NODE_MAP_EXISTS" = false ]; then
    echo "Creating shared node mapping file..."
    create_initial_node_mapping "$NODE_MAP_FILE" "$CLUSTER_NAME"
fi

# Verify all components were created successfully
if [ -f "$HEARTBEAT_DEVICE_PATH" ] && [ -s "$HEARTBEAT_DEVICE_PATH" ] && \
   [ -f "$FENCE_DEVICE_PATH" ] && [ -s "$FENCE_DEVICE_PATH" ] && \
   [ -f "$NODE_MAP_FILE" ] && [ -s "$NODE_MAP_FILE" ]; then
    echo "Both SBR devices and shared node mapping successfully initialized:"
    echo "  Heartbeat device: $HEARTBEAT_DEVICE_PATH ($(ls -lh "$HEARTBEAT_DEVICE_PATH" | awk '{print $5}'))"
    echo "  Fence device: $FENCE_DEVICE_PATH ($(ls -lh "$FENCE_DEVICE_PATH" | awk '{print $5}'))"
    echo "  Shared node mapping: $NODE_MAP_FILE ($(ls -lh "$NODE_MAP_FILE" | awk '{print $5}'))"
else
    echo "ERROR: Failed to create one or more SBR devices or the shared node mapping file"
    exit 1
fi

echo "SBR devices initialization completed successfully"
`,
									agent.SharedStorageSBRDeviceDirectory,
									agent.SharedStorageSBRDeviceFile,
									agent.SharedStorageFenceDeviceSuffix,
									agent.SharedStorageNodeMappingSuffix),
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER_NAME",
									Value: sbrConfig.Name,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "shared-storage",
									MountPath: sbrConfig.Spec.GetSharedStorageMountPath(),
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
	if err := controllerutil.SetControllerReference(sbrConfig, desiredJob, r.Scheme); err != nil {
		return controllerutil.OperationResultNone,
			fmt.Errorf(
				"failed to set controller reference on SBR device init job: %w",
				err,
			)
	}

	// Check if job already exists and is completed
	existingJob := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: sbrConfig.Namespace}, existingJob)
	if err == nil {
		// Job exists, check if it's completed successfully
		if existingJob.Status.Succeeded > 0 {
			logger.V(1).Info("SBR device initialization job already completed successfully")
			return controllerutil.OperationResultNone, nil
		}

		// If job failed, delete it so it can be recreated
		if existingJob.Status.Failed > 0 {
			logger.Info("SBR device initialization job failed, recreating...",
				"failedCount", existingJob.Status.Failed)
			if err := r.Delete(ctx, existingJob); err != nil {
				logger.Error(err, "Failed to delete failed SBR device init job")
				return controllerutil.OperationResultNone, fmt.Errorf("failed to delete failed SBR device init job: %w", err)
			}
			// Wait a bit for deletion to complete
			return controllerutil.OperationResultUpdated, nil
		}

		// Job is still running - prevent DaemonSet creation until job completes
		logger.Info("SBR device initialization job is still running, waiting for completion before creating DaemonSet",
			"job.name", jobName,
			"job.active", existingJob.Status.Active,
			"job.succeeded", existingJob.Status.Succeeded,
			"job.failed", existingJob.Status.Failed,
			"job.conditions", len(existingJob.Status.Conditions))
		return controllerutil.OperationResultNone, fmt.Errorf(
			"SBR device initialization job '%s' is still running, DaemonSet creation will be delayed until job completes",
			jobName)
	} else if !errors.IsNotFound(err) {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to get SBR device init job: %w", err)
	}

	// Create the job
	logger.Info("Creating SBR device initialization job")
	if err := r.Create(ctx, desiredJob); err != nil {
		logger.Error(err, "Failed to create SBR device initialization job")
		r.emitEventf(sbrConfig, EventTypeWarning, ReasonSBRDeviceInitError,
			"Failed to create SBR device initialization job: %v", err)
		return controllerutil.OperationResultNone, fmt.Errorf("failed to create SBR device initialization job: %w", err)
	}

	logger.Info("SBR device initialization job created successfully, waiting for completion before creating DaemonSet")
	r.emitEventf(sbrConfig, EventTypeNormal, ReasonSBRDeviceInitialized,
		"SBR device initialization job '%s' created successfully", jobName)

	return controllerutil.OperationResultCreated, nil
}

// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=use,resourceNames=privileged

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For StorageBasedRemediationConfig, this implementation deploys and manages the SBR Agent DaemonSet.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *StorageBasedRemediationConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("sbrconfig-controller").WithValues(
		"request", req.NamespacedName,
		"controller", "StorageBasedRemediationConfig",
	)

	// Initialize retry configuration if not already done
	if r.retryConfig.MaxRetries == 0 {
		r.initializeRetryConfig(logger)
	}

	// Retrieve the StorageBasedRemediationConfig object
	var sbrConfig medik8sv1alpha1.StorageBasedRemediationConfig
	err := r.Get(ctx, req.NamespacedName, &sbrConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// The StorageBasedRemediationConfig resource was deleted
			logger.Info("StorageBasedRemediationConfig resource not found, it may have been deleted",
				"name", req.Name,
				"namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue with backoff for transient errors
		logger.Error(err, "Failed to get StorageBasedRemediationConfig",
			"name", req.Name,
			"namespace", req.Namespace)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Add resource-specific context to logger
	logger = logger.WithValues(
		"sbrconfig.name", sbrConfig.Name,
		"sbrconfig.namespace", sbrConfig.Namespace,
		"sbrconfig.generation", sbrConfig.Generation,
		"sbrconfig.resourceVersion", sbrConfig.ResourceVersion,
	)

	// Handle deletion with finalizer
	if sbrConfig.DeletionTimestamp != nil {
		logger.Info("StorageBasedRemediationConfig is being deleted, performing cleanup")
		return r.handleDeletion(ctx, &sbrConfig, logger)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&sbrConfig, StorageBasedRemediationConfigFinalizerName) {
		logger.Info("Adding finalizer to StorageBasedRemediationConfig")
		controllerutil.AddFinalizer(&sbrConfig, StorageBasedRemediationConfigFinalizerName)
		return ctrl.Result{Requeue: true}, r.Update(ctx, &sbrConfig)
	}

	// Get the operator image first for logging and DaemonSet creation
	operatorImage := r.getOperatorImage(ctx, logger)

	agentImage, err := medik8sv1alpha1.DeriveAgentImageFromOperator(operatorImage)
	if err != nil {
		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	}
	logger.Info("Resolved agent image", "agentImage", agentImage)
	logger.V(1).Info("Starting StorageBasedRemediationConfig reconciliation",
		"spec.image", agentImage,
		"namespace", sbrConfig.Namespace,
		"spec.sbrWatchdogPath", sbrConfig.Spec.GetWatchdogPath())

	// Validate the StorageBasedRemediationConfig spec
	if err := sbrConfig.Spec.ValidateAll(); err != nil {
		logger.Error(err, "StorageBasedRemediationConfig validation failed")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonValidationError,
			"StorageBasedRemediationConfig validation failed: %v", err)
		// Don't requeue on validation errors - user needs to fix the configuration
		return ctrl.Result{}, fmt.Errorf("StorageBasedRemediationConfig validation failed: %w", err)
	}

	// Note: Node selector overlap validation is now handled by the admission webhook
	// to provide immediate feedback and prevent invalid configurations from being created

	// Ensure the service account and RBAC resources exist with retry logic
	// Deploy in the same namespace as the StorageBasedRemediationConfig CR
	result, err := r.ensureServiceAccount(ctx, &sbrConfig, sbrConfig.Namespace, logger)
	if err != nil {
		logger.Error(err, "Failed to ensure service account exists after retries",
			"namespace", sbrConfig.Namespace,
			"operation", "serviceaccount-creation")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonServiceAccountError,
			"Failed to ensure service account 'sbr-agent' exists in namespace '%s': %v", sbrConfig.Namespace, err)

		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err

	} else if result != controllerutil.OperationResultNone {
		return ctrl.Result{Requeue: true}, err
	}

	// Ensure SCC permissions are configured for OpenShift
	result, err = r.ensureSCCPermissions(ctx, &sbrConfig, sbrConfig.Namespace, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure SCC permissions after retries",
			"namespace", sbrConfig.Namespace,
			"operation", "scc-permissions")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonSCCError,
			"Failed to ensure SCC permissions for service account 'sbr-agent' in namespace '%s': %v", sbrConfig.Namespace, err)

		// Return requeue with backoff for transient errors
		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	} else if result != controllerutil.OperationResultNone {
		return ctrl.Result{Requeue: true}, err
	}

	// Validate storage class compatibility
	err = r.performKubernetesAPIOperationWithRetry(ctx, "validate storage class", func() error {
		return r.validateStorageClass(ctx, &sbrConfig, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to validate storage class compatibility after retries",
			"namespace", sbrConfig.Namespace,
			"operation", "storage-class-validation")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonPVCError,
			"Failed to validate storage class compatibility for PVC '%s': %v",
			sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name), err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Ensure PVC exists for shared storage
	action, err := r.ensurePVC(ctx, &sbrConfig, logger)
	if err != nil {
		logger.Error(err, "Waiting for PVC to be created",
			"namespace", sbrConfig.Namespace,
			"operation", "pvc-creation")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonPVCError,
			"Waiting for PVC for shared storage in namespace '%s': %v", sbrConfig.Namespace, err)
	} else if action != controllerutil.OperationResultNone {
		// Return requeue with backoff
		return ctrl.Result{Requeue: true}, err
	}

	// Ensure SBR device exists in shared storage
	action, err = r.ensureSBRDevice(ctx, &sbrConfig, logger)
	if err != nil {
		logger.Error(err, "Waiting for SBR device to be initialized",
			"namespace", sbrConfig.Namespace,
			"operation", "sbr-device-init")
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonSBRDeviceInitError, err.Error())

		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	} else if action != controllerutil.OperationResultNone {
		return ctrl.Result{Requeue: true}, err
	}

	// Define the desired DaemonSet
	desiredDaemonSet := r.buildDaemonSet(&sbrConfig, agentImage)
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

	daemonSetLogger.Info("Creating or updating DaemonSet",
		"operation", "daemonset-create-or-update",
		"desired.image", desiredDaemonSet.Spec.Template.Spec.Containers[0].Image)
	daemonSetLogger.Info("SBR agent DaemonSet watchdog configuration",
		"watchdogPath", sbrConfig.Spec.GetWatchdogPath())

	action, err = controllerutil.CreateOrUpdate(ctx, r.Client, actualDaemonSet, func() error {
		// Update the DaemonSet spec with the desired configuration
		actualDaemonSet.Spec = desiredDaemonSet.Spec
		actualDaemonSet.Labels = desiredDaemonSet.Labels
		actualDaemonSet.Annotations = desiredDaemonSet.Annotations

		// Set the controller reference
		return controllerutil.SetControllerReference(&sbrConfig, actualDaemonSet, r.Scheme)
	})
	if err != nil {
		daemonSetLogger.Error(err, "Failed to create or update DaemonSet after retries",
			"operation", "daemonset-create-or-update",
			"desired.image", desiredDaemonSet.Spec.Template.Spec.Containers[0].Image)
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonDaemonSetError,
			"Failed to create or update DaemonSet '%s': %v", desiredDaemonSet.Name, err)

		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	} else if action != controllerutil.OperationResultNone {
		// Emit event for DaemonSet management
		r.emitEventf(&sbrConfig, EventTypeNormal, ReasonDaemonSetManaged,
			"DaemonSet '%s' for SBR Agent %s successfully", actualDaemonSet.Name, action)
		daemonSetLogger.Info(fmt.Sprintf("DaemonSet %s successfully", action))
	}

	// Update the StorageBasedRemediationConfig status with retry logic
	err = r.performKubernetesAPIOperationWithRetry(ctx, "update StorageBasedRemediationConfig status", func() error {
		return r.updateStatus(ctx, &sbrConfig, actualDaemonSet)
	}, logger)
	if err != nil {
		logger.Error(err, "Failed to update StorageBasedRemediationConfig status after retries",
			"operation", "status-update",
			"daemonset.name", actualDaemonSet.Name)
		r.emitEventf(&sbrConfig, EventTypeWarning, ReasonReconcileError,
			"Failed to update StorageBasedRemediationConfig status: %v", err)

		// Return requeue with backoff for transient errors
		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	}

	logger.Info("Successfully reconciled StorageBasedRemediationConfig",
		"operation", "reconcile-complete",
		"daemonset.name", actualDaemonSet.Name)

	// Emit success event for StorageBasedRemediationConfig reconciliation
	r.emitEventf(&sbrConfig, EventTypeNormal, ReasonStorageBasedRemediationConfigReconciled,
		"StorageBasedRemediationConfig '%s' successfully reconciled", sbrConfig.Name)

	return ctrl.Result{}, nil
}

// handleDeletion handles the cleanup when an StorageBasedRemediationConfig is being deleted
func (r *StorageBasedRemediationConfigReconciler) handleDeletion(
	ctx context.Context, sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Starting StorageBasedRemediationConfig cleanup")

	// Clean up the ClusterRoleBinding specific to this StorageBasedRemediationConfig
	clusterRoleBindingName := fmt.Sprintf("sbr-agent-%s-%s", sbrConfig.Namespace, sbrConfig.Name)
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleBindingName,
		},
	}

	err := r.Delete(ctx, clusterRoleBinding)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete ClusterRoleBinding during cleanup", "clusterRoleBinding", clusterRoleBindingName)
		r.emitEventf(sbrConfig, EventTypeWarning, ReasonCleanupError,
			"Failed to delete ClusterRoleBinding '%s': %v", clusterRoleBindingName, err)
		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	}

	if err == nil {
		logger.Info("ClusterRoleBinding deleted successfully", "clusterRoleBinding", clusterRoleBindingName)
	}

	// Note: We don't delete the shared service account because other StorageBasedRemediationConfigs in the same namespace might be using it
	// The service account will be cleaned up when the namespace is deleted or manually by administrators

	// Note: DaemonSet cleanup is handled automatically by Kubernetes garbage collection due to OwnerReference

	// Remove the finalizer to allow deletion
	controllerutil.RemoveFinalizer(sbrConfig, StorageBasedRemediationConfigFinalizerName)
	err = r.Update(ctx, sbrConfig)
	if err != nil {
		logger.Error(err, "Failed to remove finalizer from StorageBasedRemediationConfig")
		r.emitEventf(sbrConfig, EventTypeWarning, ReasonCleanupError,
			"Failed to remove finalizer: %v", err)
		return ctrl.Result{RequeueAfter: InitialStorageBasedRemediationConfigRetryDelay}, err
	}

	logger.Info("StorageBasedRemediationConfig cleanup completed successfully")
	r.emitEventf(sbrConfig, EventTypeNormal, ReasonCleanupCompleted,
		"StorageBasedRemediationConfig cleanup completed successfully")

	return ctrl.Result{}, nil
}

// ensureServiceAccount creates the service account and RBAC resources if they don't exist
func (r *StorageBasedRemediationConfigReconciler) ensureServiceAccount(
	ctx context.Context,
	sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig,
	namespaceName string,
	logger logr.Logger,
) (controllerutil.OperationResult, error) {
	// Create the service account - SHARED across multiple StorageBasedRemediationConfigs in the same namespace
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sbr-agent",
			Namespace: namespaceName,
			Labels: map[string]string{
				"app":                          "sbr-agent",
				"app.kubernetes.io/name":       "sbr-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbr-operator",
				"app.kubernetes.io/managed-by": "sbr-operator",
			},
			Annotations: map[string]string{
				"sbr-operator/shared-resource": "true",
				"sbr-operator/managed-by":      "sbr-operator",
			},
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		// DO NOT set controller reference - allow multiple StorageBasedRemediationConfigs to share this service account
		// Instead, use labels and annotations to track management
		if serviceAccount.Labels == nil {
			serviceAccount.Labels = make(map[string]string)
		}
		if serviceAccount.Annotations == nil {
			serviceAccount.Annotations = make(map[string]string)
		}

		// Update management labels
		serviceAccount.Labels["app"] = "sbr-agent"
		serviceAccount.Labels["app.kubernetes.io/name"] = "sbr-agent"
		serviceAccount.Labels["app.kubernetes.io/component"] = "agent"
		serviceAccount.Labels["app.kubernetes.io/part-of"] = SBROperatorName
		serviceAccount.Labels["app.kubernetes.io/managed-by"] = SBROperatorName
		serviceAccount.Annotations["sbr-operator/shared-resource"] = "true"
		serviceAccount.Annotations["sbr-operator/managed-by"] = SBROperatorName

		return nil
	})

	if err != nil {
		return result, fmt.Errorf("failed to create or update service account: %w", err)
	}

	if result == controllerutil.OperationResultCreated {
		logger.Info("Service account created for SBR agent", "serviceAccount", "sbr-agent", "namespace", namespaceName)
		r.emitEventf(sbrConfig, EventTypeNormal, ReasonServiceAccountCreated,
			"Service account 'sbr-agent' created in namespace '%s'", namespaceName)
	}

	// Create the ClusterRoleBinding to use the existing sbr-agent-role
	// Use StorageBasedRemediationConfig-specific name to avoid conflicts
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("sbr-agent-%s-%s", namespaceName, sbrConfig.Name),
			Labels: map[string]string{
				"app":                          "sbr-agent",
				"app.kubernetes.io/name":       "sbr-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbr-operator",
				"app.kubernetes.io/managed-by": "sbr-operator",
				"sbrconfig":                    sbrConfig.Name,
				"sbrconfig-namespace":          namespaceName,
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "sbr-agent",
				Namespace: namespaceName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "sbr-operator-sbr-agent-role", // Use the generated cluster role with proper permissions
		},
	}

	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, clusterRoleBinding, func() error {
		// Do not set owner reference on cluster-scoped resources when owner is namespace-scoped
		// This is not allowed in Kubernetes and will cause the operation to fail
		// The ClusterRoleBinding will be managed by the operator without ownership
		return nil
	})

	if err != nil {
		return result, fmt.Errorf("failed to create or update cluster role binding: %w", err)
	}

	if result == controllerutil.OperationResultCreated {
		logger.Info("ClusterRoleBinding created for SBR agent",
			"clusterRoleBinding", fmt.Sprintf("sbr-agent-%s-%s", namespaceName, sbrConfig.Name))
		r.emitEventf(sbrConfig, EventTypeNormal, ReasonClusterRoleBindingCreated,
			"ClusterRoleBinding 'sbr-agent-%s-%s' created", namespaceName, sbrConfig.Name)
	}

	// Bind sbr-agent SA to privileged SCC (OpenShift) so pods can run without a custom SCC.
	privilegedSCCBindingName := fmt.Sprintf("sbr-operator-sbr-agent-privileged-scc-%s", namespaceName)
	privilegedSCCBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: privilegedSCCBindingName,
			Labels: map[string]string{
				"app":                          "sbr-agent",
				"app.kubernetes.io/name":       "sbr-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbr-operator",
				"app.kubernetes.io/managed-by": "sbr-operator",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "sbr-agent",
				Namespace: namespaceName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "sbr-operator-sbr-agent-privileged-scc",
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, privilegedSCCBinding, func() error { return nil })
	if err != nil {
		return result, fmt.Errorf("failed to create or update privileged SCC cluster role binding: %w", err)
	}

	return result, nil
}

// buildDaemonSet constructs the desired DaemonSet based on the StorageBasedRemediationConfig
func (r *StorageBasedRemediationConfigReconciler) buildDaemonSet(sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig, agentImage string) *appsv1.DaemonSet {
	daemonSetName := fmt.Sprintf("sbr-agent-%s", sbrConfig.Name)
	labels := map[string]string{
		"app":        "sbr-agent",
		"component":  "sbr-agent",
		"version":    "latest",
		"managed-by": "sbr-operator",
		"sbrconfig":  sbrConfig.Name,
	}

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonSetName,
			Namespace: sbrConfig.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       "sbr-agent",
					"sbrconfig": sbrConfig.Name,
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
					ServiceAccountName: "sbr-agent",
					HostNetwork:        true,
					HostPID:            true,
					DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
					PriorityClassName:  "system-node-critical",
					RestartPolicy:      corev1.RestartPolicyAlways,
					NodeSelector:       r.buildNodeSelector(sbrConfig),
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
							Name:  "sbr-agent",
							Image: agentImage,
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
							Args:         r.buildSBRAgentArgs(sbrConfig),
							VolumeMounts: r.buildVolumeMounts(sbrConfig),
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "grep -l sbr-agent /proc/*/cmdline 2>/dev/null"},
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
										Command: []string{"/bin/sh", "-c",
											fmt.Sprintf("test -c %s && grep -l sbr-agent /proc/*/cmdline 2>/dev/null",
												sbrConfig.Spec.GetWatchdogPath())},
									},
								},
								InitialDelaySeconds: 60,
								PeriodSeconds:       10,
								TimeoutSeconds:      5,
								FailureThreshold:    6,
								SuccessThreshold:    1,
							},
							StartupProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "grep -l sbr-agent /proc/*/cmdline 2>/dev/null"},
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
					Volumes:                       r.buildVolumes(sbrConfig),
					TerminationGracePeriodSeconds: &[]int64{30}[0],
				},
			},
		},
	}
}

// buildSBRAgentArgs builds the command line arguments for the sbr-agent container
func (r *StorageBasedRemediationConfigReconciler) buildSBRAgentArgs(sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig) []string {
	maxConsecutiveFailures := sbrConfig.Spec.GetMaxConsecutiveFailures()
	args := []string{
		fmt.Sprintf("--%s=%s", agent.FlagWatchdogPath, sbrConfig.Spec.GetWatchdogPath()),
		fmt.Sprintf("--%s=%s", agent.FlagLogLevel, agent.LogLevel),
		fmt.Sprintf("--%s=%s", agent.FlagClusterName, sbrConfig.Name),
		fmt.Sprintf("--%s=%s", agent.FlagStaleNodeTimeout, agent.StaleNodeTimeout),
		fmt.Sprintf("--io-timeout=%s", agent.IoTimeout),
		fmt.Sprintf("--%s=%s", agent.FlagRebootMethod, agent.RebootMethod),
		fmt.Sprintf("--%s=%d", agent.FlagSBRTimeoutSeconds, agent.SbrTimeoutSeconds),
		fmt.Sprintf("--%s=%d", agent.FlagMaxConsecutiveFailures, maxConsecutiveFailures),
		fmt.Sprintf("--%s=%s", agent.FlagSBRUpdateInterval, agent.SbrUpdateInterval),
		fmt.Sprintf("--%s=%s", agent.FlagPeerCheckInterval, agent.PeerCheckInterval),
	}

	// Add shared storage arguments if configured
	if sbrConfig.Spec.HasSharedStorage() {
		// Set heartbeat device to a file within the shared storage mount
		// The fence device will be automatically generated by appending the fence suffix
		heartbeatDevicePath := fmt.Sprintf("%s/%s",
			sbrConfig.Spec.GetSharedStorageMountPath(), agent.SharedStorageSBRDeviceFile)
		args = append(args, fmt.Sprintf("--%s=%s", agent.FlagSBRDevice, heartbeatDevicePath))

		// Enable file locking for shared storage safety
		args = append(args, fmt.Sprintf("--%s=true", agent.FlagSBRFileLocking))
	}

	if sbrConfig.Spec.GetDetectOnlyMode() {
		args = append(args, fmt.Sprintf("--%s=true", agent.FlagDetectOnlyMode))
	}

	return args
}

// buildNodeSelector builds the node selector for the DaemonSet, merging user-specified selectors with OS requirement
func (r *StorageBasedRemediationConfigReconciler) buildNodeSelector(sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig) map[string]string {
	// Start with the user-specified node selector (defaults to worker nodes only)
	nodeSelector := make(map[string]string)
	for k, v := range sbrConfig.Spec.GetNodeSelector() {
		nodeSelector[k] = v
	}

	// Always require Linux OS
	nodeSelector["kubernetes.io/os"] = "linux"

	return nodeSelector
}

// buildVolumeMounts builds the volume mounts for the sbr-agent container
func (r *StorageBasedRemediationConfigReconciler) buildVolumeMounts(sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: "dev", MountPath: "/dev"},
		{Name: "sys", MountPath: "/sys", ReadOnly: true},
		{Name: "proc", MountPath: "/proc", ReadOnly: true},
	}

	// Add shared storage mount if configured
	if sbrConfig.Spec.HasSharedStorage() {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "shared-storage",
			MountPath: sbrConfig.Spec.GetSharedStorageMountPath(),
		})
	}

	return mounts
}

// buildVolumes builds the volumes for the DaemonSet pod spec
func (r *StorageBasedRemediationConfigReconciler) buildVolumes(sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig) []corev1.Volume {
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
	if sbrConfig.Spec.HasSharedStorage() {
		volumes = append(volumes, corev1.Volume{
			Name: "shared-storage",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name),
				},
			},
		})
	}

	return volumes
}

// updateStatus updates the StorageBasedRemediationConfig status based on the DaemonSet state
func (r *StorageBasedRemediationConfigReconciler) updateStatus(
	ctx context.Context, sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig, daemonSet *appsv1.DaemonSet) error {
	// Check if we need to fetch the latest DaemonSet status
	latestDaemonSet := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      daemonSet.Name,
		Namespace: daemonSet.Namespace,
	}, latestDaemonSet)
	if err != nil {
		return err
	}

	// Determine DaemonSet readiness status
	daemonSetReady := latestDaemonSet.Status.NumberReady == latestDaemonSet.Status.DesiredNumberScheduled &&
		latestDaemonSet.Status.DesiredNumberScheduled > 0

	// Set DaemonSet readiness condition
	if daemonSetReady {
		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionDaemonSetReady,
			metav1.ConditionTrue,
			"DaemonSetReady",
			fmt.Sprintf("All %d SBR agent pods are ready", latestDaemonSet.Status.NumberReady),
		)
	} else {
		var reason, message string
		if latestDaemonSet.Status.DesiredNumberScheduled == 0 {
			reason = "NoNodesScheduled"
			message = "No nodes are scheduled to run SBR agent pods"
		} else {
			reason = "PodsNotReady"
			message = fmt.Sprintf("%d of %d SBR agent pods are ready",
				latestDaemonSet.Status.NumberReady,
				latestDaemonSet.Status.DesiredNumberScheduled)
		}
		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionDaemonSetReady,
			metav1.ConditionFalse,
			reason,
			message,
		)
	}

	// Set shared storage readiness condition
	if sbrConfig.Spec.HasSharedStorage() {
		// For now, we'll assume shared storage is ready if the PVC name is specified
		// In the future, we could add more sophisticated checks
		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionSharedStorageReady,
			metav1.ConditionTrue,
			"SharedStorageConfigured",
			fmt.Sprintf("Shared storage PVC '%s' is configured", sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name)),
		)
	} else {
		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionSharedStorageReady,
			metav1.ConditionTrue,
			"SharedStorageNotRequired",
			"Shared storage is not configured and not required",
		)
	}

	// Set overall readiness condition
	if daemonSetReady && (sbrConfig.IsConditionTrue(medik8sv1alpha1.SBRConfigConditionSharedStorageReady)) {
		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionReady,
			metav1.ConditionTrue,
			"Ready",
			"StorageBasedRemediationConfig is ready and all components are operational",
		)
	} else {
		var reasons []string
		if !daemonSetReady {
			reasons = append(reasons, "DaemonSet not ready")
		}
		if !sbrConfig.IsConditionTrue(medik8sv1alpha1.SBRConfigConditionSharedStorageReady) {
			reasons = append(reasons, "Shared storage not ready")
		}

		sbrConfig.SetCondition(
			medik8sv1alpha1.SBRConfigConditionReady,
			metav1.ConditionFalse,
			"NotReady",
			fmt.Sprintf("StorageBasedRemediationConfig is not ready: %s", strings.Join(reasons, ", ")),
		)
	}

	// Update the status
	return r.Status().Update(ctx, sbrConfig)
}

// mustParseQuantity is a helper function for parsing resource quantities
func mustParseQuantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return q
}

// Example predicate that only triggers on meaningful changes
// nolint:unused // kept for future event debugging
func (r *StorageBasedRemediationConfigReconciler) filterEvents() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			r.FilterLog.Info("CREATE event", "object", e.Object.GetName(),
				"kind", e.Object.GetResourceVersion(),
				"namespace", e.Object.GetNamespace())
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			r.FilterLog.Info("UPDATE event",
				"object", e.ObjectNew.GetName(),
				"oldGeneration", e.ObjectOld.GetGeneration(),
				"newGeneration", e.ObjectNew.GetGeneration(),
				"kind", e.ObjectNew.GetResourceVersion(),
				"namespace", e.ObjectNew.GetNamespace())
			// Only reconcile if spec actually changed
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			r.FilterLog.Info("DELETE event", "object", e.Object.GetName(),
				"kind", e.Object.GetResourceVersion(),
				"namespace", e.Object.GetNamespace())
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			r.FilterLog.Info("GENERIC event", "object", e.Object.GetName(),
				"kind", e.Object.GetResourceVersion(),
				"namespace", e.Object.GetNamespace())
			return true
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageBasedRemediationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("setup").WithValues("controller", "StorageBasedRemediationConfig")

	logger.Info("Setting up StorageBasedRemediationConfig controller")

	r.FilterLog = logger.WithName("filter")

	err := ctrl.NewControllerManagedBy(mgr).
		For(&medik8sv1alpha1.StorageBasedRemediationConfig{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Named("sbrconfig").
		Complete(r)

	//	WithEventFilter(r.filterEvents()).

	if err != nil {
		logger.Error(err, "Failed to setup StorageBasedRemediationConfig controller")
		return err
	}

	logger.Info("StorageBasedRemediationConfig controller setup completed successfully")
	return nil
}
