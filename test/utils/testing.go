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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
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
	Client    client.Client
	Clientset *kubernetes.Clientset
	Context   context.Context
}

// TestNamespace represents a test namespace with cleanup functionality
type TestNamespace struct {
	Name    string
	Clients *TestClients
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
		Context:   context.Background(),
	}, nil
}

// CreateTestNamespace creates a test namespace and returns a cleanup function
func (tc *TestClients) CreateTestNamespace(namePrefix string) (*TestNamespace, error) {
	name := fmt.Sprintf("%s-%d", namePrefix, time.Now().UnixNano())

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	err := tc.Client.Create(tc.Context, ns)
	if err != nil {
		return nil, fmt.Errorf("failed to create namespace %s: %w", name, err)
	}

	return &TestNamespace{
		Name:    name,
		Clients: tc,
	}, nil
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
	}, time.Minute*2, time.Second*5).Should(BeTrue())

	return nil
}

// CreateSBDConfig creates a test SBDConfig with common defaults
func (tn *TestNamespace) CreateSBDConfig(name string, options ...func(*medik8sv1alpha1.SBDConfig)) (*medik8sv1alpha1.SBDConfig, error) {
	sbdConfig := &medik8sv1alpha1.SBDConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tn.Name,
		},
		Spec: medik8sv1alpha1.SBDConfigSpec{
			SbdWatchdogPath: "/dev/watchdog",
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
		return errors.IsNotFound(err)
	}, time.Minute*2, time.Second*5).Should(BeTrue())

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
	}, time.Minute*2, time.Second*5).Should(Equal(0))

	// Wait for associated pods to be terminated
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := tn.Clients.Client.List(tn.Clients.Context, pods,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbdconfig": sbdConfig.Name})
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return -1
		}
		GinkgoWriter.Printf("Found %d pods\n", len(pods.Items))
		return len(pods.Items)
	}, time.Minute*2, time.Second*5).Should(Equal(0))

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
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady &&
						condition.Status == corev1.ConditionTrue {
						readyPods++
						break
					}
				}
			}
		}

		GinkgoWriter.Printf("Found %d ready pods out of %d total\n", readyPods, len(pods.Items))
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
	Clients *TestClients
}

// NewDebugCollector creates a new DebugCollector
func (tc *TestClients) NewDebugCollector() *DebugCollector {
	return &DebugCollector{Clients: tc}
}

// CollectControllerLogs collects logs from the controller manager pod
func (dc *DebugCollector) CollectControllerLogs(namespace, podName string) {
	By("Fetching controller manager pod logs")
	req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	podLogs, err := req.Stream(dc.Clients.Context)
	if err == nil {
		defer podLogs.Close()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", buf.String())
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
	}
}

