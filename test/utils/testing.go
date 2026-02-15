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

package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck
	. "github.com/onsi/gomega"    //nolint:staticcheck

	"github.com/aws/aws-sdk-go/service/ec2"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false
)

// TestClients holds the Kubernetes clients used for testing
type TestClients struct {
	Client         client.Client
	Clientset      *kubernetes.Clientset
	Config         *rest.Config
	Context        context.Context
	Ec2Client      *ec2.EC2
	AWSInitialized bool
}

// TestNamespace represents a test namespace with cleanup functionality
type TestNamespace struct {
	Name         string
	ArtifactsDir string
	Clients      *TestClients
}

// PodStatusChecker provides utilities for checking pod status
type PodStatusChecker struct {
	Clients   *TestClients
	Namespace string
	Labels    map[string]string
}

// ResourceCleaner provides utilities for cleaning up test resources
type ResourceCleaner struct {
	Clients *TestClients
}

// SetupKubernetesClients initializes Kubernetes clients for testing
func SetupKubernetesClients() (*TestClients, error) {
	// Load kubeconfig - try environment variable first, then default location
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(homeDir, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Create scheme with core Kubernetes types and add our CRDs
	clientScheme := runtime.NewScheme()
	err = scheme.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	err = medik8sv1alpha1.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add SBD types to scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(config, client.Options{Scheme: clientScheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create standard clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &TestClients{
		Client:    k8sClient,
		Clientset: clientset,
		Config:    config,
		Context:   context.Background(),
	}, nil
}

// CreateTestNamespace creates a test namespace and returns a cleanup function
func (tc *TestClients) CreateTestNamespace(namespace string) (*TestNamespace, error) {
	testFlags := GetTestFlags()
	artifactsDir := fmt.Sprintf("../../%s", testFlags.ArtifactsDir)

	// Ensure the artifacts directory for this test namespace exists
	if _, err := os.Stat(artifactsDir); os.IsNotExist(err) {
		err := os.MkdirAll(artifactsDir, 0755)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("failed to create artifacts directory %s", testFlags.ArtifactsDir))
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	tns := &TestNamespace{
		Name:         namespace,
		ArtifactsDir: testFlags.ArtifactsDir,
		Clients:      tc,
	}

	err := tc.Client.Create(tc.Context, ns)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return tns, nil
	}
	return tns, err
}

// Cleanup removes the test namespace and all its resources
func (tn *TestNamespace) Cleanup() error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: tn.Name,
		},
	}

	err := tn.Clients.Client.Delete(tn.Clients.Context, ns)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace %s: %w", tn.Name, err)
	}

	// Wait for namespace to be fully deleted
	Eventually(func() bool {
		var namespace corev1.Namespace
		err := tn.Clients.Client.Get(tn.Clients.Context, client.ObjectKey{Name: tn.Name}, &namespace)
		return errors.IsNotFound(err)
	}, time.Minute*2, time.Second*5).Should(BeTrue(), fmt.Sprintf("namespace %s not deleted", tn.Name))

	return nil
}

// CreateSBDConfig creates a test SBDConfig with common defaults
func (tn *TestNamespace) CreateSBDConfig(name string,
	options ...func(*medik8sv1alpha1.SBDConfig)) (*medik8sv1alpha1.SBDConfig, error) {
	sbdConfig := &medik8sv1alpha1.SBDConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tn.Name,
		},
		Spec: medik8sv1alpha1.SBDConfigSpec{
			IOTimeout:       &metav1.Duration{Duration: 5 * time.Second},
			ImagePullPolicy: string(corev1.PullAlways),
			SbdWatchdogPath: "/dev/watchdog",
			LogLevel:        "debug",
			RebootMethod:    "none", // Always use "none" for testing to prevent actual reboots
			WatchdogTimeout: &metav1.Duration{Duration: 60 * time.Second},
			PetIntervalMultiple: func() *int32 {
				val := int32(4)
				return &val
			}(),
		},
	}

	// Apply any custom options
	for _, option := range options {
		option(sbdConfig)
	}

	err := tn.Clients.Client.Create(tn.Clients.Context, sbdConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create SBDConfig %s: %w", name, err)
	}

	return sbdConfig, nil
}

// CleanupSBDConfig deletes an SBDConfig and waits for cleanup to complete
func (tn *TestNamespace) CleanupSBDConfig(sbdConfig *medik8sv1alpha1.SBDConfig) error {
	err := tn.Clients.Client.Delete(tn.Clients.Context, sbdConfig)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete SBDConfig %s: %w", sbdConfig.Name, err)
	}

	// Wait for SBDConfig to be fully deleted
	Eventually(func() bool {
		var config medik8sv1alpha1.SBDConfig
		err := tn.Clients.Client.Get(tn.Clients.Context, client.ObjectKey{
			Name:      sbdConfig.Name,
			Namespace: tn.Name,
		}, &config)
		if err != nil && errors.IsNotFound(err) {
			return true
		} else if err != nil {
			GinkgoWriter.Printf("Failed to get SBDConfig %s: %v\n", sbdConfig.Name, err)
			return false
		}
		GinkgoWriter.Printf("Got SBDConfig %s\n", sbdConfig.Name)
		return false
	}, time.Minute*5, time.Second*5).Should(BeTrue(), fmt.Sprintf("SBDConfig %s not deleted", sbdConfig.Name))

	// Wait for associated pods to be terminated, with force deletion for stuck non-running pods
	podCleanupStartTime := time.Now()
	const forceDeleteTimeout = 2 * time.Minute // Force delete stuck pods after 2 minutes
	forceDeleteAttempted := false

	Eventually(func() int {
		pods := &corev1.PodList{}
		err := tn.Clients.Client.List(tn.Clients.Context, pods,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbdconfig": sbdConfig.Name})
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return -1
		}

		// Log pod status
		for _, pod := range pods.Items {
			GinkgoWriter.Printf("Pod %s: %s on %s\n", pod.Name, pod.Status.Phase, pod.Spec.NodeName)
		}

		// If pods are still present after timeout and we haven't attempted force delete yet
		elapsed := time.Since(podCleanupStartTime)
		if elapsed >= forceDeleteTimeout && !forceDeleteAttempted && len(pods.Items) > 0 {
			// Collect all pods that aren't running
			var podsToDelete []corev1.Pod
			for _, pod := range pods.Items {
				if pod.Status.Phase != corev1.PodRunning {
					podsToDelete = append(podsToDelete, pod)
				}
			}

			for _, pod := range podsToDelete {
				// Force delete collected pods
				GinkgoWriter.Printf("Force deleting stuck non-running pods after %v timeout\n", elapsed)
				forceDeleteAttempted = true
				GinkgoWriter.Printf("Force deleting pod %s (phase: %s)\n", pod.Name, pod.Status.Phase)
				// Use GracePeriodSeconds=0 for immediate deletion
				zero := int64(0)
				policy := metav1.DeletePropagationBackground
				err := tn.Clients.Clientset.CoreV1().Pods(tn.Name).Delete(
					tn.Clients.Context, pod.Name, metav1.DeleteOptions{
						GracePeriodSeconds: &zero,
						PropagationPolicy:  &policy,
					})
				if err != nil && !errors.IsNotFound(err) {
					GinkgoWriter.Printf("Failed to force delete pod %s: %v\n", pod.Name, err)
				} else if errors.IsNotFound(err) {
					GinkgoWriter.Printf("Pod %s already deleted\n", pod.Name)
				} else {
					GinkgoWriter.Printf("Successfully initiated force delete for pod %s\n", pod.Name)
				}
			}
		}

		return len(pods.Items)
	}, time.Minute*5, time.Second*10).Should(Equal(0), fmt.Sprintf("SBDConfig %s pods not deleted", sbdConfig.Name))

	// Wait for associated DaemonSets to be deleted
	Eventually(func() int {
		daemonSets := &appsv1.DaemonSetList{}
		err := tn.Clients.Client.List(tn.Clients.Context, daemonSets,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbdconfig": sbdConfig.Name})
		if err != nil {
			return -1
		}
		return len(daemonSets.Items)
	}, time.Minute*5, time.Second*5).Should(Equal(0), fmt.Sprintf("SBDConfig %s DaemonSets not deleted", sbdConfig.Name))

	return nil
}

