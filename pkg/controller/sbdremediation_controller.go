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
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/mocks"
	"github.com/medik8s/sbd-operator/pkg/retry"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

const (
	SBDRemediationFinalizer = "medik8s.io/sbd-remediation-finalizer"
	// ReasonCompleted indicates the remediation was completed successfully
	ReasonCompleted = "RemediationCompleted"
	// ReasonInProgress indicates the remediation is in progress
	ReasonInProgress = "RemediationInProgress"
	// ReasonFailed indicates the remediation failed
	ReasonFailed = "RemediationFailed"
	// ReasonAgentDelegated indicates the remediation was delegated to agents
	ReasonAgentDelegated = "RemediationAgentDelegated"
	// SBDAgentAnnotationKey marks a remediation created by sbd
	SBDAgentAnnotationKey = "medik8s.io/sbd-agent"
	// SBDAgentOOSTaintTimestampAnnotation records when OOS taint was placed on the node for this remediation
	SBDAgentOOSTaintTimestampAnnotation = "medik8s.io/sbd-oos-placed-at"

	// Fresh window and requeue delay for SBD agent remediations before placing OOS taint
	SBDAgentRemediationFreshAge     = 15 * time.Second * (5 + 2) //TODO mshitrit should work around dependecies to update the calculation to be: main.SBDDefaultTimeoutSec/2 * (main.MaxConsecutiveFailures+2)  if SBD_TIMEOUT_SECONDS defined, use instead of main.SBDDefaultTimeoutSec
	SBDAgentRemediationRequeueDelay = 10 * time.Second
	// SBDAgentOOSTaintStaleAge is the benchmark duration after which a remediation
	// is considered stale since OOS taint placement (annotation-based)
	SBDAgentOOSTaintStaleAge = 120 * time.Second

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

	// Event reasons for StorageBasedRemediation operations
	ReasonFencingInitiated     = "FencingInitiated"
	ReasonNodeFenced           = "NodeFenced"
	ReasonFencingFailed        = "FencingFailed"
	ReasonRemediationCompleted = "RemediationCompleted"
	ReasonRemediationFailed    = "RemediationFailed"
	ReasonRemediationInitiated = "RemediationInitiated"
	ReasonFinalizerProcessed   = "FinalizerProcessed"
	ReasonAgentCoordination    = "AgentCoordination"
)

// outOfServiceTaint is used to evict workloads from the remediated node after successful fencing
var outOfServiceTaint = corev1.Taint{
	Key:    corev1.TaintNodeOutOfService,
	Value:  "nodeshutdown",
	Effect: corev1.TaintEffectNoExecute,
}

// nodeUnschedulableTaint represents the standard unschedulable taint applied by the NodeController
var nodeUnschedulableTaint = corev1.Taint{
	Key:    corev1.TaintNodeUnschedulable,
	Effect: corev1.TaintEffectNoSchedule,
}

// SBDRemediationReconciler reconciles a StorageBasedRemediation object
// This controller performs actual SBD fencing operations by writing fence messages to the SBD device.
type SBDRemediationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Retry configurations for API operations
	statusRetryConfig retry.Config
	apiRetryConfig    retry.Config

	// SBD device for fencing operations
	sbdDevice   mocks.BlockDeviceInterface
	fenceDevice mocks.BlockDeviceInterface
	nodeManager *sbdprotocol.NodeManager
	ownNodeID   uint16
	ownNodeName string // Changed from uint32 to uint64
	sequence    uint64 // Changed from uint32 to uint64
}

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=storagebasedremediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=storagebasedremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=storagebasedremediations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// setSBDDevices allows setting custom SBD devices (useful for testing)
func (s *SBDRemediationReconciler) SetSBDDevices(heartbeatDevice, fenceDevice mocks.BlockDeviceInterface) {
	s.sbdDevice = heartbeatDevice
	s.fenceDevice = fenceDevice
}

// SetNodeManager sets the node manager for node ID resolution
func (r *SBDRemediationReconciler) SetNodeManager(nodeManager *sbdprotocol.NodeManager) {
	r.nodeManager = nodeManager
}

