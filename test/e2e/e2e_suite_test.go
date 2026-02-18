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

	"github.com/medik8s/sbd-operator/test/utils"
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
	GinkgoWriter.Print("Starting sbd-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var skipall = false
var _ = BeforeSuite(func() {
	// Skip slow tests if requested

	// Check cluster connection first - skip entire suite if not available
	if err := utils.CheckClusterConnection(); err != nil {
		skipall = true
		Skip(fmt.Sprintf("Cluster connection not available: %v", err))
	}

	if testFlags.DebugMode {
		GinkgoWriter.Printf("Using test configuration:\n")
		if testFlags.NodeSelector != "" {
			GinkgoWriter.Printf("  Node selector: %s\n", testFlags.NodeSelector)
		}
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
	if err := initAWS(testClients); err != nil {
		By(fmt.Sprintf("AWS not available for disruption tests: %v", err))
	} else {
		By("AWS initialized successfully for disruption tests")
	}

	// Clean up any leftover artifacts from previous test runs
	By("Cleaning up previous test attempts")
	cleanupTestArtifacts(testNamespace)
	utils.WaitForNodesReady(testNamespace, "10m", "30s", true)
	utils.CleanupSBDConfigs(testNamespace)

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

	GinkgoWriter.Printf("\n\n--------------------------------\n")
	GinkgoWriter.Printf("Artefacts available at: %s\n", testNamespace.ArtifactsDir)
	GinkgoWriter.Printf("--------------------------------\n\n")
})

var _ = BeforeEach(func() {
})

var _ = AfterEach(func() {
	createReportAndCleanUp()
	utils.WaitForNodesReady(testNamespace, "10m", "30s", false)
	utils.CleanupSBDConfigs(testNamespace)
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
		utils.DescribeEnvironment(testClients, testNamespace.OperatorNamespace())
		utils.DescribeEnvironment(testClients, testNamespace)
	}
}
