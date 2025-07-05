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
	"github.com/medik8s/sbd-operator/pkg/retry"
)

const (
	SBDRemediationFinalizer = "medik8s.io/sbd-remediation-finalizer"
	// ReasonCompleted indicates the remediation was completed successfully
	ReasonCompleted = "RemediationCompleted"
	// ReasonInProgress indicates the remediation is in progress
	ReasonInProgress = "RemediationInProgress"
	// ReasonFailed indicates the remediation failed
	ReasonFailed = "RemediationFailed"

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
// This controller coordinates SBD remediation operations while SBD agents
// perform the actual fencing operations with direct device access.
type SBDRemediationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Retry configurations for API operations
	statusRetryConfig retry.Config
	apiRetryConfig    retry.Config
}

// conditionUpdate represents an update to a condition
type conditionUpdate struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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

// initializeRetryConfigs initializes the retry configurations for different operation types
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

// isTransientKubernetesError checks if an error is likely transient and should be retried
func (r *SBDRemediationReconciler) isTransientKubernetesError(err error) bool {
	return apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsInternalError(err)
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
// The SBDRemediation controller coordinates remediation operations:
// 1. Monitors SBDRemediation CRs for status updates
// 2. Manages finalizers and cleanup operations
// 3. Emits events for coordination with SBD agents
// 4. Updates status based on agent feedback
//
// Note: Actual fencing operations are performed by SBD agents with direct device access.
// This controller provides coordination and monitoring only.
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
		"status.operatorInstance", sbdRemediation.Status.OperatorInstance)

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
	}

	// Emit initial event for remediation initiation
	if len(sbdRemediation.Status.Conditions) == 0 {
		r.emitEventf(&sbdRemediation, "Normal", ReasonRemediationInitiated,
			"SBD remediation initiated for node '%s' - coordinating with SBD agents", sbdRemediation.Spec.NodeName)
	}

	// Check if we already completed this remediation
	if sbdRemediation.IsFencingSucceeded() {
		logger.Info("SBDRemediation already completed successfully",
			"fencingSucceeded", true)
		return ctrl.Result{}, nil
	}

	// Update status to indicate coordination with agents
	operatorInstanceID := r.getOperatorInstanceID()
	if sbdRemediation.Status.OperatorInstance != operatorInstanceID {
		sbdRemediation.Status.OperatorInstance = operatorInstanceID
		sbdRemediation.Status.LastUpdateTime = &metav1.Time{Time: time.Now()}

		// Set initial conditions for agent coordination
		sbdRemediation.SetCondition(medik8sv1alpha1.SBDRemediationConditionReady,
			metav1.ConditionFalse, "AgentCoordination",
			fmt.Sprintf("Coordinating with SBD agents for fencing node %s", sbdRemediation.Spec.NodeName))

		if err := r.Status().Update(ctx, &sbdRemediation); err != nil {
			logger.Error(err, "Failed to update SBDRemediation status")
			return ctrl.Result{}, err
		}

		logger.Info("Updated SBDRemediation status for agent coordination",
			"operatorInstance", operatorInstanceID,
			"nodeName", sbdRemediation.Spec.NodeName)

		// Emit event for agent coordination
		r.emitEventf(&sbdRemediation, "Normal", ReasonAgentCoordination,
			"SBDRemediation created for node '%s' - SBD agents will handle fencing", sbdRemediation.Spec.NodeName)
	}

	// Monitor the remediation progress
	// The actual fencing is performed by SBD agents watching this CR
	// We just need to monitor and requeue for status updates
	logger.V(1).Info("SBDRemediation monitoring - agents will handle fencing",
		"nodeName", sbdRemediation.Spec.NodeName,
		"fencingInProgress", sbdRemediation.IsFencingInProgress(),
		"fencingSucceeded", sbdRemediation.IsFencingSucceeded())

	// Requeue periodically to monitor progress
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleDeletion handles the deletion of a SBDRemediation resource
func (r *SBDRemediationReconciler) handleDeletion(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, logger logr.Logger) (ctrl.Result, error) {
	// Remove finalizer to allow deletion
	if controllerutil.ContainsFinalizer(sbdRemediation, SBDRemediationFinalizer) {
		controllerutil.RemoveFinalizer(sbdRemediation, SBDRemediationFinalizer)
		if err := r.Update(ctx, sbdRemediation); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from SBDRemediation",
			"finalizer", SBDRemediationFinalizer)
	}

	return ctrl.Result{}, nil
}

// updateStatusWithConditions updates the status of SBDRemediation with multiple conditions
func (r *SBDRemediationReconciler) updateStatusWithConditions(ctx context.Context, sbdRemediation *medik8sv1alpha1.SBDRemediation, conditions map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate, logger logr.Logger) (ctrl.Result, error) {
	// Update all conditions
	for condType, update := range conditions {
		sbdRemediation.SetCondition(condType, update.status, update.reason, update.message)
	}

	// Update last update time
	sbdRemediation.Status.LastUpdateTime = &metav1.Time{Time: time.Now()}

	// Perform the status update with retry
	if err := r.updateStatusWithRetry(ctx, sbdRemediation); err != nil {
		logger.Error(err, "Failed to update SBDRemediation status with retry")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Updated SBDRemediation status with conditions",
		"conditions", len(conditions))

	return ctrl.Result{}, nil
}

// updateStatusWithRetry updates the status of SBDRemediation with retry logic
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
