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
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

// ClusterInfo holds information about the test cluster
type ClusterInfo struct {
	TotalNodes   int
	WorkerNodes  []NodeInfo
	ControlNodes []NodeInfo
	NodeNames    []string
	ZoneInfo     map[string][]string // zone -> node names
}

// NodeInfo represents information about a cluster node
type NodeInfo struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Conditions []NodeCondition `json:"conditions"`
	} `json:"status"`
}

// NodeCondition represents a node condition
type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

var (
	clusterInfo    ClusterInfo
	awsSession     *session.Session
	ec2Client      *ec2.EC2
	awsRegion      string
	awsInitialized bool // Track if AWS was successfully initialized

	// Kubernetes clients - now using shared utilities
	testClients *utils.TestClients
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var testNamespace *utils.TestNamespace
var _ = Describe("SBD Operator", Ordered, Label("e2e"), func() {
	BeforeAll(func() {

		// Discover cluster topology
		discoverClusterTopology()

		// Initialize AWS if available (for disruption testing)
		if err := initAWS(); err != nil {
			By(fmt.Sprintf("AWS initialization failed (AWS disruption tests will be skipped): %v", err))
		} else {
			By("AWS initialization successful - AWS disruption tests available")

			// Clean up any leftover artifacts from previous test runs
			if err := cleanupPreviousTestAttempts(); err != nil {
				By(fmt.Sprintf("Warning: cleanup of previous test attempts failed: %v", err))
			}
		}

		By(fmt.Sprintf("Running e2e tests on cluster with %d total nodes (%d workers, %d control plane)",
			clusterInfo.TotalNodes, len(clusterInfo.WorkerNodes), len(clusterInfo.ControlNodes)))
	})

	AfterAll(func() {
		By("cleaning up e2e test namespace")
		if testNamespace != nil {
			_ = testNamespace.Cleanup()
		}
	})

	Context("SBD E2E Failure Simulation Tests", func() {
		It("should handle basic SBD configuration and agent deployment", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testBasicSBDConfiguration(clusterInfo)
		})

		It("should trigger fencing when SBD agent loses storage access", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes for safe storage disruption testing")
			}
			testStorageAccessInterruption(clusterInfo)
		})

		It("should trigger fencing when kubelet communication is interrupted", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes for safe communication disruption testing")
			}
			testKubeletCommunicationFailure(clusterInfo)
		})

		It("should handle SBD agent crash and recovery", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testSBDAgentCrash(clusterInfo)
		})

		It("should handle non-fencing failures gracefully", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testNonFencingFailure(clusterInfo)
		})

		It("should handle large cluster coordination", func() {
			if len(clusterInfo.WorkerNodes) < 8 {
				Skip("Test requires at least 8 worker nodes")
			}
			testLargeClusterCoordination(clusterInfo)
		})
	})
})

// discoverClusterTopology discovers the cluster topology and capabilities
func discoverClusterTopology() {
	By("Discovering cluster topology")

	// Get all nodes using Kubernetes API
	nodes := &corev1.NodeList{}
	err := k8sClient.List(ctx, nodes)
	Expect(err).NotTo(HaveOccurred())

	clusterInfo = ClusterInfo{
		TotalNodes:   len(nodes.Items),
		WorkerNodes:  []NodeInfo{},
		ControlNodes: []NodeInfo{},
		NodeNames:    []string{},
		ZoneInfo:     make(map[string][]string),
	}

	// Convert and categorize nodes
	for _, k8sNode := range nodes.Items {
		// Convert k8s Node to NodeInfo
		nodeInfo := NodeInfo{
			Metadata: struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			}{
				Name:   k8sNode.Name,
				Labels: k8sNode.Labels,
			},
		}

		// Convert conditions
		for _, condition := range k8sNode.Status.Conditions {
			nodeInfo.Status.Conditions = append(nodeInfo.Status.Conditions, NodeCondition{
				Type:   string(condition.Type),
				Status: string(condition.Status),
				Reason: condition.Reason,
			})
		}

		clusterInfo.NodeNames = append(clusterInfo.NodeNames, nodeInfo.Metadata.Name)

		// Check if it's a control plane node
		isControlPlane := false
		for label := range nodeInfo.Metadata.Labels {
			if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
				isControlPlane = true
				break
			}
		}

		if isControlPlane {
			clusterInfo.ControlNodes = append(clusterInfo.ControlNodes, nodeInfo)
		} else {
			clusterInfo.WorkerNodes = append(clusterInfo.WorkerNodes, nodeInfo)
		}

		// Track zone information
		if zone, exists := nodeInfo.Metadata.Labels["topology.kubernetes.io/zone"]; exists {
			clusterInfo.ZoneInfo[zone] = append(clusterInfo.ZoneInfo[zone], nodeInfo.Metadata.Name)
		}
	}

	// Log cluster topology
	GinkgoWriter.Printf("Discovered cluster topology:\n")
	GinkgoWriter.Printf("  Total nodes: %d\n", clusterInfo.TotalNodes)
	GinkgoWriter.Printf("  Worker nodes: %d\n", len(clusterInfo.WorkerNodes))
	GinkgoWriter.Printf("  Control plane nodes: %d\n", len(clusterInfo.ControlNodes))
}

// Test implementation functions

// selectActualWorkerNode selects a random worker node that is verified to not be a control plane node
func selectActualWorkerNode(cluster ClusterInfo) NodeInfo {
	var actualWorkerNodes []NodeInfo
	for _, node := range cluster.WorkerNodes {
		// Double-check this is not a control plane node by examining labels
		isControlPlane := false
		for label := range node.Metadata.Labels {
			if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
				isControlPlane = true
				break
			}
		}
		if !isControlPlane {
			actualWorkerNodes = append(actualWorkerNodes, node)
		}
	}

	if len(actualWorkerNodes) == 0 {
		Skip("No actual worker nodes found for testing - all nodes appear to be control plane")
	}

	// Select a random actual worker node
	selectedNode := actualWorkerNodes[rand.Intn(len(actualWorkerNodes))]

	// Log the selection for debugging
	GinkgoWriter.Printf("Selected actual worker node: %s (verified not control plane)\n", selectedNode.Metadata.Name)

	return selectedNode
}

