package odf

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"os"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Config holds ODF-specific configuration
type Config struct {
	StorageClassName       string
	ClusterName            string
	Namespace              string
	StorageSize            string
	ReplicaCount           int
	EnableEncryption       bool
	EnableStorageDeviceSet bool
	AggressiveCoherency    bool
	DryRun                 bool
	UpdateMode             bool
}

// SetupResult contains the results of ODF storage setup
type SetupResult struct {
	StorageClassName string
	ClusterName      string
	Namespace        string
	TestPassed       bool
}

// Manager handles ODF operations
type Manager struct {
	config        *Config
	clientset     *kubernetes.Clientset
	dynamicClient dynamic.Interface
}

// NewManager creates a new ODF storage manager
func NewManager(ctx context.Context, config *Config) (*Manager, error) {
	// Set defaults
	setDefaults(config)

	if config.DryRun {
		log.Println("[DRY-RUN] Would create ODF storage manager with configuration:")
		printConfig(config)
		return &Manager{config: config}, nil
	}

	// Build Kubernetes clients
	clientset, dynamicClient, err := buildKubernetesClients()
	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}

	return &Manager{
		config:        config,
		clientset:     clientset,
		dynamicClient: dynamicClient,
	}, nil
}

// SetupODFStorage orchestrates the complete ODF setup process
func (m *Manager) SetupODFStorage(ctx context.Context) (*SetupResult, error) {
	result := &SetupResult{
		StorageClassName: m.config.StorageClassName,
		ClusterName:      m.config.ClusterName,
		Namespace:        m.config.Namespace,
	}

	if m.config.DryRun {
		return m.dryRunSetup(ctx)
	}

	log.Printf("🚀 Starting OpenShift Data Foundation setup...")

	// Step 1: Check and install ODF operator
	log.Println("🔧 Checking OpenShift Data Foundation operator...")
	if err := m.checkODFOperator(ctx); err != nil {
		log.Println("⚠️ ODF operator not found, installing...")
		if installErr := m.installODFOperator(ctx); installErr != nil {
			return nil, fmt.Errorf("failed to install ODF operator: %w", installErr)
		}
	} else {
		log.Println("✅ ODF operator found")
	}

	// Step 2: Wait for operator to be ready
	log.Println("⏳ Waiting for ODF operator to be ready...")
	if err := m.waitForODFOperator(ctx); err != nil {
		return nil, fmt.Errorf("ODF operator failed to become ready: %w", err)
	}

	// Step 3: Create StorageCluster
	log.Println("🏗️ Creating ODF StorageCluster...")
	if err := m.createStorageCluster(ctx); err != nil {
		return nil, fmt.Errorf("failed to create StorageCluster: %w", err)
	}

	// Step 4: Wait for StorageCluster to be ready
	log.Println("⏳ Waiting for StorageCluster to be ready...")
	if err := m.waitForStorageCluster(ctx); err != nil {
		return nil, fmt.Errorf("StorageCluster failed to become ready: %w", err)
	}

	// Step 5: Create CephFS StorageClass
	log.Println("💾 Creating CephFS StorageClass with SBD cache coherency options...")
	if err := m.createCephFSStorageClass(ctx); err != nil {
		return nil, fmt.Errorf("failed to create CephFS StorageClass: %w", err)
	}
	log.Printf("✅ CephFS StorageClass created: %s", result.StorageClassName)

	// Step 6: Test CephFS storage
	log.Println("🧪 Testing CephFS storage...")
	testPassed, err := m.testCephFSStorage(ctx, result.StorageClassName)
	if err != nil {
		log.Printf("⚠️ Storage test failed: %v", err)
		result.TestPassed = false
	} else {
		result.TestPassed = testPassed
		if testPassed {
			log.Println("✅ CephFS storage test passed")
		} else {
			log.Println("⚠️ Storage test failed, but setup completed")
		}
	}

	return result, nil
}

