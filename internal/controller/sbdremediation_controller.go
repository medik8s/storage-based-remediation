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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/blockdevice"
	"github.com/medik8s/sbd-operator/pkg/retry"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	SBDRemediationFinalizer = "medik8s.io/sbd-remediation-finalizer"
	// OperatorNodeID is the node ID used by the operator when writing fence messages
	OperatorNodeID = uint16(255)
	// ReasonCompleted indicates the remediation was completed successfully
	ReasonCompleted = "RemediationCompleted"
	// ReasonInProgress indicates the remediation is in progress
	ReasonInProgress = "RemediationInProgress"
	// ReasonFailed indicates the remediation failed
	ReasonFailed = "RemediationFailed"
	// ReasonWaitingForLeadership indicates waiting for leadership to perform fencing
	ReasonWaitingForLeadership = "WaitingForLeadership"

	// Enhanced retry configuration for SBD Remediation operations
	// MaxFencingRetries is the maximum number of retry attempts for fencing operations
	MaxFencingRetries = 5
	// InitialFencingRetryDelay is the initial delay between fencing operation retries
	InitialFencingRetryDelay = 1 * time.Second
	// MaxFencingRetryDelay is the maximum delay between fencing operation retries
	MaxFencingRetryDelay = 30 * time.Second
	// FencingRetryBackoffFactor is the exponential backoff factor for fencing operation retries
	FencingRetryBackoffFactor = 2.0

	// Status update retry configuration
	MaxStatusUpdateRetries    = 10
	InitialStatusUpdateDelay  = 100 * time.Millisecond
	MaxStatusUpdateDelay      = 5 * time.Second
	StatusUpdateBackoffFactor = 1.5

	// Kubernetes API retry configuration
	MaxKubernetesAPIRetries    = 3
	InitialKubernetesAPIDelay  = 200 * time.Millisecond
	MaxKubernetesAPIDelay      = 10 * time.Second
	KubernetesAPIBackoffFactor = 2.0

	// Event reasons for SBDRemediation operations
	ReasonFencingInitiated     = "FencingInitiated"
	ReasonNodeFenced           = "NodeFenced"
	ReasonFencingFailed        = "FencingFailed"
	ReasonSBDDeviceError       = "SBDDeviceError"
	ReasonNodeIDMappingError   = "NodeIDMappingError"
	ReasonLeadershipWaiting    = "LeadershipWaiting"
	ReasonRemediationCompleted = "RemediationCompleted"
	ReasonRemediationFailed    = "RemediationFailed"
	ReasonRemediationInitiated = "RemediationInitiated"
	ReasonFinalizerProcessed   = "FinalizerProcessed"
)

// DefaultSBDDevicePath is the default path where the SBD device is mounted for shared storage coordination
// This must match the path used by SBD agents: /sbd-shared/sbd-device
const DefaultSBDDevicePath = "/sbd-shared/sbd-device"

// SBDRemediationReconciler reconciles a SBDRemediation object
type SBDRemediationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Leadership tracking - simple approach using environment variable or config
	leaderElectionEnabled bool

	// SBD device configuration
	sbdDevicePath string
	sbdDevice     *blockdevice.Device

	// Node mapping functionality (same as used by SBD agents)
	nodeManager *sbdprotocol.NodeManager
	clusterName string

	// Retry configurations for different operation types
	fencingRetryConfig retry.Config
	statusRetryConfig  retry.Config
	apiRetryConfig     retry.Config
}

// emitEvent is a helper function to emit Kubernetes events for the SBDRemediation controller
func (r *SBDRemediationReconciler) emitEvent(object client.Object, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(object, eventType, reason, message)
	}
}

// emitEventf is a helper function to emit formatted Kubernetes events for the SBDRemediation controller
func (r *SBDRemediationReconciler) emitEventf(object client.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(object, eventType, reason, messageFmt, args...)
	}
}

// FencingError represents an error that occurred during the fencing process
type FencingError struct {
	Operation  string
	Underlying error
	Retryable  bool
	NodeName   string
	NodeID     uint16
}

// conditionUpdate represents an update to a condition
type conditionUpdate struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

func (e *FencingError) Error() string {
	retryableStr := "non-retryable"
	if e.Retryable {
		retryableStr = "retryable"
	}
	return fmt.Sprintf("fencing error during %s for node %s (ID: %d): %v (%s)",
		e.Operation, e.NodeName, e.NodeID, e.Underlying, retryableStr)
}

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/finalizers,verbs=update
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update;patch;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SetLeaderElectionEnabled sets whether leader election is enabled for this controller
func (r *SBDRemediationReconciler) SetLeaderElectionEnabled(enabled bool) {
	r.leaderElectionEnabled = enabled
}

// OnBecomeLeader is called when this operator instance becomes the leader
func (r *SBDRemediationReconciler) OnBecomeLeader() {
	// This method is kept for compatibility and future use
	// Leadership checking is now done through the reconciliation loop
}

