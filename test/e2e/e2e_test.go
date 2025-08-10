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
	gomegatypes "github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
		By(fmt.Sprintf("Running e2e tests on cluster with %d total nodes (%d workers, %d control plane)",
			clusterInfo.TotalNodes, len(clusterInfo.WorkerNodes), len(clusterInfo.ControlNodes)))
	})

	AfterAll(func() {
	})

	AfterEach(func() {
	})

	Context("SBD E2E Failure Simulation Tests", func() {

		It("should handle basic SBD configuration and agent deployment", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testBasicSBDConfiguration()
		})

		It("should inspect SBD node mapping and device state", func() {
			testSBDInspection()
		})

		It("should handle fake remediation CRs", func() {
			testFakeRemediation()
		})

		It("should handle node remediation", func() {
			if len(clusterInfo.WorkerNodes) < 2 {
				Skip("Test requires at least 2 worker nodes")
			}
			testNodeRemediation(clusterInfo)
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

		It("should reject incompatible storage classes", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testIncompatibleStorageClass()
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

func testBasicSBDConfiguration() {
	By("Creating SBDConfig with proper agent deployment")

	// Look for a storage class that supports RWX (ReadWriteMany) access mode
	By("Looking for RWX-compatible storage class")
	storageClasses := &storagev1.StorageClassList{}
	err := k8sClient.List(ctx, storageClasses)
	Expect(err).NotTo(HaveOccurred())

	var rwxStorageClass *storagev1.StorageClass
	for _, sc := range storageClasses.Items {
		// Check if this storage class supports RWX access mode using known provisioners
		if isRWXCompatibleProvisioner(sc.Provisioner) {
			rwxStorageClass = &sc
			GinkgoWriter.Printf("Found RWX-compatible storage class: %s (provisioner: %s)\n", sc.Name, sc.Provisioner)
			break
		}
	}

	if rwxStorageClass == nil {
		Skip("No RWX-compatible storage classes found - skipping storage-dependent tests")
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
	Expect(err).NotTo(HaveOccurred(), "SBDConfig creation failed")

	validator := testNamespace.NewSBDAgentValidator()
	opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
	opts.ExpectedArgs = []string{
		"--watchdog-path=/dev/watchdog",
		"--watchdog-timeout=1m30s",
	}
	err = validator.ValidateAgentDeployment(opts)
	Expect(err).NotTo(HaveOccurred(), "SBD agent deployment failed")
}

// isRWXCompatibleProvisioner checks if a CSI provisioner is known to support ReadWriteMany
func isRWXCompatibleProvisioner(provisioner string) bool {
	// Known RWX-compatible provisioners
	rwxProvisioners := map[string]bool{
		// AWS
		"efs.csi.aws.com": true,

		// Azure
		"file.csi.azure.com": true,

		// GCP
		"filestore.csi.storage.gke.io": true,

		// NFS
		"nfs.csi.k8s.io": true,
		"cluster.local/nfs-subdir-external-provisioner": true,
		"k8s-sigs.io/nfs-subdir-external-provisioner":   true,

		// CephFS
		"cephfs.csi.ceph.com":                    true,
		"openshift-storage.cephfs.csi.ceph.com": true,

		// GlusterFS
		"gluster.org/glusterfs": true,

		// Other known RWX provisioners
		"nfs-provisioner": true,
		"csi-nfsplugin":   true,
	}

	return rwxProvisioners[provisioner]
}

func testIncompatibleStorageClass() {
	By("Testing SBD controller rejection of incompatible storage classes")

	// First, create a gp3-csi storage class (EBS - ReadWriteOnce only)
	By("Creating a gp3-csi storage class that only supports ReadWriteOnce")
	gp3StorageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("gp3-csi-test-%d", time.Now().Unix()),
		},
		Provisioner: "ebs.csi.aws.com",
		Parameters: map[string]string{
			"type": "gp3",
		},
		AllowVolumeExpansion: &[]bool{true}[0],
	}

	err := k8sClient.Create(ctx, gp3StorageClass)
	Expect(err).NotTo(HaveOccurred())

	// Ensure cleanup happens
	defer func() {
		By("Cleaning up test storage class")
		err := k8sClient.Delete(ctx, gp3StorageClass)
		if err != nil {
			GinkgoWriter.Printf("Warning: failed to clean up test storage class: %v\n", err)
		}
	}()

	By("Creating SBDConfig with incompatible storage class")
	sbdConfig, err := testNamespace.CreateSBDConfig("test-bad-storage-class", func(config *medik8sv1alpha1.SBDConfig) {
		config.Spec.SbdWatchdogPath = "/dev/watchdog"
		config.Spec.SharedStorageClass = gp3StorageClass.Name
		config.Spec.StaleNodeTimeout = &metav1.Duration{Duration: 2 * time.Hour}
		config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
	})

	By("Expecting SBDConfig creation to succeed initially")
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for controller to detect storage class incompatibility")
	// The controller should detect the incompatible storage class and report an error
	Eventually(func() bool {
		// Check events for storage class validation errors
		events := &corev1.EventList{}
		err := k8sClient.List(ctx, events, client.InNamespace(testNamespace.Name))
		if err != nil {
			return false
		}

		for _, event := range events.Items {
			if event.Type == "Warning" &&
				strings.Contains(event.Reason, "PVCError") &&
				strings.Contains(event.Message, "ReadWriteMany") {
				By(fmt.Sprintf("Found expected storage class validation error: %s", event.Message))
				return true
			}
		}
		return false
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	By("Verifying PVC was not created due to storage class incompatibility")
	// The PVC should not be created because the storage class validation failed
	pvc := &corev1.PersistentVolumeClaim{}
	pvcName := sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name)
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      pvcName,
		Namespace: testNamespace.Name,
	}, pvc)

	// We expect the PVC to not exist or be in a failed state
	if err != nil {
		By("PVC was not created (expected due to storage class incompatibility)")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	} else {
		By("PVC exists but should be in Pending state due to unsupported access mode")
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimPending))
	}

	By("Verifying SBD agents are not deployed due to storage validation failure")
	// The DaemonSet should not be created or should have 0 ready replicas
	daemonSet := &appsv1.DaemonSet{}
	daemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfig.Name)
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      daemonSetName,
		Namespace: testNamespace.Name,
	}, daemonSet)

	// DaemonSet may not exist at all, or may exist but have 0 ready replicas
	if err != nil {
		By("DaemonSet was not created (expected due to storage validation failure)")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	} else {
		By("DaemonSet exists but should have 0 ready replicas due to storage validation failure")
		Expect(daemonSet.Status.NumberReady).To(Equal(int32(0)))
	}

	GinkgoWriter.Printf("Incompatible storage class test completed successfully\n")
}

func getNodeBootIDs(cluster ClusterInfo) map[string]string {
	bootIDs := make(map[string]string)
	for _, node := range cluster.WorkerNodes {
		bootIDs[node.Metadata.Name] = getNodeBootID(node.Metadata.Name)
	}
	return bootIDs
}

func getNodeBootID(nodeName string) string {
	node := &corev1.Node{}
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err == nil {
			return node.Status.NodeInfo.BootID != ""
		}
		return false
	}, time.Minute*1, time.Second*10).Should(BeTrue())
	return node.Status.NodeInfo.BootID
}