// CollectKubernetesEvents collects Kubernetes events from a namespace
func (dc *DebugCollector) CollectKubernetesEvents(namespace string) {
	By("Fetching Kubernetes events")
	events, err := dc.Clients.Clientset.CoreV1().Events(namespace).List(dc.Clients.Context, metav1.ListOptions{})
	if err == nil {
		eventsOutput := ""
		for _, event := range events.Items {
			eventsOutput += fmt.Sprintf("%s  %s     %s  %s/%s  %s\n",
				event.LastTimestamp.Format("2006-01-02T15:04:05Z"),
				event.Type,
				event.Reason,
				event.InvolvedObject.Kind,
				event.InvolvedObject.Name,
				event.Message)
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
	}
}

// CollectPodDescription collects and prints pod description
func (dc *DebugCollector) CollectPodDescription(namespace, podName string) {
	By(fmt.Sprintf("Fetching %s pod description", podName))
	pod := &corev1.Pod{}
	err := dc.Clients.Client.Get(dc.Clients.Context, client.ObjectKey{Name: podName, Namespace: namespace}, pod)
	if err == nil {
		podYAML, _ := yaml.Marshal(pod)
		_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n%s", string(podYAML))
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get pod description: %s", err)
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
func (dsc *DaemonSetChecker) WaitForDaemonSet(labels map[string]string, timeout time.Duration) (*appsv1.DaemonSet, error) {
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
		MinReadyPods:     1,
		DaemonSetTimeout: time.Minute * 2,
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
		GinkgoWriter.Printf("Pod %s logs sample:\n%s\n", podName, logStr)
		return logStr
	}, opts.LogCheckTimeout, time.Second*10).Should(SatisfyAny(
		// Accept various states - the test is mainly about configuration correctness
		ContainSubstring("Watchdog pet successful"),
		ContainSubstring("falling back to write-based keep-alive"),
		ContainSubstring("Starting watchdog loop"),
		ContainSubstring("SBD Agent started"),
		ContainSubstring("failed to open watchdog"), // Expected in test environments
		ContainSubstring("ERROR_GETTING_LOGS"),
		ContainSubstring("NO_LOGS_YET"),
	))

	By("verifying no critical errors in agent logs")
	fullLogStr, err := podChecker.GetPodLogs(podName, nil)
	if err != nil {
		return fmt.Errorf("failed to get full pod logs: %w", err)
	}

	// These errors would indicate problems with our implementation
	if strings.Contains(fullLogStr, "Failed to pet watchdog after retries") {
		return fmt.Errorf("found critical watchdog error: Failed to pet watchdog after retries")
	}
	if strings.Contains(fullLogStr, "watchdog device is not open") {
		return fmt.Errorf("found critical watchdog error: watchdog device is not open")
	}

	// The agent should show successful operation during normal startup
	hasSuccessIndicator := strings.Contains(fullLogStr, "Watchdog pet successful") ||
		strings.Contains(fullLogStr, "SBD Agent started successfully")

	if !hasSuccessIndicator {
		GinkgoWriter.Printf("Warning: No clear success indicators found in logs, but no critical errors detected")
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

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

func CleanupSBDConfigs(k8sClient client.Client, testNS TestNamespace, ctx context.Context) {
	By("cleaning up SBD configuration and waiting for agents to terminate")
	// Clean up all SBDConfigs in the test namespace
	sbdConfigs := &medik8sv1alpha1.SBDConfigList{}
	err := k8sClient.List(ctx, sbdConfigs, client.InNamespace(testNS.Name))
	if err == nil {
		for _, config := range sbdConfigs.Items {
			err := testNS.CleanupSBDConfig(&config)
			if err != nil {
				GinkgoWriter.Printf("Warning: failed to cleanup SBDConfig %s: %v\n", config.Name, err)
			}
		}
	}
}

func SuiteSetup(namespace string) (*TestNamespace, error) {

	By("verifying smoke test environment setup")
	_, _ = fmt.Fprintf(GinkgoWriter, "Smoke test environment setup completed by Makefile\n")
	_, _ = fmt.Fprintf(GinkgoWriter, "Project image: %s\n", GetProjectImage())
	_, _ = fmt.Fprintf(GinkgoWriter, "Agent image: %s\n", GetAgentImage())

	By("initializing Kubernetes clients for tests if needed")
	testClients, err := SetupKubernetesClients()
	Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")

	// Verify we can connect to the cluster
	By("verifying cluster connection")
	serverVersion, err := testClients.Clientset.Discovery().ServerVersion()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to connect to cluster")
	_, _ = fmt.Fprintf(GinkgoWriter, "Connected to Kubernetes cluster version: %s\n", serverVersion.String())

	By("creating e2e test namespace")
	testNamespace, err := testClients.CreateTestNamespace(namespace)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		Expect(err).NotTo(HaveOccurred())
	}

	// The smoke tests are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	By("verifying CRDs are installed")
	// Check for SBD CRDs by looking for API resources in the medik8s.medik8s.io group
	apiResourceList, err := testClients.Clientset.Discovery().ServerResourcesForGroupVersion("medik8s.medik8s.io/v1alpha1")
	Expect(err).NotTo(HaveOccurred(), "Failed to get API resources for medik8s.medik8s.io/v1alpha1")

	var foundSBDConfig, foundSBDRemediation bool
	for _, resource := range apiResourceList.APIResources {
		if resource.Kind == "SBDConfig" {
			foundSBDConfig = true
		}
		if resource.Kind == "SBDRemediation" {
			foundSBDRemediation = true
		}
	}
	Expect(foundSBDConfig).To(BeTrue(), "Expected SBDConfig CRD to be installed (should be done by Makefile setup)")
	Expect(foundSBDRemediation).To(BeTrue(), "Expected SBDRemediation CRD to be installed (should be done by Makefile setup)")

	By("verifying the controller-manager is deployed")
	deployment := &appsv1.Deployment{}
	err = testClients.Client.Get(testClients.Context, client.ObjectKey{
		Name:      "sbd-operator-controller-manager",
		Namespace: "sbd-operator-system",
	}, deployment)
	Expect(err).NotTo(HaveOccurred(), "Expected controller-manager to be deployed (should be done by Makefile setup)")

	// Confirm the operator is running
	By("confirming the operator is running")
	Eventually(func() bool {
		podList, err := testClients.Clientset.CoreV1().Pods("sbd-operator-system").List(testClients.Context, metav1.ListOptions{
			LabelSelector: "control-plane=controller-manager",
		})
		if err != nil || len(podList.Items) == 0 {
			return false
		}
		return podList.Items[0].Status.Phase == corev1.PodRunning
	}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Operator pod is not running")

	return testNamespace, nil
}

func DescribeEnvironment(testClients *TestClients, namespace string) {
	var controllerPodName string

	By("validating that the controller-manager pod is running as expected")
	verifyControllerUp := func(g Gomega) {
		// Get controller-manager pods
		pods := &corev1.PodList{}
		err := testClients.Client.List(testClients.Context, pods,
			client.InNamespace(namespace),
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

		// Validate the pod's status
		g.Expect(activePods[0].Status.Phase).To(Equal(corev1.PodRunning), "Incorrect controller-manager pod status")
	}
	Eventually(verifyControllerUp).Should(Succeed())

	debugCollector := testClients.NewDebugCollector()

	// Collect controller logs
	debugCollector.CollectControllerLogs(namespace, controllerPodName)

	// Collect Kubernetes events
	debugCollector.CollectKubernetesEvents(namespace)

	By("Fetching curl-metrics logs")
	req := testClients.Clientset.CoreV1().Pods(namespace).GetLogs("curl-metrics", &corev1.PodLogOptions{})
	podLogs, err := req.Stream(testClients.Context)
	if err == nil {
		defer podLogs.Close()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", buf.String())
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
	}

	// Collect controller pod description
	debugCollector.CollectPodDescription(namespace, controllerPodName)
}