// OnLoseLeadership is called when this operator instance loses leadership
func (r *SBDRemediationReconciler) OnLoseLeadership() {
	// This method is kept for compatibility and future use
	// Leadership checking is now done through the reconciliation loop
}

// IsLeader returns whether this operator instance is currently the leader
// For simplicity, we'll use a basic approach - in production, this would
// check the actual leader election lease status
func (r *SBDRemediationReconciler) IsLeader() bool {
	if !r.leaderElectionEnabled {
		// If leader election is disabled, we're always the leader
		return true
	}

	// TODO: In a production implementation, this would check the actual
	// leader election lease status. For now, we'll use a simple placeholder
	// that assumes we're the leader (since we're participating in leader election)
	return true
}

// nodeNameToNodeID converts a Kubernetes node name to a numeric node ID for SBD operations
// This uses the same NodeManager functionality as the SBD agents to ensure consistent slot assignment
func (r *SBDRemediationReconciler) nodeNameToNodeID(ctx context.Context, nodeName string) (uint16, error) {
	if nodeName == "" {
		return 0, fmt.Errorf("node name cannot be empty")
	}

	// Ensure NodeManager is initialized
	if r.nodeManager == nil {
		return 0, fmt.Errorf("node manager not initialized - SBD device may not be available")
	}

	// Use NodeManager to get the slot for this node (same logic as SBD agents)
	slotID, err := r.nodeManager.GetSlotForNode(nodeName)
	if err != nil {
		return 0, fmt.Errorf("failed to get slot for node %s: %w", nodeName, err)
	}

	// Validate slot ID is in the valid range
	if slotID < 1 || slotID > sbdprotocol.SBD_MAX_NODES {
		return 0, fmt.Errorf("invalid slot ID %d for node %s: must be in range [1, %d]", slotID, nodeName, sbdprotocol.SBD_MAX_NODES)
	}

	return slotID, nil
}

// getOperatorInstanceID returns a unique identifier for this operator instance
func (r *SBDRemediationReconciler) getOperatorInstanceID() string {
	// Use pod name if available, otherwise hostname
	if podName := os.Getenv("POD_NAME"); podName != "" {
		return podName
	}
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return "unknown-operator-instance"
}

// discoverSBDConfigForNode discovers which SBDConfig manages the target node
// This ensures the controller uses the correct shared storage and slot assignment for remediation
func (r *SBDRemediationReconciler) discoverSBDConfigForNode(ctx context.Context, nodeName string, logger logr.Logger) (*medik8sv1alpha1.SBDConfig, error) {
	// Get the target node to check its labels
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return nil, fmt.Errorf("failed to get target node %s: %w", nodeName, err)
	}

	// List all SBDConfig resources across all namespaces
	sbdConfigs := &medik8sv1alpha1.SBDConfigList{}
	err := r.List(ctx, sbdConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to list SBDConfig resources: %w", err)
	}

	// Find SBDConfig(s) that would manage this node based on nodeSelector
	var matchingSBDConfigs []*medik8sv1alpha1.SBDConfig
	for i := range sbdConfigs.Items {
		sbdConfig := &sbdConfigs.Items[i]

		// Check if this SBDConfig's nodeSelector matches the target node
		if r.nodeMatchesSelector(node, sbdConfig.Spec.GetNodeSelector()) {
			matchingSBDConfigs = append(matchingSBDConfigs, sbdConfig)
			logger.V(1).Info("Found matching SBDConfig for node",
				"sbdConfig", sbdConfig.Name,
				"namespace", sbdConfig.Namespace,
				"nodeName", nodeName)
		}
	}

	if len(matchingSBDConfigs) == 0 {
		return nil, fmt.Errorf("no SBDConfig found that manages node %s - check nodeSelector configurations", nodeName)
	}

	if len(matchingSBDConfigs) > 1 {
		// Multiple SBDConfigs match - this could be a configuration issue
		logger.Info("Multiple SBDConfigs match the target node - using the first one with shared storage",
			"nodeName", nodeName,
			"matchingConfigs", len(matchingSBDConfigs))

		// Prefer SBDConfigs with shared storage
		for _, sbdConfig := range matchingSBDConfigs {
			if sbdConfig.Spec.HasSharedStorage() {
				logger.Info("Selected SBDConfig with shared storage",
					"sbdConfig", sbdConfig.Name,
					"namespace", sbdConfig.Namespace,
					"nodeName", nodeName)
				return sbdConfig, nil
			}
		}

		// If none have shared storage, use the first one
		logger.Info("No matching SBDConfig has shared storage, using first match",
			"sbdConfig", matchingSBDConfigs[0].Name,
			"namespace", matchingSBDConfigs[0].Namespace,
			"nodeName", nodeName)
		return matchingSBDConfigs[0], nil
	}

	// Exactly one match - perfect
	sbdConfig := matchingSBDConfigs[0]
	logger.Info("Found unique SBDConfig for node",
		"sbdConfig", sbdConfig.Name,
		"namespace", sbdConfig.Namespace,
		"nodeName", nodeName,
		"hasSharedStorage", sbdConfig.Spec.HasSharedStorage())

	return sbdConfig, nil
}

