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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/sbd-operator/test/utils"
)

var (
	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	// It uses environment variables that match the Makefile QUAY_* variables.
	projectImage = utils.GetProjectImage()

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
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting sbd-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	var err error
	testNamespace, err = utils.SuiteSetup("sbd-test-e2e")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup test clients")
	testClients = testNamespace.Clients

	// Update global clients for backward compatibility
	k8sClient = testClients.Client
	ctx = testClients.Context

	By("Checking AWS availability for disruption tests (one-time setup)")
	if err := initAWS(); err != nil {
		By(fmt.Sprintf("AWS not available for disruption tests: %v", err))
		awsInitialized = false
	} else {
		By("AWS initialized successfully for disruption tests")
		awsInitialized = true
	}
})

var _ = AfterSuite(func() {
	utils.UninstallCertManager()
})

var _ = BeforeEach(func() {
	GinkgoWriter.Printf("Cleaning up SBDConfigs before each test: %v %v\n", testClients, testNamespace)
	utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
})

var _ = AfterEach(func() {
	specReport := CurrentSpecReport()
	if specReport.Failed() {
		utils.DescribeEnvironment(testClients, "sbd-operator-system")
	}
})