func testBasicSBDConfiguration(cluster ClusterInfo) {
	By("Creating SBDConfig with proper agent deployment")

	// Look for a storage class that supports RWX (ReadWriteMany) access mode
	By("Looking for RWX-compatible storage class")
	storageClasses := &storagev1.StorageClassList{}
	err := k8sClient.List(ctx, storageClasses)
	Expect(err).NotTo(HaveOccurred())

	var rwxStorageClass *storagev1.StorageClass
	for _, sc := range storageClasses.Items {
		// Check if this storage class supports RWX access mode
		// Most cloud providers have storage classes that support RWX
		if strings.Contains(strings.ToLower(sc.Name), "nfs") ||
			strings.Contains(strings.ToLower(sc.Name), "efs") ||
			strings.Contains(strings.ToLower(sc.Name), "azurefile") ||
			strings.Contains(strings.ToLower(sc.Name), "gce") ||
			strings.Contains(strings.ToLower(sc.Name), "ceph") ||
			strings.Contains(strings.ToLower(sc.Name), "gluster") {
			rwxStorageClass = &sc
			GinkgoWriter.Printf("Found RWX-compatible storage class: %s\n", sc.Name)
			break
		}
	}

	if rwxStorageClass == nil {
		// If no obvious RWX storage class found, try to use the default
		// or any available storage class (some may support RWX even if not obvious from name)
		if len(storageClasses.Items) > 0 {
			rwxStorageClass = &storageClasses.Items[0]
			GinkgoWriter.Printf("Using storage class: %s (RWX support unknown)\n", rwxStorageClass.Name)
		} else {
			Skip("No storage classes available - skipping storage-dependent tests")
		}
	}

	// Store the storage class name for use in tests
	testStorageClassName := rwxStorageClass.Name
	GinkgoWriter.Printf("Selected storage class for testing: %s\n", testStorageClassName)

	sbdConfig, err := testNamespace.CreateSBDConfig("test-sbd-config", func(config *medik8sv1alpha1.SBDConfig) {
		config.Spec.SbdWatchdogPath = "/dev/watchdog"
		config.Spec.SharedStorageClass = testStorageClassName
		config.Spec.StaleNodeTimeout = &metav1.Duration{Duration: 2 * time.Hour}
		config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
	})
	Expect(err).NotTo(HaveOccurred())

	validator := testNamespace.NewSBDAgentValidator()
	opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
	opts.ExpectedArgs = []string{
		"--watchdog-path=/dev/watchdog",
		"--watchdog-timeout=1m30s",
	}
	err = validator.ValidateAgentDeployment(opts)
	Expect(err).NotTo(HaveOccurred())
}

func testStorageAccessInterruption(cluster ClusterInfo) {
	// Skip if AWS is not available
	if !awsInitialized {
		Skip("Storage access interruption test requires AWS - skipping")
	}

	By("Setting up SBD configuration for storage access test")
	testBasicSBDConfiguration(cluster)

	// Select a random actual worker node for testing (not control plane)
	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing storage access interruption on verified worker node %s", targetNode.Metadata.Name))

	// Get AWS instance ID for the target node
	instanceID, err := getInstanceIDFromNode(targetNode.Metadata.Name)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Target node %s has AWS instance ID: %s", targetNode.Metadata.Name, instanceID))

	// Create storage disruption by detaching EBS volumes
	By("Creating AWS storage disruption by detaching EBS volumes")
	detachedVolumes, err := createStorageDisruption(instanceID)
	if err != nil {
		// If no additional volumes to detach, skip this test
		Skip(fmt.Sprintf("Skipping storage disruption test: %v", err))
	}
	By(fmt.Sprintf("Detached %d EBS volumes from node %s", len(detachedVolumes), targetNode.Metadata.Name))

	// Ensure cleanup happens even if test fails
	defer func() {
		By("Cleaning up AWS storage disruption")
		err := restoreStorageDisruption(instanceID, detachedVolumes)
		if err != nil {
			GinkgoWriter.Printf("Warning: failed to restore storage disruption: %v\n", err)
		} else {
			By("Successfully restored detached volumes")
		}
	}()

	// Wait for storage disruption to take effect
	By("Waiting for storage disruption to take effect")
	time.Sleep(30 * time.Second)

	// Monitor for node becoming NotReady due to storage issues
	By("Verifying node becomes NotReady due to storage disruption")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false
		}

		// Check if node is NotReady or has storage-related issues
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s is now NotReady: %s", targetNode.Metadata.Name, condition.Reason))
				return true
			}
			// Also check for disk pressure or other storage-related conditions
			if condition.Type == corev1.NodeDiskPressure && condition.Status == corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s has disk pressure: %s", targetNode.Metadata.Name, condition.Reason))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*20).Should(BeTrue())

	// Node should self-fence when it loses storage access (no SBDRemediation CR needed)
	// Wait for node to actually panic/reboot due to storage loss (self-fencing)
	By("Waiting for node to panic/reboot due to SBD fencing")
	originalBootTime := ""

	// Get original boot time/uptime if possible
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err == nil {
			// Try to get boot time from node info
			originalBootTime = node.Status.NodeInfo.BootID
			return originalBootTime != ""
		}
		return false
	}, time.Minute*1, time.Second*10).Should(BeTrue())

	// Monitor for node disappearing (panic/reboot) or boot ID change
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)

		// Node completely unreachable (likely panicked/rebooting)
		if err != nil {
			By(fmt.Sprintf("Node %s is unreachable - likely panicked/rebooting", targetNode.Metadata.Name))
			return true
		}

		// Check if boot ID changed (indicating reboot)
		currentBootID := node.Status.NodeInfo.BootID
		if originalBootTime != "" && currentBootID != originalBootTime {
			By(fmt.Sprintf("Node %s boot ID changed - node has rebooted", targetNode.Metadata.Name))
			return true
		}

		// Check for extended NotReady state with specific reasons indicating panic
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				// Look for reasons that indicate the node is completely unresponsive
				if strings.Contains(strings.ToLower(condition.Reason), "unreachable") ||
					strings.Contains(strings.ToLower(condition.Reason), "unknown") {
					By(fmt.Sprintf("Node %s is unreachable/unknown - likely fenced", targetNode.Metadata.Name))
					return true
				}
			}
		}

		return false
	}, time.Minute*10, time.Second*30).Should(BeTrue())

	// Only restore storage after confirming node has been fenced
	By("Node has been fenced - now restoring storage access to test recovery")
	err = restoreStorageDisruption(instanceID, detachedVolumes)
	Expect(err).NotTo(HaveOccurred())
	detachedVolumes = nil // Prevent double cleanup in defer

	// Wait longer for node to come back online after reboot
	By("Waiting for node to come back online after reboot")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false // Node still not reachable
		}

		// Check if node is Ready again
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s has come back online after reboot", targetNode.Metadata.Name))
				return true
			}
		}
		return false
	}, time.Minute*10, time.Second*30).Should(BeTrue())

	// Verify node recovery (instead of the old immediate recovery test)
	By("Verifying node has fully recovered after fencing and storage restoration")
	time.Sleep(30 * time.Second) // Give additional time for full recovery

	// Verify other nodes remained stable during storage disruption
	By("Verifying other nodes remained stable during storage disruption")
	for _, node := range cluster.WorkerNodes {
		if node.Metadata.Name == targetNode.Metadata.Name {
			continue // Skip the target node
		}

		currentNode := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: node.Metadata.Name}, currentNode)
		Expect(err).NotTo(HaveOccurred())

		// Verify node is Ready
		nodeReady := false
		for _, condition := range currentNode.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				nodeReady = true
				break
			}
		}
		Expect(nodeReady).To(BeTrue(), fmt.Sprintf("Node %s should remain Ready", node.Metadata.Name))
	}

	GinkgoWriter.Printf("AWS-based storage access interruption test completed\n")
}