func checkNodeReboot(nodeName, reason, originalBootTime string, timeout time.Duration, target bool) {
	// Verify that the node has not rebooted during the network disruption
	rebootText := ""
	if !target {
		rebootText = "not "
	}
	By(fmt.Sprintf("Verifying node %s has %srebooted %s", nodeName, rebootText, reason))
	result := Eventually(func() bool {
		currentBootID := getNodeBootID(nodeName)
		if originalBootTime != "" && currentBootID != originalBootTime {
			GinkgoWriter.Printf("Node %s boot ID changed - node has rebooted: %v -> %v\n",
				nodeName, originalBootTime, currentBootID)
			return true
		}
		return false
	}, timeout, time.Second*15)

	resultText := fmt.Sprintf("Node %s should %shave rebooted %s", nodeName, rebootText, reason)
	if target {
		result.Should(BeTrue(), resultText)
	} else {
		result.Should(BeFalse(), resultText)
	}

	if target {
		// Wait longer for node to come back online after reboot
		By(fmt.Sprintf("Waiting for node %s to come back online after reboot", nodeName))
		Eventually(func() bool {
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err != nil {
				return false // Node still not reachable
			}

			// Check if node is Ready again
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					GinkgoWriter.Printf("Node %s has come back online after reboot\n", nodeName)
					return true
				}
			}
			return false
		}, time.Minute*10, time.Second*30).Should(BeTrue())
	}
}

func checkNodeNotReady(nodeName, reason string, timeout time.Duration, enforceFn func() gomegatypes.GomegaMatcher) {
	By(fmt.Sprintf("Checking that node %s %s", nodeName, reason))
	result := Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			GinkgoWriter.Printf("Node %s is not found: %s\n", nodeName, err)
			return false
		}

		// Check if node is NotReady or has storage-related issues
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s now has condition %v: %s - %s\n",
					nodeName, condition.Type, condition.Status, condition.Reason)
				return true
			} else if condition.Type == corev1.NodeReady {
				GinkgoWriter.Printf("Node %s now has condition %v: %s\n", nodeName, condition.Type, condition.Status)
			}
		}
		return false
	}, timeout, time.Second*20)
	if enforceFn != nil {
		result.Should(enforceFn(), fmt.Sprintf("Node %s should %s", nodeName, reason))
	}
}

func testStorageAccessInterruption(cluster ClusterInfo) {
	// Skip if AWS is not available
	if !awsInitialized {
		Skip("Storage access interruption test requires AWS - skipping")
	}

	By("Setting up SBD configuration for storage access test")
	testBasicSBDConfiguration()

	// Select a random actual worker node for testing (not control plane)
	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing storage access interruption on verified worker node %s", targetNode.Metadata.Name))

	// Node should self-fence when it loses storage access (no SBDRemediation CR needed)
	// Wait for node to actually panic/reboot due to storage loss (self-fencing)
	By("Obtaining the original boot time of the node")
	originalBootTimes := getNodeBootIDs(cluster)

	// Get AWS instance ID for the target node
	instanceID, err := getInstanceIDFromNode(targetNode.Metadata.Name)
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("Target node %s has AWS instance ID: %s\n", targetNode.Metadata.Name, instanceID)

	// Create storage disruption by blocking network access to shared storage
	By("Creating AWS storage disruption by blocking network access to shared storage")
	disruptorPods, err := createStorageDisruption(instanceID)
	if err != nil {
		// If storage disruption cannot be created, skip this test
		Skip(fmt.Sprintf("Skipping storage disruption test: %v", err))
	}
	GinkgoWriter.Printf("Created %d storage disruptor pods for node %s\n", len(disruptorPods), targetNode.Metadata.Name)

	// Ensure cleanup happens even if test fails
	// defer func() {
	// 	By("Cleaning up AWS storage disruption")
	// 	err := restoreStorageDisruption(instanceID, disruptorPods)
	// 	if err != nil {
	// 		GinkgoWriter.Printf("Warning: failed to restore storage disruption: %v\n", err)
	// 	} else {
	// 		By("Successfully restored network access to shared storage")
	// 	}
	// }()

	// Wait for network-level storage disruption to take effect
	By("Waiting for storage disruption to take effect")
	time.Sleep(30 * time.Second)

	// Monitor for node becoming NotReady due to loss of shared storage access
	checkNodeNotReady(targetNode.Metadata.Name, "becomes NotReady due to loss of shared storage access",
		time.Minute*8, nil)
	// BeTrue()

	// Monitor for node disappearing (panic/reboot) or boot ID change
	checkNodeReboot(targetNode.Metadata.Name, "during storage disruption",
		originalBootTimes[targetNode.Metadata.Name], time.Minute*2, true)

	// Verify node recovery (instead of the old immediate recovery test)
	By("Verifying node has fully recovered after fencing and shared storage restoration")
	time.Sleep(30 * time.Second) // Give additional time for full recovery

	// Verify other nodes remained stable during network-level storage disruption
	By("Verifying other nodes remained stable during network-level storage disruption")
	for _, node := range cluster.WorkerNodes {
		if node.Metadata.Name == targetNode.Metadata.Name {
			continue // Skip the target node
		}
		checkNodeReboot(node.Metadata.Name, "during storage disruption",
			originalBootTimes[node.Metadata.Name], time.Second, false)
	}

	GinkgoWriter.Printf("Network-level storage access interruption test completed\n")
}

func testKubeletCommunicationFailure(cluster ClusterInfo) {

	By("Setting up SBD configuration for kubelet communication test")
	testBasicSBDConfiguration()

	// Select a random actual worker node for testing (not control plane)
	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing kubelet communication failure on verified worker node %s", targetNode.Metadata.Name))

	// Get AWS instance ID for the target node
	instanceID, err := getInstanceIDFromNode(targetNode.Metadata.Name)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Target node %s has AWS instance ID: %s", targetNode.Metadata.Name, instanceID))

	originalBootTimes := getNodeBootIDs(cluster)

	// Create network disruption using AWS security groups
	By("Disrupting kubelet communication")
	disruptionPodName, err := createNetworkDisruption(targetNode.Metadata.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(disruptionPodName).NotTo(BeNil())

	defer func() {
		By(fmt.Sprintf("Initiating deletion of disruptor pod %v...", disruptionPodName))
		// Try to delete the disruptor pod so that it isn't restarted when the node becomes Ready
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      *disruptionPodName,
				Namespace: "default",
			},
		}
		err = k8sClient.Delete(ctx, pod)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for kubelet to be stopped and node to become NotReady
	By("Waiting for node to become NotReady due to kubelet termination...")
	checkNodeNotReady(targetNode.Metadata.Name, "becomes NotReady due to kubelet termination",
		time.Minute*8, BeTrue)

	checkNodeReboot(targetNode.Metadata.Name, "due to kubelet termination",
		originalBootTimes[targetNode.Metadata.Name], time.Minute*2, false)

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
	checkNodeReboot(targetNode.Metadata.Name, "due to remediation CR",
		originalBootTimes[targetNode.Metadata.Name], time.Minute*10, true)

	// Verify node recovery (instead of the old immediate recovery test)
	GinkgoWriter.Printf("Waiting for the cluster to stabilize after remediation\n")
	time.Sleep(30 * time.Second)

	// Verify other nodes remain stable during the disruption
	By("Verifying other nodes remained stable during network disruption")
	for _, node := range cluster.WorkerNodes {
		if node.Metadata.Name == targetNode.Metadata.Name {
			continue // Skip the target node
		}
		checkNodeReboot(node.Metadata.Name, "due to remediation CR",
			originalBootTimes[node.Metadata.Name], time.Second, false)
	}

	GinkgoWriter.Printf("kubelet-based communication failure test completed successfully\n")
}

