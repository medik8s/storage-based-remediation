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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/blockdevice"
	"github.com/medik8s/sbd-operator/pkg/retry"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	corev1 "k8s.io/api/core/v1"
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
	ReasonRemediationCompleted = "RemediationCompleted"
	ReasonRemediationFailed    = "RemediationFailed"
	ReasonRemediationInitiated = "RemediationInitiated"
	ReasonFinalizerProcessed   = "FinalizerProcessed"
	ReasonAgentCoordination    = "AgentCoordination"
)

// SBDRemediationReconciler reconciles a SBDRemediation object
// This controller performs actual SBD fencing operations by writing fence messages to the SBD device.
type SBDRemediationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Retry configurations for API operations
	statusRetryConfig retry.Config
	apiRetryConfig    retry.Config

	// SBD device for fencing operations
	sbdDevice   *blockdevice.Device
	fenceDevice *blockdevice.Device
	nodeManager *sbdprotocol.NodeManager
	ownNodeID   uint16
	ownNodeName string // Changed from uint32 to uint64
	sequence    uint64 // Changed from uint32 to uint64
}

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SetSBDDevice sets the Heartbeat device for checking heartbeat age
func (r *SBDRemediationReconciler) SetSBDDevice(device *blockdevice.Device) {
	r.sbdDevice = device
}