// SetOwnNodeInfo sets the own node information
func (r *SBDRemediationReconciler) SetOwnNodeInfo(nodeID uint16, nodeName string) {
	r.ownNodeID = nodeID
	r.ownNodeName = nodeName
}

// getNextSequence returns the next sequence number for messages
func (r *SBDRemediationReconciler) getNextSequence() uint64 {
	r.sequence++
	return r.sequence
}

// initializeRetryConfigs initializes retry configurations for API operations
func (r *SBDRemediationReconciler) initializeRetryConfigs(logger logr.Logger) {
	// Status update retry configuration
	r.statusRetryConfig = retry.Config{
		MaxRetries:    MaxStatusUpdateRetries,
		InitialDelay:  InitialStatusUpdateDelay,
		MaxDelay:      MaxStatusUpdateDelay,
		BackoffFactor: StatusUpdateBackoffFactor,
	}

	// Kubernetes API retry configuration
	r.apiRetryConfig = retry.Config{
		MaxRetries:    MaxKubernetesAPIRetries,
		InitialDelay:  InitialKubernetesAPIDelay,
		MaxDelay:      MaxKubernetesAPIDelay,
		BackoffFactor: KubernetesAPIBackoffFactor,
	}

	logger.V(1).Info("Retry configurations initialized",
		"statusRetryConfig", r.statusRetryConfig,
		"apiRetryConfig", r.apiRetryConfig)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The StorageBasedRemediation controller performs actual fencing operations:
// 1. Validates the remediation request
// 2. Resolves target node name to node ID
// 3. Writes fence message to SBD device
// 4. Updates status based on fencing results
func (r *SBDRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("sbdremediation-controller").WithValues(
		"request", req.NamespacedName,
		"controller", "StorageBasedRemediation",
	)

	// Initialize retry configurations if not already done
	if r.statusRetryConfig.MaxRetries == 0 {
		r.initializeRetryConfigs(logger)
	}

	// Fetch the StorageBasedRemediation instance
	var sbdRemediation medik8sv1alpha1.StorageBasedRemediation
	if err := r.Get(ctx, req.NamespacedName, &sbdRemediation); err != nil {
		if apierrors.IsNotFound(err) {
			// StorageBasedRemediation resource not found, probably deleted
			logger.Info("StorageBasedRemediation resource not found, probably deleted",
				"name", req.Name,
				"namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get StorageBasedRemediation",
			"name", req.Name,
			"namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	// Get node name from remediation name (the name is the node name)
	nodeName := sbdRemediation.Name

	// Don't fence ourselves
	if nodeName == r.ownNodeName {
		logger.Info("Found own node in remediation request, skipping")
		r.emitEventOnly(&sbdRemediation, "Normal", ReasonCompleted,
			"Skipping remediation for own node")
		return ctrl.Result{}, nil
	}

	// Add resource-specific context to logger
	logger = logger.WithValues(
		"sbdremediation.name", sbdRemediation.Name,
		"sbdremediation.namespace", sbdRemediation.Namespace,
		"sbdremediation.generation", sbdRemediation.Generation,
		"sbdremediation.resourceVersion", sbdRemediation.ResourceVersion,
		"nodeName", nodeName,
		"spec.timeoutSeconds", sbdRemediation.Spec.TimeoutSeconds,
		"status.ready", sbdRemediation.IsReady(),
		"status.fencingSucceeded", sbdRemediation.IsFencingSucceeded(),
	)

	logger.V(1).Info("Starting StorageBasedRemediation reconciliation",
		"nodeName", nodeName)

	// Handle deletion
	if !sbdRemediation.DeletionTimestamp.IsZero() {
		logger.Info("StorageBasedRemediation is being deleted, processing finalizers",
			"deletionTimestamp", sbdRemediation.DeletionTimestamp,
			"finalizers", sbdRemediation.Finalizers)
		r.emitEventf(&sbdRemediation, "Normal", ReasonFinalizerProcessed,
			"Processing deletion of StorageBasedRemediation for node '%s'", nodeName)
		return r.handleDeletion(ctx, &sbdRemediation, logger)
	}

	logger.V(1).Info("Checking finalizers for StorageBasedRemediation",
		"nodeName", nodeName)

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&sbdRemediation, SBDRemediationFinalizer) {
		controllerutil.AddFinalizer(&sbdRemediation, SBDRemediationFinalizer)
		if err := r.Update(ctx, &sbdRemediation); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Added finalizer to StorageBasedRemediation",
			"finalizer", SBDRemediationFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	logger.V(1).Info("Checking conditions for StorageBasedRemediation",
		"conditions", sbdRemediation.Status.Conditions)

	// Emit initial event for remediation initiation
	if len(sbdRemediation.Status.Conditions) == 0 {
		r.emitEventf(&sbdRemediation, "Normal", ReasonRemediationInitiated,
			"SBD remediation initiated for node '%s'", nodeName)
	}

	logger.V(1).Info("Checking if remediation is ready",
		"ready", sbdRemediation.IsReady(),
		"fencingSucceeded", sbdRemediation.IsFencingSucceeded(),
		"fencingInProgress", sbdRemediation.IsFencingInProgress(),
		"condition", sbdRemediation.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded),
	)

	// Check if we already completed this remediation
	if sbdRemediation.IsFencingSucceeded() {
		logger.Info("StorageBasedRemediation already completed successfully",
			"fencingSucceeded", true)
		return ctrl.Result{}, nil
	}

	// Check if fencing is already in progress
	if sbdRemediation.IsFencingInProgress() {
		logger.Info("StorageBasedRemediation fencing already in progress")
		// Check if target node has been fenced (stopped heartbeating and/or became NotReady)
		fenced := r.checkFencingCompletion(ctx, &sbdRemediation, logger)

		if !fenced {
			// Still waiting for fencing to complete, requeue to check again
			logger.V(1).Info("Fencing not yet complete, requeueing for monitoring",
				"targetNode", nodeName)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		// For fresh SBD-agent remediations, delay placing the OOS taint
		if isSBDAgentRemediation(&sbdRemediation) && isRemediationFresh(&sbdRemediation, time.Now()) {
			logger.V(1).Info("Fresh SBD-agent remediation detected; delaying OutOfService taint",
				"age", time.Since(sbdRemediation.CreationTimestamp.Time),
				"requeueAfter", SBDAgentRemediationRequeueDelay)
			return ctrl.Result{RequeueAfter: SBDAgentRemediationRequeueDelay}, nil
		}

		// Fencing completed successfully - apply OutOfService taint prior to success handling
		if err := r.ensureOutOfServiceTaint(ctx, nodeName, logger); err != nil {
			logger.Error(err, "Failed to ensure OutOfService taint on remediated node",
				"node", nodeName)
			return ctrl.Result{}, err
		}

		// If this is an SBD-agent remediation, stamp OOS placement time only once and requeue to avoid update conflicts
		if isSBDAgentRemediation(&sbdRemediation) {
			if sbdRemediation.Annotations == nil {
				sbdRemediation.Annotations = map[string]string{}
			}
			if _, exists := sbdRemediation.Annotations[SBDAgentOOSTaintTimestampAnnotation]; !exists {
				sbdRemediation.Annotations[SBDAgentOOSTaintTimestampAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
				if err := r.Update(ctx, &sbdRemediation); err != nil {
					logger.Error(err, "Failed to annotate remediation with OOS placement timestamp")
					return ctrl.Result{}, err
				}
				// Requeue to proceed with success handling on a fresh ResourceVersion
				return ctrl.Result{Requeue: true}, nil
			}
		}

		// Proceed with successful fencing handling
		r.handleFencingSuccess(ctx, &sbdRemediation, logger)
		return ctrl.Result{}, nil
	}

	if r.nodeManager == nil {
		err := fmt.Errorf("node manager is not available for node ID resolution")
		logger.Error(err, "Cannot perform fencing")
		r.emitEventf(&sbdRemediation, "Warning", ReasonFailed,
			"Node manager is not available for node ID resolution: %v", err)
		return ctrl.Result{}, err
	}
	// Perform the actual fencing operation
	logger.Info("Starting fencing operation",
		"targetNode", nodeName,
		"reason", sbdRemediation.Spec.Reason)

	// Ensure the node is cordoned BEFORE setting FencingInProgress
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		logger.Error(err, "Failed to get node before cordon", "node", nodeName)
		return ctrl.Result{}, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	// Only cordon if not already unschedulable (avoid unnecessary updates)
	if !node.Spec.Unschedulable {
		if err := r.markNodeAsUnschedulable(ctx, node, logger); err != nil {
			logger.Error(err, "Failed to mark node unschedulable prior to fencing",
				"node", nodeName)
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Update status to indicate fencing is in progress
	if err := r.updateRemediationCondition(ctx, &sbdRemediation, medik8sv1alpha1.SBDRemediationConditionFencingInProgress, metav1.ConditionTrue, ReasonInProgress, fmt.Sprintf("Fencing node %s", nodeName), logger); err != nil {
		logger.Error(err, "Failed to update remediation condition to in progress")
	}

	// Execute fencing
	if err := r.executeFencing(&sbdRemediation, logger); err != nil {
		r.handleFencingFailure(ctx, &sbdRemediation, err, logger)
		return ctrl.Result{}, err
	}

	// Fence message written successfully, now monitor for actual fencing completion
	logger.Info("Fence message written, monitoring for target node fencing completion",
		"targetNode", nodeName,
		"timeoutSeconds", sbdRemediation.Spec.TimeoutSeconds)

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// executeFencing performs the actual fencing operation via SBD device
func (r *SBDRemediationReconciler) executeFencing(
	remediation *medik8sv1alpha1.StorageBasedRemediation, logger logr.Logger) error {
	targetNodeName := remediation.Name

	// Get target node ID using node manager
	targetNodeID, err := r.nodeManager.LookupNodeIDForNode(targetNodeName)
	if err != nil {
		logger.Info("Target node not found in node manager", "targetNode", targetNodeName)
		return err
	}

	logger.Info("Writing fence message to SBD device",
		"targetNode", targetNodeName,
		"targetNodeID", targetNodeID,
		"reason", remediation.Spec.Reason)

	// Write fence message to target node's slot
	if err := r.writeFenceMessage(targetNodeID, remediation.Spec.Reason, logger); err != nil {
		return fmt.Errorf("failed to write fence message to target node %d: %w", targetNodeID, err)
	}

	logger.Info("Fencing operation completed successfully",
		"targetNode", targetNodeName,
		"targetNodeID", targetNodeID)

	return nil
}

// markNodeAsUnschedulable marks the target node as unschedulable (cordon) so it will not accept new workloads
func (r *SBDRemediationReconciler) markNodeAsUnschedulable(ctx context.Context, node *corev1.Node, logger logr.Logger) error {
	node.Spec.Unschedulable = true
	if err := r.Update(ctx, node); err != nil {
		return fmt.Errorf("failed to set node %s unschedulable: %w", node.Name, err)
	}
	logger.Info("Node marked unschedulable prior to fencing", "node", node.Name)
	return nil
}

// markNodeAsSchedulable marks the node as schedulable again by clearing spec.unschedulable
func (r *SBDRemediationReconciler) markNodeAsSchedulable(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	if !node.Spec.Unschedulable {
		return nil
	}
	node.Spec.Unschedulable = false
	if err := r.Update(ctx, node); err != nil {
		return fmt.Errorf("failed to uncordon node %s: %w", nodeName, err)
	}
	return nil
}

// writeFenceMessage writes a fence message to the target node's slot in the SBD device
func (r *SBDRemediationReconciler) writeFenceMessage(targetNodeID uint16,
	reason medik8sv1alpha1.SBDRemediationReason, logger logr.Logger) error {
	if r.fenceDevice == nil || r.fenceDevice.IsClosed() {
		return fmt.Errorf("SBD device is not available")
	}

	// Create fence message
	fenceReason := sbdprotocol.FENCE_REASON_NONE // Map from CR reason to SBD reason
	switch reason {
	case medik8sv1alpha1.SBDRemediationReasonNone:
		fenceReason = sbdprotocol.FENCE_REASON_NONE
	case medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout:
		fenceReason = sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT
	case medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive:
		fenceReason = sbdprotocol.FENCE_REASON_MANUAL
	case medik8sv1alpha1.SBDRemediationReasonManualFencing:
		fenceReason = sbdprotocol.FENCE_REASON_MANUAL
	}

	fenceMsg := sbdprotocol.NewFence(r.ownNodeID, targetNodeID, r.getNextSequence(), fenceReason)
	msgData, err := sbdprotocol.MarshalFence(fenceMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal fence message: %w", err)
	}

	// Calculate slot offset for the target node
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to target node's slot
	n, err := r.fenceDevice.WriteAt(msgData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write fence message to slot %d (offset %d): %w", targetNodeID, slotOffset, err)
	}

	if n != len(msgData) {
		return fmt.Errorf("partial write to slot %d: wrote %d bytes, expected %d", targetNodeID, n, len(msgData))
	}

	// Sync to ensure data is written to storage
	if err := r.fenceDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync fence message to storage: %w", err)
	}

	logger.Info("Fence message written successfully",
		"targetNodeID", targetNodeID,
		"sourceNodeID", r.ownNodeID,
		"reason", fenceReason,
		"slotOffset", slotOffset,
		"messageSize", len(msgData))

	return nil
}

// handleDeletion handles the deletion of a StorageBasedRemediation resource
func (r *SBDRemediationReconciler) handleDeletion(
	ctx context.Context, sbdRemediation *medik8sv1alpha1.StorageBasedRemediation, logger logr.Logger) (ctrl.Result, error) {
	nodeName := sbdRemediation.Name
	// First: uncordon the node so it can accept workloads again
	if err := r.markNodeAsSchedulable(ctx, nodeName); err != nil {
		logger.Error(err, "Failed to mark node schedulable during remediation deletion",
			"node", nodeName)
		return ctrl.Result{}, err
	}
	// Wait until the NodeController removes the unschedulable taint
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	if taintExists(node.Spec.Taints, nodeUnschedulableTaint) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Second: remove OutOfService taint; on failure, return error to retry
	if err := r.removeOutOfServiceTaint(ctx, nodeName); err != nil {
		logger.Error(err, "Failed to remove OutOfService taint during remediation deletion",
			"node", nodeName)
		return ctrl.Result{}, err
	}
	r.emitEventOnly(sbdRemediation, "Normal", "OOSTaintRemoved",
		fmt.Sprintf("Out-of-service taint removed from node '%s'", nodeName))

	// Check if our finalizer is present
	if controllerutil.ContainsFinalizer(sbdRemediation, SBDRemediationFinalizer) {
		logger.Info("Removing finalizer from StorageBasedRemediation")

		// Remove our finalizer from the list and update it
		controllerutil.RemoveFinalizer(sbdRemediation, SBDRemediationFinalizer)
		if err := r.Update(ctx, sbdRemediation); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}

		logger.Info("Finalizer removed successfully")
	}

	return ctrl.Result{}, nil
}

// emitEventf emits an event for the StorageBasedRemediation resource
func (r *SBDRemediationReconciler) emitEventf(obj *medik8sv1alpha1.StorageBasedRemediation,
	eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		combinedFmt := fmt.Sprintf("%s (%d): %s", r.ownNodeName, r.ownNodeID, messageFmt)
		r.Recorder.Eventf(obj, eventType, reason, combinedFmt, args...)
	}
}

// updateRemediationCondition updates a condition on an StorageBasedRemediation CR
func (r *SBDRemediationReconciler) updateRemediationCondition(ctx context.Context, remediation *medik8sv1alpha1.StorageBasedRemediation, conditionType medik8sv1alpha1.SBDRemediationConditionType, status metav1.ConditionStatus, reason, message string, logger logr.Logger) error {
	logger.Info("Setting Condition on StorageBasedRemediation:", "conditionType", conditionType, "reason", reason, "message", message)
	// Set the condition
	remediation.SetCondition(conditionType, status, reason, message)

	// Update the status
	if err := r.Status().Update(ctx, remediation); err != nil {
		return fmt.Errorf("failed to update StorageBasedRemediation condition: %w", err)
	}

	return nil
}

// emitEventOnly emits an event without attempting to update any conditions
// This is useful for pure observability when condition updates might fail due to RBAC or other issues
func (r *SBDRemediationReconciler) emitEventOnly(remediation *medik8sv1alpha1.StorageBasedRemediation,
	eventType, eventReason, eventMessage string) {
	r.emitEventf(remediation, eventType, eventReason, eventMessage)
}

// handleFencingFailure is a helper function to handle fencing failures consistently
func (r *SBDRemediationReconciler) handleFencingFailure(
	ctx context.Context, remediation *medik8sv1alpha1.StorageBasedRemediation, err error, logger logr.Logger) {
	nodeName := remediation.Name
	logger.Error(err, "Fencing operation failed")

	// Always emit failure event for observability, regardless of whether condition updates succeed
	r.emitEventOnly(remediation, "Warning", ReasonFencingFailed,
		fmt.Sprintf("Fencing failed for node '%s': %v", nodeName, err))

	// Try to update multiple conditions for failure state
	// Log but don't fail if these updates don't work
	if updateErr := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionFencingInProgress, metav1.ConditionFalse, ReasonFailed, err.Error(), logger); updateErr != nil {
		logger.Error(updateErr, "Failed to update FencingInProgress condition")
		r.emitEventOnly(remediation, "Warning", "ConditionUpdateFailed",
			fmt.Sprintf("Failed to update FencingInProgress condition: %v", updateErr))
	}

	if updateErr := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionReady, metav1.ConditionFalse, ReasonFailed, err.Error(), logger); updateErr != nil {
		logger.Error(updateErr, "Failed to update Ready condition")
		r.emitEventOnly(remediation, "Warning", "ConditionUpdateFailed",
			fmt.Sprintf("Failed to update Ready condition: %v", updateErr))
	}
}

// handleFencingSuccess is a helper function to handle fencing success consistently
func (r *SBDRemediationReconciler) handleFencingSuccess(
	ctx context.Context, remediation *medik8sv1alpha1.StorageBasedRemediation, logger logr.Logger) {
	nodeName := remediation.Name
	logger.Info("Fencing operation completed successfully",
		"targetNode", nodeName)

	// Update multiple conditions for success state
	if err := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionFencingInProgress, metav1.ConditionFalse, ReasonCompleted, "Fencing completed", logger); err != nil {
		logger.Error(err, "Failed to update fencing in progress condition")
	}
	if err := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionFencingSucceeded, metav1.ConditionTrue, ReasonCompleted, fmt.Sprintf("Node %s fenced successfully", nodeName), logger); err != nil {
		logger.Error(err, "Failed to update fencing succeeded condition")
	}
	if err := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionReady, metav1.ConditionTrue, ReasonCompleted, "Remediation completed successfully", logger); err != nil {
		logger.Error(err, "Failed to update ready condition")
	}

	// Emit success event
	r.emitEventf(remediation, "Normal", ReasonNodeFenced,
		"Node '%s' has been fenced successfully", nodeName)

	logger.Info("Cleared fencing operation",
		"targetNode", nodeName)

}