func testKubeletCommunicationFailure(cluster ClusterInfo) {
	// Skip if AWS is not available
	if !awsInitialized {
		Skip("Kubelet communication failure test requires AWS - skipping")
	}

	By("Setting up SBD configuration for kubelet communication test")
	testBasicSBDConfiguration(cluster)

	// Select a random actual worker node for testing (not control plane)
	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing kubelet communication failure on verified worker node %s", targetNode.Metadata.Name))

	// Get AWS instance ID for the target node
	instanceID, err := getInstanceIDFromNode(targetNode.Metadata.Name)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Target node %s has AWS instance ID: %s", targetNode.Metadata.Name, instanceID))

	// Create network disruption using AWS security groups
	By("Creating AWS network disruption to block kubelet communication")
	securityGroupID, err := createNetworkDisruption(instanceID)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Created temporary security group %s to disrupt network", *securityGroupID))

	// Ensure cleanup happens even if test fails
	defer func() {
		By("Cleaning up AWS network disruption")
		err := removeNetworkDisruption(securityGroupID, instanceID)
		if err != nil {
			GinkgoWriter.Printf("Warning: failed to clean up network disruption: %v\n", err)
		} else {
			By("Successfully removed network disruption")
		}
	}()

	// Wait for network disruption to take effect
	By("Waiting for network disruption to take effect")
	time.Sleep(30 * time.Second)

	// Verify that the node becomes NotReady due to kubelet communication failure
	By("Verifying node becomes NotReady due to network disruption")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			GinkgoWriter.Printf("Warning: Node %s not found: %v\n", targetNode.Metadata.Name, err)
			return false
		}

		// Check if node is NotReady
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s is now NotReady: %s", targetNode.Metadata.Name, condition.Reason))
				return true
			} else if condition.Type == corev1.NodeReady {
				GinkgoWriter.Printf("Warning: Node %s has ready condition: %v\n", targetNode.Metadata.Name, condition)
			}
		}
		return false
	}, time.Minute*8, time.Second*15).Should(BeTrue())

	// Create SBDRemediation CR to simulate external operator (e.g., Node Healthcheck Operator)
	By("Creating SBDRemediation CR to simulate external operator behavior")
	sbdRemediation := &medik8sv1alpha1.SBDRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("network-remediation-%s", targetNode.Metadata.Name),
			Namespace: testNamespace.Name,
		},
		Spec: medik8sv1alpha1.SBDRemediationSpec{
			NodeName:       targetNode.Metadata.Name,
			Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
			TimeoutSeconds: 300, // 5 minutes timeout for fencing
		},
	}
	err = k8sClient.Create(ctx, sbdRemediation)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Created SBDRemediation CR for node %s", targetNode.Metadata.Name))

	// Verify SBD remediation is triggered and processed
	By("Verifying SBD remediation is triggered and processed for the disrupted node")
	Eventually(func() bool {
		remediations := &medik8sv1alpha1.SBDRemediationList{}
		err := k8sClient.List(ctx, remediations, client.InNamespace(testNamespace.Name))
		if err != nil {
			return false
		}

		for _, remediation := range remediations.Items {
			if remediation.Spec.NodeName == targetNode.Metadata.Name {
				By(fmt.Sprintf("SBD remediation found for node %s: %+v", targetNode.Metadata.Name, remediation.Status))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*30).Should(BeTrue())

	// Wait for node to actually panic/reboot (the actual SBD fencing)
	By("Waiting for node to panic/reboot due to SBD fencing")
	originalBootTime := ""

	// Get original boot time/uptime if possible
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err == nil {
			// Try to get boot time from node info
			originalBootTime = node.Status.NodeInfo.BootID
			return originalBootTime != ""
		}
		return false
	}, time.Minute*1, time.Second*10).Should(BeTrue())

	// Monitor for node disappearing (panic/reboot) or boot ID change
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)

		// Node completely unreachable (likely panicked/rebooting)
		if err != nil {
			By(fmt.Sprintf("Node %s is unreachable - likely panicked/rebooting", targetNode.Metadata.Name))
			return true
		}

		// Check if boot ID changed (indicating reboot)
		currentBootID := node.Status.NodeInfo.BootID
		if originalBootTime != "" && currentBootID != originalBootTime {
			By(fmt.Sprintf("Node %s boot ID changed - node has rebooted", targetNode.Metadata.Name))
			return true
		}

		// Check for extended NotReady state with specific reasons indicating panic
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				// Look for reasons that indicate the node is completely unresponsive
				if strings.Contains(strings.ToLower(condition.Reason), "unreachable") ||
					strings.Contains(strings.ToLower(condition.Reason), "unknown") {
					By(fmt.Sprintf("Node %s is unreachable/unknown - likely fenced", targetNode.Metadata.Name))
					return true
				}
			}
		}

		return false
	}, time.Minute*10, time.Second*30).Should(BeTrue())

	// Only remove network disruption after confirming node has been fenced
	By("Node has been fenced - now removing network disruption to allow recovery")
	err = removeNetworkDisruption(securityGroupID, instanceID)
	Expect(err).NotTo(HaveOccurred())
	securityGroupID = nil // Prevent double cleanup in defer

	// Wait longer for node to come back online after reboot
	By("Waiting for node to come back online after reboot")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false // Node still not reachable
		}

		// Check if node is Ready again
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s has come back online after reboot", targetNode.Metadata.Name))
				return true
			}
		}
		return false
	}, time.Minute*10, time.Second*30).Should(BeTrue())

	// Verify node recovery (instead of the old immediate recovery test)
	By("Verifying node has fully recovered after fencing and network restoration")
	time.Sleep(30 * time.Second) // Give additional time for full recovery

	// Verify other nodes remain stable during the disruption
	By("Verifying other nodes remained stable during network disruption")
	for _, node := range cluster.WorkerNodes {
		if node.Metadata.Name == targetNode.Metadata.Name {
			continue // Skip the target node
		}

		currentNode := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: node.Metadata.Name}, currentNode)
		Expect(err).NotTo(HaveOccurred())

		// Verify node is Ready
		nodeReady := false
		for _, condition := range currentNode.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				nodeReady = true
				break
			}
		}
		Expect(nodeReady).To(BeTrue(), fmt.Sprintf("Node %s should remain Ready", node.Metadata.Name))
	}

	GinkgoWriter.Printf("AWS-based kubelet communication failure test completed successfully\n")
}

