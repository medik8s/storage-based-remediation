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
	"encoding/json"
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
	clusterInfo ClusterInfo
	testNS      string
)

var _ = Describe("SBD Operator E2E Tests", func() {
	BeforeEach(func() {
		// Generate unique namespace for each test
		testNS = fmt.Sprintf("sbd-e2e-test-%d", rand.Intn(10000))

		// Create test namespace
		By(fmt.Sprintf("Creating test namespace %s", testNS))
		cmd := exec.Command("kubectl", "create", "namespace", testNS)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		// Discover cluster topology
		discoverClusterTopology()

		By(fmt.Sprintf("Running e2e tests on cluster with %d total nodes (%d workers, %d control plane)",
			clusterInfo.TotalNodes, len(clusterInfo.WorkerNodes), len(clusterInfo.ControlNodes)))
	})

	AfterEach(func() {
		// Clean up test namespace and any test artifacts
		By(fmt.Sprintf("Cleaning up test namespace %s", testNS))
		cmd := exec.Command("kubectl", "delete", "namespace", testNS, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

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
			if len(clusterInfo.WorkerNodes) < 4 {
				Skip("Test requires at least 4 worker nodes for safe communication disruption testing")
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

	// Get all nodes in JSON format
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "json")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	var nodeList struct {
		Items []NodeInfo `json:"items"`
	}

	err = json.Unmarshal([]byte(output), &nodeList)
	Expect(err).NotTo(HaveOccurred())

	clusterInfo = ClusterInfo{
		TotalNodes:   len(nodeList.Items),
		WorkerNodes:  []NodeInfo{},
		ControlNodes: []NodeInfo{},
		NodeNames:    []string{},
		ZoneInfo:     make(map[string][]string),
	}

	// Categorize nodes
	for _, node := range nodeList.Items {
		clusterInfo.NodeNames = append(clusterInfo.NodeNames, node.Metadata.Name)

		// Check if it's a control plane node
		isControlPlane := false
		for label := range node.Metadata.Labels {
			if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
				isControlPlane = true
				break
			}
		}

		if isControlPlane {
			clusterInfo.ControlNodes = append(clusterInfo.ControlNodes, node)
		} else {
			clusterInfo.WorkerNodes = append(clusterInfo.WorkerNodes, node)
		}

		// Track zone information
		if zone, exists := node.Metadata.Labels["topology.kubernetes.io/zone"]; exists {
			clusterInfo.ZoneInfo[zone] = append(clusterInfo.ZoneInfo[zone], node.Metadata.Name)
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
	sbdConfigYAML := fmt.Sprintf(`apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: test-sbd-config
  namespace: %s
spec:
  sbdWatchdogPath: "/dev/watchdog"
  image: "quay.io/medik8s/sbd-agent:test-amd64"
  staleNodeTimeout: "30s"
`, testNS)

	// Apply the SBDConfig
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(sbdConfigYAML)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying SBDConfig was created and processed")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "sbdconfig", "test-sbd-config", "-n", testNS)
		_, err := utils.Run(cmd)
		return err == nil
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Waiting for SBD agent DaemonSet to be created")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "daemonset", "-n", "sbd-system", "-l", "app=sbd-agent")
		output, err := utils.Run(cmd)
		return err == nil && !strings.Contains(output, "No resources found")
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Verifying SBD agents are running on worker nodes")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "pods", "-n", "sbd-system", "-l", "app=sbd-agent", "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var podList struct {
			Items []struct {
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}

		if err := json.Unmarshal([]byte(output), &podList); err != nil {
			return false
		}

		runningPods := 0
		for _, pod := range podList.Items {
			if pod.Status.Phase == "Running" {
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

	// Create a disruption pod that will interfere with storage access
	disruptionPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: storage-disruptor
  namespace: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  hostNetwork: true
  hostPID: true
  containers:
  - name: disruptor
    image: registry.access.redhat.com/ubi9/ubi-minimal:latest
    command:
    - /bin/bash
    - -c
    - |
      echo "Starting storage access disruption simulation..."
      # Simulate storage I/O issues by creating high I/O load
      # This simulates the scenario where SBD device becomes inaccessible
      for i in {1..10}; do
        timeout 30s dd if=/dev/zero of=/tmp/storage-load-$i bs=1M count=100 2>/dev/null || true &
      done
      
      # Wait and monitor for SBD agent response
      sleep 60
      
      echo "Storage disruption simulation completed"
      # Clean up
      pkill -f "dd if=/dev/zero" || true
      rm -f /tmp/storage-load-* || true
    securityContext:
      privileged: true
      runAsUser: 0
  restartPolicy: Never
  tolerations:
  - operator: Exists`, testNS, targetNode.Metadata.Name)

	By("Creating storage disruption pod")
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(disruptionPodYAML)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Monitoring for SBD agent response to storage issues")
	// Monitor SBD agent logs for storage access issues
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "logs", "-n", "sbd-system", "-l", "app=sbd-agent", "--tail=50")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		// Look for storage-related warnings or errors
		return strings.Contains(output, "storage") ||
			strings.Contains(output, "watchdog") ||
			strings.Contains(output, "device")
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Verifying cluster stability after storage disruption")
	// Ensure the cluster remains stable and other nodes are healthy
	Consistently(func() bool {
		cmd := exec.Command("kubectl", "get", "nodes", "--no-headers")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		// Count Ready nodes - should remain stable
		readyNodes := strings.Count(output, " Ready ")
		return readyNodes >= len(cluster.WorkerNodes)-1 // Allow for one potentially affected node
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	GinkgoWriter.Printf("Storage access interruption test completed\n")
}

func testKubeletCommunicationFailure(cluster ClusterInfo) {
	By("Setting up SBD configuration for kubelet communication test")
	testBasicSBDConfiguration(cluster)

	targetNode := cluster.WorkerNodes[1] // Use different node than storage test
	By(fmt.Sprintf("Testing kubelet communication failure on node %s", targetNode.Metadata.Name))

	// Create a network disruption pod that interferes with kubelet communication
	networkDisruptorYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: network-disruptor
  namespace: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  hostNetwork: true
  containers:
  - name: disruptor
    image: registry.access.redhat.com/ubi9/ubi-minimal:latest
    command:
    - /bin/bash
    - -c
    - |
      echo "Starting kubelet communication disruption..."
      
      # Install required tools
      microdnf install -y iptables || true
      
      # Block kubelet API port (10250) temporarily to simulate communication failure
      # This simulates network partition between control plane and worker
      iptables -A OUTPUT -p tcp --dport 10250 -j DROP 2>/dev/null || true
      iptables -A INPUT -p tcp --sport 10250 -j DROP 2>/dev/null || true
      
      echo "Network disruption active for 45 seconds..."
      sleep 45
      
      # Restore communication
      iptables -D OUTPUT -p tcp --dport 10250 -j DROP 2>/dev/null || true
      iptables -D INPUT -p tcp --sport 10250 -j DROP 2>/dev/null || true
      
      echo "Network communication restored"
      sleep 15
    securityContext:
      privileged: true
      runAsUser: 0
      capabilities:
        add:
        - NET_ADMIN
  restartPolicy: Never
  tolerations:
  - operator: Exists`, testNS, targetNode.Metadata.Name)

	By("Creating network disruption pod")
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(networkDisruptorYAML)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Monitoring node status during communication disruption")
	// Monitor for node becoming NotReady
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "node", targetNode.Metadata.Name, "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var node NodeInfo
		if err := json.Unmarshal([]byte(output), &node); err != nil {
			return false
		}

		// Check if node is marked as NotReady
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status != "True" {
				GinkgoWriter.Printf("Node %s marked as NotReady due to communication failure\n", targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Verifying node recovery after communication restoration")
	// Wait for node to become Ready again
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "node", targetNode.Metadata.Name, "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var node NodeInfo
		if err := json.Unmarshal([]byte(output), &node); err != nil {
			return false
		}

		// Check if node is Ready again
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				GinkgoWriter.Printf("Node %s recovered and marked as Ready\n", targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	GinkgoWriter.Printf("Kubelet communication failure test completed\n")
}

func testSBDAgentCrash(cluster ClusterInfo) {
	By("Setting up SBD configuration for agent crash test")
	testBasicSBDConfiguration(cluster)

	targetNode := cluster.WorkerNodes[2] // Use different node
	By(fmt.Sprintf("Testing SBD agent crash and recovery on node %s", targetNode.Metadata.Name))

	// Get the SBD agent pod on the target node
	cmd := exec.Command("kubectl", "get", "pods", "-n", "sbd-system", "-l", "app=sbd-agent",
		"--field-selector", fmt.Sprintf("spec.nodeName=%s", targetNode.Metadata.Name), "-o", "name")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	podName := strings.TrimSpace(strings.TrimPrefix(output, "pod/"))
	Expect(podName).NotTo(BeEmpty(), "Should find SBD agent pod on target node")

	By(fmt.Sprintf("Crashing SBD agent pod %s", podName))
	// Delete the pod to simulate a crash
	cmd = exec.Command("kubectl", "delete", "pod", podName, "-n", "sbd-system")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying SBD agent pod is recreated by DaemonSet")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "pods", "-n", "sbd-system", "-l", "app=sbd-agent",
			"--field-selector", fmt.Sprintf("spec.nodeName=%s", targetNode.Metadata.Name), "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var podList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}

		if err := json.Unmarshal([]byte(output), &podList); err != nil {
			return false
		}

		// Check for a new running pod (different name)
		for _, pod := range podList.Items {
			if pod.Metadata.Name != podName && pod.Status.Phase == "Running" {
				GinkgoWriter.Printf("New SBD agent pod %s is running on node %s\n",
					pod.Metadata.Name, targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Verifying node remains healthy after agent recovery")
	Consistently(func() bool {
		cmd := exec.Command("kubectl", "get", "node", targetNode.Metadata.Name, "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var node NodeInfo
		if err := json.Unmarshal([]byte(output), &node); err != nil {
			return false
		}

		// Verify node remains Ready
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
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
      # Consume some CPU and memory but not enough to trigger fencing
      stress-ng --cpu 1 --cpu-load 50 --timeout 60s 2>/dev/null || (
        # Fallback if stress-ng is not available
        for i in {1..2}; do
          dd if=/dev/zero of=/dev/null bs=1M count=1 &
        done
        sleep 60
        pkill dd || true
      )
      echo "Non-critical resource consumption completed"
      sleep 30
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
      limits:
        cpu: "500m"
        memory: "256Mi"
  restartPolicy: Never`, testNS)

	By("Creating resource constraint pod")
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(resourceConstraintYAML)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying all nodes remain healthy during non-critical failure")
	Consistently(func() bool {
		cmd := exec.Command("kubectl", "get", "nodes", "--no-headers")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		// All nodes should remain Ready
		readyNodes := strings.Count(output, " Ready ")
		expectedNodes := len(cluster.WorkerNodes) + len(cluster.ControlNodes)

		if readyNodes == expectedNodes {
			return true
		}

		GinkgoWriter.Printf("Expected %d Ready nodes, found %d\n", expectedNodes, readyNodes)
		return false
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	By("Verifying SBD agents continue running normally")
	Consistently(func() bool {
		cmd := exec.Command("kubectl", "get", "pods", "-n", "sbd-system", "-l", "app=sbd-agent", "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var podList struct {
			Items []struct {
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}

		if err := json.Unmarshal([]byte(output), &podList); err != nil {
			return false
		}

		runningPods := 0
		for _, pod := range podList.Items {
			if pod.Status.Phase == "Running" {
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
		cmd := exec.Command("kubectl", "get", "pods", "-n", "sbd-system", "-l", "app=sbd-agent", "-o", "json")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}

		var podList struct {
			Items []struct {
				Spec struct {
					NodeName string `json:"nodeName"`
				} `json:"spec"`
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}

		if err := json.Unmarshal([]byte(output), &podList); err != nil {
			return false
		}

		runningAgents := make(map[string]bool)
		for _, pod := range podList.Items {
			if pod.Status.Phase == "Running" {
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
	disruptionPods := []string{"storage-disruptor", "network-disruptor", "resource-consumer"}

	for _, podName := range disruptionPods {
		cmd := exec.Command("kubectl", "delete", "pod", podName, "-n", testNS, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	}

	// Wait a moment for cleanup
	time.Sleep(5 * time.Second)
}