// nodeMatchesSelector checks if a node matches the given nodeSelector
func (r *SBDRemediationReconciler) nodeMatchesSelector(node *corev1.Node, nodeSelector map[string]string) bool {
	if len(nodeSelector) == 0 {
		return true // Empty selector matches all nodes
	}

	nodeLabels := node.Labels
	if nodeLabels == nil {
		nodeLabels = make(map[string]string)
	}

	// All selector labels must match
	for key, value := range nodeSelector {
		nodeValue, exists := nodeLabels[key]
		if !exists {
			return false // Node doesn't have required label
		}
		if value != "" && nodeValue != value {
			return false // Label value doesn't match
		}
		// If selector value is empty (""), any node with that label key matches
	}

	return true
}

// discoverSBDDevicePath discovers the SBD device path from the SBDConfig managing the target node
func (r *SBDRemediationReconciler) discoverSBDDevicePath(ctx context.Context, nodeName string, logger logr.Logger) (string, *medik8sv1alpha1.SBDConfig, error) {
	// Find the SBDConfig that manages this node
	sbdConfig, err := r.discoverSBDConfigForNode(ctx, nodeName, logger)
	if err != nil {
		return "", nil, fmt.Errorf("failed to discover SBDConfig for node %s: %w", nodeName, err)
	}

	// Determine the device path based on the SBDConfig
	var devicePath string
	if sbdConfig.Spec.HasSharedStorage() {
		// Use the same path calculation as the SBD agents for shared storage
		devicePath = fmt.Sprintf("%s/%s", sbdConfig.Spec.GetSharedStorageMountPath(), agent.SharedStorageSBDDeviceFile)
		logger.Info("Using shared storage device path from SBDConfig",
			"sbdConfig", sbdConfig.Name,
			"namespace", sbdConfig.Namespace,
			"devicePath", devicePath)
	} else {
		// No shared storage - use default path
		devicePath = DefaultSBDDevicePath
		logger.Info("SBDConfig has no shared storage, using default device path",
			"sbdConfig", sbdConfig.Name,
			"namespace", sbdConfig.Namespace,
			"devicePath", devicePath)
	}

	return devicePath, sbdConfig, nil
}

// initializeRetryConfigs initializes the retry configurations for different operation types
func (r *SBDRemediationReconciler) initializeRetryConfigs(logger logr.Logger) {
	// Configuration for fencing operations (most critical)
	r.fencingRetryConfig = retry.Config{
		MaxRetries:    MaxFencingRetries,
		InitialDelay:  InitialFencingRetryDelay,
		MaxDelay:      MaxFencingRetryDelay,
		BackoffFactor: FencingRetryBackoffFactor,
		Logger:        logger.WithName("fencing-retry"),
	}

	// Configuration for status updates
	r.statusRetryConfig = retry.Config{
		MaxRetries:    MaxStatusUpdateRetries,
		InitialDelay:  InitialStatusUpdateDelay,
		MaxDelay:      MaxStatusUpdateDelay,
		BackoffFactor: StatusUpdateBackoffFactor,
		Logger:        logger.WithName("status-retry"),
	}

	// Configuration for Kubernetes API operations
	r.apiRetryConfig = retry.Config{
		MaxRetries:    MaxKubernetesAPIRetries,
		InitialDelay:  InitialKubernetesAPIDelay,
		MaxDelay:      MaxKubernetesAPIDelay,
		BackoffFactor: KubernetesAPIBackoffFactor,
		Logger:        logger.WithName("api-retry"),
	}
}

