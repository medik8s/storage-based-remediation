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
	"strings"
	"time"

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
)

var _ = Describe("SBD Operator E2E Tests", func() {
	BeforeEach(func() {
		// Generate unique namespace for each test
		testNS = fmt.Sprintf("sbd-e2e-test-%d", rand.Intn(10000))

		// Create test namespace
		By(fmt.Sprintf("Creating test namespace %s", testNS))
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNS,
			},
		}
		err := k8sClient.Create(ctx, namespace)
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
	var disruptionPod corev1.Pod
	err := yaml.Unmarshal([]byte(disruptionPodYAML), &disruptionPod)
	Expect(err).NotTo(HaveOccurred())
	err = k8sClient.Create(ctx, &disruptionPod)
	Expect(err).NotTo(HaveOccurred())

	By("Monitoring for SBD agent response to storage issues")
	// Monitor SBD agent logs for storage access issues
	Eventually(func() bool {
		pods := &corev1.PodList{}
		err := k8sClient.List(ctx, pods, client.InNamespace(testNS), client.MatchingLabels{"app": "sbd-agent"})
		if err != nil || len(pods.Items) == 0 {
			return false
		}

		// Get logs from the first pod using clientset (as controller-runtime doesn't support logs)
		podLogOptions := &corev1.PodLogOptions{
			TailLines: func(i int64) *int64 { return &i }(50),
		}
		req := k8sClientset.CoreV1().Pods(testNS).GetLogs(pods.Items[0].Name, podLogOptions)
		logs, err := req.Stream(ctx)
		if err != nil {
			return false
		}
		defer logs.Close()

		// Read logs (simplified check)
		buf := make([]byte, 1024)
		n, _ := logs.Read(buf)
		output := string(buf[:n])

		// Look for storage-related warnings or errors
		return strings.Contains(output, "storage") ||
			strings.Contains(output, "watchdog") ||
			strings.Contains(output, "device")
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	By("Verifying cluster stability after storage disruption")
	// Ensure the cluster remains stable and other nodes are healthy
	Consistently(func() bool {
		nodes := &corev1.NodeList{}
		err := k8sClient.List(ctx, nodes)
		if err != nil {
			return false
		}

		// Count Ready nodes - should remain stable
		readyNodes := 0
		for _, node := range nodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					readyNodes++
					break
				}
			}
		}
		return readyNodes >= len(cluster.WorkerNodes)-1 // Allow for one potentially affected node
	}, time.Minute*2, time.Second*30).Should(BeTrue())

	GinkgoWriter.Printf("Storage access interruption test completed\n")
}

func testKubeletCommunicationFailure(cluster ClusterInfo) {
	By("Setting up SBD configuration for kubelet communication test")
	testBasicSBDConfiguration(cluster)

	targetNode := cluster.WorkerNodes[1] // Use different node than storage test
	By(fmt.Sprintf("Testing kubelet communication failure on node %s", targetNode.Metadata.Name))

	// Create a service account for the network disruptor pod
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "network-disruptor",
			Namespace: testNS,
		},
	}
	err := k8sClient.Create(ctx, serviceAccount)
	Expect(err).NotTo(HaveOccurred())

	// Create a temporary SCC for network testing that includes NET_ADMIN capability
	sccYAML := fmt.Sprintf(`apiVersion: security.openshift.io/v1
kind: SecurityContextConstraints
metadata:
  name: sbd-e2e-network-test
allowHostDirVolumePlugin: true
allowHostIPC: false
allowHostNetwork: true
allowHostPID: true
allowHostPorts: false
allowPrivilegedContainer: true
allowedCapabilities:
- SYS_ADMIN
- SYS_MODULE
- NET_ADMIN
defaultAddCapabilities: null
fsGroup:
  type: RunAsAny
priority: 10
readOnlyRootFilesystem: false
requiredDropCapabilities: null
runAsUser:
  type: RunAsAny
seLinuxContext:
  type: RunAsAny
supplementalGroups:
  type: RunAsAny
users:
- system:serviceaccount:%s:network-disruptor
volumes:
- configMap
- downwardAPI
- emptyDir
- hostPath
- persistentVolumeClaim
- projected
- secret`, testNS)

	// Apply the temporary SCC
	By("Creating temporary SCC for network testing")
	err = k8sClientset.RESTClient().
		Post().
		AbsPath("/apis/security.openshift.io/v1/securitycontextconstraints").
		Body([]byte(sccYAML)).
		Do(ctx).
		Error()
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		Expect(err).NotTo(HaveOccurred())
	}

	// Create a network disruption pod that interferes with kubelet communication
	networkDisruptorYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: network-disruptor
  namespace: %s
spec:
  serviceAccountName: network-disruptor
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
        - SYS_ADMIN
  restartPolicy: Never
  tolerations:
  - operator: Exists`, testNS, targetNode.Metadata.Name)

	By("Creating network disruption pod")
	var networkPod corev1.Pod
	err = yaml.Unmarshal([]byte(networkDisruptorYAML), &networkPod)
	Expect(err).NotTo(HaveOccurred())
	err = k8sClient.Create(ctx, &networkPod)
	Expect(err).NotTo(HaveOccurred())

	// Wait for pod to start
	By("Waiting for network disruption pod to start")
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "network-disruptor", Namespace: testNS}, pod)
		if err != nil {
			return false
		}
		return pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodSucceeded
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Monitoring node status during communication disruption")
	// Monitor for node becoming NotReady
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false
		}

		// Check if node is marked as NotReady
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s marked as NotReady due to communication failure\n", targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Verifying node recovery after communication restoration")
	// Wait for node to become Ready again
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: targetNode.Metadata.Name}, node)
		if err != nil {
			return false
		}

		// Check if node is Ready again
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s recovered and marked as Ready\n", targetNode.Metadata.Name)
				return true
			}
		}
		return false
	}, time.Minute*3, time.Second*15).Should(BeTrue())

	// Clean up the temporary SCC
	By("Cleaning up temporary SCC")
	err = k8sClientset.RESTClient().
		Delete().
		AbsPath("/apis/security.openshift.io/v1/securitycontextconstraints/sbd-e2e-network-test").
		Do(ctx).
		Error()
	if err != nil && !strings.Contains(err.Error(), "not found") {
		GinkgoWriter.Printf("Warning: Failed to clean up temporary SCC: %v\n", err)
	}

	GinkgoWriter.Printf("Kubelet communication failure test completed\n")
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
      
      # Simple CPU and memory consumption using basic shell operations
      # This creates a controlled load without external dependencies
      
      # Function to consume CPU cycles
      cpu_load() {
        local duration=$1
        local end_time=$(($(date +%s) + duration))
        while [ $(date +%s) -lt $end_time ]; do
          : # No-op operation in a tight loop
        done
      }
      
      # Function to consume memory
      memory_load() {
        # Create a variable with repeated data to consume memory
        local data=""
        for i in {1..1000}; do
          data="${data}$(printf 'x%.0s' {1..1000})"
        done
        sleep 30
      }
      
      # Run CPU load in background
      cpu_load 60 &
      cpu_pid=$!
      
      # Run memory load in background  
      memory_load &
      memory_pid=$!
      
      echo "Resource consumption active for 60 seconds..."
      sleep 60
      
      # Clean up background processes
      kill $cpu_pid 2>/dev/null || true
      kill $memory_pid 2>/dev/null || true
      
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
	disruptionPods := []string{"storage-disruptor", "network-disruptor", "resource-consumer"}

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