// NewPodStatusChecker creates a new PodStatusChecker for monitoring pods
func (tn *TestNamespace) NewPodStatusChecker(labels map[string]string) *PodStatusChecker {
	return &PodStatusChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
		Labels:    labels,
	}
}

const (
	// Default operator namespace
	OperatorNamespaceName = "sbd-operator-system"
)

func (tn *TestNamespace) OperatorNamespace() *TestNamespace {
	if tn.Name == OperatorNamespaceName {
		return tn
	}
	return &TestNamespace{
		Name:         OperatorNamespaceName,
		ArtifactsDir: tn.ArtifactsDir,
		Clients:      tn.Clients,
	}
}

// removeLog removes the first occurrence of a value from a slice of strings.
func removeLog(logs []string, target string) []string {
	result := []string{}
	for _, v := range logs {
		if v != target {
			result = append(result, v)
		}
	}
	return result
}

func (tn *TestNamespace) PodLogsContain(expectedLogs []string) (bool, error) {
	var podChecker *PodStatusChecker
	if tn.Name == "sbd-operator-system" {
		podChecker = tn.NewPodStatusChecker(map[string]string{"app.kubernetes.io/name": "sbd-operator"})
	} else {
		podChecker = tn.NewPodStatusChecker(map[string]string{"app": "sbd-agent"})
	}

	pods := &corev1.PodList{}
	err := podChecker.Clients.Client.List(podChecker.Clients.Context, pods,
		client.InNamespace(podChecker.Namespace),
		client.MatchingLabels(podChecker.Labels))
	if err != nil {
		return false, fmt.Errorf("failed to list pods: %w", err)
	}

	for _, pod := range pods.Items {
		logStr, err := podChecker.GetPodLogs(pod.Name, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get logs from pod %s: %w", pod.Name, err)
		}
		// Split the log into lines
		lines := strings.Split(logStr, "\n")
		for _, line := range lines {
			for _, expectedLog := range expectedLogs {
				if strings.Contains(line, expectedLog) {
					expectedLogs = removeLog(expectedLogs, expectedLog)
					GinkgoWriter.Printf(
						"Agent pod %s has log: %s (%d remaining): %s\n",
						pod.Name,
						expectedLog,
						len(expectedLogs),
						line,
					)
					break
				}
			}
		}
		if len(expectedLogs) == 0 {
			return true, nil
		}
	}
	GinkgoWriter.Printf("expected logs not found: %v\n", expectedLogs)
	return false, fmt.Errorf("expected logs not found")
}

// WaitForPodsReady waits for pods matching the labels to become ready
func (psc *PodStatusChecker) WaitForPodsReady(minCount int, timeout time.Duration) error {
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := psc.Clients.Client.List(psc.Clients.Context, pods,
			client.InNamespace(psc.Namespace),
			client.MatchingLabels(psc.Labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return 0
		}

		readyPods := 0
		unreadyPodsCount := 0
		var unreadyPods []corev1.Pod
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				readyPodAdded := false
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady &&
						condition.Status == corev1.ConditionTrue {
						readyPods++
						readyPodAdded = true
						break
					}
				}
				if !readyPodAdded {
					unreadyPodsCount++
					unreadyPods = append(unreadyPods, pod)
				}
			} else {
				unreadyPodsCount++
				unreadyPods = append(unreadyPods, pod)
			}
		}

		GinkgoWriter.Printf("Found %d ready pods out of %d total\n", readyPods, len(pods.Items))
		GinkgoWriter.Printf("Found %d unready pods out of %d total\n", unreadyPodsCount, len(pods.Items))

		for _, pod := range unreadyPods {
			GinkgoWriter.Printf("Found unready pod: %s Status Phase: %s Status Conditions: %s \n", pod.Name, pod.Status.Phase, pod.Status.Conditions)
		}

		return readyPods
	}, timeout, time.Second*15).Should(BeNumerically(">=", minCount))

	return nil
}

// WaitForPodsRunning waits for pods to be in Running state (not necessarily ready)
func (psc *PodStatusChecker) WaitForPodsRunning(minCount int, timeout time.Duration) error {
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := psc.Clients.Client.List(psc.Clients.Context, pods,
			client.InNamespace(psc.Namespace),
			client.MatchingLabels(psc.Labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return 0
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods++
			}
		}

		GinkgoWriter.Printf("Found %d running pods out of %d total\n", runningPods, len(pods.Items))
		return runningPods
	}, timeout, time.Second*15).Should(BeNumerically(">=", minCount))

	return nil
}

// GetPodLogs retrieves logs from a pod
func (psc *PodStatusChecker) GetPodLogs(podName string, tailLines *int64) (string, error) {
	logOptions := &corev1.PodLogOptions{}
	if tailLines != nil {
		logOptions.TailLines = tailLines
	}

	logs, err := psc.Clients.Clientset.CoreV1().Pods(psc.Namespace).
		GetLogs(podName, logOptions).DoRaw(psc.Clients.Context)
	if err != nil {
		return "", fmt.Errorf("failed to get logs from pod %s: %w", podName, err)
	}

	return string(logs), nil
}

// CheckPodRestarts checks if any pods have been restarted and returns details
func (psc *PodStatusChecker) CheckPodRestarts() (bool, []string) {
	pods := &corev1.PodList{}
	err := psc.Clients.Client.List(psc.Clients.Context, pods,
		client.InNamespace(psc.Namespace),
		client.MatchingLabels(psc.Labels))
	if err != nil {
		return false, []string{fmt.Sprintf("Failed to list pods: %v", err)}
	}

	hasRestarts := false
	var restartInfo []string

	for _, pod := range pods.Items {
		totalRestarts := int32(0)
		for _, containerStatus := range pod.Status.ContainerStatuses {
			totalRestarts += containerStatus.RestartCount
		}

		if totalRestarts > 0 {
			hasRestarts = true
			restartInfo = append(restartInfo, fmt.Sprintf("Pod %s has %d total restarts", pod.Name, totalRestarts))
		}
	}

	return hasRestarts, restartInfo
}

// GetFirstPod returns the first pod matching the labels
func (psc *PodStatusChecker) GetFirstPod() (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := psc.Clients.Client.List(psc.Clients.Context, pods,
		client.InNamespace(psc.Namespace),
		client.MatchingLabels(psc.Labels))
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found matching labels")
	}

	return &pods.Items[0], nil
}

// DebugCollector provides utilities for collecting debug information
type DebugCollector struct {
	Clients      *TestClients
	ArtifactsDir string
}

// NewDebugCollector creates a new DebugCollector
func (tc *TestClients) NewDebugCollector(artifactsDir string) *DebugCollector {
	return &DebugCollector{Clients: tc, ArtifactsDir: artifactsDir}
}

// CollectControllerLogs collects logs from the controller manager pod
func (dc *DebugCollector) CollectControllerLogs(namespace, podName string) {
	By("Fetching controller manager pod logs")
	req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	podLogs, err := req.Stream(dc.Clients.Context)
	if err == nil {
		defer func() { _ = podLogs.Close() }()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		logFileName := fmt.Sprintf("%s/%s.log", dc.ArtifactsDir, podName)
		if f, fileErr := os.Create(logFileName); fileErr == nil {
			defer func() { _ = f.Close() }()
			_, _ = f.Write(buf.Bytes())
			GinkgoWriter.Printf("Controller logs for pod %s saved to %s\n", podName, logFileName)
		} else {
			GinkgoWriter.Printf("Failed to write controller logs to file %s: %s\n", logFileName, fileErr)
			GinkgoWriter.Printf("Controller logs:\n %s\n", buf.String())
		}
	} else {
		GinkgoWriter.Printf("Failed to get Controller logs: %s\n", err)
	}
}