// isTransientKubernetesError determines if a Kubernetes API error is transient and should be retried
func (r *SBDRemediationReconciler) isTransientKubernetesError(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific transient Kubernetes errors
	if apierrors.IsConflict(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsTimeout(err) {
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
func (r *SBDRemediationReconciler) performKubernetesAPIOperationWithRetry(ctx context.Context, operation string, fn func() error, logger logr.Logger) error {
	return retry.Do(ctx, r.apiRetryConfig, operation, func() error {
		err := fn()
		if err != nil {
			// Wrap error with retry information
			return retry.NewRetryableError(err, r.isTransientKubernetesError(err), operation)
		}
		return nil
	})
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The SBDRemediation controller is responsible for:
// 1. Validating that this operator instance is the leader before performing fencing
// 2. Writing fence messages to the shared SBD device for target nodes
// 3. Monitoring the status of fencing operations
// 4. Updating the SBDRemediation status to reflect the current state
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *SBDRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("sbdremediation-controller").WithValues(
		"request", req.NamespacedName,
		"controller", "SBDRemediation",
	)

	// Fetch the SBDRemediation instance
	var sbdRemediation medik8sv1alpha1.SBDRemediation
	if err := r.Get(ctx, req.NamespacedName, &sbdRemediation); err != nil {
		if apierrors.IsNotFound(err) {
			// SBDRemediation resource not found, probably deleted
			logger.Info("SBDRemediation resource not found, probably deleted",
				"name", req.Name,
				"namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get SBDRemediation",
			"name", req.Name,
			"namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	// Add resource-specific context to logger
	logger = logger.WithValues(
		"sbdremediation.name", sbdRemediation.Name,
		"sbdremediation.namespace", sbdRemediation.Namespace,
		"sbdremediation.generation", sbdRemediation.Generation,
		"sbdremediation.resourceVersion", sbdRemediation.ResourceVersion,
		"spec.nodeName", sbdRemediation.Spec.NodeName,
		"status.ready", sbdRemediation.IsReady(),
		"status.fencingSucceeded", sbdRemediation.IsFencingSucceeded(),
	)

	logger.V(1).Info("Starting SBDRemediation reconciliation",
		"spec.nodeName", sbdRemediation.Spec.NodeName,
		"status.nodeID", sbdRemediation.Status.NodeID,
		"status.operatorInstance", sbdRemediation.Status.OperatorInstance)

	// Handle deletion
	if !sbdRemediation.DeletionTimestamp.IsZero() {
		logger.Info("SBDRemediation is being deleted, processing finalizers",
			"deletionTimestamp", sbdRemediation.DeletionTimestamp,
			"finalizers", sbdRemediation.Finalizers)
		r.emitEventf(&sbdRemediation, EventTypeNormal, ReasonFinalizerProcessed,
			"Processing deletion of SBDRemediation for node '%s'", sbdRemediation.Spec.NodeName)
		return r.handleDeletion(ctx, &sbdRemediation, logger)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&sbdRemediation, SBDRemediationFinalizer) {
		controllerutil.AddFinalizer(&sbdRemediation, SBDRemediationFinalizer)
		if err := r.Update(ctx, &sbdRemediation); err != nil {
			logger.Error(err, "Failed to add finalizer",
				"finalizer", SBDRemediationFinalizer,
				"operation", "finalizer-add")
			return ctrl.Result{}, err
		}
		logger.Info("Added finalizer to SBDRemediation",
			"finalizer", SBDRemediationFinalizer)
	}

	// Emit initial event for remediation initiation
	if len(sbdRemediation.Status.Conditions) == 0 {
		r.emitEventf(&sbdRemediation, EventTypeNormal, ReasonRemediationInitiated,
			"SBD remediation initiated for node '%s'", sbdRemediation.Spec.NodeName)
	}

	// Check if we already completed this remediation
	if sbdRemediation.IsFencingSucceeded() {
		logger.Info("SBDRemediation already completed successfully",
			"fencingSucceeded", true,
			"status.nodeID", sbdRemediation.Status.NodeID)
		return ctrl.Result{}, nil
	}

	// Leadership check - only the leader should perform fencing operations
	if r.leaderElectionEnabled && !r.IsLeader() {
		logger.Info("Not the leader - waiting for leadership to perform fencing operations",
			"leaderElectionEnabled", r.leaderElectionEnabled,
			"isLeader", r.IsLeader())
		r.emitEventf(&sbdRemediation, EventTypeNormal, ReasonLeadershipWaiting,
			"Waiting for leadership to perform fencing for node '%s'", sbdRemediation.Spec.NodeName)
		return r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
			medik8sv1alpha1.SBDRemediationConditionLeadershipAcquired: {
				status:  metav1.ConditionFalse,
				reason:  "WaitingForLeadership",
				message: "Waiting for operator leadership to perform fencing operations",
			},
			medik8sv1alpha1.SBDRemediationConditionReady: {
				status:  metav1.ConditionFalse,
				reason:  "WaitingForLeadership",
				message: "Waiting for operator leadership to perform fencing operations",
			},
		}, logger)
	}

	// Mark leadership acquired
	if !sbdRemediation.HasLeadership() {
		sbdRemediation.SetCondition(medik8sv1alpha1.SBDRemediationConditionLeadershipAcquired,
			metav1.ConditionTrue, "LeadershipAcquired", "Operator leadership acquired for fencing operations")
	}

	logger.Info("Leader confirmed - proceeding with fencing operations",
		"nodeName", sbdRemediation.Spec.NodeName,
		"isLeader", r.IsLeader(),
		"operation", "fencing-initiation")

	// Convert node name to node ID
	targetNodeID, err := r.nodeNameToNodeID(ctx, sbdRemediation.Spec.NodeName)
	if err != nil {
		logger.Error(err, "Failed to map node name to node ID",
			"nodeName", sbdRemediation.Spec.NodeName,
			"operation", "node-name-to-id-mapping")
		r.emitEventf(&sbdRemediation, EventTypeWarning, ReasonNodeIDMappingError,
			"Failed to map node name '%s' to node ID: %v", sbdRemediation.Spec.NodeName, err)
		return r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
			medik8sv1alpha1.SBDRemediationConditionFencingSucceeded: {
				status:  metav1.ConditionFalse,
				reason:  "NodeIDMappingFailed",
				message: fmt.Sprintf("Failed to map node name to node ID: %v", err),
			},
			medik8sv1alpha1.SBDRemediationConditionReady: {
				status:  metav1.ConditionTrue,
				reason:  "Failed",
				message: fmt.Sprintf("Failed to map node name to node ID: %v", err),
			},
		}, logger)
	}

	logger = logger.WithValues("target.nodeID", targetNodeID)

	// Initialize SBD device if needed
	if r.sbdDevice == nil {
		if err := r.initializeSBDDevice(ctx, sbdRemediation.Spec.NodeName, logger); err != nil {
			logger.Error(err, "Failed to initialize SBD device",
				"sbdDevicePath", r.sbdDevicePath,
				"operation", "sbd-device-initialization")
			r.emitEventf(&sbdRemediation, EventTypeWarning, ReasonSBDDeviceError,
				"Failed to initialize SBD device for fencing node '%s': %v", sbdRemediation.Spec.NodeName, err)
			return r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
				medik8sv1alpha1.SBDRemediationConditionFencingSucceeded: {
					status:  metav1.ConditionFalse,
					reason:  "SBDDeviceInitializationFailed",
					message: fmt.Sprintf("Failed to initialize SBD device: %v", err),
				},
				medik8sv1alpha1.SBDRemediationConditionReady: {
					status:  metav1.ConditionTrue,
					reason:  "Failed",
					message: fmt.Sprintf("Failed to initialize SBD device: %v", err),
				},
			}, logger)
		}
	}

	// Update status to indicate fencing is in progress
	if !sbdRemediation.IsFencingInProgress() {
		// Set NodeID in status before updating
		sbdRemediation.Status.NodeID = &targetNodeID
		sbdRemediation.Status.OperatorInstance = r.getOperatorInstanceID()

		logger.Info("Updating status to fencing in progress",
			"previousFencingInProgress", sbdRemediation.IsFencingInProgress(),
			"newFencingInProgress", true,
			"operatorInstance", sbdRemediation.Status.OperatorInstance)

		if result, err := r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
			medik8sv1alpha1.SBDRemediationConditionFencingInProgress: {
				status:  metav1.ConditionTrue,
				reason:  "FencingInitiated",
				message: fmt.Sprintf("Initiating fencing for node %s (ID: %d)", sbdRemediation.Spec.NodeName, targetNodeID),
			},
			medik8sv1alpha1.SBDRemediationConditionReady: {
				status:  metav1.ConditionFalse,
				reason:  "FencingInProgress",
				message: fmt.Sprintf("Fencing in progress for node %s (ID: %d)", sbdRemediation.Spec.NodeName, targetNodeID),
			},
		}, logger); err != nil {
			return result, err
		}

		// Emit event for fencing initiation
		r.emitEventf(&sbdRemediation, EventTypeNormal, ReasonFencingInitiated,
			"Fencing initiated for node '%s' via SBD", sbdRemediation.Spec.NodeName)
	}

	// Perform the fencing operation with retry logic
	if err := r.performFencingWithRetry(ctx, &sbdRemediation, targetNodeID, logger); err != nil {
		logger.Error(err, "Failed to fence node",
			"nodeName", sbdRemediation.Spec.NodeName,
			"nodeID", targetNodeID,
			"operation", "fencing-execution")

		// Emit failure event
		r.emitEventf(&sbdRemediation, EventTypeWarning, ReasonFencingFailed,
			"Failed to fence node '%s' via SBD: %s", sbdRemediation.Spec.NodeName, err.Error())

		return r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
			medik8sv1alpha1.SBDRemediationConditionFencingInProgress: {
				status:  metav1.ConditionFalse,
				reason:  "FencingFailed",
				message: fmt.Sprintf("Fencing failed: %v", err),
			},
			medik8sv1alpha1.SBDRemediationConditionFencingSucceeded: {
				status:  metav1.ConditionFalse,
				reason:  "FencingFailed",
				message: fmt.Sprintf("Fencing failed: %v", err),
			},
			medik8sv1alpha1.SBDRemediationConditionReady: {
				status:  metav1.ConditionTrue,
				reason:  "Failed",
				message: fmt.Sprintf("Fencing failed: %v", err),
			},
		}, logger)
	}

	logger.Info("Successfully fenced node",
		"nodeName", sbdRemediation.Spec.NodeName,
		"nodeID", targetNodeID,
		"operation", "fencing-completed")

	// Emit success event
	r.emitEventf(&sbdRemediation, EventTypeNormal, ReasonNodeFenced,
		"Node '%s' successfully fenced via SBD", sbdRemediation.Spec.NodeName)

	// Update status to indicate successful fencing
	return r.updateStatusWithConditions(ctx, &sbdRemediation, map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
		medik8sv1alpha1.SBDRemediationConditionFencingInProgress: {
			status:  metav1.ConditionFalse,
			reason:  "FencingCompleted",
			message: fmt.Sprintf("Node %s (ID: %d) successfully fenced via SBD device", sbdRemediation.Spec.NodeName, targetNodeID),
		},
		medik8sv1alpha1.SBDRemediationConditionFencingSucceeded: {
			status:  metav1.ConditionTrue,
			reason:  "FencingCompleted",
			message: fmt.Sprintf("Node %s (ID: %d) successfully fenced via SBD device", sbdRemediation.Spec.NodeName, targetNodeID),
		},
		medik8sv1alpha1.SBDRemediationConditionReady: {
			status:  metav1.ConditionTrue,
			reason:  "Succeeded",
			message: fmt.Sprintf("Node %s (ID: %d) successfully fenced via SBD device", sbdRemediation.Spec.NodeName, targetNodeID),
		},
	}, logger)
}

