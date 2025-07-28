package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Config holds Kubernetes-specific configuration
type Config struct {
	StorageClassName    string
	AggressiveCoherency bool
}

// Manager handles Kubernetes operations
type Manager struct {
	config    *Config
	clientset *kubernetes.Clientset
}

// NewManager creates a new Kubernetes manager
func NewManager(ctx context.Context, config *Config) (*Manager, error) {
	// Build Kubernetes client
	clientset, err := buildKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes client: %w", err)
	}

	return &Manager{
		config:    config,
		clientset: clientset,
	}, nil
}

// CheckStandardNFSCSIDriver checks if the Standard NFS CSI driver is installed
func (m *Manager) CheckStandardNFSCSIDriver(ctx context.Context) error {
	// Check if the Standard NFS CSI driver DaemonSet exists
	_, err := m.clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "csi-nfs-node", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("Standard NFS CSI driver not found")
		}
		return fmt.Errorf("failed to check Standard NFS CSI driver: %w", err)
	}

	// Check if the controller is running
	_, err = m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "csi-nfs-controller", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("Standard NFS CSI controller not found")
		}
		return fmt.Errorf("failed to check Standard NFS CSI controller: %w", err)
	}

	return nil
}

// InstallStandardNFSCSIDriver installs the Standard NFS CSI driver
func (m *Manager) InstallStandardNFSCSIDriver(ctx context.Context) error {
	log.Println("🔧 Installing Standard NFS CSI driver...")

	// List of manifest URLs to apply in order
	manifestURLs := []string{
		"https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/csi-nfs-driverinfo.yaml",
		"https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/rbac-csi-nfs.yaml",
		"https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/csi-nfs-controller.yaml",
		"https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/csi-nfs-node.yaml",
	}

	// Apply each manifest
	for _, url := range manifestURLs {
		log.Printf("🔗 Applying manifest: %s", url)
		if err := m.applyManifestFromURL(ctx, url); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", url, err)
		}
	}

	// Wait for driver to be ready
	log.Println("⏳ Waiting for Standard NFS CSI driver to be ready...")
	if err := m.waitForStandardNFSCSIDriver(ctx); err != nil {
		return fmt.Errorf("Standard NFS CSI driver failed to become ready: %w", err)
	}

	log.Println("✅ Standard NFS CSI driver installed successfully")
	return nil
}

// CreateStandardNFSStorageClass creates a StorageClass for Standard NFS CSI driver with SBD cache coherency options
func (m *Manager) CreateStandardNFSStorageClass(ctx context.Context, nfsServer, nfsShare string) error {
	storageClassName := m.config.StorageClassName
	if storageClassName == "" {
		storageClassName = "sbd-nfs-coherent"
	}

	// Check if StorageClass already exists
	_, err := m.clientset.StorageV1().StorageClasses().Get(ctx, storageClassName, metav1.GetOptions{})
	if err == nil {
		log.Printf("💾 StorageClass '%s' already exists", storageClassName)
		return nil
	}

	// Choose mount options based on aggressive coherency setting
	var mountOptions []string
	var mountOptionsDesc string

	if m.config.AggressiveCoherency {
		// Aggressive cache coherency mount options for strict SBD coordination
		mountOptions = []string{
			"nfsvers=4.1",      // Use NFSv4.1 for better performance
			"hard",             // Hard mount for reliability
			"timeo=600",        // 60 second timeout
			"retrans=2",        // 2 retransmissions
			"noresvport",       // AWS EFS recommended
			"actimeo=0",        // Disable ALL attribute caching
			"lookupcache=none", // Disable directory lookup caching
			"rsize=65536",      // Smaller read size for coherency
			"wsize=65536",      // Smaller write size for coherency
			"sync",             // Force synchronous operations
			"local_lock=none",  // Send all locks to server for coordination
		}
		mountOptionsDesc = "aggressive cache coherency (actimeo=0, sync, lookupcache=none)"
	} else {
		// Standard EFS-optimized mount options
		mountOptions = []string{
			"nfsvers=4.1",   // Use NFSv4.1 for better performance and features
			"rsize=1048576", // 1MB read size for performance
			"wsize=1048576", // 1MB write size for performance
			"hard",          // Hard mount - retry indefinitely on server failure
			"timeo=600",     // 60 second timeout
			"retrans=2",     // 2 retransmissions before timeout
			"noresvport",    // AWS recommended for EFS cache coherency and reconnection
		}
		mountOptionsDesc = "EFS-optimized (nfsvers=4.1, hard, noresvport for reliability)"
	}

	// Create StorageClass with appropriate mount options
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
				"storage.medik8s.io/type":      "shared-nfs",
			},
		},
		Provisioner: "nfs.csi.k8s.io",
		Parameters: map[string]string{
			"server": nfsServer,
			"share":  nfsShare,
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

	log.Printf("✅ Standard NFS StorageClass '%s' created with SBD cache coherency options:", storageClassName)
	log.Printf("   📡 NFS Server: %s", nfsServer)
	log.Printf("   📁 NFS Share: %s", nfsShare)
	log.Printf("   🔄 Mount Options: %s", mountOptionsDesc)

	return nil
}