// Cleanup removes created ODF resources
func (m *Manager) Cleanup(ctx context.Context) error {
	if m.config.DryRun {
		log.Println("[DRY-RUN] Would clean up ODF resources")
		return nil
	}

	log.Println("🧹 Starting ODF cleanup...")

	// Cleanup in reverse order
	if err := m.cleanupStorageClass(ctx); err != nil {
		log.Printf("⚠️ StorageClass cleanup failed: %v", err)
	}

	if err := m.cleanupStorageCluster(ctx); err != nil {
		log.Printf("⚠️ StorageCluster cleanup failed: %v", err)
	}

	log.Println("✅ ODF cleanup completed")
	return nil
}

// checkODFOperator checks if the ODF operator is installed
func (m *Manager) checkODFOperator(ctx context.Context) error {
	// Check for ODF operator subscription
	subscriptionGVR := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "subscriptions",
	}

	subscriptions, err := m.dynamicClient.Resource(subscriptionGVR).Namespace(m.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list subscriptions: %w", err)
	}

	for _, sub := range subscriptions.Items {
		if name, found, _ := unstructured.NestedString(sub.Object, "spec", "name"); found {
			if name == "odf-operator" || name == "ocs-operator" {
				return nil
			}
		}
	}

	return fmt.Errorf("ODF operator not found")
}

// installODFOperator installs the ODF operator via OperatorHub
func (m *Manager) installODFOperator(ctx context.Context) error {
	log.Println("📦 Installing ODF operator...")

	// Create namespace if it doesn't exist
	if err := m.createNamespace(ctx); err != nil {
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	// Create OperatorGroup
	if err := m.createOperatorGroup(ctx); err != nil {
		return fmt.Errorf("failed to create OperatorGroup: %w", err)
	}

	// Create Subscription
	if err := m.createSubscription(ctx); err != nil {
		return fmt.Errorf("failed to create Subscription: %w", err)
	}

	log.Println("✅ ODF operator installation initiated")
	return nil
}

// createNamespace creates the openshift-storage namespace
func (m *Manager) createNamespace(ctx context.Context) error {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.config.Namespace,
			Labels: map[string]string{
				"openshift.io/cluster-monitoring": "true",
			},
		},
	}

	_, err := m.clientset.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	log.Printf("✅ Namespace %s ensured", m.config.Namespace)
	return nil
}

// createOperatorGroup creates the OperatorGroup for ODF
func (m *Manager) createOperatorGroup(ctx context.Context) error {
	operatorGroupGVR := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1",
		Resource: "operatorgroups",
	}

	operatorGroup := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]interface{}{
				"name":      "openshift-storage-operatorgroup",
				"namespace": m.config.Namespace,
			},
			"spec": map[string]interface{}{
				"targetNamespaces": []string{m.config.Namespace},
			},
		},
	}

	_, err := m.dynamicClient.Resource(operatorGroupGVR).Namespace(m.config.Namespace).Create(ctx, operatorGroup, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create OperatorGroup: %w", err)
	}

	log.Println("✅ OperatorGroup created")
	return nil
}

// createSubscription creates the Subscription for ODF operator
func (m *Manager) createSubscription(ctx context.Context) error {
	subscriptionGVR := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "subscriptions",
	}

	subscription := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "odf-operator",
				"namespace": m.config.Namespace,
			},
			"spec": map[string]interface{}{
				"channel":             "stable-4.15",
				"installPlanApproval": "Automatic",
				"name":                "odf-operator",
				"source":              "redhat-operators",
				"sourceNamespace":     "openshift-marketplace",
			},
		},
	}

	_, err := m.dynamicClient.Resource(subscriptionGVR).Namespace(m.config.Namespace).Create(ctx, subscription, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Subscription: %w", err)
	}

	log.Println("✅ Subscription created")
	return nil
}

