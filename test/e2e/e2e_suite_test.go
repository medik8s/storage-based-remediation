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
	"os"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/sbd-operator/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	// It uses environment variables that match the Makefile QUAY_* variables.
	projectImage = getProjectImage()
)

// getProjectImage returns the project image name based on environment variables.
// It uses the same pattern as the Makefile QUAY_* variables, with sensible defaults for local testing.
func getProjectImage() string {
	registry := os.Getenv("QUAY_REGISTRY")
	if registry == "" {
		registry = "localhost:5000" // Local registry for testing
	}

	org := os.Getenv("QUAY_ORG")
	if org == "" {
		org = "sbd-operator"
	}

	version := os.Getenv("VERSION")
	if version == "" {
		version = "e2e-test"
	}

	// Allow complete override via TEST_IMG environment variable
	if testImg := os.Getenv("TEST_IMG"); testImg != "" {
		return testImg
	}

	return fmt.Sprintf("%s/%s/sbd-operator:%s", registry, org, version)
}

// TestE2E runs the smoke and multinode test suites for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting sbd-operator integration test suite\n")
	RunSpecs(t, "smoke and multinode suite")
}

var _ = BeforeSuite(func() {
	By("verifying test environment setup")
	_, _ = fmt.Fprintf(GinkgoWriter, "Test environment setup completed by Makefile\n")
	_, _ = fmt.Fprintf(GinkgoWriter, "Project image: %s\n", projectImage)

	// Verify we can connect to the cluster
	By("verifying cluster connection")
	cmd := exec.Command("kubectl", "cluster-info")
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to connect to cluster")

	// The smoke tests are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}
})

var _ = AfterSuite(func() {
	// Teardown CertManager after the suite if not skipped and if it was not already installed
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})