// ensureOutOfServiceTaint adds the OutOfService taint to the given node if not already present
func (r *SBDRemediationReconciler) ensureOutOfServiceTaint(ctx context.Context, nodeName string, logger logr.Logger) error {
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if taintExists(node.Spec.Taints, outOfServiceTaint) {
		return nil
	}

	node.Spec.Taints = append(node.Spec.Taints, outOfServiceTaint)
	if err := r.Update(ctx, node); err != nil {
		return err
	}
	logger.Info("Out Of Service Taint successfully applied on node", "node name", nodeName)
	return nil
}

// removeOutOfServiceTaint removes the OutOfService taint from the given node if present
func (r *SBDRemediationReconciler) removeOutOfServiceTaint(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if !taintExists(node.Spec.Taints, outOfServiceTaint) {
		return nil
	}

	if !removeTaint(&node.Spec.Taints, outOfServiceTaint) {
		return nil
	}
	if err := r.Update(ctx, node); err != nil {
		return err
	}
	return nil
}

func taintExists(taints []corev1.Taint, target corev1.Taint) bool {
	for _, t := range taints {
		if t.Key == target.Key && t.Effect == target.Effect {
			return true
		}
	}
	return false
}

func removeTaint(taints *[]corev1.Taint, target corev1.Taint) bool {
	list := *taints
	out := make([]corev1.Taint, 0, len(list))
	removed := false
	for _, t := range list {
		if t.Key == target.Key && t.Effect == target.Effect {
			removed = true
			continue
		}
		out = append(out, t)
	}
	if removed {
		*taints = out
	}
	return removed
}