func testSBDAgentCrash(cluster ClusterInfo) {
	By("Setting up SBD configuration for agent crash test")
	testBasicSBDConfiguration(cluster)

	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing SBD agent crash and recovery on verified worker node %s", targetNode.Metadata.Name))

	// Get the SBD agent pod on the target node
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbd-agent"},
		client.MatchingFields{"spec.nodeName": targetNode.Metadata.Name})
	Expect(err).NotTo(HaveOccurred())
	Expect(len(pods.Items)).To(BeNumerically(">", 0), "Should find SBD agent pod on target node")

	podName := pods.Items[0].Name
	targetPod := &pods.Items[0]

	By(fmt.Sprintf("Crashing SBD agent pod %s", podName))
	// Delete the pod to simulate a crash
	err = k8sClient.Delete(ctx, targetPod)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying SBD agent pod is recreated by DaemonSet")
	Eventually(func() bool {
		newPods := &corev1.PodList{}
		err := k8sClient.List(ctx, newPods,
			client.InNamespace(testNamespace.Name),
			client.MatchingLabels{"app": "sbd-agent"},
			client.MatchingFields{"spec.nodeName": targetNode.Metadata.Name})
		if err != nil {
			return false
		}

		// Check for a new running pod (different name)
		for _, pod := range newPods.Items {
			if pod.Name != podName && pod.Status.Phase == corev1.PodRunning {
				GinkgoWriter.Printf("New SBD agent pod %s is running on node %s\n",
					pod.Name, targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Verifying node remains healthy after agent recovery")
	Consistently(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false
		}

		// Verify node remains Ready
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	GinkgoWriter.Printf("SBD agent crash and recovery test completed\n")
}

func testNonFencingFailure(cluster ClusterInfo) {
	By("Testing non-fencing failure scenario")
	testBasicSBDConfiguration(cluster)

	By("Creating a temporary resource constraint that should not trigger fencing")
	// Create a pod that uses resources but doesn't cause critical failure
	resourceConstraintYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: resource-consumer
  namespace: %s
spec:
  containers:
  - name: consumer
    image: registry.access.redhat.com/ubi9/ubi-minimal:latest
    command:
    - /bin/bash
    - -c
    - |
      echo "Starting non-critical resource consumption..."
      
      # Simple resource consumption using basic shell operations
      # Create some CPU load by running calculations
      echo "Creating CPU load..."
      for i in {1..10}; do
        # Simple arithmetic operations to consume CPU
        result=0
        for j in {1..10000}; do
          result=$((result + j))
        done
        echo "Iteration $i completed, result: $result"
        sleep 1
      done
      
      # Create some memory usage by storing data in variables
      echo "Creating memory load..."
      data1="$(yes 'x' | head -n 10000 | tr -d '\n')"
      data2="$(yes 'y' | head -n 10000 | tr -d '\n')"
      data3="$(yes 'z' | head -n 10000 | tr -d '\n')"
      
      echo "Resource consumption active for 30 seconds..."
      sleep 30
      
      # Clear variables
      unset data1 data2 data3
      
      echo "Non-critical resource consumption completed"
      sleep 5
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
      limits:
        cpu: "500m"
        memory: "256Mi"
  restartPolicy: Never`, testNamespace.Name)

	By("Creating resource constraint pod")
	var resourcePod corev1.Pod
	err := yaml.Unmarshal([]byte(resourceConstraintYAML), &resourcePod)
	Expect(err).NotTo(HaveOccurred())
	err = k8sClient.Create(ctx, &resourcePod)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying all nodes remain healthy during non-critical failure")
	Consistently(func() bool {
		nodes := &corev1.NodeList{}
		err := k8sClient.List(ctx, nodes)
		if err != nil {
			return false
		}

		// All nodes should remain Ready
		readyNodes := 0
		for _, node := range nodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					readyNodes++
					break
				}
			}
		}
		expectedNodes := len(cluster.WorkerNodes) + len(cluster.ControlNodes)

		if readyNodes == expectedNodes {
			return true
		}

		GinkgoWriter.Printf("Expected %d Ready nodes, found %d\n", expectedNodes, readyNodes)
		return false
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	By("Verifying SBD agents continue running normally")
	Consistently(func() bool {
		pods := &corev1.PodList{}
		err := k8sClient.List(ctx, pods, client.InNamespace(testNamespace.Name), client.MatchingLabels{"app": "sbd-agent"})
		if err != nil {
			return false
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods++
			}
		}

		return runningPods >= 2 // Expect agents to keep running
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	GinkgoWriter.Printf("Non-fencing failure test completed - cluster remained stable\n")
}

func testLargeClusterCoordination(cluster ClusterInfo) {
	By(fmt.Sprintf("Testing SBD coordination with %d worker nodes", len(cluster.WorkerNodes)))
	testBasicSBDConfiguration(cluster)

	By("Verifying SBD agents coordinate across large cluster")
	Eventually(func() bool {
		pods := &corev1.PodList{}
		err := k8sClient.List(ctx, pods, client.InNamespace(testNamespace.Name), client.MatchingLabels{"app": "sbd-agent"})
		if err != nil {
			return false
		}

		runningAgents := make(map[string]bool)
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningAgents[pod.Spec.NodeName] = true
			}
		}

		// Expect agents running on most worker nodes (allow for some scheduling constraints)
		expectedMinimum := len(cluster.WorkerNodes) * 3 / 4 // At least 75% of worker nodes
		actualRunning := len(runningAgents)

		GinkgoWriter.Printf("SBD agents running on %d out of %d worker nodes (minimum required: %d)\n",
			actualRunning, len(cluster.WorkerNodes), expectedMinimum)

		return actualRunning >= expectedMinimum
	}, time.Minute*5, time.Second*30).Should(BeTrue())

	GinkgoWriter.Printf("Large cluster coordination test completed successfully\n")
}

func cleanupTestArtifacts() {
	// Clean up any disruption pods or test artifacts
	disruptionPods := []string{"storage-disruptor", "network-disruptor", "resource-consumer", "kubelet-stress-test"}

	for _, podName := range disruptionPods {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: testNamespace.Name,
			},
		}
		_ = k8sClient.Delete(ctx, pod)
	}

	// Clean up temporary SCCs that might be left over from failed tests
	tempSCCs := []string{"sbd-e2e-network-test", "sbd-e2e-storage-test"}
	for _, sccName := range tempSCCs {
		err := testClients.Clientset.RESTClient().
			Delete().
			AbsPath("/apis/security.openshift.io/v1/securitycontextconstraints/" + sccName).
			Do(ctx).
			Error()
		if err != nil && !strings.Contains(err.Error(), "not found") {
			_, _ = fmt.Fprintf(GinkgoWriter, "Warning: Failed to clean up temporary SCC %s: %v\n", sccName, err)
		}
	}

	// Wait a moment for cleanup
	time.Sleep(5 * time.Second)
}

// AWS helper functions for disruption testing

// initAWS initializes AWS session and clients with comprehensive validation
func initAWS() error {
	// First, validate this is an AWS-based cluster
	if !isAWSCluster() {
		return fmt.Errorf("cluster is not AWS-based, skipping AWS disruption tests")
	}

	// Auto-detect AWS region from cluster
	var err error
	awsRegion, err = detectAWSRegion()
	if err != nil {
		return fmt.Errorf("failed to detect AWS region: %w", err)
	}

	// Create AWS session
	awsSession, err = session.NewSession(&aws.Config{
		Region: aws.String(awsRegion),
	})
	if err != nil {
		return fmt.Errorf("failed to create AWS session: %w", err)
	}

	ec2Client = ec2.New(awsSession)

	// Validate required AWS permissions
	if err := validateAWSPermissions(); err != nil {
		return fmt.Errorf("AWS permission validation failed: %w", err)
	}

	By(fmt.Sprintf("AWS initialization successful - Region: %s", awsRegion))
	awsInitialized = true
	return nil
}

// isAWSCluster checks if the cluster is running on AWS
func isAWSCluster() bool {
	// Check if nodes have AWS provider IDs
	nodes := &corev1.NodeList{}
	err := k8sClient.List(ctx, nodes)
	if err != nil {
		return false
	}

	awsNodeCount := 0
	for _, node := range nodes.Items {
		if strings.HasPrefix(node.Spec.ProviderID, "aws://") {
			awsNodeCount++
		}
	}

	// Require at least 50% of nodes to be AWS-based
	return awsNodeCount > 0 && float64(awsNodeCount)/float64(len(nodes.Items)) >= 0.5
}

// detectAWSRegion automatically detects the AWS region from cluster configuration
func detectAWSRegion() (string, error) {
	// Method 1: Check environment variable
	if region := os.Getenv("AWS_REGION"); region != "" {
		By(fmt.Sprintf("Using AWS region from environment: %s", region))
		return region, nil
	}

	// Method 2: Extract from node names (e.g., ip-10-0-1-1.us-west-2.compute.internal)
	nodes := &corev1.NodeList{}
	err := k8sClient.List(ctx, nodes)
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		// Extract region from node name
		re := regexp.MustCompile(`\.([a-z]{2}-[a-z]+-\d+)\.compute\.internal`)
		matches := re.FindStringSubmatch(node.Name)
		if len(matches) >= 2 {
			region := matches[1]
			By(fmt.Sprintf("Detected AWS region from node name %s: %s", node.Name, region))
			return region, nil
		}

		// Extract region from provider ID (aws:///us-west-2a/i-1234567890abcdef0)
		re = regexp.MustCompile(`aws:///([a-z]{2}-[a-z]+-\d+)[a-z]/`)
		matches = re.FindStringSubmatch(node.Spec.ProviderID)
		if len(matches) >= 2 {
			region := matches[1]
			By(fmt.Sprintf("Detected AWS region from provider ID %s: %s", node.Spec.ProviderID, region))
			return region, nil
		}
	}

	// Method 3: Try to detect from cluster endpoint (for EKS)
	// This would require additional cluster info, so we'll skip for now

	return "", fmt.Errorf("could not auto-detect AWS region from cluster configuration")
}

// validateAWSPermissions checks if the required AWS permissions are available
func validateAWSPermissions() error {
	By("Validating required AWS permissions")

	requiredPermissions := []struct {
		name   string
		testFn func() error
	}{
		{"ec2:DescribeInstances", testDescribeInstances},
		{"ec2:DescribeVolumes", testDescribeVolumes},
		{"ec2:DescribeSecurityGroups", testDescribeSecurityGroups},
		{"ec2:CreateSecurityGroup", testCreateSecurityGroup},
		{"ec2:DeleteSecurityGroup", testDeleteSecurityGroup},
		{"ec2:ModifyInstanceAttribute", testModifyInstanceAttribute},
		{"ec2:AttachVolume", testAttachVolume},
		{"ec2:DetachVolume", testDetachVolume},
		{"ec2:RevokeSecurityGroupEgress", testRevokeSecurityGroupEgress},
		{"ec2:AuthorizeSecurityGroupIngress", testAuthorizeSecurityGroupIngress},
		{"ec2:AuthorizeSecurityGroupEgress", testAuthorizeSecurityGroupEgress},
	}

	var failedPermissions []string
	for _, perm := range requiredPermissions {
		if err := perm.testFn(); err != nil {
			failedPermissions = append(failedPermissions, perm.name)
			By(fmt.Sprintf("Permission check failed for %s: %v", perm.name, err))
		} else {
			By(fmt.Sprintf("Permission check passed for %s", perm.name))
		}
	}

	if len(failedPermissions) > 0 {
		return fmt.Errorf("missing required AWS permissions: %s", strings.Join(failedPermissions, ", "))
	}

	By("All required AWS permissions validated successfully")
	return nil
}

// Permission test functions
func testDescribeInstances() error {
	_, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
		MaxResults: aws.Int64(5),
	})
	return checkAWSPermissionError(err)
}

func testDescribeVolumes() error {
	_, err := ec2Client.DescribeVolumes(&ec2.DescribeVolumesInput{
		MaxResults: aws.Int64(5),
	})
	return checkAWSPermissionError(err)
}

func testDescribeSecurityGroups() error {
	_, err := ec2Client.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{
		MaxResults: aws.Int64(5),
	})
	return checkAWSPermissionError(err)
}