func testFakeRemediation() {
	By("Setting up SBD configuration for remediation loop test")
	testBasicSBDConfiguration()

	// Create SBDRemediation CR to simulate external operator (e.g., Node Healthcheck Operator)
	By("Creating SBDRemediation CR to simulate external operator behavior")
	sbdRemediation := &medik8sv1alpha1.SBDRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("network-remediation-%s", "fake-node"),
			Namespace: testNamespace.Name,
		},
		Spec: medik8sv1alpha1.SBDRemediationSpec{
			NodeName:       "fake-node",
			Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
			TimeoutSeconds: 300, // 5 minutes timeout for fencing
		},
	}
	err := k8sClient.Create(ctx, sbdRemediation)
	Expect(err).NotTo(HaveOccurred())
	By(fmt.Sprintf("Created SBDRemediation CR for node %s", "fake-node"))

	// Verify SBD remediation is triggered and processed
	By("Verifying SBD remediation is triggered and processed for the disrupted node")
	Eventually(func() bool {
		remediations := &medik8sv1alpha1.SBDRemediationList{}
		err := k8sClient.List(ctx, remediations, client.InNamespace(testNamespace.Name))
		if err != nil {
			return false
		}

		for _, remediation := range remediations.Items {
			if remediation.Spec.NodeName != "fake-node" {
				By(fmt.Sprintf("SBD remediation found for node %s: %+v", "fake-node", remediation.Status))
				return false
			}
		}

		expectedLogs := []string{
			"Starting SBDRemediation reconciliation",
			"Starting fencing operation",
			"Fencing operation completed successfully",
		}

		agentPods := &corev1.PodList{}
		err = k8sClient.List(ctx, agentPods, client.InNamespace(testNamespace.Name),
			client.MatchingLabels{"app": "sbd-agent"})
		Expect(err).NotTo(HaveOccurred())
		for _, agentPod := range agentPods.Items {
			req := testNamespace.Clients.Clientset.CoreV1().Pods(testNamespace.Name).GetLogs(agentPod.Name,
				&corev1.PodLogOptions{})
			podLogs, err := req.Stream(testNamespace.Clients.Context)
			if err == nil {
				defer func() { _ = podLogs.Close() }()
				buf := new(bytes.Buffer)
				_, _ = io.Copy(buf, podLogs)

				allFound := true
				for _, log := range expectedLogs {
					if !strings.Contains(buf.String(), log) {
						allFound = false
					} else {
						GinkgoWriter.Printf("Agent pod %s has log: %s\n", agentPod.Name, log)
					}
				}
				if allFound {
					GinkgoWriter.Printf("Agent pod %s has all expected logs\n", agentPod.Name)
					return true
				}
			} else {
				GinkgoWriter.Printf("Agent pod %s has no logs: %v\n", agentPod.Name, err)
			}
		}
		return false
	}, time.Minute*3, time.Second*30).Should(BeTrue())
}

func testNodeRemediation(cluster ClusterInfo) {
	By("Setting up SBD configuration for node remediation test")
	testBasicSBDConfiguration()

	// Select a random actual worker node for testing (not control plane)
	targetNode := selectActualWorkerNode(cluster)
	originalBootTimes := getNodeBootIDs(cluster)

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
	err := k8sClient.Create(ctx, sbdRemediation)
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
	checkNodeReboot(targetNode.Metadata.Name, "due to remediation CR",
		originalBootTimes[targetNode.Metadata.Name], time.Minute*10, true)

	// Verify node recovery (instead of the old immediate recovery test)
	GinkgoWriter.Printf("Waiting for the cluster to stabilize after remediation\n")
	time.Sleep(30 * time.Second)

	// Verify other nodes remain stable during the disruption
	By("Verifying other nodes remained stable during network disruption")
	for _, node := range cluster.WorkerNodes {
		if node.Metadata.Name == targetNode.Metadata.Name {
			continue // Skip the target node
		}
		checkNodeReboot(node.Metadata.Name, "due to remediation CR",
			originalBootTimes[node.Metadata.Name], time.Second, false)
	}

	GinkgoWriter.Printf("node remediation test completed successfully\n")
}