// SetFenceDevice sets the fence device for fencing operations
func (r *SBDRemediationReconciler) SetFenceDevice(device *blockdevice.Device) {
	r.fenceDevice = device
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
func (r *SBDRemediationReconciler) getNextSequence() uint64 { // Changed return type to uint64
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
// The SBDRemediation controller performs actual fencing operations:
// 1. Validates the remediation request
// 2. Resolves target node name to node ID
// 3. Writes fence message to SBD device
// 4. Updates status based on fencing results
func (r *SBDRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("sbdremediation-controller").WithValues(
		"request", req.NamespacedName,
		"controller", "SBDRemediation",
	)

	// Initialize retry configurations if not already done
	if r.statusRetryConfig.MaxRetries == 0 {
		r.initializeRetryConfigs(logger)
	}

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

	// Don't fence ourselves
	if sbdRemediation.Spec.NodeName == r.ownNodeName {
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
		"spec.nodeName", sbdRemediation.Spec.NodeName,
		"spec.timeoutSeconds", sbdRemediation.Spec.TimeoutSeconds,
		"status.ready", sbdRemediation.IsReady(),
		"status.fencingSucceeded", sbdRemediation.IsFencingSucceeded(),
	)

	logger.V(1).Info("Starting SBDRemediation reconciliation",
		"spec.nodeName", sbdRemediation.Spec.NodeName)

	// Handle deletion
	if !sbdRemediation.DeletionTimestamp.IsZero() {
		logger.Info("SBDRemediation is being deleted, processing finalizers",
			"deletionTimestamp", sbdRemediation.DeletionTimestamp,
			"finalizers", sbdRemediation.Finalizers)
		r.emitEventf(&sbdRemediation, "Normal", ReasonFinalizerProcessed,
			"Processing deletion of SBDRemediation for node '%s'", sbdRemediation.Spec.NodeName)
		return r.handleDeletion(ctx, &sbdRemediation, logger)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&sbdRemediation, SBDRemediationFinalizer) {
		controllerutil.AddFinalizer(&sbdRemediation, SBDRemediationFinalizer)
		if err := r.Update(ctx, &sbdRemediation); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Added finalizer to SBDRemediation",
			"finalizer", SBDRemediationFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	// Emit initial event for remediation initiation
	if len(sbdRemediation.Status.Conditions) == 0 {
		r.emitEventf(&sbdRemediation, "Normal", ReasonRemediationInitiated,
			"SBD remediation initiated for node '%s'", sbdRemediation.Spec.NodeName)
	}

	// Check if we already completed this remediation
	if sbdRemediation.IsFencingSucceeded() {
		logger.Info("SBDRemediation already completed successfully",
			"fencingSucceeded", true)
		return ctrl.Result{}, nil
	}

	// Check if fencing is already in progress
	if sbdRemediation.IsFencingInProgress() {
		logger.Info("SBDRemediation fencing already in progress")
		// Check if target node has been fenced (stopped heartbeating and/or became NotReady)
		fenced := r.checkFencingCompletion(ctx, &sbdRemediation, logger)

		if !fenced {
			// Still waiting for fencing to complete, requeue to check again
			logger.V(1).Info("Fencing not yet complete, requeueing for monitoring",
				"targetNode", sbdRemediation.Spec.NodeName)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Fencing completed successfully
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
		"targetNode", sbdRemediation.Spec.NodeName,
		"reason", sbdRemediation.Spec.Reason)

	// Update status to indicate fencing is in progress
	if err := r.updateRemediationCondition(ctx, &sbdRemediation, medik8sv1alpha1.SBDRemediationConditionFencingInProgress,
		metav1.ConditionTrue, ReasonInProgress, fmt.Sprintf("Fencing node %s", sbdRemediation.Spec.NodeName)); err != nil {
		logger.Error(err, "Failed to update remediation condition to in progress")
	}

	// Execute fencing
	if err := r.executeFencing(&sbdRemediation, logger); err != nil {
		r.handleFencingFailure(ctx, &sbdRemediation, err, logger)
		return ctrl.Result{}, err
	}

	// Fence message written successfully, now monitor for actual fencing completion
	logger.Info("Fence message written, monitoring for target node fencing completion",
		"targetNode", sbdRemediation.Spec.NodeName,
		"timeoutSeconds", sbdRemediation.Spec.TimeoutSeconds)

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// executeFencing performs the actual fencing operation via SBD device
func (r *SBDRemediationReconciler) executeFencing(
	remediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) error {
	targetNodeName := remediation.Spec.NodeName

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

// clearFenceSlotForNode clears the SBD slot associated with the provided node name.
func (r *SBDRemediationReconciler) clearFenceSlotForNode(
	remediation *medik8sv1alpha1.SBDRemediation,
	logger logr.Logger,
) error {

	nodeID, err := r.nodeManager.GetNodeIDForNode(remediation.Spec.NodeName)
	if err != nil {
		return fmt.Errorf("failed to get node ID for %s: %w", remediation.Spec.NodeName, err)
	}
	if err := r.writeFenceMessage(nodeID, medik8sv1alpha1.SBDRemediationReasonNone, logger); err != nil {
		return fmt.Errorf("failed to write fence message to node %s: %w", remediation.Spec.NodeName, err)
	}

	logger.Info("Cleared SBD slot for node after remediation completion",
		"nodeName", remediation.Spec.NodeName,
		"nodeID", nodeID)

	r.emitEventf(remediation, "Normal", ReasonCompleted,
		"Cleared SBD slot for node after remediation completion",
		"nodeName", remediation.Spec.NodeName,
		"nodeID", nodeID)

	return nil
}

// handleDeletion handles the deletion of a SBDRemediation resource
func (r *SBDRemediationReconciler) handleDeletion(
	ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) (ctrl.Result, error) {
	// Check if our finalizer is present
	if controllerutil.ContainsFinalizer(sbdRemediation, SBDRemediationFinalizer) {
		logger.Info("Removing finalizer from SBDRemediation")

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

// emitEventf emits an event for the SBDRemediation resource
func (r *SBDRemediationReconciler) emitEventf(obj *medik8sv1alpha1.SBDRemediation,
	eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// updateRemediationCondition updates a condition on an SBDRemediation CR
func (r *SBDRemediationReconciler) updateRemediationCondition(ctx context.Context,
	remediation *medik8sv1alpha1.SBDRemediation, conditionType medik8sv1alpha1.SBDRemediationConditionType,
	status metav1.ConditionStatus, reason, message string) error {
	// Set the condition
	remediation.SetCondition(conditionType, status, reason, message)

	// Update the status
	if err := r.Status().Update(ctx, remediation); err != nil {
		return fmt.Errorf("failed to update SBDRemediation condition: %w", err)
	}

	return nil
}

// emitEventOnly emits an event without attempting to update any conditions
// This is useful for pure observability when condition updates might fail due to RBAC or other issues
func (r *SBDRemediationReconciler) emitEventOnly(remediation *medik8sv1alpha1.SBDRemediation,
	eventType, eventReason, eventMessage string) {
	r.emitEventf(remediation, eventType, eventReason, eventMessage)
}

// handleFencingFailure is a helper function to handle fencing failures consistently
func (r *SBDRemediationReconciler) handleFencingFailure(
	ctx context.Context, remediation *medik8sv1alpha1.SBDRemediation, err error, logger logr.Logger) {
	logger.Error(err, "Fencing operation failed")

	// Always emit failure event for observability, regardless of whether condition updates succeed
	r.emitEventOnly(remediation, "Warning", ReasonFencingFailed,
		fmt.Sprintf("Fencing failed for node '%s': %v", remediation.Spec.NodeName, err))

	// Try to update multiple conditions for failure state
	// Log but don't fail if these updates don't work (e.g., due to RBAC issues)
	if updateErr := r.updateRemediationCondition(ctx, remediation,
		medik8sv1alpha1.SBDRemediationConditionFencingInProgress,
		metav1.ConditionFalse, ReasonFailed, err.Error()); updateErr != nil {
		logger.Error(updateErr, "Failed to update FencingInProgress condition")
		r.emitEventOnly(remediation, "Warning", "ConditionUpdateFailed",
			fmt.Sprintf("Failed to update FencingInProgress condition: %v", updateErr))
	}

	if updateErr := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionReady,
		metav1.ConditionFalse, ReasonFailed, err.Error()); updateErr != nil {
		logger.Error(updateErr, "Failed to update Ready condition")
		r.emitEventOnly(remediation, "Warning", "ConditionUpdateFailed",
			fmt.Sprintf("Failed to update Ready condition: %v", updateErr))
	}
}

// handleFencingSuccess is a helper function to handle fencing success consistently
func (r *SBDRemediationReconciler) handleFencingSuccess(
	ctx context.Context, remediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) {
	logger.Info("Fencing operation completed successfully",
		"targetNode", remediation.Spec.NodeName)

	// Update multiple conditions for success state
	if err := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionFencingInProgress,
		metav1.ConditionFalse, ReasonCompleted, "Fencing completed"); err != nil {
		logger.Error(err, "Failed to update fencing in progress condition")
	}
	if err := r.updateRemediationCondition(ctx, remediation,
		medik8sv1alpha1.SBDRemediationConditionFencingSucceeded, metav1.ConditionTrue, ReasonCompleted,
		fmt.Sprintf("Node %s fenced successfully", remediation.Spec.NodeName)); err != nil {
		logger.Error(err, "Failed to update fencing succeeded condition")
	}
	if err := r.updateRemediationCondition(ctx, remediation, medik8sv1alpha1.SBDRemediationConditionReady,
		metav1.ConditionTrue, ReasonCompleted, "Remediation completed successfully"); err != nil {
		logger.Error(err, "Failed to update ready condition")
	}

	// Best-effort clear of the target node's slot after successful remediation
	if err := r.clearFenceSlotForNode(remediation, logger); err != nil {
		logger.Error(err, "Failed to clear SBD slot after remediation completion",
			"node", remediation.Spec.NodeName)
	}

	// Emit success event
	r.emitEventf(remediation, "Normal", ReasonNodeFenced,
		"Node '%s' has been fenced successfully", remediation.Spec.NodeName)

	logger.Info("Cleared fencing operation",
		"targetNode", remediation.Spec.NodeName)

}

// SetupWithManager sets up the controller with the Manager.
func (r *SBDRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("setup").WithValues("controller", "SBDRemediation")

	logger.Info("Setting up SBDRemediation controller with fencing capabilities")

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

// checkFencingCompletion checks if the target node has been successfully fenced
func (r *SBDRemediationReconciler) checkFencingCompletion(
	ctx context.Context, remediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) bool {
	targetNodeName := remediation.Spec.NodeName
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
			logger.V(1).Info("Node heartbeat is stale",
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
func (r *SBDRemediationReconciler) getFencingStartTime(remediation *medik8sv1alpha1.SBDRemediation) time.Time {
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
	ctx context.Context, remediation *medik8sv1alpha1.SBDRemediation) {
	// The FencingInProgress condition is already set when we write the fence message
	// We just need to ensure it's properly timestamped, which SetCondition handles
}