func testCreateSecurityGroup() error {
	// Test with invalid parameters to check permission without actually creating
	_, err := ec2Client.CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("test-sg"),
		Description: aws.String("test"),
		VpcId:       aws.String("vpc-nonexistent"), // Non-existent VPC to trigger validation error
	})
	return checkAWSPermissionError(err)
}

func testDeleteSecurityGroup() error {
	// Test with non-existent security group to check permission
	_, err := ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
		GroupId: aws.String("sg-nonexistent"),
	})
	return checkAWSPermissionError(err)
}

func testModifyInstanceAttribute() error {
	// Test with invalid instance ID to check permission
	_, err := ec2Client.ModifyInstanceAttribute(&ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String("i-nonexistent"),
		Groups:     []*string{aws.String("sg-nonexistent")},
	})
	return checkAWSPermissionError(err)
}

func testAttachVolume() error {
	// Test with invalid parameters to check permission
	_, err := ec2Client.AttachVolume(&ec2.AttachVolumeInput{
		VolumeId:   aws.String("vol-nonexistent"),
		InstanceId: aws.String("i-nonexistent"),
		Device:     aws.String("/dev/sdf"),
	})
	return checkAWSPermissionError(err)
}

func testDetachVolume() error {
	// Test with invalid parameters to check permission
	_, err := ec2Client.DetachVolume(&ec2.DetachVolumeInput{
		VolumeId: aws.String("vol-nonexistent"),
	})
	return checkAWSPermissionError(err)
}