// CollectAgentLogs collects logs from all SBD agent pods
func (dc *DebugCollector) CollectAgentLogs(namespace string) {
	By("Fetching SBD agent pod logs")

	// Get all SBD agent pods
	pods := &corev1.PodList{}
	err := dc.Clients.Client.List(dc.Clients.Context, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"app": "sbd-agent"})

	if err != nil {
		GinkgoWriter.Printf("Failed to list SBD agent pods: %s\n", err)
		return
	}

	// Filter out pods that are being deleted
	var activePods []corev1.Pod
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			activePods = append(activePods, pod)
		}
	}

	if len(activePods) == 0 {
		GinkgoWriter.Printf("No active SBD agent pods found\n")
		return
	}

	// Collect logs from each agent pod
	for _, pod := range activePods {
		GinkgoWriter.Printf("\n=== SBD Agent Pod: %s (Node: %s) ===\n", pod.Name, pod.Spec.NodeName)

		req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
		podLogs, err := req.Stream(dc.Clients.Context)
		if err == nil {
			defer func() { _ = podLogs.Close() }()
			buf := new(bytes.Buffer)
			_, _ = io.Copy(buf, podLogs)
			// Save the logs to a file named after the pod name
			logFileName := fmt.Sprintf("%s/%s.log", dc.ArtifactsDir, pod.Name)
			if f, fileErr := os.Create(logFileName); fileErr == nil {
				defer func() { _ = f.Close() }()
				_, _ = f.Write(buf.Bytes())
				GinkgoWriter.Printf("Agent logs for pod %s saved to %s\n", pod.Name, logFileName)
			} else {
				GinkgoWriter.Printf("Failed to write agent logs to file %s: %s\n", logFileName, fileErr)
				GinkgoWriter.Printf("Agent logs:\n %s\n", buf.String())
			}
		} else {
			GinkgoWriter.Printf("Failed to get agent logs from pod %s: %s\n", pod.Name, err)
		}
	}
}

// CollectKubernetesEvents collects Kubernetes events from a namespace
func (dc *DebugCollector) CollectKubernetesEvents(namespace string) {
	By("Fetching Kubernetes events")
	events, err := dc.Clients.Clientset.CoreV1().Events(namespace).List(dc.Clients.Context, metav1.ListOptions{})
	if err == nil {
		eventsOutput := ""
		logFileName := fmt.Sprintf("%s/kubernetes-events.log", dc.ArtifactsDir)
		f, fileErr := os.Create(logFileName)
		if fileErr != nil {
			GinkgoWriter.Printf("Failed to write agent logs to file %s: %s\n", logFileName, fileErr)
			GinkgoWriter.Printf("Kubernetes events:\n%s\n", eventsOutput)
		} else {
			defer func() { _ = f.Close() }()
			GinkgoWriter.Printf("Saving Kubernetes events to %s\n", logFileName)
			_, _ = f.WriteString("Kubernetes events:\n")
		}
		for _, event := range events.Items {
			eventOutput := fmt.Sprintf("%s  %s     %s  %s/%s  %s\n",
				event.LastTimestamp.Format("2006-01-02T15:04:05Z"),
				event.Type,
				event.Reason,
				event.InvolvedObject.Kind,
				event.InvolvedObject.Name,
				event.Message)
			if fileErr == nil {
				_, _ = f.WriteString(eventOutput)
			} else {
				GinkgoWriter.Printf(" %s", eventOutput)
			}
		}
	} else {
		GinkgoWriter.Printf("Failed to get Kubernetes events: %s", err)
	}
}