// initializeSBDDevice initializes the SBD device for the controller to perform fencing operations
func (r *SBDRemediationReconciler) initializeSBDDevice(ctx context.Context, nodeName string, logger logr.Logger) error {
	// Skip initialization if already done
	if r.sbdDevice != nil && !r.sbdDevice.IsClosed() && r.nodeManager != nil {
		return nil
	}

	// Discover the SBD device path from the SBDConfig managing the target node
	devicePath, sbdConfig, err := r.discoverSBDDevicePath(ctx, nodeName, logger)
	if err != nil {
		return fmt.Errorf("failed to discover SBD device path: %w", err)
	}
	r.sbdDevicePath = devicePath

	// Store the cluster name from the SBDConfig for NodeManager initialization
	if sbdConfig.Spec.HasSharedStorage() {
		// Use the SBDConfig namespace as cluster name for better isolation
		r.clusterName = fmt.Sprintf("%s-%s", sbdConfig.Namespace, sbdConfig.Name)
	} else {
		r.clusterName = "default-cluster"
	}

	// Open the SBD device
	device, err := blockdevice.Open(r.sbdDevicePath)
	if err != nil {
		return fmt.Errorf("failed to open SBD device %s: %w", r.sbdDevicePath, err)
	}

	r.sbdDevice = device
	logger.Info("SBD device initialized successfully",
		"sbdDevicePath", r.sbdDevicePath,
		"sbdConfig", sbdConfig.Name,
		"namespace", sbdConfig.Namespace,
		"clusterName", r.clusterName)

	// Initialize NodeManager for consistent node-to-slot mapping
	if err := r.initializeNodeManager(ctx, logger); err != nil {
		// Close the device if NodeManager initialization fails
		device.Close()
		r.sbdDevice = nil
		return fmt.Errorf("failed to initialize node manager: %w", err)
	}

	return nil
}