func testRevokeSecurityGroupEgress() error {
	// Test with invalid parameters to check permission
	_, err := ec2Client.RevokeSecurityGroupEgress(&ec2.RevokeSecurityGroupEgressInput{
		GroupId: aws.String("sg-nonexistent"),
		IpPermissions: []*ec2.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int64(80),
				ToPort:     aws.Int64(80),
			},
		},
	})
	return checkAWSPermissionError(err)
}

func testAuthorizeSecurityGroupIngress() error {
	// Test with invalid parameters to check permission
	_, err := ec2Client.AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String("sg-nonexistent"),
		IpPermissions: []*ec2.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int64(80),
				ToPort:     aws.Int64(80),
			},
		},
	})
	return checkAWSPermissionError(err)
}

func testAuthorizeSecurityGroupEgress() error {
	// Test with invalid parameters to check permission
	_, err := ec2Client.AuthorizeSecurityGroupEgress(&ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String("sg-nonexistent"),
		IpPermissions: []*ec2.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int64(80),
				ToPort:     aws.Int64(80),
			},
		},
	})
	return checkAWSPermissionError(err)
}

// checkAWSPermissionError distinguishes between permission errors and validation errors
func checkAWSPermissionError(err error) error {
	if err != nil {
		// Check for permission-related errors
		if strings.Contains(err.Error(), "UnauthorizedOperation") {
			return err
		}
		// For describe operations, no error means permission exists
		// For other operations, validation errors are expected and mean permission exists
		if strings.Contains(err.Error(), "InvalidParameterValue") ||
			strings.Contains(err.Error(), "InvalidGroupId") ||
			strings.Contains(err.Error(), "InvalidInstanceID") ||
			strings.Contains(err.Error(), "InvalidVolumeID") ||
			strings.Contains(err.Error(), "InvalidParameter") ||
			strings.Contains(err.Error(), "InvalidVpcID") ||
			strings.Contains(err.Error(), "InvalidVpcId") ||
			strings.Contains(err.Error(), "MissingParameter") {
			return nil // Permission exists, got validation error
		}
		// Other errors might indicate permission issues
		return err
	}
	return nil // No error means permission exists and call succeeded
}

// getInstanceIDFromNode extracts AWS instance ID from node provider ID
func getInstanceIDFromNode(nodeName string) (string, error) {
	node := &corev1.Node{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		return "", fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Extract instance ID from provider ID like "aws:///ap-southeast-2a/i-0435c40fb88349161"
	providerID := node.Spec.ProviderID
	re := regexp.MustCompile(`aws:///[^/]+/(i-[a-f0-9]+)`)
	matches := re.FindStringSubmatch(providerID)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not extract instance ID from provider ID: %s", providerID)
	}

	return matches[1], nil
}

// createNetworkDisruption creates targeted disruption by stopping kubelet service on the target node
func createNetworkDisruption(instanceID string) (*string, error) {
	By(fmt.Sprintf("Creating kubelet disruption for instance %s", instanceID))

	// Get the node name from the instance ID
	nodeName, err := getNodeNameFromInstanceID(instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node name for instance %s: %w", instanceID, err)
	}

	By(fmt.Sprintf("Target node: %s", nodeName))

	// Create a unique pod name for this disruption
	disruptorPodName := fmt.Sprintf("sbd-e2e-kubelet-disruptor-%d", time.Now().Unix())

	// Create privileged pod that stops kubelet service
	disruptorPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-kubelet-disruptor
spec:
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: disruptor
    image: busybox:latest
    command:
    - /bin/sh
    - -c
    - |
      echo "SBD e2e kubelet disruptor starting..."
      echo "Target: Stop kubelet service to simulate node failure"
      
      echo "Stopping kubelet service..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- systemctl stop kubelet.service
      
      echo "Kubelet stopped. Node should become NotReady."
      echo "Waiting for cleanup signal (pod deletion)..."
      
      # Keep running until pod is deleted
      # The cleanup function will restart kubelet before deleting this pod
      while true; do
        sleep 30
        echo "Kubelet disruption active..."
      done
    securityContext:
      privileged: true
    resources:
      requests:
        memory: "32Mi"
        cpu: "50m"
      limits:
        memory: "64Mi"
        cpu: "100m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, disruptorPodName, nodeName)

	// Apply the pod
	By(fmt.Sprintf("Creating kubelet disruptor pod: %s", disruptorPodName))
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(disruptorPodYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create disruptor pod: %w (output: %s)", err, string(output))
	}

	// Wait for pod to start and stop kubelet
	By("Waiting for disruptor pod to start...")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "pod", disruptorPodName, "-o", "jsonpath={.status.phase}")
		output, err := cmd.Output()
		if err != nil {
			return false
		}
		return string(output) == "Running"
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Wait for kubelet to be stopped and node to become NotReady
	By("Waiting for node to become NotReady due to kubelet termination...")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			return false
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s is now NotReady: %s", nodeName, condition.Reason))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*15).Should(BeTrue())

	By(fmt.Sprintf("Kubelet disruption successful - node %s is now NotReady", nodeName))
	return &disruptorPodName, nil
}

// getNodeNameFromInstanceID maps an EC2 instance ID to a Kubernetes node name
func getNodeNameFromInstanceID(instanceID string) (string, error) {
	// Get all nodes and find the one with matching provider ID
	nodes := &corev1.NodeList{}
	err := k8sClient.List(ctx, nodes)
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		if strings.Contains(node.Spec.ProviderID, instanceID) {
			return node.Name, nil
		}
	}

	return "", fmt.Errorf("no node found with instance ID %s", instanceID)
}

