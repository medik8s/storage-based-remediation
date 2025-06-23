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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/retry"
)

// Event types and reasons for SBDConfig controller
const (
	// Event types
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	// Event reasons for SBDConfig operations
	ReasonSBDConfigReconciled       = "SBDConfigReconciled"
	ReasonDaemonSetManaged          = "DaemonSetManaged"
	ReasonNamespaceCreated          = "NamespaceCreated"
	ReasonServiceAccountCreated     = "ServiceAccountCreated"
	ReasonClusterRoleBindingCreated = "ClusterRoleBindingCreated"
	ReasonReconcileError            = "ReconcileError"
	ReasonDaemonSetError            = "DaemonSetError"
	ReasonNamespaceError            = "NamespaceError"
	ReasonServiceAccountError       = "ServiceAccountError"

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

// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list

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

	logger.V(1).Info("Starting SBDConfig reconciliation",
		"spec.image", sbdConfig.Spec.Image,
		"spec.namespace", sbdConfig.Spec.Namespace,
		"spec.sbdWatchdogPath", sbdConfig.Spec.GetSbdWatchdogPath(),
		"spec.staleNodeTimeout", sbdConfig.Spec.GetStaleNodeTimeout())

	// Set defaults if not specified
	if sbdConfig.Spec.Image == "" {
		sbdConfig.Spec.Image = "sbd-agent:latest"
		logger.V(1).Info("Set default image", "image", sbdConfig.Spec.Image)
	}

	if sbdConfig.Spec.Namespace == "" {
		sbdConfig.Spec.Namespace = "sbd-system"
		logger.V(1).Info("Set default namespace", "namespace", sbdConfig.Spec.Namespace)
	}

	// Ensure the namespace exists with retry logic
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure namespace", func() error {
		return r.ensureNamespace(ctx, &sbdConfig, sbdConfig.Spec.Namespace, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure namespace exists after retries",
			"namespace", sbdConfig.Spec.Namespace,
			"operation", "namespace-creation")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonNamespaceError,
			"Failed to ensure namespace '%s' exists: %v", sbdConfig.Spec.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Ensure the service account and RBAC resources exist with retry logic
	err = r.performKubernetesAPIOperationWithRetry(ctx, "ensure service account", func() error {
		return r.ensureServiceAccount(ctx, &sbdConfig, sbdConfig.Spec.Namespace, logger)
	}, logger)

	if err != nil {
		logger.Error(err, "Failed to ensure service account exists after retries",
			"namespace", sbdConfig.Spec.Namespace,
			"operation", "serviceaccount-creation")
		r.emitEventf(&sbdConfig, EventTypeWarning, ReasonServiceAccountError,
			"Failed to ensure service account 'sbd-agent' exists in namespace '%s': %v", sbdConfig.Spec.Namespace, err)

		// Return requeue with backoff for transient errors
		if r.isTransientKubernetesError(err) {
			return ctrl.Result{RequeueAfter: InitialSBDConfigRetryDelay}, err
		}
		return ctrl.Result{}, err
	}

	// Define the desired DaemonSet
	desiredDaemonSet := r.buildDaemonSet(&sbdConfig)
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

// ensureNamespace creates the namespace if it doesn't exist
func (r *SBDConfigReconciler) ensureNamespace(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, namespaceName string, logger logr.Logger) error {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sbd-operator",
				"app.kubernetes.io/component":  "sbd-system",
				"app.kubernetes.io/part-of":    "sbd-operator",
				"app.kubernetes.io/managed-by": "sbd-operator",
				// OpenShift specific labels to allow privileged workloads
				"security.openshift.io/scc.podSecurityLabelSync": "false",
				"pod-security.kubernetes.io/enforce":             "privileged",
				"pod-security.kubernetes.io/audit":               "privileged",
				"pod-security.kubernetes.io/warn":                "privileged",
			},
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		// Ensure labels are set on updates too
		if namespace.Labels == nil {
			namespace.Labels = make(map[string]string)
		}
		namespace.Labels["app.kubernetes.io/name"] = "sbd-operator"
		namespace.Labels["app.kubernetes.io/component"] = "sbd-system"
		namespace.Labels["app.kubernetes.io/part-of"] = "sbd-operator"
		namespace.Labels["app.kubernetes.io/managed-by"] = "sbd-operator"
		// OpenShift specific labels to allow privileged workloads
		namespace.Labels["security.openshift.io/scc.podSecurityLabelSync"] = "false"
		namespace.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
		namespace.Labels["pod-security.kubernetes.io/audit"] = "privileged"
		namespace.Labels["pod-security.kubernetes.io/warn"] = "privileged"
		return nil
	})

	if err == nil && result == controllerutil.OperationResultCreated {
		// Emit event for namespace creation
		logger.Info("Namespace created for SBD system with privileged security profile", "namespace", namespaceName)
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonNamespaceCreated,
			"Namespace '%s' created for SBD system with privileged security profile", namespaceName)
	} else if err == nil && result == controllerutil.OperationResultUpdated {
		logger.Info("Namespace updated for SBD system with privileged security profile", "namespace", namespaceName)
	}

	return err
}