// initializeNodeManager initializes the NodeManager for consistent node-to-slot mapping
// This ensures the remediation controller uses the same mapping strategy as SBD agents
func (r *SBDRemediationReconciler) initializeNodeManager(ctx context.Context, logger logr.Logger) error {
	if r.sbdDevice == nil {
		return fmt.Errorf("SBD device must be initialized before NodeManager")
	}

	// Determine cluster name from environment or use default
	clusterName := r.clusterName
	if clusterName == "" {
		clusterName = os.Getenv("CLUSTER_NAME")
		if clusterName == "" {
			clusterName = "default-cluster"
			logger.Info("Using default cluster name for node mapping", "clusterName", clusterName)
		} else {
			logger.Info("Using cluster name from environment", "clusterName", clusterName)
		}
		r.clusterName = clusterName
	}

	// Create NodeManager configuration (same as SBD agents)
	config := sbdprotocol.NodeManagerConfig{
		ClusterName:        clusterName,
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   1 * time.Hour,
		Logger:             logger.WithName("node-manager"),
		FileLockingEnabled: true, // Enable file locking for coordination between controller and agents
	}

	nodeManager, err := sbdprotocol.NewNodeManager(r.sbdDevice, config)
	if err != nil {
		return fmt.Errorf("failed to create node manager: %w", err)
	}

	r.nodeManager = nodeManager

	logger.Info("NodeManager initialized successfully",
		"clusterName", clusterName,
		"coordinationStrategy", nodeManager.GetCoordinationStrategy())

	return nil
}