func testSBDInspection() {
	By("Setting up SBD configuration for inspection test")
	testBasicSBDConfiguration()

	// Find an SBD agent pod to inspect
	By("Finding SBD agent pod for inspection")
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbd-agent"})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).ToNot(BeEmpty(), "Should find at least one SBD agent pod")

	time.Sleep(1 * time.Minute)

	// Use the first available pod
	podName := pods.Items[0].Name
	By(fmt.Sprintf("Using SBD agent pod %s for inspection", podName))

	// Inspect node mapping
	By("Inspecting node mapping from SBD agent")
	err = testNamespace.Clients.NodeMapSummary(podName, testNamespace.Name, "")
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve node mapping")

	// Try to inspect SBD device if available
	By("Attempting to inspect SBD device")
	err = testNamespace.Clients.SBDDeviceSummary(podName, testNamespace.Name, "")
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve SBD device info")

	// Try to inspect fence device if available
	By("Attempting to inspect fence device")
	err = testNamespace.Clients.FenceDeviceSummary(podName, testNamespace.Name, "")
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve fence device info")

	// Save inspection results to files for debugging
	By("Saving inspection results to files")
	err = testNamespace.Clients.NodeMapSummary(podName, testNamespace.Name,
		fmt.Sprintf("%s/node-mapping-debug.txt", testNamespace.ArtifactsDir))
	Expect(err).NotTo(HaveOccurred(), "Failed to save node mapping")

	err = testNamespace.Clients.SBDDeviceSummary(podName, testNamespace.Name,
		fmt.Sprintf("%s/sbd-device-debug.txt", testNamespace.ArtifactsDir))
	Expect(err).NotTo(HaveOccurred(), "Failed to save SBD device info")

	err = testNamespace.Clients.FenceDeviceSummary(podName, testNamespace.Name,
		fmt.Sprintf("%s/fence-device-debug.txt", testNamespace.ArtifactsDir))
	Expect(err).NotTo(HaveOccurred(), "Failed to save fence device info")

	// Compare the SBD device summary from all agent pods in the namespace
	By("Comparing SBD device summaries across all agent pods")

	// List all SBD agent pods in the test namespace
	allPods := &corev1.PodList{}
	err = k8sClient.List(ctx, allPods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbd-agent"})
	Expect(err).NotTo(HaveOccurred())
	Expect(allPods.Items).ToNot(BeEmpty(), "Should find at least one SBD agent pod")

	type podDeviceSummary struct {
		PodName string
		Slots   []utils.SBDNodeSummary
	}

	summaries := make([]podDeviceSummary, 0, len(allPods.Items))

	for _, pod := range allPods.Items {
		slots, err := testNamespace.Clients.GetSBDDeviceInfoFromPod(pod.Name, testNamespace.Name)
		GinkgoWriter.Printf("Pod %s SBD device slots:\n", pod.Name)
		for _, slot := range slots {
			GinkgoWriter.Printf("  - NodeID: %v, Type: %v, Sequence: %v, Timestamp: %v\n",
				slot.NodeID, slot.Type, slot.Sequence, slot.Timestamp)
		}
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get SBD device info from pod %s", pod.Name))
		summaries = append(summaries, podDeviceSummary{
			PodName: pod.Name,
			Slots:   slots,
		})
	}

	// Compare the device summaries
	reference := summaries[0].Slots
	referencePod := summaries[0].PodName
	for i := 1; i < len(summaries); i++ {
		other := summaries[i].Slots
		otherPod := summaries[i].PodName

		// Compare length first
		if len(reference) != len(other) {
			GinkgoWriter.Printf("SBD device slot count mismatch between pods %s (%d slots) and %s (%d slots)\n",
				referencePod, len(reference), otherPod, len(other))
			Fail(fmt.Sprintf("SBD device slot count mismatch between pods %s and %s", referencePod, otherPod))
		}

		// Compare slot contents
		for j := range reference {
			refSlot := reference[j]
			otherSlot := other[j]
			if refSlot.NodeID != otherSlot.NodeID ||
				!refSlot.Timestamp.Equal(otherSlot.Timestamp) ||
				refSlot.Sequence != otherSlot.Sequence ||
				refSlot.Type != otherSlot.Type ||
				refSlot.HasData != otherSlot.HasData {
				GinkgoWriter.Printf("SBD device slot %d mismatch between pods %s and %s:\n  %s: %+v\n  %s: %+v\n",
					j, referencePod, otherPod, referencePod, refSlot, otherPod, otherSlot)
				Fail(fmt.Sprintf("SBD device slot %d mismatch between pods %s and %s", j, referencePod, otherPod))
			}
		}
	}

	GinkgoWriter.Printf("SBD device summaries are consistent across all agent pods\n")

	GinkgoWriter.Printf("SBD inspection test completed\n")
}

func testSBDAgentCrash(cluster ClusterInfo) {
	By("Setting up SBD configuration for agent crash test")
	testBasicSBDConfiguration()

	targetNode := selectActualWorkerNode(cluster)
	By(fmt.Sprintf("Testing SBD agent crash and recovery on verified worker node %s", targetNode.Metadata.Name))

	// Get the SBD agent pod on the target node
	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods,
		client.InNamespace(testNamespace.Name),
		client.MatchingLabels{"app": "sbd-agent"},
		client.MatchingFields{"spec.nodeName": targetNode.Metadata.Name})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).ToNot(BeEmpty(), "Should find SBD agent pod on target node")

	podName := pods.Items[0].Name
	targetPod := &pods.Items[0]

	By(fmt.Sprintf("Crashing SBD agent pod %s", podName))
	// Delete the pod to simulate a crash - TODO this does not simulate a crash, it just kills the pod
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
	testBasicSBDConfiguration()

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
	testBasicSBDConfiguration()

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

	// Clean up storage disruptor pods (they have timestamped names)
	storageDisruptorPods := &corev1.PodList{}
	err := k8sClient.List(ctx, storageDisruptorPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-storage-disruptor"})
	if err == nil {
		for _, pod := range storageDisruptorPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up Ceph storage disruptor pods (they have timestamped names)
	cephStorageDisruptorPods := &corev1.PodList{}
	err = k8sClient.List(ctx, cephStorageDisruptorPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-ceph-storage-disruptor"})
	if err == nil {
		for _, pod := range cephStorageDisruptorPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up AWS storage disruptor pods (they have timestamped names)
	awsStorageDisruptorPods := &corev1.PodList{}
	err = k8sClient.List(ctx, awsStorageDisruptorPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-aws-storage-disruptor"})
	if err == nil {
		for _, pod := range awsStorageDisruptorPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up kubelet disruptor pods (they have timestamped names)
	kubeletDisruptorPods := &corev1.PodList{}
	err = k8sClient.List(ctx, kubeletDisruptorPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-kubelet-disruptor"})
	if err == nil {
		for _, pod := range kubeletDisruptorPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up storage cleanup pods (they have timestamped names)
	storageCleanupPods := &corev1.PodList{}
	err = k8sClient.List(ctx, storageCleanupPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-storage-cleanup"})
	if err == nil {
		for _, pod := range storageCleanupPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up storage validation pods (they have timestamped names)
	storageValidationPods := &corev1.PodList{}
	err = k8sClient.List(ctx, storageValidationPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-storage-validator"})
	if err == nil {
		for _, pod := range storageValidationPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up Ceph storage validation pods (they have timestamped names)
	cephValidationPods := &corev1.PodList{}
	err = k8sClient.List(ctx, cephValidationPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-ceph-storage-validator"})
	if err == nil {
		for _, pod := range cephValidationPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

	// Clean up AWS storage validation pods (they have timestamped names)
	awsValidationPods := &corev1.PodList{}
	err = k8sClient.List(ctx, awsValidationPods, client.InNamespace("default"),
		client.MatchingLabels{"app": "sbd-e2e-aws-storage-validator"})
	if err == nil {
		for _, pod := range awsValidationPods.Items {
			_ = k8sClient.Delete(ctx, &pod)
		}
	}

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
		// Core permissions always needed
		{"ec2:DescribeInstances", testDescribeInstances},
		{"ec2:RebootInstances", testRebootInstances}, // CRITICAL: For kubelet disruption recovery

		// Note: Storage disruption now uses network-level disruption via iptables in pods
		// No additional AWS permissions needed - only Kubernetes pod creation/deletion
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

func testRebootInstances() error {
	// Test with non-existent instance ID to check permission
	_, err := ec2Client.RebootInstances(&ec2.RebootInstancesInput{
		InstanceIds: []*string{aws.String("i-nonexistent")},
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
func createNetworkDisruption(nodeName string) (*string, error) {
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
    imagePullPolicy: IfNotPresent
    command:
    - /bin/sh
    - -c
    - |
      echo "SBD e2e kubelet disruptor starting..."
      echo "Target: Stop kubelet service to simulate node failure"
      
      echo "Stopping kubelet service..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- systemctl stop kubelet.service
      
      echo "Kubelet stopped. Node should become NotReady."
      echo "IMPORTANT: Once kubelet is stopped, this pod cannot be managed normally."
      echo "Node recovery requires AWS EC2 reboot or manual intervention."
      echo "Disruptor task completed - exiting"
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

	// Create the pod using k8s API
	By(fmt.Sprintf("Creating kubelet disruptor pod: %s", disruptorPodName))
	var disruptorPod corev1.Pod
	err := yaml.Unmarshal([]byte(disruptorPodYAML), &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal disruptor pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to create disruptor pod: %w", err)
	}

	// Wait for pod to start and stop kubelet
	// We may not see the pod move from Pending to Running since kubelet is stopped
	By("Waiting for disruptor pod to start...")
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: disruptorPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}
		GinkgoWriter.Printf("Disruptor pod status: %s\n", pod.Status.Phase)
		return pod.Status.Phase != corev1.PodPending
	}, time.Minute*2, time.Second*10).Should(BeTrue())

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

// removeNetworkDisruption removes the kubelet disruption by checking node status and rebooting only if necessary
func removeNetworkDisruption(disruptorIdentifier *string, instanceID string) error {

	GinkgoWriter.Printf("Removing kubelet disruption for instance %s (pod: %v)\n", instanceID, disruptorIdentifier)

	if disruptorIdentifier != nil {
		GinkgoWriter.Printf("Attempting to clean up disruptor pod %v...\n", *disruptorIdentifier)
		// Try to delete the disruptor pod (might work if kubelet recovered)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      *disruptorIdentifier,
				Namespace: "default",
			},
		}
		err := k8sClient.Delete(ctx, pod)
		if err != nil {
			By(fmt.Sprintf("Note: Could not delete disruptor pod (expected if kubelet was stopped): %v", err))
		} else {
			By("Disruptor pod cleanup initiated successfully")
		}
	}

	// Get the node name first
	nodeName, err := getNodeNameFromInstanceID(instanceID)
	if err != nil {
		By(fmt.Sprintf("Warning: failed to get node name: %v", err))
		return fmt.Errorf("cannot proceed without node name: %w", err)
	}

	// Check current node status first
	GinkgoWriter.Printf("Checking current status of node %s...\n", nodeName)
	node := &corev1.Node{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		GinkgoWriter.Printf("Warning: cannot get node status: %v\n", err)
	} else {
		// Check if node is already Ready
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s is already Ready - no reboot needed\n", nodeName)
				// Look for and delete any existing kubelet-disruptor pods targeting this node
				pods := &corev1.PodList{}
				err = k8sClient.List(ctx, pods, client.MatchingLabels{"app": "kubelet-disruptor"})
				if err != nil {
					GinkgoWriter.Printf("Warning: failed to list kubelet-disruptor pods: %v", err)
				} else {
					for _, pod := range pods.Items {
						if strings.Contains(pod.Name, "kubelet-disruptor") &&
							strings.Contains(pod.Spec.NodeName, nodeName) {
							GinkgoWriter.Printf("Found existing kubelet-disruptor pod %s on node %s - cleaning up", pod.Name, nodeName)
							err = k8sClient.Delete(ctx, &pod)
							if err != nil {
								GinkgoWriter.Printf("Warning: failed to delete existing disruptor pod %s: %v", pod.Name, err)
							} else {
								GinkgoWriter.Printf("Successfully deleted existing disruptor pod %s", pod.Name)
							}
						}
					}
				}

				GinkgoWriter.Printf("Kubelet disruption cleanup completed - node already recovered\n")
				return nil

			} else if condition.Type == corev1.NodeReady {
				GinkgoWriter.Printf("Node %s current status: %s (%s) - reboot required",
					nodeName, condition.Status, condition.Reason)
				break
			}
		}
	}

	// Node is not Ready, proceed with reboot
	By("Node is not Ready - proceeding with reboot to restore kubelet service")

	// CRITICAL: When kubelet is stopped, we cannot exec into pods or delete pods
	// because kubelet manages the pod lifecycle. The only reliable way to restore
	// the node is to reboot it using AWS EC2 API.

	// Check if AWS is available for reboot
	if !awsInitialized {
		return fmt.Errorf("AWS not initialized - cannot reboot node to restore kubelet. Manual intervention required")
	}

	// Reboot the instance to restore kubelet service
	By(fmt.Sprintf("Rebooting AWS instance %s to restore kubelet service", instanceID))
	_, err = ec2Client.RebootInstances(&ec2.RebootInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	})
	if err != nil {
		return fmt.Errorf("failed to reboot instance %s: %w", instanceID, err)
	}

	GinkgoWriter.Print("Reboot initiated successfully")

	GinkgoWriter.Printf("Waiting for node %s to reboot and become Ready...", nodeName)
	Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			return false
		}

		// Check if node is Ready
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s has rebooted and is Ready", nodeName)
				return true
			}
		}

		// Log current condition for debugging
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				GinkgoWriter.Printf("Node %s current status: %s (%s)", nodeName, condition.Status, condition.Reason)
				break
			}
		}
		return false
	}, time.Minute*5, time.Second*30).Should(BeTrue())

	// The reboot will have automatically cleaned up the disruptor pod
	GinkgoWriter.Print("Node reboot completed - kubelet disruption resolved")
	GinkgoWriter.Print("Note: Disruptor pod was automatically cleaned up during reboot")

	return nil
}

// StorageBackendType represents the type of storage backend in use
type StorageBackendType string

const (
	StorageBackendAWS   StorageBackendType = "aws"
	StorageBackendCeph  StorageBackendType = "ceph"
	StorageBackendNFS   StorageBackendType = "nfs"
	StorageBackendOther StorageBackendType = "other"
)

// detectStorageBackend detects the storage backend type based on available StorageClasses
func detectStorageBackend() (StorageBackendType, string, error) {
	// Get all StorageClasses
	storageClasses := &storagev1.StorageClassList{}
	err := k8sClient.List(ctx, storageClasses)
	if err != nil {
		return StorageBackendOther, "", fmt.Errorf("failed to list StorageClasses: %w", err)
	}

	// Look for StorageClasses with known provisioners
	for _, sc := range storageClasses.Items {
		provisioner := sc.Provisioner
		
		// Check for Ceph-based provisioners
		if provisioner == "cephfs.csi.ceph.com" || provisioner == "openshift-storage.cephfs.csi.ceph.com" {
			return StorageBackendCeph, sc.Name, nil
		}
		
		// Check for AWS-based provisioners
		if provisioner == "efs.csi.aws.com" {
			return StorageBackendAWS, sc.Name, nil
		}
		
		// Check for NFS-based provisioners
		if provisioner == "nfs.csi.k8s.io" || 
		   strings.Contains(provisioner, "nfs") {
			return StorageBackendNFS, sc.Name, nil
		}
	}

	return StorageBackendOther, "", nil
}

// createStorageDisruption creates network-level disruption to block access to shared storage
func createStorageDisruption(instanceID string) ([]string, error) {
	By(fmt.Sprintf("Creating network-level storage disruption for instance %s", instanceID))

	// Get the node name from the instance ID
	nodeName, err := getNodeNameFromInstanceID(instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node name for instance %s: %w", instanceID, err)
	}

	By(fmt.Sprintf("Target node: %s", nodeName))

	// Detect storage backend to use appropriate disruption method
	storageBackend, storageClassName, err := detectStorageBackend()
	if err != nil {
		By(fmt.Sprintf("Warning: Could not detect storage backend, using default method: %v", err))
		storageBackend = StorageBackendOther
	}

	By(fmt.Sprintf("Detected storage backend: %s (StorageClass: %s)", storageBackend, storageClassName))

	// Use backend-specific disruption method
	switch storageBackend {
	case StorageBackendCeph:
		return createCephStorageDisruption(nodeName)
	case StorageBackendAWS, StorageBackendNFS, StorageBackendOther:
		return createAWSStorageDisruption(nodeName)
	default:
		return createAWSStorageDisruption(nodeName)
	}

}

// createCephStorageDisruption creates network-level disruption specifically for Ceph storage
func createCephStorageDisruption(nodeName string) ([]string, error) {
	By(fmt.Sprintf("Creating Ceph storage disruption for node %s", nodeName))

	// Create a unique pod name for this disruption
	disruptorPodName := fmt.Sprintf("sbd-e2e-ceph-storage-disruptor-%d", time.Now().Unix())

	// Create privileged pod that disrupts Ceph storage access
	disruptorPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-ceph-storage-disruptor
spec:
  automountServiceAccountToken: false
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: disruptor
    image: registry.redhat.io/ubi9/ubi:latest
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - -c
    - |
      echo "SBD e2e Ceph storage disruptor starting..."
      echo "Target: Block access to Ceph storage services"
      
      # Get the shared storage mount info from the host
      echo "Analyzing Ceph storage configuration..."
      
      # Method 1: Block Ceph Monitor traffic (port 6789)
      echo "Blocking Ceph Monitor traffic on port 6789..."
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 6789 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for Ceph Monitor port 6789"
        exit 1
      fi
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I INPUT -p tcp --sport 6789 -j DROP; then
        echo "ERROR: Failed to apply INPUT rule for Ceph Monitor port 6789"
        exit 1
      fi
      
      # Method 2: Block Ceph OSD traffic (ports 6800-7300 range used by OSDs)
      echo "Blocking Ceph OSD traffic on port range 6800-7300..."
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 6800:7300 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for Ceph OSD port range 6800-7300"
        exit 1
      fi
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I INPUT -p tcp --sport 6800:7300 -j DROP; then
        echo "ERROR: Failed to apply INPUT rule for Ceph OSD port range 6800-7300"
        exit 1
      fi
      
      # Method 3: Block Ceph Metadata Server traffic (port 6800)
      echo "Blocking Ceph MDS traffic on port 6800..."
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 6800 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for Ceph MDS port 6800"
        exit 1
      fi
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I INPUT -p tcp --sport 6800 -j DROP; then
        echo "ERROR: Failed to apply INPUT rule for Ceph MDS port 6800"
        exit 1
      fi
      
      # Method 4: Block traffic to known Ceph service networks
      echo "Blocking traffic to Ceph service networks..."
      
      # Find and block Ceph service IPs by analyzing running Ceph pods
      ceph_ips=""
      
      # Look for Ceph monitor services in the openshift-storage namespace
      if nsenter --target 1 --mount --uts --ipc --net --pid -- which kubectl >/dev/null 2>&1; then
        echo "Attempting to discover Ceph service IPs..."
        ceph_ips=$(nsenter --target 1 --mount --uts --ipc --net --pid -- kubectl get svc -n openshift-storage -l app=rook-ceph-mon -o jsonpath='{.items[*].spec.clusterIP}' 2>/dev/null || echo "")
        
        if [ -n "$ceph_ips" ]; then
          for ip in $ceph_ips; do
            echo "Blocking traffic to Ceph monitor IP: $ip"
            if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -d "$ip" -j DROP; then
              echo "WARNING: Failed to block traffic to Ceph monitor IP $ip"
            fi
          done
        else
          echo "No Ceph monitor IPs discovered, using network-based blocking"
        fi
      else
        echo "kubectl not available, using network-based blocking only"
      fi
      
      # Method 5: Block common Ceph cluster network ranges
      echo "Blocking common Ceph cluster network ranges..."
      # Block common cluster network ranges where Ceph typically operates
      for network in "10.96.0.0/12" "172.30.0.0/16" "10.244.0.0/16"; do
        # Only block if we can't find specific service IPs
        if [ -z "$ceph_ips" ]; then
          echo "Blocking network range: $network"
          if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -d "$network" -p tcp --dport 6789 -j DROP; then
            echo "WARNING: Failed to block Ceph traffic to network $network"
          fi
        fi
      done
      
      # Verify rules were applied successfully
      echo "Verifying iptables rules were applied..."
      rule_count=$(nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n | grep -E "(6789|6800)" | wc -l)
      if [ "$rule_count" -lt 4 ]; then
        echo "ERROR: Expected at least 4 Ceph blocking rules, found $rule_count"
        exit 1
      fi
      
      echo "Ceph storage disruption rules applied successfully. Found $rule_count blocking rules."
      echo "This will cause SBD agents to lose access to Ceph coordination storage."
      
      # Set up signal handlers for graceful cleanup
      trap 'echo "Received signal, cleaning up..."; exit 0' TERM INT
      
      # Keep the pod running to maintain the disruption
      echo "Maintaining Ceph storage disruption..."
      sleep 600  # 10 minutes
      
      echo "Ceph storage disruptor timeout reached - exiting gracefully..."
    securityContext:
      privileged: true
      capabilities:
        add:
        - SYS_ADMIN
        - NET_ADMIN
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, disruptorPodName, nodeName)

	// Create the pod using k8s API
	By(fmt.Sprintf("Creating Ceph storage disruptor pod: %s", disruptorPodName))
	var disruptorPod corev1.Pod
	err := yaml.Unmarshal([]byte(disruptorPodYAML), &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal Ceph disruptor pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to create Ceph disruptor pod: %w", err)
	}

	// Wait for pod to start and apply Ceph storage disruption rules
	By("Waiting for Ceph disruptor pod to start and apply storage disruption rules...")
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: disruptorPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}
		return pod.Status.Phase == corev1.PodRunning
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Give time for iptables rules to take effect
	By("Waiting for Ceph storage disruption rules to take effect...")
	time.Sleep(15 * time.Second)

	// VALIDATION: Verify that Ceph-specific iptables rules are actually applied
	By("Validating that Ceph storage disruption rules are successfully applied...")
	validationPodName := fmt.Sprintf("sbd-e2e-ceph-storage-validator-%d", time.Now().Unix())

	validationPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-ceph-storage-validator
spec:
  automountServiceAccountToken: false
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: validator
    image: registry.redhat.io/ubi9/ubi:latest
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - -c
    - |
      echo "Ceph storage disruption validation starting..."
      
      # Check if Ceph-specific iptables rules are present
      echo "Checking Ceph iptables rules..."
      rule_count=$(nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n | grep -E "(6789|6800)" | wc -l)
      echo "Found $rule_count Ceph storage blocking rules"
      
      if [ "$rule_count" -lt 4 ]; then
        echo "VALIDATION FAILED: Expected at least 4 Ceph storage blocking rules, found $rule_count"
        echo "Current OUTPUT rules:"
        nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n -v | head -15
        exit 1
      fi
      
      # Test Ceph port access blocking
      echo "Testing Ceph port access blocking..."
      
      # Install netcat if not available
      if ! command -v nc >/dev/null 2>&1; then
        echo "Installing netcat for connectivity testing..."
        dnf install -y nmap-ncat >/dev/null 2>&1 || echo "Warning: Could not install netcat"
      fi
      
      # Try to connect to Ceph Monitor port (should fail)
      if command -v nc >/dev/null 2>&1; then
        echo "Testing connection to port 6789 (should timeout)..."
        # Try to connect to a likely Ceph monitor IP (using service network)
        timeout 5 nc -z 10.96.0.1 6789 && {
          echo "VALIDATION FAILED: Connection to Ceph Monitor port 6789 succeeded (should be blocked)"
          exit 1
        } || echo "Ceph Monitor port 6789 correctly blocked"
      else
        echo "Warning: netcat not available, skipping connectivity test"
      fi
      
      echo "VALIDATION PASSED: Ceph storage disruption rules are active and blocking access"
      echo "Validation completed successfully"
    securityContext:
      privileged: true
      capabilities:
        add:
        - SYS_ADMIN
        - NET_ADMIN
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, validationPodName, nodeName)

	// Create validation pod
	By(fmt.Sprintf("Creating Ceph storage disruption validation pod: %s", validationPodName))
	var validationPod corev1.Pod
	err = yaml.Unmarshal([]byte(validationPodYAML), &validationPod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal Ceph validation pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &validationPod)
	if err != nil {
		return nil, fmt.Errorf("failed to create Ceph validation pod: %w", err)
	}

	// Wait for validation to complete
	By("Waiting for Ceph storage disruption validation to complete...")
	validationSucceeded := false
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: validationPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}

		if pod.Status.Phase == corev1.PodSucceeded {
			validationSucceeded = true
			return true
		}

		if pod.Status.Phase == corev1.PodFailed {
			// Get logs for debugging
			By("Ceph validation failed - retrieving logs for analysis...")
			return true
		}

		return false
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Clean up validation pod
	By(fmt.Sprintf("Cleaning up Ceph validation pod: %s", validationPodName))
	err = k8sClient.Delete(ctx, &validationPod)
	if err != nil {
		By(fmt.Sprintf("Warning: Could not delete Ceph validation pod %s: %v", validationPodName, err))
	}

	// Check validation results
	if !validationSucceeded {
		// Clean up disruptor pod since validation failed
		By("Ceph validation failed - cleaning up disruptor pod")
		err = k8sClient.Delete(ctx, &disruptorPod)
		if err != nil {
			By(fmt.Sprintf("Warning: Could not delete Ceph disruptor pod %s: %v", disruptorPodName, err))
		}
		return nil, fmt.Errorf(
			"Ceph storage disruption validation failed - iptables rules were not successfully applied or are not effective")
	}

	GinkgoWriter.Printf("Ceph storage disruption validation successful - node %s should lose Ceph storage access\n", nodeName)
	return []string{disruptorPodName}, nil
}