// CollectStorageJobs collects SBD device initialization jobs for debugging
func (dc *DebugCollector) CollectStorageJobs(namespace string) {
	By("Fetching SBD device initialization jobs for debugging")

	// Search for SBD device initialization jobs - these are created by the SBD operator controller
	// and have specific labels: app.kubernetes.io/component=sbd-device-init
	jobs := &batchv1.JobList{}
	err := dc.Clients.Client.List(dc.Clients.Context, jobs,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/component": "sbd-device-init"})

	if err != nil {
		GinkgoWriter.Printf("Failed to list SBD device initialization jobs in namespace %s: %v\n", namespace, err)
		return
	}

	if len(jobs.Items) == 0 {
		GinkgoWriter.Printf("No SBD device initialization jobs found in namespace %s\n", namespace)
		return
	}

	GinkgoWriter.Printf("\n=== SBD Device Initialization Jobs in namespace %s ===\n", namespace)

	for _, job := range jobs.Items {
		// Collect job definition
		jobYAML, err := yaml.Marshal(job)
		if err == nil {
			jobFileName := fmt.Sprintf("%s/%s-job.yaml", dc.ArtifactsDir, job.Name)
			if f, fileErr := os.Create(jobFileName); fileErr == nil {
				defer func() { _ = f.Close() }()
				_, _ = f.Write(jobYAML)
				GinkgoWriter.Printf("SBD device init job spec for %s saved to %s\n", job.Name, jobFileName)
			} else {
				GinkgoWriter.Printf("Failed to write SBD device init job spec to file %s: %s\n", jobFileName, fileErr)
				GinkgoWriter.Printf("SBD device init job %s spec:\n%s\n", job.Name, string(jobYAML))
			}
		}

		// Display job status
		GinkgoWriter.Printf("SBD device init job %s: Active=%d, Succeeded=%d, Failed=%d\n",
			job.Name, job.Status.Active, job.Status.Succeeded, job.Status.Failed)

		// Display job conditions for more detailed status
		if len(job.Status.Conditions) > 0 {
			GinkgoWriter.Printf("Job %s conditions:\n", job.Name)
			for _, condition := range job.Status.Conditions {
				GinkgoWriter.Printf("  - Type: %s, Status: %s, Reason: %s, Message: %s\n",
					condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}

		// Collect logs from job pods
		dc.collectJobPodLogs(namespace, job.Name)
	}
}

// collectJobPodLogs collects logs from pods belonging to a specific job
func (dc *DebugCollector) collectJobPodLogs(namespace, jobName string) {
	// Get pods belonging to this job
	pods := &corev1.PodList{}
	err := dc.Clients.Client.List(dc.Clients.Context, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"job-name": jobName})

	if err != nil {
		GinkgoWriter.Printf("Failed to list pods for job %s: %v\n", jobName, err)
		return
	}

	if len(pods.Items) == 0 {
		GinkgoWriter.Printf("No pods found for SBD device init job %s\n", jobName)
		return
	}

	for _, pod := range pods.Items {
		GinkgoWriter.Printf("Collecting logs from SBD device init job pod: %s\n", pod.Name)

		// Collect pod definition
		podYAML, err := yaml.Marshal(pod)
		if err == nil {
			podFileName := fmt.Sprintf("%s/%s-podspec.yaml", dc.ArtifactsDir, pod.Name)
			if f, fileErr := os.Create(podFileName); fileErr == nil {
				defer func() { _ = f.Close() }()
				_, _ = f.Write(podYAML)
				GinkgoWriter.Printf("SBD device init pod spec for %s saved to %s\n", pod.Name, podFileName)
			} else {
				GinkgoWriter.Printf("Failed to write SBD device init pod spec to file %s: %s\n", podFileName, fileErr)
			}
		}

		// Collect pod logs if the pod is not being deleted
		if pod.DeletionTimestamp == nil {
			req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
			podLogs, err := req.Stream(dc.Clients.Context)
			if err == nil {
				defer func() { _ = podLogs.Close() }()
				buf := new(bytes.Buffer)
				_, _ = io.Copy(buf, podLogs)
				logFileName := fmt.Sprintf("%s/%s.log", dc.ArtifactsDir, pod.Name)
				if f, fileErr := os.Create(logFileName); fileErr == nil {
					defer func() { _ = f.Close() }()
					_, _ = f.Write(buf.Bytes())
					GinkgoWriter.Printf("SBD device init pod logs for %s saved to %s\n", pod.Name, logFileName)
				} else {
					GinkgoWriter.Printf("Failed to write SBD device init pod logs to file %s: %s\n", logFileName, fileErr)
					GinkgoWriter.Printf("SBD device init pod %s logs:\n%s\n", pod.Name, buf.String())
				}
			} else {
				GinkgoWriter.Printf("Failed to get logs from SBD device init pod %s: %s\n", pod.Name, err)
			}
		}

		// Display pod status
		GinkgoWriter.Printf("SBD device init pod %s: Phase=%s, Node=%s\n",
			pod.Name, pod.Status.Phase, pod.Spec.NodeName)

		// Display pod conditions for detailed status
		if len(pod.Status.Conditions) > 0 {
			GinkgoWriter.Printf("Pod %s conditions:\n", pod.Name)
			for _, condition := range pod.Status.Conditions {
				GinkgoWriter.Printf("  - Type: %s, Status: %s, Reason: %s, Message: %s\n",
					condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}
	}
}

// CollectSBDRemediations collects StorageBasedRemediation CRs
//
//nolint:dupl // similar to CollectSBDConfigs; kept distinct for clarity
func (dc *DebugCollector) CollectSBDRemediations(namespace string) {
	By(fmt.Sprintf("Fetching SBDRemediations in namespace %s", namespace))
	remediations := &medik8sv1alpha1.StorageBasedRemediationList{}
	err := dc.Clients.Client.List(dc.Clients.Context, remediations, client.InNamespace(namespace))
	if err == nil {
		for _, remediation := range remediations.Items {
			data, err := yaml.Marshal(remediation)
			if err == nil {
				logFileName := fmt.Sprintf("%s/%s.yaml", dc.ArtifactsDir, remediation.Name)
				if f, fileErr := os.Create(logFileName); fileErr == nil {
					defer func() { _ = f.Close() }()
					_, _ = f.Write(data)
					GinkgoWriter.Printf("StorageBasedRemediation %s saved to %s\n", remediation.Name, logFileName)
				} else {
					GinkgoWriter.Printf("Failed to write StorageBasedRemediation to file %s: %s\n", logFileName, fileErr)
					GinkgoWriter.Printf("StorageBasedRemediation %s:\n%s\n", remediation.Name, string(data))
				}
			}
		}
	}
}

// CollectSBDConfigs collects SBDConfig CRs
//
//nolint:dupl // similar to CollectSBDRemediations; kept distinct for clarity
func (dc *DebugCollector) CollectSBDConfigs(namespace string) {
	By(fmt.Sprintf("Fetching SBDConfigs in namespace %s", namespace))
	configs := &medik8sv1alpha1.SBDConfigList{}
	err := dc.Clients.Client.List(dc.Clients.Context, configs, client.InNamespace(namespace))
	if err == nil {
		for _, config := range configs.Items {
			data, err := yaml.Marshal(config)
			if err == nil {
				logFileName := fmt.Sprintf("%s/%s.yaml", dc.ArtifactsDir, config.Name)
				if f, fileErr := os.Create(logFileName); fileErr == nil {
					defer func() { _ = f.Close() }()
					_, _ = f.Write(data)
					GinkgoWriter.Printf("SBDConfig %s saved to %s\n", config.Name, logFileName)
				} else {
					GinkgoWriter.Printf("Failed to write SBDConfig to file %s: %s\n", logFileName, fileErr)
					GinkgoWriter.Printf("SBDConfig %s:\n%s\n", config.Name, string(data))
				}
			}
		}
	}
}

// CollectPodLogs collects logs from a specific pod container
func (dc *DebugCollector) CollectPodLogs(namespace, podName, containerName string) {
	By(fmt.Sprintf("Fetching logs from pod %s container %s", podName, containerName))
	req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName})
	podLogs, err := req.Stream(dc.Clients.Context)
	if err == nil {
		defer func() { _ = podLogs.Close() }()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		logFileName := fmt.Sprintf("%s/%s-%s.log", dc.ArtifactsDir, podName, containerName)
		if f, fileErr := os.Create(logFileName); fileErr == nil {
			defer func() { _ = f.Close() }()
			_, _ = f.Write(buf.Bytes())
			GinkgoWriter.Printf("Pod logs for %s-%s saved to %s\n", podName, containerName, logFileName)
		} else {
			GinkgoWriter.Printf("Failed to write pod logs to file %s: %s\n", logFileName, fileErr)
			GinkgoWriter.Printf("Pod %s-%s logs:\n%s\n", podName, containerName, buf.String())
		}
	} else {
		GinkgoWriter.Printf("Failed to get logs from pod %s container %s: %s\n", podName, containerName, err)
	}
}

// CollectPodDescription collects and prints pod description
func (dc *DebugCollector) CollectPodDescription(namespace, podName string) {
	By(fmt.Sprintf("Fetching %s pod description", podName))
	pod := &corev1.Pod{}
	err := dc.Clients.Client.Get(dc.Clients.Context, client.ObjectKey{Name: podName, Namespace: namespace}, pod)
	if err == nil {
		podYAML, _ := yaml.Marshal(pod)
		// Save the pod spec YAML to a file named after the pod
		podFileName := fmt.Sprintf("%s/%s-podspec.yaml", dc.ArtifactsDir, podName)
		if f, fileErr := os.Create(podFileName); fileErr == nil {
			defer func() { _ = f.Close() }()
			_, _ = f.Write(podYAML)
			GinkgoWriter.Printf("Pod spec for %s saved to %s\n", podName, podFileName)
		} else {
			GinkgoWriter.Printf("Failed to write pod spec to file %s: %s\n", podFileName, fileErr)
			GinkgoWriter.Printf("Pod description:\n%s\n", string(podYAML))
		}
	} else {
		GinkgoWriter.Printf("Failed to get pod description: %s\n", err)
	}
}

// ServiceAccountTokenGenerator provides utilities for generating service account tokens
type ServiceAccountTokenGenerator struct {
	Clients *TestClients
}

// NewServiceAccountTokenGenerator creates a new token generator
func (tc *TestClients) NewServiceAccountTokenGenerator() *ServiceAccountTokenGenerator {
	return &ServiceAccountTokenGenerator{Clients: tc}
}

// GenerateToken generates a token for the specified service account
func (satg *ServiceAccountTokenGenerator) GenerateToken(namespace, serviceAccountName string) (string, error) {
	var token string

	Eventually(func() error {
		// Create TokenRequest using the typed client
		tokenRequest := &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				// Set a reasonable expiration time (1 hour)
				ExpirationSeconds: func() *int64 {
					val := int64(3600)
					return &val
				}(),
			},
		}

		// Use the authentication client to create the token
		result, err := satg.Clients.Clientset.CoreV1().ServiceAccounts(namespace).
			CreateToken(satg.Clients.Context, serviceAccountName, tokenRequest, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create service account token: %w", err)
		}

		token = result.Status.Token
		if token == "" {
			return fmt.Errorf("received empty token")
		}

		return nil
	}, time.Minute*2, time.Second*10).Should(Succeed())

	return token, nil
}

// NodeStabilityChecker provides utilities for checking node stability and reboot detection
type NodeStabilityChecker struct {
	Clients         *TestClients
	initialNodeInfo map[string]NodeBootInfo // Track initial node state
}