func isSBDAgentRemediation(rem *medik8sv1alpha1.StorageBasedRemediation) bool {
	if rem.Annotations == nil {
		return false
	}
	_, ok := rem.Annotations[SBDAgentAnnotationKey]
	return ok
}

func isRemediationFresh(rem *medik8sv1alpha1.StorageBasedRemediation, now time.Time) bool {
	if rem.CreationTimestamp.IsZero() {
		return false
	}
	return now.Sub(rem.CreationTimestamp.Time) < SBDAgentRemediationFreshAge
}

// SetupWithManager sets up the controller with the Manager.
func (r *SBDRemediationReconciler) SetupWithManager(mgr ctrl.Manager, suffix string) error {
	logger := mgr.GetLogger().WithName("setup").WithValues("controller", "StorageBasedRemediation")

	logger.Info("Setting up StorageBasedRemediation controller with fencing capabilities")

	controllerName := "sbdremediation"
	if suffix != "" {
		controllerName = fmt.Sprintf("%s-%s", controllerName, suffix)
	}
	err := ctrl.NewControllerManagedBy(mgr).
		For(&medik8sv1alpha1.StorageBasedRemediation{}).
		Named(controllerName).
		Complete(r)

	if err != nil {
		logger.Error(err, "Failed to setup StorageBasedRemediation controller")
		return err
	}

	logger.Info("StorageBasedRemediation controller setup completed successfully")
	return nil
}