// createAWSStorageDisruption creates network-level disruption for AWS/EFS storage
func createAWSStorageDisruption(nodeName string) ([]string, error) {
	By(fmt.Sprintf("Creating AWS/EFS storage disruption for node %s", nodeName))

	// Create a unique pod name for this disruption
	disruptorPodName := fmt.Sprintf("sbd-e2e-aws-storage-disruptor-%d", time.Now().Unix())

	// Create privileged pod that disrupts shared storage access
	disruptorPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-aws-storage-disruptor
spec:
  automountServiceAccountToken: false
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: disruptor
    image: registry.redhat.io/ubi9/ubi:latest
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - -c
    - |
      echo "SBD e2e storage disruptor starting..."
      echo "Target: Block access to shared storage services"
      
      # Get the shared storage mount info from the host
      # Look for common shared storage mount points and services
      echo "Analyzing shared storage configuration..."
      
      # Method 1: Block EFS traffic (port 2049 - NFS)
      echo "Blocking EFS/NFS traffic on port 2049..."
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 2049 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for port 2049"
        exit 1
      fi
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I INPUT -p tcp --sport 2049 -j DROP; then
        echo "ERROR: Failed to apply INPUT rule for port 2049"
        exit 1
      fi
      
      # Method 2: Block common storage service ports
      echo "Blocking additional storage service ports..."
      # CephFS (port 6789)
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 6789 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for port 6789"
        exit 1
      fi
      # GlusterFS (port 24007)
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -p tcp --dport 24007 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for port 24007"
        exit 1
      fi
      
      # Method 3: Block traffic to storage service IP ranges (AWS EFS)
      echo "Blocking traffic to EFS service IP ranges..."
      # AWS EFS typically uses 169.254.x.x range for mount targets
      if ! nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -d 169.254.0.0/16 -j DROP; then
        echo "ERROR: Failed to apply OUTPUT rule for 169.254.0.0/16"
        exit 1
      fi
      
      # Verify rules were applied successfully
      echo "Verifying iptables rules were applied..."
      rule_count=$(nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n | grep -E "(2049|6789|24007|169\.254)" | wc -l)
      if [ "$rule_count" -lt 4 ]; then
        echo "ERROR: Expected at least 4 storage blocking rules, found $rule_count"
        exit 1
      fi
      
      echo "Storage disruption rules applied successfully. Found $rule_count blocking rules."
      echo "This will cause SBD agents to lose access to coordination storage."
      
      # Set up signal handlers for graceful cleanup
      trap 'echo "Received signal, cleaning up..."; exit 0' TERM INT
      
      # Keep the pod running to maintain the disruption
      echo "Maintaining storage disruption..."
      sleep 600  # 10 minutes
      
      echo "Storage disruptor timeout reached - exiting gracefully..."
    securityContext:
      privileged: true
      capabilities:
        add:
        - SYS_ADMIN
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, disruptorPodName, nodeName)

	// Create the pod using k8s API
	By(fmt.Sprintf("Creating AWS storage disruptor pod: %s", disruptorPodName))
	var disruptorPod corev1.Pod
	err := yaml.Unmarshal([]byte(disruptorPodYAML), &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal disruptor pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &disruptorPod)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS disruptor pod: %w", err)
	}

	// Wait for pod to start and apply storage disruption rules
	By("Waiting for disruptor pod to start and apply storage disruption rules...")
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: disruptorPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}
		return pod.Status.Phase == corev1.PodRunning
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Give time for iptables rules to take effect
	By("Waiting for storage disruption rules to take effect...")
	time.Sleep(15 * time.Second)

	// VALIDATION: Verify that iptables rules are actually applied
	By("Validating that storage disruption rules are successfully applied...")
	validationPodName := fmt.Sprintf("sbd-e2e-aws-storage-validator-%d", time.Now().Unix())

	//nolint:lll
	validationPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-aws-storage-validator