// TestCredentials tests the StorageClass by creating a test PVC
func (m *Manager) TestCredentials(ctx context.Context, storageClassName string) (bool, error) {
	testNamespace := "default"
	testPVCName := fmt.Sprintf("test-pvc-%d", time.Now().Unix())

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

	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var bound bool
	for {
		select {
		case <-timeout:
			// Clean up test PVC
			m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Delete(ctx, testPVCName, metav1.DeleteOptions{})
			return false, fmt.Errorf("test PVC failed to bind within 2 minutes")
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

// Cleanup removes the StorageClass created by this tool
func (m *Manager) Cleanup(ctx context.Context) error {
	storageClassName := m.config.StorageClassName
	if storageClassName == "" {
		storageClassName = "sbd-nfs-coherent"
	}

	// Delete StorageClass
	err := m.clientset.StorageV1().StorageClasses().Delete(ctx, storageClassName, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("🗑️ StorageClass '%s' not found (already deleted)", storageClassName)
			return nil
		}
		return fmt.Errorf("failed to delete StorageClass '%s': %w", storageClassName, err)
	}

	log.Printf("🗑️ Deleted StorageClass: %s", storageClassName)
	return nil
}

// buildKubernetesClient creates a Kubernetes client
func buildKubernetesClient() (*kubernetes.Clientset, error) {
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
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return clientset, nil
}

// applyManifestFromURL downloads and applies a Kubernetes manifest from URL
func (m *Manager) applyManifestFromURL(ctx context.Context, url string) error {
	// Download manifest
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download manifest: HTTP %d", resp.StatusCode)
	}

	// Read manifest content
	manifestData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}

	// Apply manifest using kubectl
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(manifestData))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply manifest: %w, output: %s", err, string(output))
	}

	log.Printf("📋 Applied manifest from %s", url)
	return nil
}

// waitForStandardNFSCSIDriver waits for the Standard NFS CSI driver to be ready
func (m *Manager) waitForStandardNFSCSIDriver(ctx context.Context) error {
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for Standard NFS CSI driver to be ready")
		case <-ticker.C:
			// Check DaemonSet
			ds, err := m.clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "csi-nfs-node", metav1.GetOptions{})
			if err != nil {
				log.Printf("⏳ Waiting for csi-nfs-node DaemonSet...")
				continue
			}

			// Check Deployment
			dep, err := m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "csi-nfs-controller", metav1.GetOptions{})
			if err != nil {
				log.Printf("⏳ Waiting for csi-nfs-controller Deployment...")
				continue
			}

			// Check if both are ready
			dsReady := ds.Status.NumberReady > 0 && ds.Status.NumberReady == ds.Status.DesiredNumberScheduled
			depReady := dep.Status.ReadyReplicas > 0 && dep.Status.ReadyReplicas == dep.Status.Replicas

			if dsReady && depReady {
				log.Printf("✅ Standard NFS CSI driver is ready (DaemonSet: %d/%d, Deployment: %d/%d)",
					ds.Status.NumberReady, ds.Status.DesiredNumberScheduled,
					dep.Status.ReadyReplicas, dep.Status.Replicas)
				return nil
			}

			log.Printf("⏳ Standard NFS CSI driver not ready yet (DaemonSet: %d/%d, Deployment: %d/%d)",
				ds.Status.NumberReady, ds.Status.DesiredNumberScheduled,
				dep.Status.ReadyReplicas, dep.Status.Replicas)
		}
	}
}