// checkFencingCompletion checks if the target node has been successfully fenced
func (r *SBDRemediationReconciler) checkFencingCompletion(
	ctx context.Context, remediation *medik8sv1alpha1.StorageBasedRemediation, logger logr.Logger) bool {
	targetNodeName := remediation.Name
	timeoutSeconds := remediation.Spec.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 60 // Default timeout if not specified
	}

	// Check when fencing was initiated to enforce timeout
	fencingStartTime := r.getFencingStartTime(remediation)
	if fencingStartTime.IsZero() {
		// Record when we started fencing monitoring
		r.recordFencingStartTime(ctx, remediation)
		return false // First check, need to wait
	}

	elapsed := time.Since(fencingStartTime)
	timeout := time.Duration(timeoutSeconds) * time.Second

	logger.V(1).Info("Checking fencing completion",
		"targetNode", targetNodeName,
		"elapsed", elapsed,
		"timeout", timeout)

	// Method 1: Check if target node is NotReady in Kubernetes
	nodeNotReady, err := r.isNodeNotReady(ctx, targetNodeName)
	if err != nil {
		logger.V(1).Info("Could not check node status", "error", err)
		// Don't fail immediately, try other methods
	}

	// Method 2: Check if target node has stopped heartbeating to SBD device
	heartbeatStopped, err := r.hasNodeStoppedHeartbeating(targetNodeName, logger)
	if err != nil {
		logger.V(1).Info("Could not check SBD heartbeat", "error", err)
		// Don't fail immediately, try other methods
	}

	// Consider fencing complete if either condition is met
	fenced := nodeNotReady || heartbeatStopped
	if elapsed > timeout {
		fenced = true
	}

	if fenced {
		logger.Info("Fencing completion detected",
			"targetNode", targetNodeName,
			"nodeNotReady", nodeNotReady,
			"heartbeatStopped", heartbeatStopped,
			"elapsed", elapsed)
	}

	return fenced
}

