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
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	agentImage   = getAgentImage()
)

// getProjectImage returns the project image name based on environment variables.
// It uses the same pattern as the Makefile QUAY_* variables, with sensible defaults for local testing.
func getProjectImage() string {
	// Allow complete override via OPERATOR_IMG environment variable
	if testImg := os.Getenv("OPERATOR_IMG"); testImg != "" {
		return testImg
	}

	registry := os.Getenv("QUAY_REGISTRY")
	if registry == "" {
		registry = "localhost:5000" // Local registry for testing
	}

	org := os.Getenv("QUAY_ORG")
	if org == "" {
		org = "sbd-operator"
	}

	version := os.Getenv("TAG")
	if version == "" {
		version = "smoke-test"
	}

	return fmt.Sprintf("%s/%s/sbd-operator:%s", registry, org, version)
}

// getAgentImage returns the agent image name based on environment variables.
// It uses the same pattern as the Makefile QUAY_* variables, with sensible defaults for local testing.
func getAgentImage() string {
	// Allow complete override via AGENT_IMG environment variable
	if agentImg := os.Getenv("AGENT_IMG"); agentImg != "" {
		return agentImg
	}

	registry := os.Getenv("QUAY_REGISTRY")
	if registry == "" {
		registry = "localhost:5000" // Local registry for testing
	}

	org := os.Getenv("QUAY_ORG")
	if org == "" {
		org = "sbd-operator"
	}

	version := os.Getenv("TAG")
	if version == "" {
		version = "smoke-test"
	}

	return fmt.Sprintf("%s/%s/sbd-agent:%s", registry, org, version)
}

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
	By("verifying smoke test environment setup")
	_, _ = fmt.Fprintf(GinkgoWriter, "Smoke test environment setup completed by Makefile\n")
	_, _ = fmt.Fprintf(GinkgoWriter, "Project image: %s\n", projectImage)
	_, _ = fmt.Fprintf(GinkgoWriter, "Agent image: %s\n", agentImage)

	By("initializing Kubernetes clients for watchdog tests if needed")
	if k8sClient == nil {
		err := setupKubernetesClients()
		Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
	}

	// Verify we can connect to the cluster
	By("verifying cluster connection")
	serverVersion, err := clientset.Discovery().ServerVersion()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to connect to cluster")
	_, _ = fmt.Fprintf(GinkgoWriter, "Connected to Kubernetes cluster version: %s\n", serverVersion.String())

	By("creating test namespace for watchdog smoke tests if it doesn't exist")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	err = k8sClient.Create(ctx, ns)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		Expect(err).NotTo(HaveOccurred())
	}

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

	By("verifying CRDs are installed")
	// Check for SBD CRDs by looking for API resources in the medik8s.medik8s.io group
	apiResourceList, err := clientset.Discovery().ServerResourcesForGroupVersion("medik8s.medik8s.io/v1alpha1")
	Expect(err).NotTo(HaveOccurred(), "Failed to get API resources for medik8s.medik8s.io/v1alpha1")

	var foundSBDConfig, foundSBDRemediation bool
	for _, resource := range apiResourceList.APIResources {
		if resource.Kind == "SBDConfig" {
			foundSBDConfig = true
		}
		if resource.Kind == "SBDRemediation" {
			foundSBDRemediation = true
		}
	}
	Expect(foundSBDConfig).To(BeTrue(), "Expected SBDConfig CRD to be installed (should be done by Makefile setup)")
	Expect(foundSBDRemediation).To(BeTrue(), "Expected SBDRemediation CRD to be installed (should be done by Makefile setup)")

	By("verifying the controller-manager is deployed")
	deployment := &appsv1.Deployment{}
	err = k8sClient.Get(ctx, client.ObjectKey{
		Name:      "sbd-operator-controller-manager",
		Namespace: "sbd-operator-system",
	}, deployment)
	Expect(err).NotTo(HaveOccurred(), "Expected controller-manager to be deployed (should be done by Makefile setup)")

	// Confirm the operator is running
	By("confirming the operator is running")
	Eventually(func() bool {
		podList, err := clientset.CoreV1().Pods("sbd-operator-system").List(ctx, metav1.ListOptions{
			LabelSelector: "control-plane=controller-manager",
		})
		if err != nil || len(podList.Items) == 0 {
			return false
		}
		return podList.Items[0].Status.Phase == corev1.PodRunning
	}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Operator pod is not running")
})

var _ = AfterSuite(func() {
	// Teardown CertManager after the suite if not skipped and if it was not already installed
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})
