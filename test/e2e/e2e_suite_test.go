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
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/sbd-operator/test/utils"
)

var (
	// Kubernetes clients
	k8sClient client.Client
	ctx       context.Context
)

// TestE2E runs the e2e test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	GinkgoWriter.Print("Starting sbd-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var skipall = false
var _ = BeforeSuite(func() {
	// Check cluster connection first - skip entire suite if not available
	if err := utils.CheckClusterConnection(); err != nil {
		skipall = true
		Skip(fmt.Sprintf("Cluster connection not available: %v", err))
	}

	var err error
	testNamespace, err = utils.SuiteSetup("sbd-test-e2e")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup test clients")
	testClients = testNamespace.Clients

	// Update global clients for backward compatibility
	k8sClient = testClients.Client
	ctx = testClients.Context

	// Discover cluster topology
	discoverClusterTopology()

	By("Checking AWS availability for disruption tests (one-time setup)")
	if err := initAWS(); err != nil {
		By(fmt.Sprintf("AWS not available for disruption tests: %v", err))
		awsInitialized = false
	} else {
		By("AWS initialized successfully for disruption tests")
		awsInitialized = true
	}

	// Clean up any leftover artifacts from previous test runs
	if err := cleanupPreviousTestAttempts(); err != nil {
		By(fmt.Sprintf("Warning: cleanup of previous test attempts failed: %v", err))
	}
	By("Complete: Cleaning up previous test attempts")
})

var _ = AfterSuite(func() {
	if skipall {
		return
	}

	utils.UninstallCertManager()

	By("cleaning up e2e test namespace")
	if testNamespace != nil {
		_ = testNamespace.Cleanup()
	}
})

var _ = BeforeEach(func() {
	By("waiting for all cluster nodes to be Ready before test")
	Eventually(func() bool {
		nodeList, err := testClients.Clientset.CoreV1().Nodes().List(testClients.Context, metav1.ListOptions{})
		if err != nil {
			GinkgoWriter.Printf("Failed to list nodes: %v\n", err)
			return false
		}
		if len(nodeList.Items) == 0 {
			GinkgoWriter.Printf("No nodes found in cluster\n")
			return false
		}
		for _, node := range nodeList.Items {
			ready := false
			for _, cond := range node.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "True" {
					ready = true
					break
				}
			}
			if !ready {
				GinkgoWriter.Printf("Node %s is not Ready\n", node.Name)
				return false
			}
		}
		return true
	}, "10m", "20s").Should(BeTrue(), "expected all nodes to be Ready before test")

	GinkgoWriter.Printf("Cleaning up SBDConfigs in namespace %v before each test\n", testNamespace.Name)
	utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
})

var _ = AfterEach(func() {
	specReport := CurrentSpecReport()
	if specReport.Failed() {
		systemNamespace := &utils.TestNamespace{
			Name:         "sbd-operator-system",
			ArtifactsDir: "testrun/sbd-operator-system",
			Clients:      testClients,
		}
		utils.DescribeEnvironment(testClients, systemNamespace)
		utils.DescribeEnvironment(testClients, testNamespace)
	}
	By("Cleaning up previous test attempts")
	_ = cleanupPreviousTestAttempts()
})

// var _ = AfterAll(func() {
// })
