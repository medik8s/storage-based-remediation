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
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
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

	// Kubernetes clients
	k8sClient    client.Client
	k8sClientset *kubernetes.Clientset
	ctx          context.Context
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

	version := os.Getenv("TAG")
	if version == "" {
		version = "e2e-test"
	}

	// Allow complete override via TEST_IMG environment variable
	if testImg := os.Getenv("TEST_IMG"); testImg != "" {
		return testImg
	}

	return fmt.Sprintf("%s/%s/sbd-operator:%s", registry, org, version)
}

// setupKubernetesClients initializes the Kubernetes clients for API calls
func setupKubernetesClients() error {
	var config *rest.Config
	var err error

	// Get kubeconfig path
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory: %w", err)
		}
		kubeconfig = filepath.Join(homeDir, ".kube", "config")
	}

	// Build config from kubeconfig
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to build config from kubeconfig: %w", err)
	}

	// Create scheme and add types
	scheme := runtime.NewScheme()
	err = corev1.AddToScheme(scheme)
	if err != nil {
		return fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	err = appsv1.AddToScheme(scheme)
	if err != nil {
		return fmt.Errorf("failed to add apps types to scheme: %w", err)
	}
	err = medik8sv1alpha1.AddToScheme(scheme)
	if err != nil {
		return fmt.Errorf("failed to add SBD types to scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err = client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create clientset for operations not available in controller-runtime
	k8sClientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	return nil
}

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
	// Initialize context
	ctx = context.Background()

	By("verifying e2e test environment setup")
	_, _ = fmt.Fprintf(GinkgoWriter, "E2E test environment setup completed by Makefile\n")
	_, _ = fmt.Fprintf(GinkgoWriter, "Project image: %s\n", projectImage)

	// Initialize Kubernetes clients
	By("setting up Kubernetes clients")
	err := setupKubernetesClients()
	Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")

	// Verify we can connect to the cluster
	By("verifying cluster connection")
	nodes := &corev1.NodeList{}
	err = k8sClient.List(ctx, nodes)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to connect to cluster")

	// The e2e tests are intended to run on an existing cluster that supports comprehensive testing.
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