spec:
  automountServiceAccountToken: false
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: validator
    image: registry.redhat.io/ubi9/ubi:latest
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - -c
    - |
      echo "AWS storage disruption validation starting..."
      
      # Check if iptables rules are present
      echo "Checking iptables rules..."
      rule_count=$(nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n | grep -E "(2049|6789|24007|169\.254)" | wc -l)
      echo "Found $rule_count storage blocking rules"
      
      if [ "$rule_count" -lt 4 ]; then
        echo "VALIDATION FAILED: Expected at least 4 storage blocking rules, found $rule_count"
        echo "Current OUTPUT rules:"
        nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n -v | head -10
        exit 1
      fi
      
      # Test storage access blocking
      echo "Testing storage access blocking..."
      
      # Install netcat if not available
      if ! command -v nc >/dev/null 2>&1; then
        echo "Installing netcat for connectivity testing..."
        dnf install -y nmap-ncat >/dev/null 2>&1 || echo "Warning: Could not install netcat"
      fi
      
      # Try to connect to common NFS ports (should fail)
      if command -v nc >/dev/null 2>&1; then
        echo "Testing connection to port 2049 (should timeout)..."
        timeout 5 nc -z 169.254.0.1 2049 && {
          echo "VALIDATION FAILED: Connection to port 2049 succeeded (should be blocked)"
          exit 1
        } || echo "Port 2049 correctly blocked"
      else
        echo "Warning: netcat not available, skipping connectivity test"
      fi
      
      echo "VALIDATION PASSED: AWS storage disruption rules are active and blocking access"
      echo "Validation completed successfully"
    securityContext:
      privileged: true
      capabilities:
        add:
        - SYS_ADMIN
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, validationPodName, nodeName)

	// Create validation pod
	By(fmt.Sprintf("Creating AWS storage disruption validation pod: %s", validationPodName))
	var validationPod corev1.Pod
	err = yaml.Unmarshal([]byte(validationPodYAML), &validationPod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal AWS validation pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &validationPod)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS validation pod: %w", err)
	}

	// Wait for validation to complete
	By("Waiting for AWS storage disruption validation to complete...")
	validationSucceeded := false
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: validationPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}

		if pod.Status.Phase == corev1.PodSucceeded {
			validationSucceeded = true
			return true
		}

		if pod.Status.Phase == corev1.PodFailed {
			// Get logs for debugging
			By("AWS validation failed - retrieving logs for analysis...")
			return true
		}

		return false
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Clean up validation pod
	By(fmt.Sprintf("Cleaning up AWS validation pod: %s", validationPodName))
	err = k8sClient.Delete(ctx, &validationPod)
	if err != nil {
		By(fmt.Sprintf("Warning: Could not delete AWS validation pod %s: %v", validationPodName, err))
	}

	// Check validation results
	if !validationSucceeded {
		// Clean up disruptor pod since validation failed
		By("AWS validation failed - cleaning up disruptor pod")
		err = k8sClient.Delete(ctx, &disruptorPod)
		if err != nil {
			By(fmt.Sprintf("Warning: Could not delete AWS disruptor pod %s: %v", disruptorPodName, err))
		}
		return nil, fmt.Errorf(
			"storage disruption validation failed - iptables rules were not successfully applied or are not effective")
	}

	GinkgoWriter.Printf("Storage disruption validation successful - node %s should lose shared storage access\n", nodeName)
	return []string{disruptorPodName}, nil
}