// waitForODFOperator waits for the ODF operator to be ready
func (m *Manager) waitForODFOperator(ctx context.Context) error {
	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	csvGVR := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "clusterserviceversions",
	}

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for ODF operator to be ready")
		case <-ticker.C:
			csvs, err := m.dynamicClient.Resource(csvGVR).Namespace(m.config.Namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				log.Printf("⏳ Waiting for ODF operator CSV...")
				continue
			}

			for _, csv := range csvs.Items {
				if name, found, _ := unstructured.NestedString(csv.Object, "metadata", "name"); found {
					if strings.Contains(name, "odf-operator") {
						phase, found, _ := unstructured.NestedString(csv.Object, "status", "phase")
						if found && phase == "Succeeded" {
							log.Println("✅ ODF operator is ready")
							return nil
						}
					}
				}
			}

			log.Printf("⏳ ODF operator not ready yet...")
		}
	}
}

// createStorageCluster creates the StorageCluster resource
func (m *Manager) createStorageCluster(ctx context.Context) error {
	storageClusterGVR := schema.GroupVersionResource{
		Group:    "ocs.openshift.io",
		Version:  "v1",
		Resource: "storageclusters",
	}

	storageCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "ocs.openshift.io/v1",
			"kind":       "StorageCluster",
			"metadata": map[string]interface{}{
				"name":      m.config.ClusterName,
				"namespace": m.config.Namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "sbd-operator",
				},
			},
			"spec": map[string]interface{}{
				"storageDeviceSets": []interface{}{
					map[string]interface{}{
						"name":             "ocs-deviceset",
						"count":            m.config.ReplicaCount,
						"replica":          1,
						"portable":         true,
						"preparePlacement": map[string]interface{}{},
						"dataPVCTemplate": map[string]interface{}{
							"spec": map[string]interface{}{
								"accessModes": []string{"ReadWriteOnce"},
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"storage": m.config.StorageSize,
									},
								},
								"storageClassName": "gp3-csi",
								"volumeMode":       "Block",
							},
						},
					},
				},
			},
		},
	}

	// Add encryption if enabled
	if m.config.EnableEncryption {
		spec := storageCluster.Object["spec"].(map[string]interface{})
		spec["encryption"] = map[string]interface{}{
			"enable": true,
		}
	}

	_, err := m.dynamicClient.Resource(storageClusterGVR).Namespace(m.config.Namespace).Create(ctx, storageCluster, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create StorageCluster: %w", err)
	}

	log.Printf("✅ StorageCluster %s created", m.config.ClusterName)
	return nil
}

// waitForStorageCluster waits for the StorageCluster to be ready
func (m *Manager) waitForStorageCluster(ctx context.Context) error {
	timeout := time.After(15 * time.Minute)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	storageClusterGVR := schema.GroupVersionResource{
		Group:    "ocs.openshift.io",
		Version:  "v1",
		Resource: "storageclusters",
	}

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for StorageCluster to be ready")
		case <-ticker.C:
			cluster, err := m.dynamicClient.Resource(storageClusterGVR).Namespace(m.config.Namespace).Get(ctx, m.config.ClusterName, metav1.GetOptions{})
			if err != nil {
				log.Printf("⏳ Waiting for StorageCluster...")
				continue
			}

			phase, found, _ := unstructured.NestedString(cluster.Object, "status", "phase")
			if found && phase == "Ready" {
				log.Println("✅ StorageCluster is ready")
				return nil
			}

			log.Printf("⏳ StorageCluster phase: %s", phase)
		}
	}
}