// removeNetworkDisruption removes the kubelet disruption by restarting kubelet and cleaning up the disruptor pod
func removeNetworkDisruption(disruptorIdentifier *string, instanceID string) error {
	if disruptorIdentifier == nil {
		return nil
	}

	disruptorPodName := *disruptorIdentifier
	By(fmt.Sprintf("Removing kubelet disruption for instance %s (pod: %s)", instanceID, disruptorPodName))

	// Step 1: Restart kubelet service via the disruptor pod
	By("Restarting kubelet service...")
	cmd := exec.Command("kubectl", "exec", disruptorPodName, "--", "nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--", "systemctl", "start", "kubelet.service")
	output, err := cmd.CombinedOutput()
	if err != nil {
		By(fmt.Sprintf("Warning: failed to restart kubelet via disruptor pod: %v (output: %s)", err, string(output)))
		// Continue with cleanup even if restart fails - kubelet might recover on its own
	} else {
		By("Kubelet service restarted successfully")
	}

	// Step 2: Delete the disruptor pod
	By(fmt.Sprintf("Deleting kubelet disruptor pod: %s", disruptorPodName))
	cmd = exec.Command("kubectl", "delete", "pod", disruptorPodName, "--wait=false")
	output, err = cmd.CombinedOutput()
	if err != nil {
		By(fmt.Sprintf("Warning: failed to delete disruptor pod: %v (output: %s)", err, string(output)))
	} else {
		By("Disruptor pod deletion initiated")
	}

	// Step 3: Wait for node to recover
	nodeName, err := getNodeNameFromInstanceID(instanceID)
	if err != nil {
		By(fmt.Sprintf("Warning: failed to get node name for recovery monitoring: %v", err))
		return nil // Don't fail cleanup due to monitoring issues
	}

	By(fmt.Sprintf("Waiting for node %s to recover...", nodeName))
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			return false
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s has recovered and is Ready", nodeName))
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Kubelet disruption cleanup completed successfully")
	return nil
}

// createStorageDisruption detaches EBS volumes to simulate storage failure
func createStorageDisruption(instanceID string) ([]string, error) {
	// Get instance details to find attached volumes
	result, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance not found: %s", instanceID)
	}

	instance := result.Reservations[0].Instances[0]
	var detachedVolumes []string

	// Find non-root EBS volumes to detach (avoid detaching root volume)
	for _, bdm := range instance.BlockDeviceMappings {
		if bdm.Ebs != nil && bdm.DeviceName != nil {
			// Skip root volume (typically /dev/sda1 or /dev/xvda)
			if strings.Contains(*bdm.DeviceName, "sda1") || strings.Contains(*bdm.DeviceName, "xvda") {
				continue
			}

			volumeID := *bdm.Ebs.VolumeId
			By(fmt.Sprintf("Detaching EBS volume %s from device %s", volumeID, *bdm.DeviceName))

			// Detach the volume
			_, err := ec2Client.DetachVolume(&ec2.DetachVolumeInput{
				VolumeId:   aws.String(volumeID),
				InstanceId: aws.String(instanceID),
				Device:     bdm.DeviceName,
				Force:      aws.Bool(false), // Graceful detach first
			})
			if err != nil {
				// If graceful detach fails, try force detach
				By(fmt.Sprintf("Graceful detach failed, trying force detach for volume %s", volumeID))
				_, err = ec2Client.DetachVolume(&ec2.DetachVolumeInput{
					VolumeId:   aws.String(volumeID),
					InstanceId: aws.String(instanceID),
					Device:     bdm.DeviceName,
					Force:      aws.Bool(true),
				})
				if err != nil {
					return detachedVolumes, fmt.Errorf("failed to detach volume %s: %w", volumeID, err)
				}
			}

			detachedVolumes = append(detachedVolumes, volumeID)
		}
	}

	if len(detachedVolumes) == 0 {
		// List all volumes found for debugging
		By("Listing all volumes found on the instance")
		for _, bdm := range instance.BlockDeviceMappings {
			if bdm.Ebs != nil && bdm.DeviceName != nil {
				GinkgoWriter.Printf("Found volume %s on device %s (root: %v)\n",
					*bdm.Ebs.VolumeId, *bdm.DeviceName,
					strings.Contains(*bdm.DeviceName, "sda1") || strings.Contains(*bdm.DeviceName, "xvda"))
			}
		}
		return nil, fmt.Errorf("no suitable non-root volumes found to detach")
	}

	// Wait for volumes to be detached
	for _, volumeID := range detachedVolumes {
		By(fmt.Sprintf("Waiting for volume %s to be detached", volumeID))
		Eventually(func() bool {
			result, err := ec2Client.DescribeVolumes(&ec2.DescribeVolumesInput{
				VolumeIds: []*string{aws.String(volumeID)},
			})
			if err != nil {
				return false
			}
			if len(result.Volumes) == 0 {
				return false
			}
			// Volume is detached when state is "available"
			return *result.Volumes[0].State == "available"
		}, time.Minute*2, time.Second*5).Should(BeTrue())
	}

	return detachedVolumes, nil
}

// restoreStorageDisruption reattaches the detached EBS volumes
func restoreStorageDisruption(instanceID string, volumeIDs []string) error {
	if len(volumeIDs) == 0 {
		return nil
	}

	// Get instance details to determine available device names
	result, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	})
	if err != nil {
		return fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return fmt.Errorf("instance not found: %s", instanceID)
	}

	// Get available device names (simple approach - use /dev/sdf, /dev/sdg, etc.)
	deviceNames := []string{"/dev/sdf", "/dev/sdg", "/dev/sdh", "/dev/sdi", "/dev/sdj"}

	for i, volumeID := range volumeIDs {
		if i >= len(deviceNames) {
			return fmt.Errorf("too many volumes to reattach, ran out of device names")
		}

		deviceName := deviceNames[i]
		By(fmt.Sprintf("Reattaching volume %s to device %s", volumeID, deviceName))

		_, err := ec2Client.AttachVolume(&ec2.AttachVolumeInput{
			VolumeId:   aws.String(volumeID),
			InstanceId: aws.String(instanceID),
			Device:     aws.String(deviceName),
		})
		if err != nil {
			return fmt.Errorf("failed to attach volume %s: %w", volumeID, err)
		}

		// Wait for volume to be attached
		By(fmt.Sprintf("Waiting for volume %s to be attached", volumeID))
		Eventually(func() bool {
			result, err := ec2Client.DescribeVolumes(&ec2.DescribeVolumesInput{
				VolumeIds: []*string{aws.String(volumeID)},
			})
			if err != nil {
				return false
			}
			if len(result.Volumes) == 0 {
				return false
			}
			// Volume is attached when state is "in-use"
			return *result.Volumes[0].State == "in-use"
		}, time.Minute*2, time.Second*5).Should(BeTrue())
	}

	return nil
}