// restoreStorageDisruption removes the network-level storage disruption for both AWS and Ceph backends
func restoreStorageDisruption(instanceID string, disruptorPodNames []string) error {
	if len(disruptorPodNames) == 0 {
		return nil
	}

	By(fmt.Sprintf("Removing network-level storage disruption for instance %s", instanceID))

	// Get the node name from the instance ID
	nodeName, err := getNodeNameFromInstanceID(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get node name for instance %s: %w", instanceID, err)
	}

	// Detect what type of disruption was used
	storageBackend, _, err := detectStorageBackend()
	if err != nil {
		By(fmt.Sprintf("Warning: Could not detect storage backend for cleanup, using comprehensive cleanup: %v", err))
		storageBackend = StorageBackendOther
	}

	By(fmt.Sprintf("Cleaning up storage disruption for backend: %s", storageBackend))

	// Clean up the disruptor pod first
	for _, podName := range disruptorPodNames {
		By(fmt.Sprintf("Cleaning up storage disruptor pod: %s", podName))
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: "default",
			},
		}
		err := k8sClient.Delete(ctx, pod)
		if err != nil {
			By(fmt.Sprintf("Warning: Could not delete disruptor pod %s: %v", podName, err))
		}
	}

	// Create a cleanup pod to remove the iptables rules
	cleanupPodName := fmt.Sprintf("sbd-e2e-storage-cleanup-%d", time.Now().Unix())
	//nolint:lll
	cleanupPodYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    app: sbd-e2e-storage-cleanup