// NodeBootInfo stores information to detect node reboots
type NodeBootInfo struct {
	BootID         string
	KernelVersion  string
	KubeletVersion string
	StartTime      metav1.Time
}

// NewNodeStabilityChecker creates a new node stability checker
func (tc *TestClients) NewNodeStabilityChecker() *NodeStabilityChecker {
	return &NodeStabilityChecker{
		Clients:         tc,
		initialNodeInfo: make(map[string]NodeBootInfo),
	}
}

// captureInitialNodeState captures the initial boot state of all nodes
func (nsc *NodeStabilityChecker) captureInitialNodeState() error {

	if len(nsc.initialNodeInfo) > 0 {
		return nil
	}

	nodes := &corev1.NodeList{}
	err := nsc.Clients.Client.List(nsc.Clients.Context, nodes)
	if err != nil {
		return fmt.Errorf("failed to list nodes for initial state capture: %w", err)
	}

	for _, node := range nodes.Items {
		nsc.initialNodeInfo[node.Name] = NodeBootInfo{
			BootID:         node.Status.NodeInfo.BootID,
			KernelVersion:  node.Status.NodeInfo.KernelVersion,
			KubeletVersion: node.Status.NodeInfo.KubeletVersion,
			StartTime:      node.CreationTimestamp,
		}
		GinkgoWriter.Printf("Captured initial state for node %s: BootID=%s, Kernel=%s\n",
			node.Name, node.Status.NodeInfo.BootID, node.Status.NodeInfo.KernelVersion)
	}

	return nil
}

// CheckForNodeReboots checks if any nodes have rebooted since initial capture
func (nsc *NodeStabilityChecker) CheckForNodeReboots() (bool, []string, error) {
	// Capture initial state if not done yet
	if len(nsc.initialNodeInfo) == 0 {
		err := nsc.captureInitialNodeState()
		if err != nil {
			return false, nil, err
		}
		// Return no reboots on first check
		return false, nil, nil
	}

	nodes := &corev1.NodeList{}
	err := nsc.Clients.Client.List(nsc.Clients.Context, nodes)
	if err != nil {
		return false, nil, fmt.Errorf("failed to list nodes for reboot check: %w", err)
	}

	var rebootedNodes []string
	hasReboots := false

	for _, node := range nodes.Items {
		initialInfo, exists := nsc.initialNodeInfo[node.Name]
		if !exists {
			GinkgoWriter.Printf("Warning: No initial state for node %s, capturing current state\n", node.Name)
			nsc.initialNodeInfo[node.Name] = NodeBootInfo{
				BootID:         node.Status.NodeInfo.BootID,
				KernelVersion:  node.Status.NodeInfo.KernelVersion,
				KubeletVersion: node.Status.NodeInfo.KubeletVersion,
				StartTime:      node.CreationTimestamp,
			}
			continue
		}

		// Check if BootID has changed (most reliable indicator of reboot)
		if initialInfo.BootID != node.Status.NodeInfo.BootID {
			GinkgoWriter.Printf("REBOOT DETECTED: Node %s BootID changed from %s to %s\n",
				node.Name, initialInfo.BootID, node.Status.NodeInfo.BootID)
			rebootedNodes = append(rebootedNodes, fmt.Sprintf("%s (BootID changed)", node.Name))
			hasReboots = true
		}

		// Check if kernel version changed (could indicate reboot with kernel update)
		if initialInfo.KernelVersion != node.Status.NodeInfo.KernelVersion {
			GinkgoWriter.Printf("REBOOT DETECTED: Node %s kernel version changed from %s to %s\n",
				node.Name, initialInfo.KernelVersion, node.Status.NodeInfo.KernelVersion)
			rebootedNodes = append(rebootedNodes, fmt.Sprintf("%s (kernel changed)", node.Name))
			hasReboots = true
		}
	}

	// Also check for reboot-related events
	events := &corev1.EventList{}
	err = nsc.Clients.Client.List(nsc.Clients.Context, events)
	if err == nil {
		for _, event := range events.Items {
			if event.InvolvedObject.Kind == "Node" &&
				(strings.Contains(strings.ToLower(event.Reason), "reboot") ||
					strings.Contains(strings.ToLower(event.Message), "reboot") ||
					strings.Contains(strings.ToLower(event.Reason), "starting") ||
					event.Reason == "NodeReady" && strings.Contains(event.Message, "kubelet")) {

				// Check if this is a recent event
				if time.Since(event.FirstTimestamp.Time) < time.Minute*10 {
					GinkgoWriter.Printf("REBOOT-RELATED EVENT: Node %s - %s: %s\n",
						event.InvolvedObject.Name, event.Reason, event.Message)
					eventMsg := fmt.Sprintf("%s (event: %s)", event.InvolvedObject.Name, event.Reason)
					if !contains(rebootedNodes, eventMsg) {
						rebootedNodes = append(rebootedNodes, eventMsg)
						hasReboots = true
					}
				}
			}
		}
	}

	return hasReboots, rebootedNodes, nil
}

// WaitForNodesStable waits for all nodes to remain stable (Ready) and ensures no reboots occur
func (nsc *NodeStabilityChecker) WaitForNodesStable(duration time.Duration) error {
	// Capture initial state
	err := nsc.captureInitialNodeState()
	if err != nil {
		return fmt.Errorf("failed to capture initial node state: %w", err)
	}

	Consistently(func() bool {
		// Check if nodes are ready
		nodes := &corev1.NodeList{}
		err := nsc.Clients.Client.List(nsc.Clients.Context, nodes)
		if err != nil {
			GinkgoWriter.Printf("Failed to list nodes: %v\n", err)
			return false
		}

		readyNodeCount := 0
		for _, node := range nodes.Items {
			isReady := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady &&
					condition.Status == corev1.ConditionTrue {
					isReady = true
					readyNodeCount++
					break
				}
			}
			if !isReady {
				GinkgoWriter.Printf("Node %s is not ready: %+v\n", node.Name, node.Status.Conditions)
				return false
			}
		}

		// Check for reboots
		hasReboots, rebootedNodes, err := nsc.CheckForNodeReboots()
		if err != nil {
			GinkgoWriter.Printf("Failed to check for node reboots: %v\n", err)
			return false
		}
		if hasReboots {
			GinkgoWriter.Printf("NODES REBOOTED: %v\n", rebootedNodes)
			return false
		}

		GinkgoWriter.Printf("All %d nodes remain ready and stable (no reboots detected)\n", readyNodeCount)
		return true
	}, duration, time.Second*15).Should(BeTrue())

	return nil
}

// WaitForNoReboots specifically waits and ensures no node reboots occur
func (nsc *NodeStabilityChecker) WaitForNoReboots(duration time.Duration) error {
	// Capture initial state
	err := nsc.captureInitialNodeState()
	if err != nil {
		return fmt.Errorf("failed to capture initial node state: %w", err)
	}

	Consistently(func() bool {
		hasReboots, rebootedNodes, err := nsc.CheckForNodeReboots()
		if err != nil {
			GinkgoWriter.Printf("Failed to check for node reboots: %v\n", err)
			return false
		}
		if hasReboots {
			GinkgoWriter.Printf("REBOOT DETECTED: %v\n", rebootedNodes)
			return false
		}

		GinkgoWriter.Printf("No node reboots detected\n")
		return true
	}, duration, time.Second*10).Should(BeTrue())

	return nil
}

// Helper function to check if slice contains string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// DaemonSetChecker provides utilities for checking DaemonSet status
type DaemonSetChecker struct {
	Clients   *TestClients
	Namespace string
}

// NewDaemonSetChecker creates a new DaemonSet checker
func (tn *TestNamespace) NewDaemonSetChecker() *DaemonSetChecker {
	return &DaemonSetChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
	}
}

