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
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
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
	clusterInfo ClusterInfo
	testNS      string
	awsSession  *session.Session
	ec2Client   *ec2.EC2
	awsRegion   string
)

var _ = Describe("SBD Operator E2E Tests", func() {
	BeforeEach(func() {
		// Generate unique namespace for each test
		testNS = fmt.Sprintf("sbd-e2e-test-%d", rand.Intn(10000))

		// Initialize AWS clients for disruption testing
		By("Initializing AWS clients")
		err := initAWS()
		if err != nil {
			Skip(fmt.Sprintf("Skipping AWS-based tests: %v", err))
		}

		// Create test namespace
		By(fmt.Sprintf("Creating test namespace %s", testNS))
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
			},
		}
		err = k8sClient.Create(ctx, namespace)
		Expect(err).NotTo(HaveOccurred())

		// Discover cluster topology
		discoverClusterTopology()

		By(fmt.Sprintf("Running e2e tests on cluster with %d total nodes (%d workers, %d control plane)",
			clusterInfo.TotalNodes, len(clusterInfo.WorkerNodes), len(clusterInfo.ControlNodes)))
	})

	AfterEach(func() {
		// Clean up test namespace and any test artifacts
		By(fmt.Sprintf("Cleaning up test namespace %s", testNS))
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
			},
		}
		err := k8sClient.Delete(ctx, namespace)
		if err != nil {
			// Ignore not found errors
			_, _ = fmt.Fprintf(GinkgoWriter, "Warning: failed to delete namespace %s: %v\n", testNS, err)
		}

		// Clean up any test pods or disruptions
		cleanupTestArtifacts()
	})

	Context("SBD E2E Failure Simulation Tests", func() {
		It("should handle basic SBD configuration and agent deployment", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testBasicSBDConfiguration(clusterInfo)
		})

		It("should trigger fencing when SBD agent loses storage access", func() {
			if len(clusterInfo.WorkerNodes) < 4 {
				Skip("Test requires at least 4 worker nodes for safe storage disruption testing")
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

func testBasicSBDConfiguration(cluster ClusterInfo) {
	By("Creating SBDConfig with proper agent deployment")
	sbdConfig := &medik8sv1alpha1.SBDConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sbd-config",
			Namespace: testNS,
		},
		Spec: medik8sv1alpha1.SBDConfigSpec{
			SbdWatchdogPath:  "/dev/watchdog",
			StaleNodeTimeout: &metav1.Duration{Duration: 90 * time.Second},
		},
	}

	// Create the SBDConfig
	err := k8sClient.Create(ctx, sbdConfig)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying SBDConfig was created and processed")
	Eventually(func() bool {
		retrievedConfig := &medik8sv1alpha1.SBDConfig{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      "test-sbd-config",
			Namespace: testNS,
		}, retrievedConfig)
		return err == nil
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Displaying SBDConfig in YAML format")
	Eventually(func() string {
		retrievedConfig := &medik8sv1alpha1.SBDConfig{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      "test-sbd-config",
			Namespace: testNS,
		}, retrievedConfig)
		if err != nil {
			return ""
		}

		// Convert to YAML
		yamlData, err := yaml.Marshal(retrievedConfig)
		if err != nil {
			return ""
		}
		return string(yamlData)
	}, time.Minute*2, time.Second*10).Should(Not(BeEmpty()))

	// Get and display the final YAML
	retrievedConfig := &medik8sv1alpha1.SBDConfig{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      "test-sbd-config",
		Namespace: testNS,
	}, retrievedConfig)
	Expect(err).NotTo(HaveOccurred())

	yamlData, err := yaml.Marshal(retrievedConfig)
	Expect(err).NotTo(HaveOccurred())

	GinkgoWriter.Printf("SBDConfig YAML:\n%s\n", string(yamlData))

	By("Waiting for SBD agent DaemonSet to be created")
	var lastSBDConfigStatus medik8sv1alpha1.SBDConfigStatus
	Eventually(func() bool {
		// First, check if the SBDConfig has been processed by the controller
		retrievedConfig := &medik8sv1alpha1.SBDConfig{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      "test-sbd-config",
			Namespace: testNS,
		}, retrievedConfig)
		if err != nil {
			GinkgoWriter.Printf("Error retrieving SBDConfig: %v\n", err)
			return false
		}

		lastSBDConfigStatus = retrievedConfig.Status
		GinkgoWriter.Printf("SBDConfig Status: ReadyNodes=%d, TotalNodes=%d, Generation=%d, Conditions=%d\n",
			retrievedConfig.Status.ReadyNodes, retrievedConfig.Status.TotalNodes, retrievedConfig.Generation, len(retrievedConfig.Status.Conditions))

		// Print condition details
		for _, condition := range retrievedConfig.Status.Conditions {
			GinkgoWriter.Printf("  Condition: Type=%s, Status=%s, Reason=%s, Message=%s\n",
				condition.Type, condition.Status, condition.Reason, condition.Message)
		}

		// Check if controller is running by looking for controller manager deployment
		deployments := &appsv1.DeploymentList{}
		err = k8sClient.List(ctx, deployments, client.InNamespace("sbd-operator-system"), client.MatchingLabels{"control-plane": "controller-manager"})
		if err == nil && len(deployments.Items) > 0 {
			deployment := deployments.Items[0]
			GinkgoWriter.Printf("Controller deployment found: %s, Ready replicas: %d/%d\n",
				deployment.Name, deployment.Status.ReadyReplicas, deployment.Status.Replicas)
		} else {
			GinkgoWriter.Printf("Controller deployment not found or error: %v\n", err)
		}

		daemonSets := &appsv1.DaemonSetList{}
		err = k8sClient.List(ctx, daemonSets, client.InNamespace(testNS), client.MatchingLabels{"app": "sbd-agent"})

		// Always list all DaemonSets in the test namespace for debugging
		By("Listing all DaemonSets in the test namespace")
		daemonSetsT := &appsv1.DaemonSetList{}
		errT := k8sClient.List(ctx, daemonSetsT, client.InNamespace(testNS))
		if errT == nil {
			GinkgoWriter.Printf("Found %d DaemonSets in namespace %s:\n", len(daemonSetsT.Items), testNS)
			for i, ds := range daemonSetsT.Items {
				GinkgoWriter.Printf("  %d. Name: %s, Labels: %v, Desired: %d, Current: %d, Ready: %d\n",
					i+1, ds.Name, ds.Labels, ds.Status.DesiredNumberScheduled, ds.Status.CurrentNumberScheduled, ds.Status.NumberReady)
			}
		} else {
			GinkgoWriter.Printf("Error listing DaemonSets in namespace %s: %v\n", testNS, errT)
		}

		// Log the specific search results
		if err == nil {
			GinkgoWriter.Printf("Found %d DaemonSets with label app=sbd-agent in namespace %s\n", len(daemonSets.Items), testNS)
		} else {
			GinkgoWriter.Printf("Error searching for DaemonSets with label app=sbd-agent: %v\n", err)
		}

		return err == nil && len(daemonSets.Items) > 0
	}, time.Minute*6, time.Second*15).Should(BeTrue())

	GinkgoWriter.Printf("Final SBDConfig Status: %+v\n", lastSBDConfigStatus)

	By("Verifying SBD agents are running on worker nodes")
	Eventually(func() bool {
		pods := &corev1.PodList{}
		err := k8sClient.List(ctx, pods, client.InNamespace(testNS), client.MatchingLabels{"app": "sbd-agent"})
		if err != nil {
			return false
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods++
			}
		}

		// Expect at least 2 running pods (minimum for meaningful SBD testing)
		return runningPods >= 2
	}, time.Minute*5, time.Second*15).Should(BeTrue())

	GinkgoWriter.Printf("SBD configuration test completed successfully\n")
}