spec:
  automountServiceAccountToken: false
  hostNetwork: true
  hostPID: true
  nodeName: %s
  containers:
  - name: cleanup
    image: registry.redhat.io/ubi9/ubi:latest
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - -c
    - |
      echo "SBD e2e storage cleanup starting..."
      echo "Target: Remove storage disruption iptables rules (comprehensive cleanup)"
      
      # Remove AWS/EFS-specific iptables rules
      echo "Removing AWS/EFS traffic blocks..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -p tcp --dport 2049 -j DROP 2>/dev/null || true
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D INPUT -p tcp --sport 2049 -j DROP 2>/dev/null || true
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -d 169.254.0.0/16 -j DROP 2>/dev/null || true
      
      # Remove Ceph-specific iptables rules
      echo "Removing Ceph storage traffic blocks..."
      # Ceph Monitor (port 6789)
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -p tcp --dport 6789 -j DROP 2>/dev/null || true
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D INPUT -p tcp --sport 6789 -j DROP 2>/dev/null || true
      
      # Ceph OSD range (6800-7300)
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -p tcp --dport 6800:7300 -j DROP 2>/dev/null || true
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D INPUT -p tcp --sport 6800:7300 -j DROP 2>/dev/null || true
      
      # Ceph MDS (port 6800 - also covered by range above but explicit cleanup)
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -p tcp --dport 6800 -j DROP 2>/dev/null || true
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D INPUT -p tcp --sport 6800 -j DROP 2>/dev/null || true
      
      # Remove other storage service port blocks (GlusterFS, etc.)
      echo "Removing other storage service blocks..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -p tcp --dport 24007 -j DROP 2>/dev/null || true
      
      # Clean up any remaining Ceph-specific IP blocks that might have been added
      echo "Cleaning up Ceph service IP blocks..."
      # Try to discover and cleanup Ceph monitor IPs if kubectl is available
      if nsenter --target 1 --mount --uts --ipc --net --pid -- which kubectl >/dev/null 2>&1; then
        ceph_ips=$(nsenter --target 1 --mount --uts --ipc --net --pid -- kubectl get svc -n openshift-storage -l app=rook-ceph-mon -o jsonpath='{.items[*].spec.clusterIP}' 2>/dev/null || echo "")
        if [ -n "$ceph_ips" ]; then
          for ip in $ceph_ips; do
            echo "Removing block for Ceph monitor IP: $ip"
            nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -d "$ip" -j DROP 2>/dev/null || true
          done
        fi
      fi
      
      # Clean up network range blocks used for Ceph
      for network in "10.96.0.0/12" "172.30.0.0/16" "10.244.0.0/16"; do
        echo "Removing Ceph network range block: $network"
        nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -d "$network" -p tcp --dport 6789 -j DROP 2>/dev/null || true
      done
      
      echo "Comprehensive storage disruption cleanup completed."
      echo "Both AWS/EFS and Ceph storage should now be accessible."
      echo "Cleanup finished successfully."
    securityContext:
      privileged: true
      capabilities:
        add:
        - SYS_ADMIN
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
`, cleanupPodName, nodeName)

	// Create the cleanup pod
	By(fmt.Sprintf("Creating storage cleanup pod: %s", cleanupPodName))
	var cleanupPod corev1.Pod
	err = yaml.Unmarshal([]byte(cleanupPodYAML), &cleanupPod)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cleanup pod YAML: %w", err)
	}

	err = k8sClient.Create(ctx, &cleanupPod)
	if err != nil {
		return fmt.Errorf("failed to create cleanup pod: %w", err)
	}

	// Wait for cleanup pod to complete
	By("Waiting for cleanup pod to complete...")
	Eventually(func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: cleanupPodName, Namespace: "default"}, pod)
		if err != nil {
			return false
		}
		return pod.Status.Phase == corev1.PodSucceeded
	}, time.Minute*2, time.Second*10).Should(BeTrue())

	// Clean up the cleanup pod
	By(fmt.Sprintf("Cleaning up storage cleanup pod: %s", cleanupPodName))
	err = k8sClient.Delete(ctx, &cleanupPod)
	if err != nil {
		By(fmt.Sprintf("Warning: Could not delete cleanup pod %s: %v", cleanupPodName, err))
	}

	By(fmt.Sprintf("Network-level storage disruption removed - node %s should regain shared storage access", nodeName))
	return nil
}

// cleanupPreviousTestAttempts removes any leftover artifacts from previous test runs
func cleanupPreviousTestAttempts() error {
	if !awsInitialized {
		GinkgoWriter.Print("AWS not initialized - skipping AWS cleanup\n")
		return nil
	}

	GinkgoWriter.Print("Cleaning up any leftover artifacts from previous test runs\n")

	// Get all instances in the cluster
	instances, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{})
	if err != nil {
		return fmt.Errorf("failed to describe instances: %w", err)
	}

	for _, reservation := range instances.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State != nil && *instance.State.Name == "running" {
				By(fmt.Sprintf("Cleaning up any leftover artifacts from previous test runs for instance %s", *instance.InstanceId))
				err = removeNetworkDisruption(nil, *instance.InstanceId)
				Expect(err).NotTo(HaveOccurred())
				err = restoreStorageDisruption(*instance.InstanceId, nil)
				Expect(err).NotTo(HaveOccurred())
			}
		}
	}

	cleanupTestArtifacts()

	GinkgoWriter.Print("Cleanup of previous test attempts completed")
	return nil
}