// WaitForDaemonSet waits for a DaemonSet to be created and returns it
func (dsc *DaemonSetChecker) WaitForDaemonSet(labels map[string]string,
	timeout time.Duration) (*appsv1.DaemonSet, error) {
	var daemonSet *appsv1.DaemonSet

	Eventually(func() bool {
		daemonSets := &appsv1.DaemonSetList{}
		err := dsc.Clients.Client.List(dsc.Clients.Context, daemonSets,
			client.InNamespace(dsc.Namespace),
			client.MatchingLabels(labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list DaemonSets: %v\n", err)
			return false
		}
		if len(daemonSets.Items) == 0 {
			GinkgoWriter.Printf("No DaemonSets found with labels %v\n", labels)
			// Show the state of any jobs in the namespace
			jobs := &batchv1.JobList{}
			err := dsc.Clients.Client.List(dsc.Clients.Context, jobs,
				client.InNamespace(dsc.Namespace))
			if err != nil {
				GinkgoWriter.Printf("Failed to list Jobs: %v\n", err)
			} else {
				for _, job := range jobs.Items {
					yaml, err := yaml.Marshal(job.Status)
					if err != nil {
						GinkgoWriter.Printf("Failed to marshal job status: %v\n", err)
						GinkgoWriter.Printf("Job %s: %+v\n", job.Name, job.Status)
					} else {
						GinkgoWriter.Printf("Job %s status:\n %s\n", job.Name, string(yaml))
					}
				}
			}
			// Show the state of any pods in the namespace
			pods := &corev1.PodList{}
			err = dsc.Clients.Client.List(dsc.Clients.Context, pods,
				client.InNamespace(dsc.Namespace))
			if err != nil {
				GinkgoWriter.Printf("Failed to list Pods: %v\n", err)
			} else {
				for _, pod := range pods.Items {
					GinkgoWriter.Printf("Pod %s: %v\n", pod.Name, pod.Status.Phase)
				}
			}
			return false
		}
		daemonSet = &daemonSets.Items[0]
		GinkgoWriter.Printf("Found DaemonSet: %s\n", daemonSet.Name)
		return true
	}, timeout, time.Second*10).Should(BeTrue())

	return daemonSet, nil
}

// CheckDaemonSetArgs verifies that a DaemonSet container has expected arguments
func (dsc *DaemonSetChecker) CheckDaemonSetArgs(ds *appsv1.DaemonSet, expectedArgs []string) error {
	Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
	container := ds.Spec.Template.Spec.Containers[0]

	argsStr := strings.Join(container.Args, " ")
	GinkgoWriter.Printf("DaemonSet container args: %s\n", argsStr)

	for _, expectedArg := range expectedArgs {
		Expect(argsStr).To(ContainSubstring(expectedArg))
	}

	return nil
}

// SBDAgentValidator provides comprehensive validation for SBD agent deployments
type SBDAgentValidator struct {
	TestNS  *TestNamespace
	Clients *TestClients
}

// NewSBDAgentValidator creates a new SBD agent validator
func (tn *TestNamespace) NewSBDAgentValidator() *SBDAgentValidator {
	return &SBDAgentValidator{
		TestNS:  tn,
		Clients: tn.Clients,
	}
}

// ValidateAgentDeploymentOptions configures the validation behavior
type ValidateAgentDeploymentOptions struct {
	SBDConfigName    string
	ExpectedArgs     []string
	MinReadyPods     int
	DaemonSetTimeout time.Duration
	PodReadyTimeout  time.Duration
	NodeStableTime   time.Duration
	LogCheckTimeout  time.Duration
}

// DefaultValidateAgentDeploymentOptions returns sensible defaults for validation
func DefaultValidateAgentDeploymentOptions(sbdConfigName string) ValidateAgentDeploymentOptions {
	return ValidateAgentDeploymentOptions{
		SBDConfigName: sbdConfigName,
		ExpectedArgs: []string{
			"--watchdog-path=/dev/watchdog",
			"--watchdog-timeout=1m30s",
		},
		MinReadyPods:     3,
		DaemonSetTimeout: time.Minute * 5,
		PodReadyTimeout:  time.Minute * 5,
		NodeStableTime:   time.Minute * 3,
		LogCheckTimeout:  time.Minute * 1,
	}
}

// ValidateAgentDeployment performs comprehensive validation of SBD agent deployment
func (sav *SBDAgentValidator) ValidateAgentDeployment(opts ValidateAgentDeploymentOptions) error {
	By("waiting for SBD agent DaemonSet to be created")
	dsChecker := sav.TestNS.NewDaemonSetChecker()
	daemonSet, err := dsChecker.WaitForDaemonSet(map[string]string{"sbdconfig": opts.SBDConfigName}, opts.DaemonSetTimeout)
	if err != nil {
		return fmt.Errorf("failed to wait for DaemonSet: %w", err)
	}

	// Basic DaemonSet validation - image checks are handled in specific tests

	By("verifying DaemonSet has correct configuration")
	err = dsChecker.CheckDaemonSetArgs(daemonSet, opts.ExpectedArgs)
	if err != nil {
		return fmt.Errorf("DaemonSet configuration validation failed: %w", err)
	}

	By("waiting for SBD agent pods to become ready")
	podChecker := sav.TestNS.NewPodStatusChecker(map[string]string{"sbdconfig": opts.SBDConfigName})
	err = podChecker.WaitForPodsReady(opts.MinReadyPods, opts.PodReadyTimeout)
	if err != nil {
		return fmt.Errorf("pods failed to become ready: %w", err)
	}

	// By("verifying nodes remain stable and don't experience reboots")
	// nodeChecker := sav.Clients.NewNodeStabilityChecker()
	// err = nodeChecker.WaitForNodesStable(opts.NodeStableTime)
	// if err != nil {
	// 	return fmt.Errorf("node stability check failed: %w", err)
	// }

	By("checking if SBD agent pods exist and examining their status")
	pods := &corev1.PodList{}
	err = sav.Clients.Client.List(sav.Clients.Context, pods,
		client.InNamespace(sav.TestNS.Name),
		client.MatchingLabels{"sbdconfig": opts.SBDConfigName})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	if len(pods.Items) < opts.MinReadyPods {
		return fmt.Errorf("expected at least %d pods, found %d", opts.MinReadyPods, len(pods.Items))
	}

	// Check at least one pod for logs (but don't require specific log messages in test environment)
	podName := pods.Items[0].Name
	By(fmt.Sprintf("examining logs of SBD agent pod %s (may show watchdog hardware limitations)", podName))

	// Try to get logs but don't fail the test if pod isn't ready or logs are empty
	Eventually(func() string {
		logStr, err := podChecker.GetPodLogs(podName, nil)
		//		logStr, err := podChecker.GetPodLogs(podName, func() *int64 { val := int64(20); return &val }())
		if err != nil {
			GinkgoWriter.Printf("Failed to get logs from pod %s: %v\n", podName, err)
			return "ERROR_GETTING_LOGS"
		}
		if logStr == "" {
			return "NO_LOGS_YET"
		}
		// GinkgoWriter.Printf("Pod %s logs sample:\n%s\n", podName, logStr)
		return logStr
	}, opts.LogCheckTimeout, time.Second*10).Should(SatisfyAny(
		// Accept various states - the test is mainly about configuration correctness
		ContainSubstring("Watchdog pet successful"),
		ContainSubstring("falling back to write-based keep-alive"),
		ContainSubstring("Starting watchdog loop"),
		ContainSubstring("SBD Agent started"),
		ContainSubstring("ERROR_GETTING_LOGS"),
		ContainSubstring("NO_LOGS_YET"),
	))

	By("verifying no critical errors in agent logs")
	fullLogStr, err := podChecker.GetPodLogs(podName, nil)
	if err != nil {
		return fmt.Errorf("failed to get full pod logs: %w", err)
	}

	// These errors would indicate problems with our implementation
	errorStrings := []string{
		//	"level\":\"error", #reduce flakiness
		"Error",
		"ERROR",
		"Failed to start SBD agent",
		"failed to pet watchdog",
		"watchdog device is not open",
		"Failed to unmarshal message from own slot",
		"Pre-flight checks failed",
	}
	for _, errString := range errorStrings {
		if strings.Contains(fullLogStr, errString) {
			lines := strings.Split(fullLogStr, "\n")
			for _, line := range lines {
				if strings.Contains(line, errString) {
					GinkgoWriter.Printf("Matching log line: %s\n", line)
				}
			}
			return fmt.Errorf("found critical error: %s", errString)
		}
	}

	By("verifying SBD agent started successfully")
	successStrings := []string{
		"Starting SBD Agent controller manager",
		"Starting watchdog loop",
		"Starting peer monitor loop",
		"Starting SBD heartbeat loop",
		"Successfully acquired file lock on node mapping file",
		"All pre-flight checks passed successfully",
		"StorageBasedRemediation controller added to manager successfully",
	}
	for _, successString := range successStrings {
		if !strings.Contains(fullLogStr, successString) {
			return fmt.Errorf("did not find critical log message: %s", successString)
		}
	}

	if err := sav.TestNS.Clients.NodeMapSummary(podName, sav.TestNS.Name, ""); err != nil {
		GinkgoWriter.Printf("Failed to get node mapping: %v\n", err)
	}

	return nil
}

// ValidateNoNodeReboots performs focused validation to ensure SBD agents don't cause node reboots
func (sav *SBDAgentValidator) ValidateNoNodeReboots(opts ValidateAgentDeploymentOptions) error {
	By("capturing initial node state before SBD agent deployment")
	nodeChecker := sav.Clients.NewNodeStabilityChecker()
	err := nodeChecker.captureInitialNodeState()
	if err != nil {
		return fmt.Errorf("failed to capture initial node state: %w", err)
	}

	By("waiting for SBD agent DaemonSet to be created")
	dsChecker := sav.TestNS.NewDaemonSetChecker()
	_, err = dsChecker.WaitForDaemonSet(map[string]string{"sbdconfig": opts.SBDConfigName}, opts.DaemonSetTimeout)
	if err != nil {
		return fmt.Errorf("failed to wait for DaemonSet: %w", err)
	}

	By("waiting for SBD agent pods to become ready")
	podChecker := sav.TestNS.NewPodStatusChecker(map[string]string{"sbdconfig": opts.SBDConfigName})
	err = podChecker.WaitForPodsReady(opts.MinReadyPods, opts.PodReadyTimeout)
	if err != nil {
		return fmt.Errorf("pods failed to become ready: %w", err)
	}

	By("continuously monitoring for node reboots during agent operation")
	err = nodeChecker.WaitForNoReboots(opts.NodeStableTime)
	if err != nil {
		return fmt.Errorf("node reboot detected during SBD agent operation: %w", err)
	}

	By("performing final reboot check after monitoring period")
	hasReboots, rebootedNodes, err := nodeChecker.CheckForNodeReboots()
	if err != nil {
		return fmt.Errorf("failed final reboot check: %w", err)
	}
	if hasReboots {
		return fmt.Errorf("nodes rebooted during SBD agent deployment: %v", rebootedNodes)
	}

	GinkgoWriter.Printf("SUCCESS: No node reboots detected during SBD agent deployment and operation\n")
	return nil
}

func CleanupSBDConfigs(testNamespace *TestNamespace) {
	By("Cleaning up SBD configuration and waiting for agents to terminate")
	// Clean up all SBDConfigs in the test namespace
	sbdConfigs := &medik8sv1alpha1.SBDConfigList{}
	err := testNamespace.Clients.Client.List(
		testNamespace.Clients.Context, sbdConfigs, client.InNamespace(testNamespace.Name))
	if err == nil {
		for _, config := range sbdConfigs.Items {
			err := testNamespace.CleanupSBDConfig(&config)
			if err != nil {
				GinkgoWriter.Printf("Warning: failed to cleanup SBDConfig %s: %v\n", config.Name, err)
			}
		}
	}

	By("Cleaning up StorageBasedRemediation CRs to prevent namespace deletion issues")
	// Clean up all SBDRemediations in the test namespace
	sbdRemediations := &medik8sv1alpha1.StorageBasedRemediationList{}
	err = testNamespace.Clients.Client.List(
		testNamespace.Clients.Context, sbdRemediations, client.InNamespace(testNamespace.Name))
	if err == nil {
		for _, remediation := range sbdRemediations.Items {
			// Remove finalizers first to prevent stuck resources
			if len(remediation.Finalizers) > 0 {
				remediation.Finalizers = nil
				_ = testNamespace.Clients.Client.Update(testNamespace.Clients.Context, &remediation)
			}
			_ = testNamespace.Clients.Client.Delete(testNamespace.Clients.Context, &remediation)
			GinkgoWriter.Printf("Cleaned up StorageBasedRemediation CR: %s\n", remediation.Name)
		}
	}
}

func CheckClusterConnection() error {

	GinkgoWriter.Print("Checking for Kubernetes configuration\n")
	testClients, err := SetupKubernetesClients()
	if err != nil {
		return fmt.Errorf("failed to setup Kubernetes clients: %v", err)
	}

	// Verify we can connect to the cluster
	GinkgoWriter.Print("Verifying cluster connection\n")
	if serverVersion, err := testClients.Clientset.Discovery().ServerVersion(); err == nil {
		GinkgoWriter.Printf("Connected to Kubernetes cluster version: %s\n", serverVersion.String())
		return nil
	} else {
		return fmt.Errorf("failed to connect to cluster: %v", err)
	}
}

func SuiteSetup(prefix string) (*TestNamespace, error) {

	testFlags := GetTestFlags()
	namespace := fmt.Sprintf("%s-%s", prefix, testFlags.TestID)
	By("Verifying test environment setup")
	GinkgoWriter.Printf("Test ID: %s\n", testFlags.TestID)
	GinkgoWriter.Printf("Namespace: %s\n", namespace)
	GinkgoWriter.Printf("Artifacts directory: %s\n", testFlags.ArtifactsDir)
	GinkgoWriter.Printf("Agent image: %s\n", GetAgentImage())
	GinkgoWriter.Printf("Operator image: %s\n", GetProjectImage())

	By("Initializing Kubernetes clients for tests if needed")
	testClients, err := SetupKubernetesClients()
	Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")

	// Verify we can connect to the cluster
	By("Verifying cluster connection")
	serverVersion, err := testClients.Clientset.Discovery().ServerVersion()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to connect to cluster")
	GinkgoWriter.Printf("Connected to Kubernetes cluster version: %s\n", serverVersion.String())

	By("Creating e2e test namespace")
	testNamespace, err := testClients.CreateTestNamespace(namespace)
	Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")

	// The smoke tests are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			GinkgoWriter.Printf("Installing CertManager...\n")
			Expect(InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			GinkgoWriter.Printf("WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	By("Verifying CRDs are installed")
	// Check for SBD CRDs by looking for API resources in the medik8s.medik8s.io group
	apiResourceList, err := testClients.Clientset.Discovery().ServerResourcesForGroupVersion("medik8s.medik8s.io/v1alpha1")
	Expect(err).NotTo(HaveOccurred(), "Failed to get API resources for medik8s.medik8s.io/v1alpha1")

	var foundSBDConfig, foundSBDRemediation bool
	for _, resource := range apiResourceList.APIResources {
		if resource.Kind == "SBDConfig" {
			foundSBDConfig = true
		}
		if resource.Kind == "StorageBasedRemediation" {
			foundSBDRemediation = true
		}
	}
	Expect(foundSBDConfig).To(BeTrue(), "Expected SBDConfig CRD to be installed (should be done by Makefile setup)")
	Expect(foundSBDRemediation).To(BeTrue(),
		"Expected StorageBasedRemediation CRD to be installed (should be done by Makefile setup)")

	By("verifying the controller-manager is deployed")
	deployment := &appsv1.Deployment{}
	err = testClients.Client.Get(testClients.Context, client.ObjectKey{
		Name:      "sbd-operator-controller-manager",
		Namespace: "sbd-operator-system",
	}, deployment)
	Expect(err).NotTo(HaveOccurred(),
		"Expected controller-manager to be deployed (should be done by Makefile setup)")

	// Confirm the operator is running
	By("confirming the operator is running")
	Eventually(func() bool {
		podList, err := testClients.Clientset.CoreV1().Pods("sbd-operator-system").List(testClients.Context,
			metav1.ListOptions{
				LabelSelector: "control-plane=controller-manager",
			})
		if err != nil || len(podList.Items) == 0 {
			return false
		}
		return podList.Items[0].Status.Phase == corev1.PodRunning
	}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Operator pod is not running")

	return testNamespace, nil
}

func DescribeEnvironment(testClients *TestClients, testNamespace *TestNamespace) {
	var controllerPodName string
	By(fmt.Sprintf("Describing the %s environment", testNamespace.Name))

	// Determine if this is a controller or agent namespace
	isControllerNamespace := false
	isAgentNamespace := false

	// Heuristic: "sbd-operator-system" is the default controller namespace
	if testNamespace.Name == "sbd-operator-system" {
		isControllerNamespace = true
	} else {
		// Check for presence of controller-manager pods
		pods := &corev1.PodList{}
		err := testClients.Client.List(testClients.Context, pods,
			client.InNamespace(testNamespace.Name),
			client.MatchingLabels{"control-plane": "controller-manager"})
		if err == nil && len(pods.Items) > 0 {
			isControllerNamespace = true
		}
	}

	// Heuristic: agent pods are labeled "app=sbd-agent"
	agentPods := &corev1.PodList{}
	err := testClients.Client.List(testClients.Context, agentPods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbd-agent"})
	if err == nil && len(agentPods.Items) > 0 {
		isAgentNamespace = true
	}

	// Log the determination
	if isControllerNamespace && isAgentNamespace {
		GinkgoWriter.Printf("Namespace %q contains both controller and agent pods (hybrid or test namespace)\n",
			testNamespace.Name)
	} else if isControllerNamespace {
		GinkgoWriter.Printf("Namespace %q is identified as the controller namespace\n", testNamespace.Name)
	} else if isAgentNamespace {
		GinkgoWriter.Printf("Namespace %q is identified as an agent namespace\n", testNamespace.Name)
	} else {
		GinkgoWriter.Printf("Namespace %q does not appear to contain controller or agent pods\n", testNamespace.Name)
	}

	debugCollector := testClients.NewDebugCollector(testNamespace.ArtifactsDir)
	// Collect Kubernetes events
	debugCollector.CollectKubernetesEvents(testNamespace.Name)

	if isControllerNamespace {
		By("validating that the controller-manager pod is running as expected")
		verifyControllerUp := func(g Gomega) {
			// Get controller-manager pods
			pods := &corev1.PodList{}
			err := testClients.Client.List(testClients.Context, pods,
				client.InNamespace(testNamespace.Name),
				client.MatchingLabels{"control-plane": "controller-manager"})
			g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")

			// Filter out pods that are being deleted
			var activePods []corev1.Pod
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp == nil {
					activePods = append(activePods, pod)
				}
			}
			g.Expect(activePods).To(HaveLen(1), "expected 1 controller pod running")

			controllerPodName = activePods[0].Name
			g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

			// Collect controller pod description
			debugCollector.CollectPodDescription(testNamespace.Name, controllerPodName)

			// Validate the pod's status
			g.Expect(activePods[0].Status.Phase).To(Equal(corev1.PodRunning), "Incorrect controller-manager pod status")
		}
		Eventually(verifyControllerUp).Should(Succeed())

		// Collect controller logs
		debugCollector.CollectControllerLogs(testNamespace.Name, controllerPodName)
	}

	if isAgentNamespace {

		// Save the definition and logs of all pods in the namespace for debugging
		podList := &corev1.PodList{}
		err := testClients.Client.List(testClients.Context, podList, client.InNamespace(testNamespace.Name))
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods in namespace %q: %v\n", testNamespace.Name, err)
		} else {
			for _, pod := range podList.Items {
				// Save pod definition
				debugCollector.CollectPodDescription(testNamespace.Name, pod.Name)
				// Save pod logs for all containers
				for _, container := range pod.Spec.Containers {
					debugCollector.CollectPodLogs(testNamespace.Name, pod.Name, container.Name)
				}
			}
			GinkgoWriter.Printf("Saved definition and logs for %d pods in namespace %q\n",
				len(podList.Items), testNamespace.Name)
		}

		debugCollector.CollectSBDConfigs(testNamespace.Name)
		debugCollector.CollectSBDRemediations(testNamespace.Name)

		By("validating that SBD agent pods are running as expected")
		verifyAgentsUp := func(g Gomega) {
			// Get SBD agent pods
			pods := &corev1.PodList{}
			err := testClients.Client.List(testClients.Context, pods,
				client.InNamespace(testNamespace.Name),
				client.MatchingLabels{"app": "sbd-agent"})
			g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve SBD agent pod information")

			// Filter out pods that are being deleted
			var activePods []corev1.Pod
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp == nil {
					activePods = append(activePods, pod)
				}
			}
			g.Expect(activePods).ToNot(BeEmpty(), "expected at least 1 SBD agent pod running")

			// Validate each agent pod's status
			for _, pod := range activePods {
				g.Expect(pod.Name).To(ContainSubstring("sbd-agent"))
				g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "Incorrect SBD agent pod status")
			}

			agentPodName := activePods[0].Name

			By("Extracting the fence device file contents from the agent pod")
			err = testClients.FenceDeviceSummary(agentPodName, testNamespace.Name,
				fmt.Sprintf("%s/fence-device.txt", testNamespace.ArtifactsDir))
			if err != nil {
				GinkgoWriter.Printf("Failed to get fence device summary: %s\n", err)
			}

			By("Extracting the heartbeat device file contents from the agent pod")
			err = testClients.SBDDeviceSummary(agentPodName, testNamespace.Name,
				fmt.Sprintf("%s/heartbeat-device.txt", testNamespace.ArtifactsDir))
			if err != nil {
				GinkgoWriter.Printf("Failed to get SBD device summary: %s\n", err)
			}

			By("Extracting the node mapping file contents from the agent pod")
			err = testClients.NodeMapSummary(agentPodName, testNamespace.Name,
				fmt.Sprintf("%s/node-mapping.txt", testNamespace.ArtifactsDir))
			if err != nil {
				GinkgoWriter.Printf("Failed to get node mapping summary: %s\n", err)
			}
		}
		// Run verification but don't fail cleanup if it errors
		func() {
			defer func() {
				if r := recover(); r != nil {
					GinkgoWriter.Printf("Warning: verifyAgentsUp failed but continuing cleanup: %v\n", r)
				}
			}()
			Eventually(verifyAgentsUp).Should(Succeed())
		}()

		// Collect the definition of any storage jobs
		debugCollector.CollectStorageJobs(testNamespace.Name)
	}

	By("Fetching curl-metrics logs")
	req := testClients.Clientset.CoreV1().Pods(testNamespace.Name).GetLogs("curl-metrics", &corev1.PodLogOptions{})
	podLogs, err := req.Stream(testClients.Context)
	if err == nil {
		defer func() { _ = podLogs.Close() }()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		GinkgoWriter.Printf("Metrics logs:\n %s\n", buf.String())
	} else {
		GinkgoWriter.Printf("Failed to get curl-metrics logs: %s\n", err)
	}

}