// createCephFSStorageClass creates a CephFS StorageClass optimized for SBD
func (m *Manager) createCephFSStorageClass(ctx context.Context) error {
	// Check if StorageClass already exists
	_, err := m.clientset.StorageV1().StorageClasses().Get(ctx, m.config.StorageClassName, metav1.GetOptions{})
	if err == nil {
		log.Printf("💾 StorageClass '%s' already exists", m.config.StorageClassName)
		return nil
	}

	// Choose mount options based on aggressive coherency setting
	var mountOptions []string
	var mountOptionsDesc string

	if m.config.AggressiveCoherency {
		// Aggressive cache coherency mount options for strict SBD coordination
		mountOptions = []string{
			"cache=strict",          // Disable client-side caching for real-time updates
			"recover_session=clean", // Clean session recovery for reliability
			"sync",                  // Force synchronous operations
			"_netdev",               // Ensure network availability before mounting
		}
		mountOptionsDesc = "aggressive cache coherency (cache=strict, sync, recover_session=clean)"
	} else {
		// Standard CephFS mount options optimized for performance and reliability
		mountOptions = []string{
			"_netdev", // Ensure network availability before mounting
		}
		mountOptionsDesc = "standard CephFS (optimized for performance)"
	}

	// Create CephFS StorageClass with appropriate mount options
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.config.StorageClassName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
				"storage.medik8s.io/type":      "shared-cephfs",
			},
			Annotations: map[string]string{
				"description": "CephFS StorageClass optimized for SBD coordination with POSIX file locking",
			},
		},
		Provisioner: "openshift-storage.cephfs.csi.ceph.com",
		Parameters: map[string]string{
			"clusterID": m.config.Namespace,
			"fsName":    "ocs-storagecluster-cephfilesystem",
			"pool":      "ocs-storagecluster-cephfilesystem-data0",
		},
		MountOptions:         mountOptions,
		AllowVolumeExpansion: &[]bool{true}[0],
		ReclaimPolicy:        &[]corev1.PersistentVolumeReclaimPolicy{corev1.PersistentVolumeReclaimRetain}[0],
		VolumeBindingMode:    &[]storagev1.VolumeBindingMode{storagev1.VolumeBindingImmediate}[0],
	}

	_, err = m.clientset.StorageV1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create StorageClass: %w", err)
	}

	log.Printf("✅ CephFS StorageClass '%s' created with SBD cache coherency options:", m.config.StorageClassName)
	log.Printf("   🗄️ CephFS: ocs-storagecluster-cephfilesystem")
	log.Printf("   🔄 Mount Options: %s", mountOptionsDesc)

	return nil
}

// testCephFSStorage tests the CephFS StorageClass by creating a test PVC
func (m *Manager) testCephFSStorage(ctx context.Context, storageClassName string) (bool, error) {
	testNamespace := "default"
	testPVCName := fmt.Sprintf("test-cephfs-pvc-%d", time.Now().Unix())

	// Create test PVC
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPVCName,
			Namespace: testNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
				"storage.medik8s.io/test":      "true",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
			StorageClassName: &storageClassName,
		},
	}

	// Create PVC
	_, err := m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to create test PVC: %w", err)
	}

	// Wait for PVC to be bound
	log.Printf("🧪 Created test PVC '%s', waiting for it to bind...", testPVCName)

	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var bound bool
	for {
		select {
		case <-timeout:
			// Clean up test PVC
			m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Delete(ctx, testPVCName, metav1.DeleteOptions{})
			return false, fmt.Errorf("test PVC failed to bind within 5 minutes")
		case <-ticker.C:
			testPVC, err := m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, testPVCName, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if testPVC.Status.Phase == corev1.ClaimBound {
				bound = true
				log.Printf("✅ Test PVC successfully bound")
				break
			}
		}
		if bound {
			break
		}
	}

	// Clean up test PVC
	err = m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Delete(ctx, testPVCName, metav1.DeleteOptions{})
	if err != nil {
		log.Printf("⚠️ Failed to clean up test PVC: %v", err)
	} else {
		log.Printf("🧹 Cleaned up test PVC")
	}

	return bound, nil
}

// cleanupStorageClass removes the created StorageClass
func (m *Manager) cleanupStorageClass(ctx context.Context) error {
	err := m.clientset.StorageV1().StorageClasses().Delete(ctx, m.config.StorageClassName, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("🗑️ StorageClass '%s' not found (already deleted)", m.config.StorageClassName)
			return nil
		}
		return fmt.Errorf("failed to delete StorageClass '%s': %w", m.config.StorageClassName, err)
	}

	log.Printf("🗑️ Deleted StorageClass: %s", m.config.StorageClassName)
	return nil
}