// isNodeNotReady checks if the target node is NotReady in Kubernetes
func (r *SBDRemediationReconciler) isNodeNotReady(ctx context.Context, nodeName string) (bool, error) {
	node := &corev1.Node{}
	err := r.Get(ctx, client.ObjectKey{Name: nodeName}, node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Node not found could indicate it was removed due to fencing
			return true, nil
		}
		return false, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Check node ready condition
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status != corev1.ConditionTrue, nil
		}
	}

	// If no Ready condition found, consider it not ready
	return true, nil
}

// hasNodeStoppedHeartbeating checks if the target node has stopped sending heartbeats to SBD device
func (r *SBDRemediationReconciler) hasNodeStoppedHeartbeating(nodeName string, logger logr.Logger) (bool, error) {
	if r.nodeManager == nil || r.sbdDevice == nil || r.sbdDevice.IsClosed() {
		return false, fmt.Errorf("SBD device or node manager not available")
	}

	// Get target node ID
	targetNodeID, err := r.nodeManager.GetNodeIDForNode(nodeName)
	if err != nil {
		return false, fmt.Errorf("failed to get node ID for %s: %w", nodeName, err)
	}

	// Read the target node's slot to check for recent heartbeat
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)

	n, err := r.sbdDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		return false, fmt.Errorf("failed to read SBD slot %d: %w", targetNodeID, err)
	}

	if n < sbdprotocol.SBD_HEADER_SIZE {
		return false, fmt.Errorf("insufficient data read from SBD slot %d", targetNodeID)
	}

	// Parse the header to check message timestamp
	header, err := sbdprotocol.Unmarshal(
		slotData[:sbdprotocol.SBD_HEADER_SIZE],
	)
	if err != nil {
		logger.V(1).Info("Could not parse SBD header, assuming node stopped", "error", err)
		return true, nil
	}

	messageTimestamp := time.Unix(int64(header.Timestamp)/1000000000, 0)
	messageAge := time.Since(messageTimestamp)
	switch header.Type {
	case sbdprotocol.SBD_MSG_TYPE_HEARTBEAT:
		logger.Info(
			"Checking SBD header",
			"type", "heartbeat",
			"age", messageAge,
			"timestamp", messageTimestamp,
			"rawTimestamp", header.Timestamp,
			"nodeID", header.NodeID,
		)
	case sbdprotocol.SBD_MSG_TYPE_FENCE:
		logger.Info(
			"Checking SBD header",
			"type", "fence",
			"age", messageAge,
			"timestamp", messageTimestamp,
			"rawTimestamp", header.Timestamp,
			"nodeID", header.NodeID,
		)
	default:
		logger.Info(
			"Checking SBD header",
			"type", header.Type,
			"age", messageAge,
			"timestamp", messageTimestamp,
			"rawTimestamp", header.Timestamp,
			"nodeID", header.NodeID,
		)
	}

	// Check if this is a heartbeat message and if it's recent
	if header.Type == sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		// Check if the heartbeat is old (more than 2x normal heartbeat interval)
		maxHeartbeatAge := 60 * time.Second // Conservative estimate for max heartbeat age

		if messageAge > maxHeartbeatAge {
			logger.Info("Node heartbeat is stale",
				"nodeID", targetNodeID,
				"messageAge", messageAge,
				"maxAge", maxHeartbeatAge)
			return true, nil
		}
	}

	// If we see a fence message in the slot, the node should have processed it
	if header.Type == sbdprotocol.SBD_MSG_TYPE_FENCE {
		logger.V(1).Info("Fence message still present in slot",
			"nodeID", targetNodeID)
		// Give more time for node to process and self-fence
		return false, nil
	}

	return false, nil
}

// getFencingStartTime gets the time when fencing monitoring started
func (r *SBDRemediationReconciler) getFencingStartTime(remediation *medik8sv1alpha1.StorageBasedRemediation) time.Time {
	// Look for the FencingInProgress condition timestamp
	for _, condition := range remediation.Status.Conditions {
		if condition.Type == string(medik8sv1alpha1.SBDRemediationConditionFencingInProgress) &&
			condition.Status == metav1.ConditionTrue {
			return condition.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// recordFencingStartTime records when fencing monitoring started
func (r *SBDRemediationReconciler) recordFencingStartTime(
	ctx context.Context, remediation *medik8sv1alpha1.StorageBasedRemediation) {
	// The FencingInProgress condition is already set when we write the fence message
	// We just need to ensure it's properly timestamped, which SetCondition handles
}
