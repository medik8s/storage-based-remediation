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

package smoke

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/sbd-operator/test/utils"
)

var (
	// Kubernetes clients - now using shared utilities
	testClients   *utils.TestClients
	testNamespace *utils.TestNamespace
)

// namespace where the project is deployed in
const namespace = "sbd-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "sbd-operator-controller-manager"

// TestSmoke runs the smoke test suite for the project. These tests execute in an isolated,
// temporary environment to validate basic functionality with the purpose to be used in CI jobs.
// The default setup requires CRC, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestSmoke(t *testing.T) {
	RegisterFailHandler(Fail)
	GinkgoWriter.Print("Starting sbd-operator smoke test suite\n")
	RunSpecs(t, "smoke suite")
}

var skipall = false
var _ = BeforeSuite(func() {
	// Check cluster connection first - skip entire suite if not available
	if err := utils.CheckClusterConnection(); err != nil {
		skipall = true
		Skip(fmt.Sprintf("Cluster connection not available: %v", err))
	}

	var err error
	testNamespace, err = utils.SuiteSetup("sbd-test")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup test clients")
	testClients = testNamespace.Clients
})

var _ = AfterSuite(func() {
	if skipall {
		return
	}

	// Teardown CertManager after the suite if not skipped and if it was not already installed
	utils.UninstallCertManager()
	By("cleaning up watchdog smoke test namespace")
	if testNamespace != nil {
		_ = testNamespace.Cleanup()
	}

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
})
