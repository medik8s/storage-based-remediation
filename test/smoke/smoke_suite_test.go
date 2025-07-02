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
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/sbd-operator/test/utils"
)

var (
	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	// It uses environment variables that match the Makefile QUAY_* variables.
	projectImage = utils.GetProjectImage()
	agentImage   = utils.GetAgentImage()

	// Kubernetes clients - now using shared utilities
	testClients *utils.TestClients
	// Legacy clients for backward compatibility
	k8sClient client.Client
	clientset *kubernetes.Clientset
	ctx       = context.Background()
)

// namespace where the project is deployed in
const namespace = "sbd-operator-system"

// testNamespace where SBDConfig resources and their DaemonSets are deployed
const testNamespace = "sbd-test"

// serviceAccountName created for the project
const serviceAccountName = "sbd-operator-controller-manager"

// TestSmoke runs the smoke test suite for the project. These tests execute in an isolated,
// temporary environment to validate basic functionality with the purpose to be used in CI jobs.
// The default setup requires CRC, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestSmoke(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting sbd-operator smoke test suite\n")
	RunSpecs(t, "smoke suite")
}

var _ = BeforeSuite(func() {
	var err error
	testClients, err = utils.SuiteSetup(testNamespace)
	Expect(err).NotTo(HaveOccurred(), "Failed to setup test clients")
})

var _ = AfterSuite(func() {
	// Teardown CertManager after the suite if not skipped and if it was not already installed
	utils.UninstallCertManager()
})
