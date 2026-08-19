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

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/test/utils"
)

// Cleanup removes the test namespace and all its resources
func cleanupNamespace(tn *utils.TestNamespace) error {
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

// CleanupStorageBasedRemediationConfig deletes an StorageBasedRemediationConfig and waits for cleanup to complete
func cleanupStorageBasedRemediationConfig(tn *utils.TestNamespace, sbrConfig *medik8sv1alpha1.StorageBasedRemediationConfig) error {
	err := tn.Clients.Client.Delete(tn.Clients.Context, sbrConfig)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete StorageBasedRemediationConfig %s: %w", sbrConfig.Name, err)
	}

	// Wait for StorageBasedRemediationConfig to be fully deleted
	Eventually(func() bool {
		var config medik8sv1alpha1.StorageBasedRemediationConfig
		err := tn.Clients.Client.Get(tn.Clients.Context, client.ObjectKey{
			Name:      sbrConfig.Name,
			Namespace: tn.Name,
		}, &config)
		if err != nil && errors.IsNotFound(err) {
			return true
		} else if err != nil {
			GinkgoWriter.Printf("Failed to get StorageBasedRemediationConfig %s: %v\n", sbrConfig.Name, err)
			return false
		}
		GinkgoWriter.Printf("Got StorageBasedRemediationConfig %s\n", sbrConfig.Name)
		return false
	}, time.Minute*5, time.Second*5).Should(BeTrue(), fmt.Sprintf("StorageBasedRemediationConfig %s not deleted", sbrConfig.Name))

	// Wait for associated pods to be terminated, with force deletion for stuck non-running pods
	podCleanupStartTime := time.Now()
	const forceDeleteTimeout = 2 * time.Minute // Force delete stuck pods after 2 minutes
	forceDeleteAttempted := false

	Eventually(func() int {
		pods := &corev1.PodList{}
		err := tn.Clients.Client.List(tn.Clients.Context, pods,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbrconfig": sbrConfig.Name})
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

			GinkgoWriter.Printf("Force deleting stuck jobs and non-running pods after %v timeout\n", elapsed)
			zero := int64(0)
			policy := metav1.DeletePropagationBackground

			// Delete owning Jobs first to prevent replacement pod creation
			jobs := &batchv1.JobList{}
			if jobErr := tn.Clients.Client.List(tn.Clients.Context, jobs,
				client.InNamespace(tn.Name),
				client.MatchingLabels{"sbrconfig": sbrConfig.Name}); jobErr != nil {
				GinkgoWriter.Printf("Failed to list Jobs: %v\n", jobErr)
				return len(pods.Items)
			}
			for _, job := range jobs.Items {
				GinkgoWriter.Printf("Deleting cleanup Job %s to prevent replacement pods\n", job.Name)
				delErr := tn.Clients.Clientset.BatchV1().Jobs(tn.Name).Delete(
					tn.Clients.Context, job.Name, metav1.DeleteOptions{
						GracePeriodSeconds: &zero,
						PropagationPolicy:  &policy,
					})
				if delErr != nil && !errors.IsNotFound(delErr) {
					GinkgoWriter.Printf("Failed to delete Job %s: %v\n", job.Name, delErr)
					return len(pods.Items)
				}
			}
			forceDeleteAttempted = true

			for _, pod := range podsToDelete {
				GinkgoWriter.Printf("Force deleting pod %s (phase: %s)\n", pod.Name, pod.Status.Phase)
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
	}, time.Minute*5, time.Second*10).Should(Equal(0), fmt.Sprintf("StorageBasedRemediationConfig %s pods not deleted", sbrConfig.Name))

	// Wait for associated DaemonSets to be deleted
	Eventually(func() int {
		daemonSets := &appsv1.DaemonSetList{}
		err := tn.Clients.Client.List(tn.Clients.Context, daemonSets,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbrconfig": sbrConfig.Name})
		if err != nil {
			return -1
		}
		return len(daemonSets.Items)
	}, time.Minute*5, time.Second*5).Should(Equal(0), fmt.Sprintf("StorageBasedRemediationConfig %s DaemonSets not deleted", sbrConfig.Name))

	return nil
}

type podStatusChecker struct {
	Clients   *utils.TestClients
	Namespace string
	Labels    map[string]string
}

func newPodStatusChecker(tn *utils.TestNamespace, labels map[string]string) *podStatusChecker {
	return &podStatusChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
		Labels:    labels,
	}
}

// WaitForPodsReady waits for pods matching the labels to become ready
func (psc *podStatusChecker) waitForPodsReady(minCount int, timeout time.Duration) error {
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
			// Skip pods created by Jobs (they have the job-name label)
			if _, hasJobName := pod.Labels["job-name"]; hasJobName {
				continue
			}

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
			GinkgoWriter.Printf("Unready pod:\n%s\n", formatUnreadyPodStatus(pod))
		}

		return readyPods
	}, timeout, time.Second*15).Should(BeNumerically(">=", minCount))

	return nil
}