// ensureServiceAccount creates the service account and RBAC resources if they don't exist
func (r *SBDConfigReconciler) ensureServiceAccount(ctx context.Context, sbdConfig *medik8sv1alpha1.SBDConfig, namespaceName string, logger logr.Logger) error {
	// Create the service account
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
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		// Set the controller reference
		return controllerutil.SetControllerReference(sbdConfig, serviceAccount, r.Scheme)
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
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("sbd-agent-%s", sbdConfig.Name),
			Labels: map[string]string{
				"app":                          "sbd-agent",
				"app.kubernetes.io/name":       "sbd-agent",
				"app.kubernetes.io/component":  "agent",
				"app.kubernetes.io/part-of":    "sbd-operator",
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbdconfig":                    sbdConfig.Name,
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
			Name:     "sbd-operator-sbd-agent-role", // Use the existing cluster role
		},
	}

	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, clusterRoleBinding, func() error {
		// Set the controller reference
		return controllerutil.SetControllerReference(sbdConfig, clusterRoleBinding, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to create or update cluster role binding: %w", err)
	}

	if result == controllerutil.OperationResultCreated {
		logger.Info("ClusterRoleBinding created for SBD agent", "clusterRoleBinding", fmt.Sprintf("sbd-agent-%s", sbdConfig.Name))
		r.emitEventf(sbdConfig, EventTypeNormal, ReasonClusterRoleBindingCreated,
			"ClusterRoleBinding 'sbd-agent-%s' created", sbdConfig.Name)
	}

	return nil
}

// buildDaemonSet constructs the desired DaemonSet based on the SBDConfig
func (r *SBDConfigReconciler) buildDaemonSet(sbdConfig *medik8sv1alpha1.SBDConfig) *appsv1.DaemonSet {
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
			Namespace: sbdConfig.Spec.Namespace,
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
					NodeSelector: map[string]string{
						"kubernetes.io/os": "linux",
					},
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
							Image:           sbdConfig.Spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
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
							Args: []string{
								fmt.Sprintf("--watchdog-path=%s", sbdConfig.Spec.GetSbdWatchdogPath()),
								"--watchdog-timeout=30s",
								"--log-level=info",
								fmt.Sprintf("--stale-node-timeout=%s", sbdConfig.Spec.GetStaleNodeTimeout().String()),
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "dev", MountPath: "/dev"},
								{Name: "sys", MountPath: "/sys", ReadOnly: true},
								{Name: "proc", MountPath: "/proc", ReadOnly: true},
							},
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
					Volumes: []corev1.Volume{
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
					},
					TerminationGracePeriodSeconds: &[]int64{30}[0],
				},
			},
		},
	}
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

	// Update status fields
	sbdConfig.Status.TotalNodes = latestDaemonSet.Status.DesiredNumberScheduled
	sbdConfig.Status.ReadyNodes = latestDaemonSet.Status.NumberReady
	sbdConfig.Status.DaemonSetReady = latestDaemonSet.Status.NumberReady == latestDaemonSet.Status.DesiredNumberScheduled && latestDaemonSet.Status.DesiredNumberScheduled > 0

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
		Named("sbdconfig").
		Complete(r)

	if err != nil {
		logger.Error(err, "Failed to setup SBDConfig controller")
		return err
	}

	logger.Info("SBDConfig controller setup completed successfully")
	return nil
}