func testStorageAccessInterruption(cluster ClusterInfo) {
	By("Setting up SBD configuration for storage access test")
	testBasicSBDConfiguration(cluster)

	targetNode := cluster.WorkerNodes[0]
	By(fmt.Sprintf("Testing storage access interruption on node %s", targetNode.Metadata.Name))

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

	// Verify SBD remediation is triggered for storage failure
	By("Verifying SBD remediation is triggered for storage failure")
	Eventually(func() bool {
		remediations := &medik8sv1alpha1.SBDRemediationList{}
		err := k8sClient.List(ctx, remediations, client.InNamespace(testNS))
		if err != nil {
			return false
		}

		for _, remediation := range remediations.Items {
			if remediation.Spec.NodeName == targetNode.Metadata.Name {
				By(fmt.Sprintf("SBD remediation created for node %s: %+v", targetNode.Metadata.Name, remediation.Status))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*30).Should(BeTrue())

	// Restore storage early to allow recovery testing
	By("Restoring storage access to test node recovery")
	err = restoreStorageDisruption(instanceID, detachedVolumes)
	Expect(err).NotTo(HaveOccurred())
	detachedVolumes = nil // Prevent double cleanup in defer

	// Wait for node to potentially recover (though it may need manual intervention)
	By("Monitoring node recovery after storage restoration")
	time.Sleep(60 * time.Second) // Give time for recovery

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
	By("Setting up SBD configuration for kubelet communication test")
	testBasicSBDConfiguration(cluster)

	targetNode := cluster.WorkerNodes[1] // Use different node than storage test
	By(fmt.Sprintf("Testing kubelet communication failure on node %s", targetNode.Metadata.Name))

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
			return false
		}

		// Check if node is NotReady
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s is now NotReady: %s", targetNode.Metadata.Name, condition.Reason))
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	// Verify SBD remediation is triggered
	By("Verifying SBD remediation is triggered for the disrupted node")
	Eventually(func() bool {
		remediations := &medik8sv1alpha1.SBDRemediationList{}
		err := k8sClient.List(ctx, remediations, client.InNamespace(testNS))
		if err != nil {
			return false
		}

		for _, remediation := range remediations.Items {
			if remediation.Spec.NodeName == targetNode.Metadata.Name {
				By(fmt.Sprintf("SBD remediation created for node %s: %+v", targetNode.Metadata.Name, remediation.Status))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*30).Should(BeTrue())

	// Remove network disruption early to allow recovery
	By("Removing network disruption to allow node recovery")
	err = removeNetworkDisruption(securityGroupID, instanceID)
	Expect(err).NotTo(HaveOccurred())
	securityGroupID = nil // Prevent double cleanup in defer

	// Wait for node to recover
	By("Waiting for node to recover after network disruption is removed")
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false
		}

		// Check if node is Ready again
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				By(fmt.Sprintf("Node %s has recovered and is Ready", targetNode.Metadata.Name))
				return true
			}
		}
		return false
	}, time.Minute*5, time.Second*30).Should(BeTrue())

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

	targetNode := cluster.WorkerNodes[2] // Use different node
	By(fmt.Sprintf("Testing SBD agent crash and recovery on node %s", targetNode.Metadata.Name))

	// Get the SBD agent pod on the target node
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(testNS),
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
			client.InNamespace(testNS),
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
  restartPolicy: Never`, testNS)

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
		err := k8sClient.List(ctx, pods, client.InNamespace(testNS), client.MatchingLabels{"app": "sbd-agent"})
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
		err := k8sClient.List(ctx, pods, client.InNamespace(testNS), client.MatchingLabels{"app": "sbd-agent"})
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
				Namespace: testNS,
			},
		}
		_ = k8sClient.Delete(ctx, pod)
	}

	// Clean up temporary SCCs that might be left over from failed tests
	tempSCCs := []string{"sbd-e2e-network-test", "sbd-e2e-storage-test"}
	for _, sccName := range tempSCCs {
		err := k8sClientset.RESTClient().
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

// initAWS initializes AWS session and clients
func initAWS() error {
	// Get AWS region from environment or detect from node names
	awsRegion = os.Getenv("AWS_REGION")
	if awsRegion == "" {
		awsRegion = "ap-southeast-2" // Default based on node names
	}

	var err error
	awsSession, err = session.NewSession(&aws.Config{
		Region: aws.String(awsRegion),
	})
	if err != nil {
		return fmt.Errorf("failed to create AWS session: %w", err)
	}

	ec2Client = ec2.New(awsSession)
	return nil
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

// createNetworkDisruption creates a temporary security group rule to block traffic
func createNetworkDisruption(instanceID string) (*string, error) {
	// Get instance details
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
	vpcID := instance.VpcId

	// Create a temporary security group that blocks kubelet traffic
	sgResult, err := ec2Client.CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("sbd-e2e-network-disruptor-%d", time.Now().Unix())),
		Description: aws.String("Temporary security group for SBD e2e network disruption testing"),
		VpcId:       vpcID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create security group: %w", err)
	}

	securityGroupID := sgResult.GroupId

	// Add rule to block all outbound traffic (more effective than just kubelet)
	_, err = ec2Client.RevokeSecurityGroupEgress(&ec2.RevokeSecurityGroupEgressInput{
		GroupId: securityGroupID,
		IpPermissions: []*ec2.IpPermission{
			{
				IpProtocol: aws.String("-1"), // All protocols
				IpRanges: []*ec2.IpRange{
					{
						CidrIp: aws.String("0.0.0.0/0"),
					},
				},
			},
		},
	})
	if err != nil {
		// Clean up security group if rule creation fails
		ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
			GroupId: securityGroupID,
		})
		return nil, fmt.Errorf("failed to remove default egress rule: %w", err)
	}

	// Get current security groups and add our disruptor group
	var allGroups []*string
	for _, sg := range instance.SecurityGroups {
		allGroups = append(allGroups, sg.GroupId)
	}
	allGroups = append(allGroups, securityGroupID)

	// Attach security group to instance (add to existing groups)
	_, err = ec2Client.ModifyInstanceAttribute(&ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		Groups:     allGroups,
	})
	if err != nil {
		// Clean up security group if attachment fails
		ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
			GroupId: securityGroupID,
		})
		return nil, fmt.Errorf("failed to attach security group: %w", err)
	}

	return securityGroupID, nil
}

// removeNetworkDisruption removes the temporary security group
func removeNetworkDisruption(securityGroupID *string, instanceID string) error {
	if securityGroupID == nil {
		return nil
	}

	// Get instance details to find current security groups
	result, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	})
	if err != nil {
		return fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return fmt.Errorf("instance not found: %s", instanceID)
	}

	instance := result.Reservations[0].Instances[0]
	var originalGroups []*string
	var hasDisruptorGroup bool

	// Find all security groups except our disruptor group
	for _, sg := range instance.SecurityGroups {
		if *sg.GroupId != *securityGroupID {
			originalGroups = append(originalGroups, sg.GroupId)
		} else {
			hasDisruptorGroup = true
		}
	}

	// Only proceed if the disruptor security group is actually attached
	if hasDisruptorGroup {
		// Restore original security groups (this removes the disruptor group)
		if len(originalGroups) > 0 {
			By(fmt.Sprintf("Restoring original security groups for instance %s", instanceID))
			_, err = ec2Client.ModifyInstanceAttribute(&ec2.ModifyInstanceAttributeInput{
				InstanceId: aws.String(instanceID),
				Groups:     originalGroups,
			})
			if err != nil {
				return fmt.Errorf("failed to restore original security groups: %w", err)
			}

			// Wait a moment for the security group to be detached
			time.Sleep(10 * time.Second)
		}
	}

	// Delete the temporary security group
	By(fmt.Sprintf("Deleting temporary security group %s", *securityGroupID))
	_, err = ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
		GroupId: securityGroupID,
	})
	if err != nil {
		// If it still fails due to dependency, wait a bit longer and retry
		if strings.Contains(err.Error(), "DependencyViolation") {
			By("Security group still has dependencies, waiting longer before retry...")
			time.Sleep(30 * time.Second)
			_, err = ec2Client.DeleteSecurityGroup(&ec2.DeleteSecurityGroupInput{
				GroupId: securityGroupID,
			})
			if err != nil {
				return fmt.Errorf("failed to delete security group after retry: %w", err)
			}
		} else {
			return fmt.Errorf("failed to delete security group: %w", err)
		}
	}

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