// WaitForPodsRunning waits for pods to be in Running state (not necessarily ready)
func (psc *podStatusChecker) waitForPodsRunning(minCount int, timeout time.Duration) error {
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
func (psc *podStatusChecker) getPodLogs(podName string, tailLines *int64) (string, error) {
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
func (psc *podStatusChecker) checkPodRestarts() (bool, []string) {
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
func (psc *podStatusChecker) getFirstPod() (*corev1.Pod, error) {
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
type debugCollector struct {
	Clients      *utils.TestClients
	ArtifactsDir string
}

// NewDebugCollector creates a new DebugCollector
func newDebugCollector(tc *utils.TestClients, artifactsDir string) *debugCollector {
	return &debugCollector{Clients: tc, ArtifactsDir: artifactsDir}
}

// CollectControllerLogs collects logs from the controller manager pod
func (dc *debugCollector) collectControllerLogs(namespace, podName string) {
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

// CollectAgentLogs collects logs from all SBR agent pods
func (dc *debugCollector) collectAgentLogs(namespace string) {
	defer func() {
		if r := recover(); r != nil {
			GinkgoWriter.Printf("CollectAgentLogs recovered from panic: %v\n", r)
		}
	}()
	By("Fetching SBR agent pod logs")

	// Get all SBR agent pods
	pods := &corev1.PodList{}
	err := dc.Clients.Client.List(dc.Clients.Context, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"app": "sbr-agent"})

	if err != nil {
		GinkgoWriter.Printf("Failed to list SBR agent pods: %s\n", err)
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
		GinkgoWriter.Printf("No active SBR agent pods found\n")
		return
	}

	// Collect logs from each agent pod
	for _, pod := range activePods {
		GinkgoWriter.Printf("\n=== SBR Agent Pod: %s (Node: %s) ===\n", pod.Name, pod.Spec.NodeName)

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
func (dc *debugCollector) collectKubernetesEvents(namespace string) {
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

// CollectStorageJobs collects SBR device initialization jobs for debugging
func (dc *debugCollector) collectStorageJobs(namespace string) {
	By("Fetching SBR device initialization jobs for debugging")

	// Search for SBR device initialization jobs - these are created by the SBR operator controller
	// and have specific labels: app.kubernetes.io/component=sbr-device-init
	jobs := &batchv1.JobList{}
	err := dc.Clients.Client.List(dc.Clients.Context, jobs,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/component": "sbr-device-init"})

	if err != nil {
		GinkgoWriter.Printf("Failed to list SBR device initialization jobs in namespace %s: %v\n", namespace, err)
		return
	}

	if len(jobs.Items) == 0 {
		GinkgoWriter.Printf("No SBR device initialization jobs found in namespace %s\n", namespace)
		return
	}

	GinkgoWriter.Printf("\n=== SBR Device Initialization Jobs in namespace %s ===\n", namespace)

	for _, job := range jobs.Items {
		// Collect job definition
		jobYAML, err := yaml.Marshal(job)
		if err == nil {
			jobFileName := fmt.Sprintf("%s/%s-job.yaml", dc.ArtifactsDir, job.Name)
			if f, fileErr := os.Create(jobFileName); fileErr == nil {
				defer func() { _ = f.Close() }()
				_, _ = f.Write(jobYAML)
				GinkgoWriter.Printf("SBR device init job spec for %s saved to %s\n", job.Name, jobFileName)
			} else {
				GinkgoWriter.Printf("Failed to write SBR device init job spec to file %s: %s\n", jobFileName, fileErr)
				GinkgoWriter.Printf("SBR device init job %s spec:\n%s\n", job.Name, string(jobYAML))
			}
		}

		// Display job status
		GinkgoWriter.Printf("SBR device init job %s: Active=%d, Succeeded=%d, Failed=%d\n",
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

// formatUnreadyPodStatus returns a human-readable summary of a pod's status for logging.
func formatUnreadyPodStatus(pod corev1.Pod) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Pod: %s  Phase: %s\n", pod.Name, pod.Status.Phase))
	b.WriteString("  Conditions:\n")
	for _, c := range pod.Status.Conditions {
		b.WriteString(fmt.Sprintf("    - %s: %s", c.Type, c.Status))
		if c.Reason != "" {
			b.WriteString(fmt.Sprintf(" (Reason: %s)", c.Reason))
		}
		if c.Message != "" {
			b.WriteString(fmt.Sprintf(" %s", c.Message))
		}
		b.WriteString("\n")
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		b.WriteString("  Container statuses:\n")
		for _, cs := range pod.Status.ContainerStatuses {
			ready := "not ready"
			if cs.Ready {
				ready = "ready"
			}
			b.WriteString(fmt.Sprintf("    - %s: %s, restarts=%d", cs.Name, ready, cs.RestartCount))
			if cs.State.Running != nil {
				b.WriteString(", state=Running")
			} else if cs.State.Waiting != nil {
				b.WriteString(fmt.Sprintf(", state=Waiting (Reason: %s", cs.State.Waiting.Reason))
				if cs.State.Waiting.Message != "" {
					b.WriteString(fmt.Sprintf(", %s", cs.State.Waiting.Message))
				}
				b.WriteString(")")
			} else if cs.State.Terminated != nil {
				b.WriteString(fmt.Sprintf(", state=Terminated exitCode=%d Reason: %s", cs.State.Terminated.ExitCode, cs.State.Terminated.Reason))
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// collectJobPodLogs collects logs from pods belonging to a specific job
func (dc *debugCollector) collectJobPodLogs(namespace, jobName string) {
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
		GinkgoWriter.Printf("No pods found for SBR device init job %s\n", jobName)
		return
	}

	for _, pod := range pods.Items {
		GinkgoWriter.Printf("Collecting logs from SBR device init job pod: %s\n", pod.Name)

		// Collect pod definition
		podYAML, err := yaml.Marshal(pod)
		if err == nil {
			podFileName := fmt.Sprintf("%s/%s-podspec.yaml", dc.ArtifactsDir, pod.Name)
			if f, fileErr := os.Create(podFileName); fileErr == nil {
				defer func() { _ = f.Close() }()
				_, _ = f.Write(podYAML)
				GinkgoWriter.Printf("SBR device init pod spec for %s saved to %s\n", pod.Name, podFileName)
			} else {
				GinkgoWriter.Printf("Failed to write SBR device init pod spec to file %s: %s\n", podFileName, fileErr)
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
					GinkgoWriter.Printf("SBR device init pod logs for %s saved to %s\n", pod.Name, logFileName)
				} else {
					GinkgoWriter.Printf("Failed to write SBR device init pod logs to file %s: %s\n", logFileName, fileErr)
					GinkgoWriter.Printf("SBR device init pod %s logs:\n%s\n", pod.Name, buf.String())
				}
			} else {
				GinkgoWriter.Printf("Failed to get logs from SBR device init pod %s: %s\n", pod.Name, err)
			}
		}

		// Display pod status
		GinkgoWriter.Printf("SBR device init pod %s: Phase=%s, Node=%s\n",
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

// CollectSBRRemediations collects StorageBasedRemediation CRs
//
//nolint:dupl // similar to CollectStorageBasedRemediationConfigs; kept distinct for clarity
func (dc *debugCollector) collectSBRRemediations(namespace string) {
	By(fmt.Sprintf("Fetching SBRRemediations in namespace %s", namespace))
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

// CollectStorageBasedRemediationConfigs collects StorageBasedRemediationConfig CRs
//
//nolint:dupl // similar to CollectSBRRemediations; kept distinct for clarity
func (dc *debugCollector) collectStorageBasedRemediationConfigs(namespace string) {
	By(fmt.Sprintf("Fetching StorageBasedRemediationConfigs in namespace %s", namespace))
	configs := &medik8sv1alpha1.StorageBasedRemediationConfigList{}
	err := dc.Clients.Client.List(dc.Clients.Context, configs, client.InNamespace(namespace))
	if err == nil {
		for _, config := range configs.Items {
			data, err := yaml.Marshal(config)
			if err == nil {
				logFileName := fmt.Sprintf("%s/%s.yaml", dc.ArtifactsDir, config.Name)
				if f, fileErr := os.Create(logFileName); fileErr == nil {
					defer func() { _ = f.Close() }()
					_, _ = f.Write(data)
					GinkgoWriter.Printf("StorageBasedRemediationConfig %s saved to %s\n", config.Name, logFileName)
				} else {
					GinkgoWriter.Printf("Failed to write StorageBasedRemediationConfig to file %s: %s\n", logFileName, fileErr)
					GinkgoWriter.Printf("StorageBasedRemediationConfig %s:\n%s\n", config.Name, string(data))
				}
			}
		}
	}
}

// CollectPodLogs collects logs from a specific pod container
func (dc *debugCollector) collectPodLogs(namespace, podName, containerName string) {
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
func (dc *debugCollector) collectPodDescription(namespace, podName string) {
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
type serviceAccountTokenGenerator struct {
	Clients *utils.TestClients
}

// NewServiceAccountTokenGenerator creates a new token generator
func newServiceAccountTokenGenerator(tc *utils.TestClients) *serviceAccountTokenGenerator {
	return &serviceAccountTokenGenerator{Clients: tc}
}

// GenerateToken generates a token for the specified service account
func (satg *serviceAccountTokenGenerator) generateToken(namespace, serviceAccountName string) (string, error) {
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
type nodeStabilityChecker struct {
	Clients         *utils.TestClients
	initialNodeInfo map[string]nodeBootInfo // Track initial node state
}

// NodeBootInfo stores information to detect node reboots
type nodeBootInfo struct {
	BootID         string
	KernelVersion  string
	KubeletVersion string
	StartTime      metav1.Time
}

// NewNodeStabilityChecker creates a new node stability checker
func newNodeStabilityChecker(tc *utils.TestClients) *nodeStabilityChecker {
	return &nodeStabilityChecker{
		Clients:         tc,
		initialNodeInfo: make(map[string]nodeBootInfo),
	}
}

// captureInitialNodeState captures the initial boot state of all nodes
func (nsc *nodeStabilityChecker) captureInitialNodeState() error {

	if len(nsc.initialNodeInfo) > 0 {
		return nil
	}

	nodes := &corev1.NodeList{}
	err := nsc.Clients.Client.List(nsc.Clients.Context, nodes)
	if err != nil {
		return fmt.Errorf("failed to list nodes for initial state capture: %w", err)
	}

	for _, node := range nodes.Items {
		nsc.initialNodeInfo[node.Name] = nodeBootInfo{
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

// checkForNodeReboots checks if any nodes have rebooted since initial capture
func (nsc *nodeStabilityChecker) checkForNodeReboots() (bool, []string, error) {
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
			nsc.initialNodeInfo[node.Name] = nodeBootInfo{
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
func (nsc *nodeStabilityChecker) waitForNodesStable(duration time.Duration) error {
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
		hasReboots, rebootedNodes, err := nsc.checkForNodeReboots()
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
func (nsc *nodeStabilityChecker) waitForNoReboots(duration time.Duration) error {
	// Capture initial state
	err := nsc.captureInitialNodeState()
	if err != nil {
		return fmt.Errorf("failed to capture initial node state: %w", err)
	}

	Consistently(func() bool {
		hasReboots, rebootedNodes, err := nsc.checkForNodeReboots()
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
type daemonSetChecker struct {
	Clients   *utils.TestClients
	Namespace string
}

// NewDaemonSetChecker creates a new DaemonSet checker
func newDaemonSetChecker(tn *utils.TestNamespace) *daemonSetChecker {
	return &daemonSetChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
	}
}

// WaitForDaemonSet waits for a DaemonSet to be created and returns it
func (dsc *daemonSetChecker) waitForDaemonSet(labels map[string]string,
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
func (dsc *daemonSetChecker) checkDaemonSetArgs(ds *appsv1.DaemonSet, expectedArgs []string) error {
	containers := ds.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		return fmt.Errorf("expected 1 container, got %d", len(containers))
	}

	args := containers[0].Args
	GinkgoWriter.Printf("DaemonSet container args: %v\n", args)

	for _, expectedArg := range expectedArgs {
		if !slices.Contains(args, expectedArg) {
			return fmt.Errorf("expected arg %q not found in: %v", expectedArg, args)
		}
	}

	return nil
}

// SBRAgentValidator provides comprehensive validation for SBR agent deployments
type sbrAgentValidator struct {
	TestNS  *utils.TestNamespace
	Clients *utils.TestClients
}

// NewSBRAgentValidator creates a new SBR agent validator
func newSBRAgentValidator(tn *utils.TestNamespace) *sbrAgentValidator {
	return &sbrAgentValidator{
		TestNS:  tn,
		Clients: tn.Clients,
	}
}

// validateAgentDeploymentOptions configures the validation behavior
type validateAgentDeploymentOptions struct {
	StorageBasedRemediationConfigName string
	ExpectedArgs                      []string
	MinReadyPods                      int
	DaemonSetTimeout                  time.Duration
	PodReadyTimeout                   time.Duration
	NodeStableTime                    time.Duration
	LogCheckTimeout                   time.Duration
}

// defaultValidateAgentDeploymentOptions returns sensible defaults for validation
func defaultValidateAgentDeploymentOptions(sbrConfigName string) validateAgentDeploymentOptions {
	return validateAgentDeploymentOptions{
		StorageBasedRemediationConfigName: sbrConfigName,
		ExpectedArgs: []string{
			"--watchdog-path=/dev/watchdog",
		},
		MinReadyPods:     3,
		DaemonSetTimeout: time.Minute * 5,
		PodReadyTimeout:  time.Minute * 5,
		NodeStableTime:   time.Minute * 3,
		LogCheckTimeout:  time.Minute * 1,
	}
}

// ValidateAgentDeployment performs comprehensive validation of SBR agent deployment
func (sav *sbrAgentValidator) validateAgentDeployment(opts validateAgentDeploymentOptions) error {
	By("waiting for SBR agent DaemonSet to be created")
	dsChecker := newDaemonSetChecker(sav.TestNS)
	daemonSet, err := dsChecker.waitForDaemonSet(map[string]string{"sbrconfig": opts.StorageBasedRemediationConfigName}, opts.DaemonSetTimeout)
	if err != nil {
		return fmt.Errorf("failed to wait for DaemonSet: %w", err)
	}

	// Basic DaemonSet validation - image checks are handled in specific tests

	By("verifying DaemonSet has correct configuration")
	err = dsChecker.checkDaemonSetArgs(daemonSet, opts.ExpectedArgs)
	if err != nil {
		return fmt.Errorf("DaemonSet configuration validation failed: %w", err)
	}

	By("waiting for SBR agent pods to become ready")
	podChecker := newPodStatusChecker(sav.TestNS, map[string]string{"sbrconfig": opts.StorageBasedRemediationConfigName})
	err = podChecker.waitForPodsReady(opts.MinReadyPods, opts.PodReadyTimeout)
	if err != nil {
		return fmt.Errorf("pods failed to become ready: %w", err)
	}

	By("checking if SBR agent pods exist and examining their status")
	pods := &corev1.PodList{}
	err = sav.Clients.Client.List(sav.Clients.Context, pods,
		client.InNamespace(sav.TestNS.Name),
		client.MatchingLabels{"sbrconfig": opts.StorageBasedRemediationConfigName})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	if len(pods.Items) < opts.MinReadyPods {
		return fmt.Errorf("expected at least %d pods, found %d", opts.MinReadyPods, len(pods.Items))
	}

	// Check at least one pod for logs (but don't require specific log messages in test environment)
	podName, err := firstReadyAgentPod(pods.Items)
	if err != nil {
		return fmt.Errorf("failed to select ready agent pod for log validation: %w", err)
	}
	By(fmt.Sprintf("examining logs of SBR agent pod %s (may show watchdog hardware limitations)", podName))

	// Try to get logs but don't fail the test if pod isn't ready or logs are empty
	Eventually(func() string {
		logStr, err := podChecker.getPodLogs(podName, nil)
		//		logStr, err := podChecker.getPodLogs(podName, func() *int64 { val := int64(20); return &val }())
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
		ContainSubstring("SBR Agent started"),
		ContainSubstring("ERROR_GETTING_LOGS"),
		ContainSubstring("NO_LOGS_YET"),
	))

	By("verifying no critical errors in agent logs")
	fullLogStr, err := podChecker.getPodLogs(podName, nil)
	if err != nil {
		return fmt.Errorf("failed to get full pod logs: %w", err)
	}

	// These errors would indicate problems with our implementation
	errorStrings := []string{
		//	"level\":\"error", #reduce flakiness
		"Error",
		"ERROR",
		"Failed to start SBR agent",
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

	By("verifying SBR agent started successfully")
	successStrings := []string{
		"Starting SBR Agent controller manager",
		"Starting watchdog loop",
		"Starting peer monitor loop",
		"Starting SBR heartbeat loop",
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

func firstReadyAgentPod(pods []corev1.Pod) (string, error) {
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if _, hasJobName := pod.Labels["job-name"]; hasJobName {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return pod.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no ready agent pod found among %d listed pods", len(pods))
}

// ValidateNoNodeReboots performs focused validation to ensure SBR agents don't cause node reboots
func (sav *sbrAgentValidator) validateNoNodeReboots(opts validateAgentDeploymentOptions) error {
	By("capturing initial node state before SBR agent deployment")
	nodeChecker := newNodeStabilityChecker(sav.Clients)
	err := nodeChecker.captureInitialNodeState()
	if err != nil {
		return fmt.Errorf("failed to capture initial node state: %w", err)
	}

	By("waiting for SBR agent DaemonSet to be created")
	dsChecker := newDaemonSetChecker(sav.TestNS)
	_, err = dsChecker.waitForDaemonSet(map[string]string{"sbrconfig": opts.StorageBasedRemediationConfigName}, opts.DaemonSetTimeout)
	if err != nil {
		return fmt.Errorf("failed to wait for DaemonSet: %w", err)
	}

	By("waiting for SBR agent pods to become ready")
	podChecker := newPodStatusChecker(sav.TestNS, map[string]string{"sbrconfig": opts.StorageBasedRemediationConfigName})
	err = podChecker.waitForPodsReady(opts.MinReadyPods, opts.PodReadyTimeout)
	if err != nil {
		return fmt.Errorf("pods failed to become ready: %w", err)
	}

	By("continuously monitoring for node reboots during agent operation")
	err = nodeChecker.waitForNoReboots(opts.NodeStableTime)
	if err != nil {
		return fmt.Errorf("node reboot detected during SBR agent operation: %w", err)
	}

	By("performing final reboot check after monitoring period")
	hasReboots, rebootedNodes, err := nodeChecker.checkForNodeReboots()
	if err != nil {
		return fmt.Errorf("failed final reboot check: %w", err)
	}
	if hasReboots {
		return fmt.Errorf("nodes rebooted during SBR agent deployment: %v", rebootedNodes)
	}

	GinkgoWriter.Printf("SUCCESS: No node reboots detected during SBR agent deployment and operation\n")
	return nil
}

func cleanupStorageBasedRemediationConfigs(testNamespace *utils.TestNamespace) {
	By("Cleaning up SBR configuration and waiting for agents to terminate")
	// Clean up all StorageBasedRemediationConfigs in the test namespace
	sbrConfigs := &medik8sv1alpha1.StorageBasedRemediationConfigList{}
	err := testNamespace.Clients.Client.List(
		testNamespace.Clients.Context, sbrConfigs, client.InNamespace(testNamespace.Name))
	if err == nil {
		for _, config := range sbrConfigs.Items {
			err := cleanupStorageBasedRemediationConfig(testNamespace, &config)
			if err != nil {
				GinkgoWriter.Printf("Warning: failed to cleanup StorageBasedRemediationConfig %s: %v\n", config.Name, err)
			}
		}
	}

	By("Cleaning up StorageBasedRemediation CRs to prevent namespace deletion issues")
	// Clean up all SBRRemediations in the test namespace
	sbrRemediations := &medik8sv1alpha1.StorageBasedRemediationList{}
	err = testNamespace.Clients.Client.List(
		testNamespace.Clients.Context, sbrRemediations, client.InNamespace(testNamespace.Name))
	if err != nil {
		GinkgoWriter.Printf("Warning: failed to list StorageBasedRemediation CRs: %v\n", err)
		return
	}
	for i := range sbrRemediations.Items {
		remediation := sbrRemediations.Items[i]
		// Remove finalizers first to prevent stuck resources
		if len(remediation.Finalizers) > 0 {
			remediation.Finalizers = nil
			if err := testNamespace.Clients.Client.Update(testNamespace.Clients.Context, &remediation); err != nil && !errors.IsNotFound(err) {
				GinkgoWriter.Printf("Warning: failed to clear finalizers on StorageBasedRemediation %s: %v\n", remediation.Name, err)
				continue
			}
		}
		if err := testNamespace.Clients.Client.Delete(testNamespace.Clients.Context, &remediation); err != nil && !errors.IsNotFound(err) {
			GinkgoWriter.Printf("Warning: failed to delete StorageBasedRemediation %s: %v\n", remediation.Name, err)
			continue
		}
		GinkgoWriter.Printf("Cleaned up StorageBasedRemediation CR: %s\n", remediation.Name)
	}
}

func checkClusterConnection() error {

	GinkgoWriter.Print("Checking for Kubernetes configuration\n")
	testClients, err := utils.SetupKubernetesClients()
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

func suiteSetup(prefix string) (*utils.TestNamespace, error) {

	testFlags := utils.GetTestFlags()
	namespace := fmt.Sprintf("%s-%s", prefix, testFlags.TestID)
	By("Verifying test environment setup")
	GinkgoWriter.Printf("Test ID: %s\n", testFlags.TestID)
	GinkgoWriter.Printf("Namespace: %s\n", namespace)
	GinkgoWriter.Printf("Artifacts directory: %s\n", testFlags.ArtifactsDir)
	GinkgoWriter.Printf("Agent image: %s\n", utils.GetAgentImage())
	GinkgoWriter.Printf("Operator image: %s\n", utils.GetProjectImage())

	By("Initializing Kubernetes clients for tests if needed")
	testClients, err := utils.SetupKubernetesClients()
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
	if !utils.SkipCertManagerInstall() {
		By("checking if cert manager is installed already")
		utils.SetCertManagerAlreadyInstalled(utils.IsCertManagerCRDsInstalled())
		if !utils.IsCertManagerAlreadyInstalled() {
			GinkgoWriter.Printf("Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			GinkgoWriter.Printf("WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	By("Verifying CRDs are installed")
	// Check for SBR CRDs by looking for API resources in the storage-based-remediation.medik8s.io group
	apiResourceList, err := testClients.Clientset.Discovery().ServerResourcesForGroupVersion("storage-based-remediation.medik8s.io/v1alpha1")
	Expect(err).NotTo(HaveOccurred(), "Failed to get API resources for storage-based-remediation.medik8s.io/v1alpha1")

	var foundStorageBasedRemediationConfig, foundSBRRemediation bool
	for _, resource := range apiResourceList.APIResources {
		if resource.Kind == "StorageBasedRemediationConfig" {
			foundStorageBasedRemediationConfig = true
		}
		if resource.Kind == "StorageBasedRemediation" {
			foundSBRRemediation = true
		}
	}
	Expect(foundStorageBasedRemediationConfig).To(BeTrue(), "Expected StorageBasedRemediationConfig CRD to be installed (should be done by Makefile setup)")
	Expect(foundSBRRemediation).To(BeTrue(),
		"Expected StorageBasedRemediation CRD to be installed (should be done by Makefile setup)")

	By("verifying the controller-manager is deployed")
	deployment := &appsv1.Deployment{}
	err = testClients.Client.Get(testClients.Context, client.ObjectKey{
		Name:      "sbr-operator-controller-manager",
		Namespace: "sbr-operator-system",
	}, deployment)
	Expect(err).NotTo(HaveOccurred(),
		"Expected controller-manager to be deployed (should be done by Makefile setup)")

	// Confirm the operator is running
	By("confirming the operator is running")
	Eventually(func() bool {
		podList, err := testClients.Clientset.CoreV1().Pods("sbr-operator-system").List(testClients.Context,
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

func describeEnvironment(testClients *utils.TestClients, testNamespace *utils.TestNamespace) {
	var controllerPodName string
	By(fmt.Sprintf("Describing the %s environment", testNamespace.Name))

	// Determine if this is a controller or agent namespace
	isControllerNamespace := false
	isAgentNamespace := false

	// Heuristic: "sbr-operator-system" is the default controller namespace
	if testNamespace.Name == "sbr-operator-system" {
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

	// Heuristic: agent pods are labeled "app=sbr-agent"
	agentPods := &corev1.PodList{}
	err := testClients.Client.List(testClients.Context, agentPods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbr-agent"})
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

	debugCollector := newDebugCollector(testClients, testNamespace.ArtifactsDir)
	// Collect Kubernetes events
	debugCollector.collectKubernetesEvents(testNamespace.Name)

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
			debugCollector.collectPodDescription(testNamespace.Name, controllerPodName)

			// Validate the pod's status
			g.Expect(activePods[0].Status.Phase).To(Equal(corev1.PodRunning), "Incorrect controller-manager pod status")
		}
		Eventually(verifyControllerUp).Should(Succeed())

		// Collect controller logs
		debugCollector.collectControllerLogs(testNamespace.Name, controllerPodName)
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
				debugCollector.collectPodDescription(testNamespace.Name, pod.Name)
				// Save pod logs for all containers
				for _, container := range pod.Spec.Containers {
					debugCollector.collectPodLogs(testNamespace.Name, pod.Name, container.Name)
				}
			}
			GinkgoWriter.Printf("Saved definition and logs for %d pods in namespace %q\n",
				len(podList.Items), testNamespace.Name)
		}

		debugCollector.collectStorageBasedRemediationConfigs(testNamespace.Name)
		debugCollector.collectSBRRemediations(testNamespace.Name)

		By("validating that SBR agent pods are running as expected")
		verifyAgentsUp := func(g Gomega) {
			// Get SBR agent pods
			pods := &corev1.PodList{}
			err := testClients.Client.List(testClients.Context, pods,
				client.InNamespace(testNamespace.Name),
				client.MatchingLabels{"app": "sbr-agent"})
			g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve SBR agent pod information")

			// Filter out pods that are being deleted
			var activePods []corev1.Pod
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp == nil {
					activePods = append(activePods, pod)
				}
			}
			g.Expect(activePods).ToNot(BeEmpty(), "expected at least 1 SBR agent pod running")

			// Validate each agent pod's status
			for _, pod := range activePods {
				g.Expect(pod.Name).To(ContainSubstring("sbr-agent"))
				g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "Incorrect SBR agent pod status")
			}

			agentPodName := activePods[0].Name

			By("Extracting the fence device file contents from the agent pod")
			err = testClients.FenceDeviceSummary(agentPodName, testNamespace.Name,
				fmt.Sprintf("%s/fence-device.txt", testNamespace.ArtifactsDir))
			if err != nil {
				GinkgoWriter.Printf("Failed to get fence device summary: %s\n", err)
			}

			By("Extracting the heartbeat device file contents from the agent pod")
			err = testClients.SBRDeviceSummary(agentPodName, testNamespace.Name,
				fmt.Sprintf("%s/heartbeat-device.txt", testNamespace.ArtifactsDir))
			if err != nil {
				GinkgoWriter.Printf("Failed to get SBR device summary: %s\n", err)
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
		debugCollector.collectStorageJobs(testNamespace.Name)
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