// cleanupStorageCluster removes the created StorageCluster
func (m *Manager) cleanupStorageCluster(ctx context.Context) error {
	storageClusterGVR := schema.GroupVersionResource{
		Group:    "ocs.openshift.io",
		Version:  "v1",
		Resource: "storageclusters",
	}

	err := m.dynamicClient.Resource(storageClusterGVR).Namespace(m.config.Namespace).Delete(ctx, m.config.ClusterName, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("🗑️ StorageCluster '%s' not found (already deleted)", m.config.ClusterName)
			return nil
		}
		return fmt.Errorf("failed to delete StorageCluster '%s': %w", m.config.ClusterName, err)
	}

	log.Printf("🗑️ Deleted StorageCluster: %s", m.config.ClusterName)
	return nil
}

// dryRunSetup shows what would be done without executing
func (m *Manager) dryRunSetup(ctx context.Context) (*SetupResult, error) {
	result := &SetupResult{
		StorageClassName: m.config.StorageClassName,
		ClusterName:      m.config.ClusterName,
		Namespace:        m.config.Namespace,
		TestPassed:       true,
	}

	log.Println("[DRY-RUN] 🔧 Would check/install OpenShift Data Foundation operator")
	log.Println("[DRY-RUN] 📦 Would create OperatorGroup and Subscription for ODF")
	log.Println("[DRY-RUN] 🏗️ Would create StorageCluster with configuration:")
	log.Printf("[DRY-RUN]   📊 Storage Size: %s", m.config.StorageSize)
	log.Printf("[DRY-RUN]   🔄 Replica Count: %d", m.config.ReplicaCount)
	log.Printf("[DRY-RUN]   🔐 Encryption: %t", m.config.EnableEncryption)
	log.Println("[DRY-RUN] 💾 Would create CephFS StorageClass with SBD cache coherency options:")
	if m.config.AggressiveCoherency {
		log.Println("[DRY-RUN]   🔄 Mount Options: cache=strict, sync, recover_session=clean")
	} else {
		log.Println("[DRY-RUN]   🔄 Mount Options: standard CephFS optimized")
	}
	log.Println("[DRY-RUN] 🧪 Would test CephFS storage with ReadWriteMany PVC")

	return result, nil
}

// buildKubernetesClients creates Kubernetes clients
func buildKubernetesClients() (*kubernetes.Clientset, dynamic.Interface, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		var kubeconfig string

		// Check KUBECONFIG environment variable first
		if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
			kubeconfig = kubeconfigEnv
		} else if home := homedir.HomeDir(); home != "" {
			kubeconfig = fmt.Sprintf("%s/.kube/config", home)
		}

		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return clientset, dynamicClient, nil
}

// setDefaults sets default values for configuration
func setDefaults(config *Config) {
	if config.StorageClassName == "" {
		config.StorageClassName = "sbd-cephfs"
	}
	if config.ClusterName == "" {
		config.ClusterName = "ocs-storagecluster"
	}
	if config.Namespace == "" {
		config.Namespace = "openshift-storage"
	}
	if config.StorageSize == "" {
		config.StorageSize = "2Ti"
	}
	if config.ReplicaCount == 0 {
		config.ReplicaCount = 3
	}
}

// printConfig prints the configuration for dry-run mode
func printConfig(config *Config) {
	log.Printf("  StorageClass Name: %s", config.StorageClassName)
	log.Printf("  Cluster Name: %s", config.ClusterName)
	log.Printf("  Namespace: %s", config.Namespace)
	log.Printf("  Storage Size: %s", config.StorageSize)
	log.Printf("  Replica Count: %d", config.ReplicaCount)
	log.Printf("  Encryption: %t", config.EnableEncryption)
	log.Printf("  Aggressive Coherency: %t", config.AggressiveCoherency)
	log.Printf("  Update Mode: %t", config.UpdateMode)
}