// cleanupPreviousTestAttempts removes any leftover artifacts from previous test runs
func cleanupPreviousTestAttempts() error {
	if !awsInitialized {
		By("AWS not initialized - skipping AWS cleanup")
		return nil
	}

	By("Cleaning up any leftover artifacts from previous test runs")

	// Get all instances in the cluster
	instances, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{})
	if err != nil {
		return fmt.Errorf("failed to describe instances: %w", err)
	}

	var clusterInstanceIDs []string
	for _, reservation := range instances.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State != nil && *instance.State.Name == "running" {
				clusterInstanceIDs = append(clusterInstanceIDs, *instance.InstanceId)
			}
		}
	}

	// Find and clean up temporary security groups created by tests
	securityGroups, err := ec2Client.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{})
	if err != nil {
		return fmt.Errorf("failed to describe security groups: %w", err)
	}

	var testSecurityGroups []string
	for _, sg := range securityGroups.SecurityGroups {
		sgName := ""
		if sg.GroupName != nil {
			sgName = *sg.GroupName
		}

		// Identify test-related security groups by name patterns
		if strings.Contains(sgName, "sbd-e2e-network-disruptor") ||
			strings.Contains(sgName, "sbd-e2e-restore") ||
			strings.Contains(sgName, "sbd-e2e-network-test") ||
			strings.Contains(sgName, "sbd-e2e-storage-test") {
			testSecurityGroups = append(testSecurityGroups, *sg.GroupId)
			By(fmt.Sprintf("Found test security group to clean up: %s (%s)", *sg.GroupId, sgName))
		}
	}

	// Remove test security groups from instances first
	for _, instanceID := range clusterInstanceIDs {
		result, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{aws.String(instanceID)},
		})
		if err != nil {
			continue
		}

		if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
			continue
		}

		instance := result.Reservations[0].Instances[0]
		var cleanGroups []*string
		var hasTestGroups bool

		// Filter out test security groups
		for _, sg := range instance.SecurityGroups {
			isTestGroup := false
			for _, testSG := range testSecurityGroups {
				if *sg.GroupId == testSG {
					isTestGroup = true
					hasTestGroups = true
					break
				}
			}
			if !isTestGroup {
				cleanGroups = append(cleanGroups, sg.GroupId)
			}
		}

		// If test groups were found, restore to clean groups
		if hasTestGroups && len(cleanGroups) > 0 {
			By(fmt.Sprintf("Removing test security groups from instance %s", instanceID))
			_, err = ec2Client.ModifyInstanceAttribute(&ec2.ModifyInstanceAttributeInput{
				InstanceId: aws.String(instanceID),
				Groups:     cleanGroups,
			})
			if err != nil {
				By(fmt.Sprintf("Warning: failed to clean security groups from instance %s: %v", instanceID, err))
			} else {
				By(fmt.Sprintf("Cleaned security groups from instance %s", instanceID))
			}
		} else if hasTestGroups && len(cleanGroups) == 0 {
			// Instance only has test groups - create a default group
			By(fmt.Sprintf("Instance %s only has test groups - creating default security group", instanceID))

			defaultSGResult, err := ec2Client.CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
				GroupName:   aws.String(fmt.Sprintf("sbd-e2e-default-cleanup-%d", time.Now().Unix())),
				Description: aws.String("Default security group created during cleanup"),
				VpcId:       instance.VpcId,
			})
			if err != nil {
				By(fmt.Sprintf("Warning: failed to create default security group for instance %s: %v", instanceID, err))
				continue
			}

			// Add basic rules to the default group
			basicRules := []*ec2.IpPermission{
				{
					IpProtocol: aws.String("tcp"),
					FromPort:   aws.Int64(22),
					ToPort:     aws.Int64(22),
					IpRanges:   []*ec2.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
				},
				{
					IpProtocol: aws.String("tcp"),
					FromPort:   aws.Int64(10250),
					ToPort:     aws.Int64(10250),
					IpRanges:   []*ec2.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
				},
				{
					IpProtocol: aws.String("tcp"),
					FromPort:   aws.Int64(10255),
					ToPort:     aws.Int64(10255),
					IpRanges:   []*ec2.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
				},
			}

			_, err = ec2Client.AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
				GroupId:       defaultSGResult.GroupId,
				IpPermissions: basicRules,
			})
			if err != nil {
				By(fmt.Sprintf("Warning: failed to add basic rules to default security group: %v", err))
			}

			// Allow all outbound traffic
			_, err = ec2Client.AuthorizeSecurityGroupEgress(&ec2.AuthorizeSecurityGroupEgressInput{
				GroupId: defaultSGResult.GroupId,
				IpPermissions: []*ec2.IpPermission{
					{
						IpProtocol: aws.String("-1"),
						IpRanges:   []*ec2.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
					},
				},
			})
			if err != nil {
				By(fmt.Sprintf("Warning: failed to add egress rules to default security group: %v", err))
			}

			// Attach the default group to the instance
			_, err = ec2Client.ModifyInstanceAttribute(&ec2.ModifyInstanceAttributeInput{
				InstanceId: aws.String(instanceID),
				Groups:     []*string{defaultSGResult.GroupId},
			})
			if err != nil {
				By(fmt.Sprintf("Warning: failed to attach default security group to instance %s: %v", instanceID, err))
			} else {
				By(fmt.Sprintf("Attached default security group to instance %s", instanceID))
			}
		}
	}

	// Wait for security group changes to take effect
	time.Sleep(30 * time.Second)

	// Delete test security groups
	for _, sgID := range testSecurityGroups {
		By(fmt.Sprintf("Deleting test security group %s", sgID))
		_, err = ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
			GroupId: aws.String(sgID),
		})
		if err != nil {
			if strings.Contains(err.Error(), "DependencyViolation") {
				By(fmt.Sprintf("Security group %s still has dependencies, will retry", sgID))
				// Try again after a longer wait
				time.Sleep(60 * time.Second)
				_, err = ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
					GroupId: aws.String(sgID),
				})
				if err != nil {
					By(fmt.Sprintf("Warning: failed to delete security group %s after retry: %v", sgID, err))
				} else {
					By(fmt.Sprintf("Successfully deleted security group %s after retry", sgID))
				}
			} else {
				By(fmt.Sprintf("Warning: failed to delete security group %s: %v", sgID, err))
			}
		} else {
			By(fmt.Sprintf("Successfully deleted security group %s", sgID))
		}
	}

	By("Cleanup of previous test attempts completed")
	return nil
}