// performFencingWithRetry performs the fencing operation with retry logic for transient errors
func (r *SBDRemediationReconciler) performFencingWithRetry(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, targetNodeID uint16, logger logr.Logger) error {
	var lastErr error
	for attempt := 1; attempt <= MaxFencingRetries; attempt++ {
		logger.Info("Attempting fencing operation",
			"attempt", attempt,
			"max-attempts", MaxFencingRetries,
			"target-node", sbdRemediation.Spec.NodeName)

		err := r.performFencing(ctx, sbdRemediation, targetNodeID)
		if err == nil {
			// Success!
			return nil
		}

		lastErr = err

		// Check if this is a retryable error
		var fencingErr *FencingError
		if errors.As(err, &fencingErr) && !fencingErr.Retryable {
			logger.Error(err, "Non-retryable fencing error encountered")
			return err // Don't retry non-retryable errors
		}

		// If this is the last attempt, don't wait
		if attempt == MaxFencingRetries {
			break
		}

		// Calculate exponential backoff delay
		delay := time.Duration(attempt) * InitialFencingRetryDelay
		if delay > MaxFencingRetryDelay {
			delay = MaxFencingRetryDelay
		}

		logger.Info("Fencing attempt failed, retrying",
			"attempt", attempt,
			"error", err,
			"retry-delay", delay)

		// Wait before retrying
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// All attempts failed
	return fmt.Errorf("fencing failed after %d attempts, last error: %w", MaxFencingRetries, lastErr)
}

// performFencing performs the actual fencing operation by writing a fence message to the SBD device
func (r *SBDRemediationReconciler) performFencing(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, targetNodeID uint16) error {
	logger := logf.FromContext(ctx)

	// Initialize SBD device if needed
	if err := r.initializeSBDDevice(ctx, sbdRemediation.Spec.NodeName, logger); err != nil {
		return err
	}

	// Convert reason to numeric value
	var reasonCode uint8 = 1 // Default to generic fencing reason
	switch sbdRemediation.Spec.Reason {
	case medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout:
		reasonCode = 2
	case medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive:
		reasonCode = 3
	case medik8sv1alpha1.SBDRemediationReasonManualFencing:
		reasonCode = 4
	}

	senderNodeID := OperatorNodeID
	sequence := uint64(time.Now().Unix())

	logger.Info("🔥 Writing fence message to SBD device",
		"target-node-name", sbdRemediation.Spec.NodeName,
		"target-node-id", targetNodeID,
		"sender-node-id", senderNodeID,
		"sequence", sequence,
		"reason", sbdRemediation.Spec.Reason,
		"reason-code", reasonCode)

	// Create fence message
	header := sbdprotocol.NewFence(senderNodeID, targetNodeID, sequence, reasonCode)
	fenceMessage := sbdprotocol.SBDFenceMessage{
		Header:       header,
		TargetNodeID: targetNodeID,
		Reason:       reasonCode,
	}

	// Marshal the fence message
	messageBytes, err := sbdprotocol.MarshalFence(fenceMessage)
	if err != nil {
		return &FencingError{
			Operation:  "fence message marshaling",
			Underlying: fmt.Errorf("failed to marshal fence message: %w", err),
			Retryable:  false, // Marshaling errors are usually permanent
			NodeName:   sbdRemediation.Spec.NodeName,
			NodeID:     targetNodeID,
		}
	}

	// Calculate target slot offset
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to target node's slot
	if _, err := r.sbdDevice.WriteAt(messageBytes, slotOffset); err != nil {
		return &FencingError{
			Operation:  "SBD device write",
			Underlying: fmt.Errorf("failed to write fence message to SBD device at offset %d: %w", slotOffset, err),
			Retryable:  true, // Write errors might be temporary (device busy, I/O errors)
			NodeName:   sbdRemediation.Spec.NodeName,
			NodeID:     targetNodeID,
		}
	}

	// Ensure data is synced to disk
	if err := r.sbdDevice.Sync(); err != nil {
		return &FencingError{
			Operation:  "SBD device sync",
			Underlying: fmt.Errorf("failed to sync fence message to SBD device: %w", err),
			Retryable:  true, // Sync errors might be temporary
			NodeName:   sbdRemediation.Spec.NodeName,
			NodeID:     targetNodeID,
		}
	}

	// Verify the write by reading back the message (optional verification)
	if err := r.verifyFenceMessage(messageBytes, slotOffset); err != nil {
		logger.Error(err, "Fence message verification failed, but write was successful")
		// Don't fail the operation for verification errors, just log them
	}

	logger.Info("✅ Fence message successfully written to SBD device",
		"target-node-name", sbdRemediation.Spec.NodeName,
		"target-node-id", targetNodeID,
		"slot-offset", slotOffset,
		"message-size", len(messageBytes))

	return nil
}

// verifyFenceMessage verifies that the fence message was written correctly by reading it back
func (r *SBDRemediationReconciler) verifyFenceMessage(expectedBytes []byte, slotOffset int64) error {
	readBuffer := make([]byte, len(expectedBytes))
	if _, err := r.sbdDevice.ReadAt(readBuffer, slotOffset); err != nil {
		return fmt.Errorf("failed to read back fence message for verification: %w", err)
	}

	// Compare the written and read data
	for i, b := range expectedBytes {
		if i < len(readBuffer) && readBuffer[i] != b {
			return fmt.Errorf("fence message verification failed: byte mismatch at position %d (expected %d, got %d)", i, b, readBuffer[i])
		}
	}

	return nil
}

// handleDeletion handles the deletion of SBDRemediation resources
func (r *SBDRemediationReconciler) handleDeletion(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("🗑️  Cleaning up SBDRemediation resource")

	// Remove finalizer
	controllerutil.RemoveFinalizer(sbdRemediation, SBDRemediationFinalizer)
	if err := r.Update(ctx, sbdRemediation); err != nil {
		logger.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// updateStatusWithConditions updates the status of the SBDRemediation resource with conditions
func (r *SBDRemediationReconciler) updateStatusWithConditions(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, conditions map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate, logger logr.Logger) (ctrl.Result, error) {
	// Update conditions
	for conditionType, update := range conditions {
		sbdRemediation.SetCondition(conditionType, update.status, update.reason, update.message)
	}

	// Update LastUpdateTime
	now := metav1.Now()
	sbdRemediation.Status.LastUpdateTime = &now

	// Set operator instance if not already set
	if sbdRemediation.Status.OperatorInstance == "" {
		sbdRemediation.Status.OperatorInstance = r.getOperatorInstanceID()
	}

	// Update fence message written flag if we're marking as successfully fenced
	if sbdRemediation.IsFencingSucceeded() {
		sbdRemediation.Status.FenceMessageWritten = true
	}

	// Update the status with retry logic
	if err := r.updateStatusWithRetry(ctx, sbdRemediation); err != nil {
		logger.Error(err, "Failed to update SBDRemediation status")
		return ctrl.Result{RequeueAfter: InitialFencingRetryDelay}, err
	}

	logger.Info("Status updated successfully")

	// Determine requeue behavior based on conditions
	switch {
	case sbdRemediation.IsConditionUnknown(medik8sv1alpha1.SBDRemediationConditionLeadershipAcquired) ||
		sbdRemediation.IsConditionFalse(medik8sv1alpha1.SBDRemediationConditionLeadershipAcquired):
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case sbdRemediation.IsReady():
		// Ready means either succeeded or failed - no requeue needed
		return ctrl.Result{}, nil
	default:
		// For other states, requeue for continued processing
		return ctrl.Result{Requeue: true}, nil
	}
}

// updateStatusWithRetry updates the status with retry logic to handle conflicts
func (r *SBDRemediationReconciler) updateStatusWithRetry(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation) error {
	logger := logf.FromContext(ctx)

	return wait.ExponentialBackoff(wait.Backoff{
		Duration: InitialStatusUpdateDelay,
		Factor:   StatusUpdateBackoffFactor,
		Jitter:   0.1,
		Steps:    MaxStatusUpdateRetries,
		Cap:      MaxStatusUpdateDelay,
	}, func() (bool, error) {
		// Get the latest version to avoid conflicts
		latest := &medik8sv1alpha1.SBDRemediation{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sbdRemediation), latest); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("SBDRemediation was deleted during status update")
				return true, nil // Stop retrying
			}
			logger.Error(err, "Failed to get latest SBDRemediation for status update")
			return false, err // Retry
		}

		// Copy our status changes to the latest version
		latest.Status = sbdRemediation.Status

		// Attempt the status update
		if err := r.Status().Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				logger.V(1).Info("Conflict during status update, retrying")
				// Update our in-memory copy for the next retry
				*sbdRemediation = *latest
				return false, nil // Retry
			}

			// For other errors, decide whether to retry
			if apierrors.IsServerTimeout(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTooManyRequests(err) {
				logger.V(1).Info("Temporary error during status update, retrying", "error", err)
				return false, nil // Retry
			}

			// Permanent error
			logger.Error(err, "Permanent error during status update")
			return false, err
		}

		// Success!
		logger.V(1).Info("Status update successful")
		return true, nil
	})
}

