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

package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/service/ec2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

var (
	skipCertManagerInstall        = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	isCertManagerAlreadyInstalled = false
)

// SkipCertManagerInstall reports whether CertManager installation is skipped via env.
func SkipCertManagerInstall() bool {
	return skipCertManagerInstall
}

// SetCertManagerAlreadyInstalled records whether CertManager was present before suite setup.
func SetCertManagerAlreadyInstalled(installed bool) {
	isCertManagerAlreadyInstalled = installed
}

// IsCertManagerAlreadyInstalled reports whether CertManager was already on the cluster.
func IsCertManagerAlreadyInstalled() bool {
	return isCertManagerAlreadyInstalled
}

// TestClients holds the Kubernetes clients used for testing
type TestClients struct {
	Client         client.Client
	Clientset      *kubernetes.Clientset
	Config         *rest.Config
	Context        context.Context
	Ec2Client      *ec2.EC2
	AWSInitialized bool
}

// TestNamespace represents a test namespace with cleanup functionality
type TestNamespace struct {
	Name         string
	ArtifactsDir string
	Clients      *TestClients
}

// SetupKubernetesClients initializes Kubernetes clients for testing
func SetupKubernetesClients() (*TestClients, error) {
	// Load kubeconfig - try environment variable first, then default location
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(homeDir, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Create scheme with core Kubernetes types and add our CRDs
	clientScheme := runtime.NewScheme()
	err = scheme.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	err = medik8sv1alpha1.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add SBR types to scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(config, client.Options{Scheme: clientScheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create standard clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &TestClients{
		Client:    k8sClient,
		Clientset: clientset,
		Config:    config,
		Context:   context.Background(),
	}, nil
}

// CreateTestNamespace creates a test namespace and returns a cleanup function
func (tc *TestClients) CreateTestNamespace(namespace string) (*TestNamespace, error) {
	testFlags := GetTestFlags()
	artifactsDir := fmt.Sprintf("../../%s", testFlags.ArtifactsDir)

	// Ensure the artifacts directory for this test namespace exists
	if _, err := os.Stat(artifactsDir); os.IsNotExist(err) {
		if err := os.MkdirAll(artifactsDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create artifacts directory %s: %w", testFlags.ArtifactsDir, err)
		}
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	tns := &TestNamespace{
		Name:         namespace,
		ArtifactsDir: testFlags.ArtifactsDir,
		Clients:      tc,
	}

	err := tc.Client.Create(tc.Context, ns)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return tns, nil
	}
	return tns, err
}

// CreateStorageBasedRemediationConfig creates a test StorageBasedRemediationConfig with common defaults
func (tn *TestNamespace) CreateStorageBasedRemediationConfig(name string,
	options ...func(*medik8sv1alpha1.StorageBasedRemediationConfig)) (*medik8sv1alpha1.StorageBasedRemediationConfig, error) {
	sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tn.Name,
		},
		Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
			WatchdogPath: "/dev/watchdog",
		},
	}

	// Apply any custom options
	for _, option := range options {
		option(sbrConfig)
	}

	err := tn.Clients.Client.Create(tn.Clients.Context, sbrConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create StorageBasedRemediationConfig %s: %w", name, err)
	}

	return sbrConfig, nil
}

const (
	// OperatorNamespaceName is the default operator namespace
	OperatorNamespaceName = "sbr-operator-system"
)

// OperatorNamespace returns a TestNamespace view of the operator namespace
func (tn *TestNamespace) OperatorNamespace() *TestNamespace {
	if tn.Name == OperatorNamespaceName {
		return tn
	}
	return &TestNamespace{
		Name:         OperatorNamespaceName,
		ArtifactsDir: tn.ArtifactsDir,
		Clients:      tn.Clients,
	}
}
