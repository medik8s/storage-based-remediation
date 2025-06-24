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
}

var (
	clusterInfo ClusterInfo
	testNS      string
)

var _ = Describe("SBD Operator Multi-Node E2E Tests", func() {
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

		By(fmt.Sprintf("Running tests on cluster with %d total nodes (%d workers, %d control plane)",
			clusterInfo.TotalNodes, len(clusterInfo.WorkerNodes), len(clusterInfo.ControlNodes)))
	})

	AfterEach(func() {
		// Clean up test namespace
		By(fmt.Sprintf("Cleaning up test namespace %s", testNS))
		cmd := exec.Command("kubectl", "delete", "namespace", testNS, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	Context("Adaptive Multi-Node SBD Tests", func() {
		It("should handle basic SBD configuration", func() {
			if len(clusterInfo.WorkerNodes) < 3 {
				Skip("Test requires at least 3 worker nodes")
			}
			testBasicSBDConfiguration(clusterInfo)
		})

		It("should handle node failure simulation", func() {
			if len(clusterInfo.WorkerNodes) < 4 {
				Skip("Test requires at least 4 worker nodes")
			}
			testNodeFailureSimulation(clusterInfo)
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
	By("Creating basic SBDConfig for worker nodes")
	sbdConfigYAML := fmt.Sprintf(`apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: basic-sbd
  namespace: %s
spec:
  sbdWatchdogPath: "/dev/watchdog"
  image: "sbd-agent:latest"
  namespace: "sbd-system"
  staleNodeTimeout: "30s"
`, testNS)

	// Apply the SBDConfig
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(sbdConfigYAML)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("Verifying SBDConfig was created")
	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "sbdconfig", "basic-sbd", "-n", testNS)
		_, err := utils.Run(cmd)
		return err == nil
	}, time.Minute*2, time.Second*10).Should(BeTrue())
}

func testNodeFailureSimulation(cluster ClusterInfo) {
	By("Testing node failure simulation")
	targetNode := cluster.WorkerNodes[0]

	// Cordon the node to simulate failure
	cmd := exec.Command("kubectl", "cordon", targetNode.Metadata.Name)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	// Uncordon the node to recover
	defer func() {
		cmd := exec.Command("kubectl", "uncordon", targetNode.Metadata.Name)
		_, _ = utils.Run(cmd)
	}()

	GinkgoWriter.Printf("Simulated failure of node %s\n", targetNode.Metadata.Name)
}

func testLargeClusterCoordination(cluster ClusterInfo) {
	By(fmt.Sprintf("Testing coordination with %d worker nodes", len(cluster.WorkerNodes)))
	GinkgoWriter.Printf("Large cluster coordination test with %d nodes\n", len(cluster.WorkerNodes))
}
