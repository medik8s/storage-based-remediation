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
	"flag"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/test/utils"
)

var (
	// Kubernetes clients
	k8sClient client.Client
	ctx       context.Context

	// Test configuration from command line flags
	testFlags *utils.TestFlags
)

// TestE2E runs the e2e test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	// Parse command line flags to make them available to tests
	flag.Parse()
	testFlags = utils.GetTestFlags()

	if testFlags.DebugMode {
		GinkgoWriter.Printf("Debug mode enabled\n")
		GinkgoWriter.Printf("Test configuration: %+v\n", testFlags)
	}

	RegisterFailHandler(Fail)
	GinkgoWriter.Print("Starting sbr-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	utils.SetLogger(GinkgoWriter)
	Expect(checkClusterConnection()).To(Succeed(), "Kubernetes cluster connection required")

	if testFlags.DebugMode {
		GinkgoWriter.Printf("Using test configuration:\n")
		if testFlags.NodeSelector != "" {
			GinkgoWriter.Printf("  Node selector: %s\n", testFlags.NodeSelector)
		}
	}

	var err error
	testNamespace, err = suiteSetup("sbr-test-e2e")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup test clients")

	testClients = testNamespace.Clients

	// Update global clients for backward compatibility
	k8sClient = testClients.Client
	ctx = testClients.Context

	// Discover cluster topology
	discoverClusterTopology()

	By("Checking AWS availability for disruption tests (one-time setup)")
	if err := initAWS(testClients); err != nil {
		By(fmt.Sprintf("AWS not available for disruption tests: %v", err))
	} else {
		By("AWS initialized successfully for disruption tests")
	}

	// Clean up any leftover artifacts from previous test runs
	By("Cleaning up previous test attempts")
	cleanupTestArtifacts(testNamespace)
	Expect(utils.WaitForNodesReady(testNamespace, "10m", "30s", true)).To(Succeed(), "expected all nodes to be Ready")
	cleanupStorageBasedRemediationConfigs(testNamespace)

	By("Complete: Cleaning up previous test attempts")
})

var _ = AfterSuite(func() {
	utils.UninstallCertManager()

	By("cleaning up e2e test namespace")
	if testNamespace != nil {
		_ = cleanupNamespace(testNamespace)
		GinkgoWriter.Printf("\n\n--------------------------------\n")
		GinkgoWriter.Printf("Artefacts available at: %s\n", testNamespace.ArtifactsDir)
		GinkgoWriter.Printf("--------------------------------\n\n")
	}
})

var _ = BeforeEach(func() {
})

var _ = AfterEach(func() {
	createReportAndCleanUp()

	// Clean up SBRCs BEFORE checking node readiness
	// This ensures nodes are not in remediation state during the check
	By("Cleaning up all SBRCs created by this test")
	cleanupStorageBasedRemediationConfigs(testNamespace)

	// Now check that nodes are ready after cleanup
	Expect(utils.WaitForNodesReady(testNamespace, "10m", "30s", false)).To(Succeed(), "expected all nodes to be Ready")

	// Collect agent logs after cleanup and readiness verification
	debugCollector := newDebugCollector(testClients, testNamespace.ArtifactsDir)
	debugCollector.collectAgentLogs(testNamespace.Name)
})

func createReportAndCleanUp() {
	DeferCleanup(func() {
		By("Cleaning up previous test attempts")
		cleanupTestArtifacts(testNamespace)
	})
	specReport := CurrentSpecReport()
	if specReport.Failed() {
		GinkgoWriter.Printf("\n\n--------------------------------\n")
		GinkgoWriter.Printf("Test failed: %s\n", specReport.FullText())
		GinkgoWriter.Printf("--------------------------------\n\n")
		describeEnvironment(testClients, testNamespace.OperatorNamespace())
		describeEnvironment(testClients, testNamespace)
	}
}