// Cleanup properly closes the NodeManager and SBD device resources
func (r *SBDRemediationReconciler) Cleanup() error {
	var errs []error

	// Close NodeManager if initialized
	if r.nodeManager != nil {
		if err := r.nodeManager.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close node manager: %w", err))
		}
		r.nodeManager = nil
	}

	// Close SBD device if initialized
	if r.sbdDevice != nil && !r.sbdDevice.IsClosed() {
		if err := r.sbdDevice.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close SBD device: %w", err))
		}
		r.sbdDevice = nil
	}

	// Return combined errors if any occurred
	if len(errs) > 0 {
		var errMsg strings.Builder
		errMsg.WriteString("cleanup errors: ")
		for i, err := range errs {
			if i > 0 {
				errMsg.WriteString("; ")
			}
			errMsg.WriteString(err.Error())
		}
		return fmt.Errorf(errMsg.String())
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SBDRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("setup").WithValues("controller", "SBDRemediation")

	logger.Info("Setting up SBDRemediation controller")

	err := ctrl.NewControllerManagedBy(mgr).
		For(&medik8sv1alpha1.SBDRemediation{}).
		Named("sbdremediation").
		Complete(r)

	if err != nil {
		logger.Error(err, "Failed to setup SBDRemediation controller")
		return err
	}

	logger.Info("SBDRemediation controller setup completed successfully")
	return nil
}
