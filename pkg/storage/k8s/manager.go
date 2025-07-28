package k8s

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/efs"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
)

// Config holds Kubernetes-specific configuration
type Config struct {
	StorageClassName string
	ClusterName      string
}

// Manager handles Kubernetes operations
type Manager struct {
	config    *Config
	clientset *kubernetes.Clientset
}

// ClusterDetector handles cluster auto-detection
type ClusterDetector struct {
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

// InstallEFSCSIDriver installs or verifies the EFS CSI driver
func (m *Manager) InstallEFSCSIDriver(ctx context.Context) error {
	// Check if EFS CSI driver is already installed
	_, err := m.clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "efs-csi-node", metav1.GetOptions{})
	if err == nil {
		log.Println("🔧 EFS CSI driver already installed")
		return nil
	}

	// Create namespace if it doesn't exist
	_, err = m.clientset.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return fmt.Errorf("kube-system namespace not found - cluster may not be ready")
	}

	// Apply EFS CSI driver manifests
	if err := m.applyEFSCSIDriver(ctx); err != nil {
		return fmt.Errorf("failed to apply EFS CSI driver: %w", err)
	}

	// Wait for driver to be ready
	return m.waitForEFSCSIDriver(ctx)
}

// ConfigureServiceAccount configures the EFS CSI driver service account with IAM role
func (m *Manager) ConfigureServiceAccount(ctx context.Context, roleARN string) error {
	// Get or create service account
	serviceAccount, err := m.clientset.CoreV1().ServiceAccounts("kube-system").Get(ctx, "efs-csi-controller-sa", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("EFS CSI controller service account not found - driver may not be installed")
		}
		return fmt.Errorf("failed to get service account: %w", err)
	}

	// Check if this is an OpenShift cluster
	if !m.isOpenShiftCluster(ctx) {
		return fmt.Errorf("this tool only supports OpenShift clusters - detected unsupported cluster type")
	}

	log.Printf("🔍 Detected OpenShift cluster - configuring AWS credentials")

	// Remove any existing IRSA annotation
	if serviceAccount.Annotations != nil {
		delete(serviceAccount.Annotations, "eks.amazonaws.com/role-arn")
	}

	// Update service account to remove IRSA annotation
	_, err = m.clientset.CoreV1().ServiceAccounts("kube-system").Update(ctx, serviceAccount, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update service account: %w", err)
	}

	// Configure EFS CSI controller deployment to use AWS credentials secret
	err = m.configureAWSCredentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to configure AWS credentials: %w", err)
	}

	log.Printf("🔗 Configured EFS CSI driver with AWS credentials secret")

	// Restart EFS CSI controller to pick up new credentials
	return m.restartEFSCSIController(ctx)
}

// CreateStorageClass creates the StorageClass for EFS
func (m *Manager) CreateStorageClass(ctx context.Context, efsID string) error {
	// Check if StorageClass already exists
	existingSC, err := m.clientset.StorageV1().StorageClasses().Get(ctx, m.config.StorageClassName, metav1.GetOptions{})
	if err == nil {
		// Check if it's configured correctly
		if existingSC.Provisioner == "efs.csi.aws.com" {
			log.Printf("💾 StorageClass %s already exists and is configured correctly", m.config.StorageClassName)
			return nil
		}
		// Delete incorrect StorageClass
		log.Printf("💾 Deleting incorrectly configured StorageClass: %s", m.config.StorageClassName)
		err = m.clientset.StorageV1().StorageClasses().Delete(ctx, m.config.StorageClassName, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete existing StorageClass: %w", err)
		}
	}

	// Determine mount options and provisioning mode based on EFS CSI driver capabilities
	var mountOptions []string
	var provisioningMode string

	// Most EFS CSI driver versions only support efs-ap (EFS utils mounting)
	supportsStandardNFS := m.detectEFSCSICapabilities(ctx)

	if supportsStandardNFS && m.isOpenShiftCluster(ctx) {
		// For OpenShift with EFS CSI drivers that support standard NFS mounting
		log.Printf("💾 Configuring StorageClass for OpenShift cluster - using standard NFS mounting with SBD cache coherency options")
		provisioningMode = "efs-mount"
		mountOptions = []string{
			"nfsvers=4.1",
			"rsize=1048576",
			"wsize=1048576",
			"hard",
			"timeo=600",
			"retrans=2",
			"cache=none",      // Disable client-side caching for SBD coherency
			"sync",            // Force synchronous operations
			"local_lock=none", // Send all locks to server (NFSv4 default, explicit for clarity)
		}
	} else {
		// For all other cases: use EFS utils mounting which handles cache coherency internally
		if m.isOpenShiftCluster(ctx) {
			log.Printf("💾 Configuring StorageClass for OpenShift cluster - using EFS utils mounting (EFS CSI driver doesn't support standard NFS)")
		} else {
			log.Printf("💾 Configuring StorageClass for EKS cluster - using EFS utils mounting (cache coherency handled internally)")
		}
		provisioningMode = "efs-ap"

		// For EFS utils mounting, follow the official pattern: no explicit mount options
		// The EFS CSI driver handles all mounting logic internally for efs-ap mode
		mountOptions = []string{}
		log.Printf("💾 Using EFS mount options: none (following official EFS CSI pattern - driver handles mounting internally)")
	}

	// Note: For SBD workloads requiring explicit cache coherency controls:
	// Consider using the Standard NFS CSI driver (nfs.csi.k8s.io) instead of EFS CSI driver
	// Standard NFS CSI supports: cache=none, sync, local_lock=none for proper SBD operation

	// Create new StorageClass
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.config.StorageClassName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
			},
		},
		Provisioner: "efs.csi.aws.com",
		Parameters: map[string]string{
			"provisioningMode": provisioningMode,
			"fileSystemId":     efsID,
			"directoryPerms":   "0755",
			"gidRangeStart":    "1000",
			"gidRangeEnd":      "2000",
			"basePath":         "/sbd",
		},
		MountOptions:         mountOptions,
		AllowVolumeExpansion: &[]bool{true}[0],
		VolumeBindingMode:    &[]storagev1.VolumeBindingMode{storagev1.VolumeBindingImmediate}[0],
	}

	_, err = m.clientset.StorageV1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create StorageClass: %w", err)
	}

	log.Printf("💾 Created StorageClass: %s", m.config.StorageClassName)

	// Validate the StorageClass configuration for SBD compatibility
	if err := m.validateStorageClassForSBD(storageClass); err != nil {
		log.Printf("⚠️ StorageClass validation warning: %v", err)
		log.Printf("💡 SBD may experience cache coherency issues with this configuration")
	} else {
		log.Printf("✅ StorageClass configuration validated for SBD cache coherency")
	}

	return nil
}

// validateStorageClassForSBD validates that the StorageClass is properly configured for SBD
func (m *Manager) validateStorageClassForSBD(sc *storagev1.StorageClass) error {
	provisioningMode := sc.Parameters["provisioningMode"]
	mountOptions := sc.MountOptions

	// Check for incompatible combinations
	if provisioningMode == "efs-ap" {
		// EFS utils mounting - check for incompatible NFS options
		incompatibleOptions := []string{"cache=none", "sync", "nfsvers"}
		for _, option := range mountOptions {
			for _, incompatible := range incompatibleOptions {
				if strings.Contains(option, incompatible) {
					return fmt.Errorf("mount option '%s' is incompatible with EFS utils mounting (efs-ap)", option)
				}
			}
		}

		// EFS utils should have tls and regional
		hasTLS := false
		hasRegional := false
		for _, option := range mountOptions {
			if option == "tls" {
				hasTLS = true
			}
			if option == "regional" {
				hasRegional = true
			}
		}

		if !hasTLS || !hasRegional {
			return fmt.Errorf("EFS utils mounting should include 'tls' and 'regional' mount options")
		}

		log.Printf("✅ EFS utils mounting configured correctly - cache coherency handled internally")

	} else if provisioningMode == "efs-mount" {
		// Standard NFS mounting - check for cache coherency options
		hasCacheNone := false
		hasSync := false

		for _, option := range mountOptions {
			if option == "cache=none" {
				hasCacheNone = true
			}
			if option == "sync" {
				hasSync = true
			}
		}

		if !hasCacheNone || !hasSync {
			return fmt.Errorf("standard NFS mounting requires 'cache=none' and 'sync' mount options for SBD cache coherency")
		}

		log.Printf("✅ Standard NFS mounting configured correctly with cache coherency options")
	}

	return nil
}

// TestCredentials tests the EFS CSI driver credentials by creating a test PVC
func (m *Manager) TestCredentials(ctx context.Context, storageClassName string) (bool, error) {
	testPVCName := fmt.Sprintf("sbd-test-pvc-%d", time.Now().Unix())
	testNamespace := "default"

	log.Printf("🧪 Creating test PVC %s to verify EFS CSI driver credentials", testPVCName)

	// Create test PVC
	testPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPVCName,
			Namespace: testNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbd.medik8s.io/test":          "credential-test",
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
	_, err := m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Create(ctx, testPVC, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to create test PVC: %w", err)
	}

	// Wait for PVC to be bound (with timeout and better error reporting)
	success := false
	var lastStatus corev1.PersistentVolumeClaimPhase
	var lastMessage string

	for i := 0; i < 60; i++ { // Wait up to 5 minutes
		pvc, err := m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, testPVCName, metav1.GetOptions{})
		if err != nil {
			log.Printf("⚠️ Failed to get test PVC status: %v", err)
			break
		}

		lastStatus = pvc.Status.Phase

		// Get any events related to the PVC for better error reporting
		if i%6 == 0 { // Check events every 30 seconds
			events, err := m.clientset.CoreV1().Events(testNamespace).List(ctx, metav1.ListOptions{
				FieldSelector: fmt.Sprintf("involvedObject.name=%s", testPVCName),
			})
			if err == nil && len(events.Items) > 0 {
				lastEvent := events.Items[len(events.Items)-1]
				lastMessage = lastEvent.Message
				log.Printf("🔍 Test PVC status: %s - %s", lastStatus, lastMessage)
			} else {
				log.Printf("🔍 Test PVC status: %s", lastStatus)
			}
		}

		if pvc.Status.Phase == corev1.ClaimBound {
			success = true
			log.Printf("✅ Test PVC successfully bound")
			break
		}

		time.Sleep(5 * time.Second)
	}

	// Clean up test PVC
	deleteErr := m.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Delete(ctx, testPVCName, metav1.DeleteOptions{})
	if deleteErr != nil {
		log.Printf("⚠️ Warning: failed to clean up test PVC: %v", deleteErr)
	} else {
		log.Printf("🧹 Cleaned up test PVC")
	}

	if !success {
		// Provide detailed error analysis for common issues
		errorAnalysis := m.analyzeTestPVCFailure(lastStatus, lastMessage)
		return false, fmt.Errorf("test PVC failed to bind (final status: %s, last message: %s)\n%s", lastStatus, lastMessage, errorAnalysis)
	}

	return true, nil
}

// analyzeTestPVCFailure provides detailed analysis and solutions for common PVC binding failures
func (m *Manager) analyzeTestPVCFailure(status corev1.PersistentVolumeClaimPhase, message string) string {
	analysis := "\n🔍 TROUBLESHOOTING ANALYSIS:\n"

	// Check for AWS permission issues
	if strings.Contains(message, "Access Denied") || strings.Contains(message, "AccessDenied") ||
		strings.Contains(message, "Unauthenticated") || strings.Contains(message, "UnauthorizedOperation") {
		analysis += "\n❌ AWS PERMISSION ISSUE DETECTED:\n"
		analysis += "   The EFS CSI driver cannot authenticate with AWS or lacks required permissions.\n\n"
		analysis += "🔧 SOLUTION:\n"
		analysis += "   1. AWS permissions are validated during setup - check setup logs for specific missing permissions\n\n"
		analysis += "   2. Verify AWS credentials are configured correctly in the EFS CSI driver:\n"
		analysis += "      kubectl get secret aws-creds -n kube-system -o yaml\n\n"
		analysis += "   3. Check if EFS CSI controller has correct environment variables:\n"
		analysis += "      kubectl describe deployment efs-csi-controller -n kube-system\n\n"
		analysis += "   4. Test credentials manually:\n"
		analysis += "      aws efs describe-file-systems --max-items 1\n"
		return analysis
	}

	// Check for EFS filesystem issues
	if strings.Contains(message, "FileSystemNotFound") || strings.Contains(message, "InvalidFileSystemId") {
		analysis += "\n❌ EFS FILESYSTEM ISSUE:\n"
		analysis += "   The specified EFS filesystem ID is invalid or not accessible.\n\n"
		analysis += "🔧 SOLUTION:\n"
		analysis += "   1. Verify the EFS filesystem exists:\n"
		analysis += "      aws efs describe-file-systems\n\n"
		analysis += "   2. Check the StorageClass parameters:\n"
		analysis += "      kubectl get storageclass sbd-efs-sc -o yaml\n"
		return analysis
	}

	// Check for networking issues
	if strings.Contains(message, "MountTargetNotFound") || strings.Contains(message, "SubnetNotFound") ||
		strings.Contains(message, "SecurityGroupNotFound") {
		analysis += "\n❌ EFS NETWORKING ISSUE:\n"
		analysis += "   EFS mount targets or networking configuration is incorrect.\n\n"
		analysis += "🔧 SOLUTION:\n"
		analysis += "   1. Verify EFS mount targets exist:\n"
		analysis += "      aws efs describe-mount-targets --file-system-id <your-efs-id>\n\n"
		analysis += "   2. Check security group allows NFS traffic (port 2049):\n"
		analysis += "      aws ec2 describe-security-groups --group-ids <security-group-id>\n"
		return analysis
	}

	// Check for general CSI driver issues
	if strings.Contains(message, "failed to provision volume") || strings.Contains(message, "rpc error") {
		analysis += "\n❌ EFS CSI DRIVER ISSUE:\n"
		analysis += "   The EFS CSI driver encountered an error during volume provisioning.\n\n"
		analysis += "🔧 SOLUTION:\n"
		analysis += "   1. Check EFS CSI controller logs:\n"
		analysis += "      kubectl logs deployment/efs-csi-controller -n kube-system -c efs-plugin\n\n"
		analysis += "   2. Verify EFS CSI driver is running:\n"
		analysis += "      kubectl get pods -n kube-system -l app=efs-csi-controller\n\n"
		analysis += "   3. Check if CSI driver is registered:\n"
		analysis += "      kubectl get csidriver efs.csi.aws.com\n"
		return analysis
	}

	// Generic timeout or pending issues
	if status == corev1.ClaimPending {
		analysis += "\n❌ PVC STUCK IN PENDING STATE:\n"
		analysis += "   The PVC could not be provisioned within the timeout period.\n\n"
		analysis += "🔧 SOLUTION:\n"
		analysis += "   1. Check recent events for this PVC:\n"
		analysis += "      kubectl describe pvc <pvc-name> -n default\n\n"
		analysis += "   2. Verify StorageClass exists and is configured:\n"
		analysis += "      kubectl get storageclass sbd-efs-sc\n\n"
		analysis += "   3. Check if there are sufficient resources:\n"
		analysis += "      kubectl get pv\n"
		return analysis
	}

	// Fallback for unknown issues
	analysis += "\n❓ UNKNOWN ISSUE:\n"
	analysis += "   Please check the following for more details:\n"
	analysis += "   1. PVC events: kubectl describe pvc <pvc-name>\n"
	analysis += "   2. EFS CSI logs: kubectl logs deployment/efs-csi-controller -n kube-system\n"
	analysis += "   3. StorageClass config: kubectl get storageclass sbd-efs-sc -o yaml\n"

	return analysis
}

// Cleanup removes all Kubernetes resources created by this manager
func (m *Manager) Cleanup(ctx context.Context) error {
	// Delete StorageClass
	err := m.clientset.StorageV1().StorageClasses().Delete(ctx, m.config.StorageClassName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete StorageClass: %v", err)
	} else {
		log.Printf("🗑️ Deleted StorageClass: %s", m.config.StorageClassName)
	}

	// Clean up test PVCs
	pvcs, err := m.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{
		LabelSelector: "sbd.medik8s.io/test=credential-test",
	})
	if err == nil {
		for _, pvc := range pvcs.Items {
			_ = m.clientset.CoreV1().PersistentVolumeClaims(pvc.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{})
		}
	}

	return nil
}

// DetectClusterName detects the cluster name from various sources using KUBECONFIG and K8s API
func (d *ClusterDetector) DetectClusterName(ctx context.Context) (string, error) {
	clientset, err := buildKubernetesClient()
	if err != nil {
		return "", fmt.Errorf("failed to build Kubernetes client: %w", err)
	}
	d.clientset = clientset

	log.Println("🔍 Detecting cluster name using multiple methods...")

	// Method 1: Check KUBECONFIG current context and cluster name
	if clusterName, err := d.detectFromKubeconfig(); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from kubeconfig: %s", clusterName)
		return clusterName, nil
	}

	// Method 2: Check kube-system namespace for cluster-info
	if clusterName, err := d.detectFromKubeSystemConfigMap(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from kube-system ConfigMap: %s", clusterName)
		return clusterName, nil
	}

	// Method 3: Check cluster-info ConfigMap in kube-public
	if clusterName, err := d.detectFromClusterInfo(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from cluster-info: %s", clusterName)
		return clusterName, nil
	}

	// Method 4: Check infrastructure name (OpenShift)
	if clusterName, err := d.detectFromOpenShiftInfrastructure(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from OpenShift infrastructure: %s", clusterName)
		return clusterName, nil
	}

	// Method 5: Check node labels for cluster name
	if clusterName, err := d.detectFromNodeLabels(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from node labels: %s", clusterName)
		return clusterName, nil
	}

	// Method 6: Extract from API server URL
	if clusterName, err := d.detectFromAPIServer(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from API server URL: %s", clusterName)
		return clusterName, nil
	}

	// Method 7: Check for EKS cluster tags on nodes
	if clusterName, err := d.detectFromEKSNodeTags(ctx); err == nil && clusterName != "" {
		log.Printf("🔍 Detected cluster name from EKS node tags: %s", clusterName)
		return clusterName, nil
	}

	return "", fmt.Errorf("could not auto-detect cluster name using any available method")
}

// DetectAWSRegion detects the AWS region from node labels or metadata
func (d *ClusterDetector) DetectAWSRegion(ctx context.Context) (string, error) {
	if d.clientset == nil {
		clientset, err := buildKubernetesClient()
		if err != nil {
			return "", fmt.Errorf("failed to build Kubernetes client: %w", err)
		}
		d.clientset = clientset
	}

	// Check environment variable first
	if region := os.Getenv("AWS_REGION"); region != "" {
		return region, nil
	}

	// Get nodes and check for region in labels or provider ID
	nodes, err := d.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		// Check node labels for region
		if region, ok := node.Labels["topology.kubernetes.io/region"]; ok {
			return region, nil
		}

		// Extract region from provider ID (aws:///us-west-2a/i-1234567890abcdef0)
		if node.Spec.ProviderID != "" {
			re := regexp.MustCompile(`aws:///([a-z]{2}-[a-z]+-\d+)[a-z]/`)
			matches := re.FindStringSubmatch(node.Spec.ProviderID)
			if len(matches) >= 2 {
				return matches[1], nil
			}
		}

		// Extract region from node name pattern
		re := regexp.MustCompile(`\.([a-z]{2}-[a-z]+-\d+)\.compute\.internal`)
		matches := re.FindStringSubmatch(node.Name)
		if len(matches) >= 2 {
			return matches[1], nil
		}
	}

	return "", fmt.Errorf("could not detect AWS region from cluster")
}

// Helper methods

func buildKubernetesClient() (*kubernetes.Clientset, error) {
	var config *rest.Config
	var err error

	// Check for KUBECONFIG environment variable first
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath != "" {
		log.Printf("🔍 Using KUBECONFIG from environment: %s", kubeconfigPath)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			log.Printf("⚠️ Failed to load config from KUBECONFIG env var: %v", err)
		} else {
			// Successfully loaded from KUBECONFIG
			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				return nil, fmt.Errorf("failed to create Kubernetes client from KUBECONFIG: %w", err)
			}
			return clientset, nil
		}
	}

	// Try in-cluster config next
	config, err = rest.InClusterConfig()
	if err == nil {
		log.Println("🔍 Using in-cluster Kubernetes configuration")
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to create in-cluster Kubernetes client: %w", err)
		}
		return clientset, nil
	}

	// Fall back to default kubeconfig location
	var kubeconfig string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
		log.Printf("🔍 Trying default kubeconfig location: %s", kubeconfig)
	}

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes config from any source: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return clientset, nil
}

func (d *ClusterDetector) detectFromClusterInfo(ctx context.Context) (string, error) {
	// Check cluster-info ConfigMap in kube-public
	cm, err := d.clientset.CoreV1().ConfigMaps("kube-public").Get(ctx, "cluster-info", metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if kubeconfig, ok := cm.Data["kubeconfig"]; ok {
		// Parse kubeconfig to extract cluster name
		var config map[string]interface{}
		if err := yaml.Unmarshal([]byte(kubeconfig), &config); err != nil {
			return "", err
		}

		if clusters, ok := config["clusters"].([]interface{}); ok && len(clusters) > 0 {
			if cluster, ok := clusters[0].(map[string]interface{}); ok {
				if name, ok := cluster["name"].(string); ok {
					return name, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no cluster name found in cluster-info")
}

func (d *ClusterDetector) detectFromKubeSystemConfigMap(ctx context.Context) (string, error) {
	// Check kube-system namespace for cluster-info or other identifying ConfigMaps
	configMaps, err := d.clientset.CoreV1().ConfigMaps("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	// Look for various ConfigMaps that might contain cluster information
	for _, cm := range configMaps.Items {
		// Check AWS Load Balancer Controller config
		if cm.Name == "aws-load-balancer-controller-leader" {
			if clusterName, ok := cm.Data["cluster-name"]; ok {
				return clusterName, nil
			}
		}

		// Check cluster-info in kube-system
		if cm.Name == "cluster-info" {
			if clusterName, ok := cm.Data["cluster-name"]; ok {
				return clusterName, nil
			}
		}

		// Check for EKS-specific ConfigMaps
		if strings.Contains(cm.Name, "eks") {
			for key, value := range cm.Data {
				if strings.Contains(key, "cluster") && value != "" {
					return value, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no cluster name found in kube-system ConfigMaps")
}

func (d *ClusterDetector) detectFromOpenShiftInfrastructure(ctx context.Context) (string, error) {
	// Try to detect OpenShift infrastructure using unstructured client
	// This would work if we have access to config.openshift.io/v1 Infrastructure
	// For now, we'll use a simpler approach checking for OpenShift-specific namespaces

	// Check if openshift-config namespace exists (indicates OpenShift)
	_, err := d.clientset.CoreV1().Namespaces().Get(ctx, "openshift-config", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("not an OpenShift cluster or no access to openshift-config namespace")
	}

	// Try to get infrastructure information from openshift-config namespace
	configMaps, err := d.clientset.CoreV1().ConfigMaps("openshift-config").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, cm := range configMaps.Items {
		if strings.Contains(cm.Name, "cluster") || strings.Contains(cm.Name, "infrastructure") {
			for key, value := range cm.Data {
				if strings.Contains(key, "name") && value != "" {
					// Extract cluster name from infrastructure name pattern
					// e.g., "beekhof-sbd-arm64-4hxmm" -> "beekhof-sbd-arm64"
					if matched, _ := regexp.MatchString(`^[a-zA-Z0-9-]+-[a-zA-Z0-9]+$`, value); matched {
						parts := strings.Split(value, "-")
						if len(parts) >= 3 {
							clusterName := strings.Join(parts[:len(parts)-1], "-")
							return clusterName, nil
						}
					}
					return value, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no cluster name found in OpenShift infrastructure")
}

func (d *ClusterDetector) detectFromNodeLabels(ctx context.Context) (string, error) {
	nodes, err := d.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, node := range nodes.Items {
		// Check for cluster name in various node label formats
		labelPatterns := []string{
			"kubernetes.io/cluster/",
			"cluster.x-k8s.io/cluster-name",
			"node.kubernetes.io/cluster-name",
			"eks.amazonaws.com/cluster-name",
		}

		for _, pattern := range labelPatterns {
			for key, value := range node.Labels {
				if strings.HasPrefix(key, pattern) {
					if pattern == "kubernetes.io/cluster/" {
						clusterName := strings.TrimPrefix(key, pattern)
						if clusterName != "" {
							return clusterName, nil
						}
					} else if value != "" {
						return value, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no cluster name found in node labels")
}

func (d *ClusterDetector) detectFromKubeconfig() (string, error) {
	// First try KUBECONFIG environment variable
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		// Fall back to default location
		if home := homedir.HomeDir(); home != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}

	if kubeconfigPath == "" {
		return "", fmt.Errorf("no kubeconfig path available")
	}

	// Load the kubeconfig file
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to load kubeconfig from %s: %w", kubeconfigPath, err)
	}

	// Get current context
	currentContext := config.CurrentContext
	if currentContext == "" {
		return "", fmt.Errorf("no current context set in kubeconfig")
	}

	// Get context details
	context, exists := config.Contexts[currentContext]
	if !exists {
		return "", fmt.Errorf("current context %s not found in kubeconfig", currentContext)
	}

	// Try cluster name from context
	clusterName := context.Cluster
	if clusterName != "" {
		// Clean up cluster name (remove common prefixes/suffixes)
		clusterName = strings.TrimSuffix(clusterName, ".local")
		clusterName = strings.TrimPrefix(clusterName, "gke_")
		clusterName = strings.TrimPrefix(clusterName, "arn:aws:eks:")

		// For EKS clusters, extract just the cluster name
		if strings.Contains(clusterName, ":cluster/") {
			parts := strings.Split(clusterName, ":cluster/")
			if len(parts) > 1 {
				clusterName = parts[1]
			}
		}

		// For GKE clusters, extract cluster name from full path
		if strings.Contains(clusterName, "_") {
			parts := strings.Split(clusterName, "_")
			if len(parts) >= 3 {
				clusterName = parts[len(parts)-1] // Last part is usually cluster name
			}
		}

		return clusterName, nil
	}

	// Try context name as cluster name
	if currentContext != "" {
		return currentContext, nil
	}

	return "", fmt.Errorf("no cluster name found in kubeconfig")
}

func (d *ClusterDetector) detectFromAPIServer(ctx context.Context) (string, error) {
	// Get the current kubeconfig to extract API server URL
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}

	if kubeconfigPath == "" {
		return "", fmt.Errorf("no kubeconfig path available")
	}

	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return "", err
	}

	currentContext := config.CurrentContext
	if currentContext == "" {
		return "", fmt.Errorf("no current context")
	}

	context, exists := config.Contexts[currentContext]
	if !exists {
		return "", fmt.Errorf("context not found")
	}

	cluster, exists := config.Clusters[context.Cluster]
	if !exists {
		return "", fmt.Errorf("cluster not found")
	}

	// Extract cluster name from API server URL
	server := cluster.Server
	if server != "" {
		// For EKS: https://A1B2C3D4E5F6G7H8I9J0.gr7.us-west-2.eks.amazonaws.com
		eksPattern := regexp.MustCompile(`https://[A-Z0-9]+\.gr[0-9]+\.([a-z0-9-]+)\.eks\.amazonaws\.com`)
		if matches := eksPattern.FindStringSubmatch(server); len(matches) > 1 {
			region := matches[1]
			// This gives us the region, but not the cluster name
			// We'd need additional API calls to get the actual cluster name
			return fmt.Sprintf("eks-cluster-%s", region), nil
		}

		// For GKE: https://A.B.C.D (IP-based) or container.googleapis.com
		if strings.Contains(server, "container.googleapis.com") {
			return "gke-cluster", nil
		}

		// For OpenShift: api.cluster-name.base-domain
		openshiftPattern := regexp.MustCompile(`https://api\.([^.]+)\.`)
		if matches := openshiftPattern.FindStringSubmatch(server); len(matches) > 1 {
			return matches[1], nil
		}
	}

	return "", fmt.Errorf("could not extract cluster name from API server URL: %s", server)
}

func (d *ClusterDetector) detectFromEKSNodeTags(ctx context.Context) (string, error) {
	nodes, err := d.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, node := range nodes.Items {
		// Check for EKS-specific labels
		if clusterName, ok := node.Labels["eks.amazonaws.com/cluster-name"]; ok {
			return clusterName, nil
		}

		// Check for alpha.eksctl.io cluster name
		if clusterName, ok := node.Labels["alpha.eksctl.io/cluster-name"]; ok {
			return clusterName, nil
		}

		// Check for other EKS-related labels
		for key, value := range node.Labels {
			if strings.Contains(key, "eks") && strings.Contains(key, "cluster") && value != "" {
				return value, nil
			}
		}
	}

	return "", fmt.Errorf("no EKS cluster name found in node labels")
}

func (m *Manager) applyEFSCSIDriver(ctx context.Context) error {
	log.Println("🔧 Installing AWS EFS CSI driver...")

	// Check if EFS CSI driver is already installed by looking for the DaemonSet
	_, err := m.clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "efs-csi-node", metav1.GetOptions{})
	if err == nil {
		log.Println("✅ EFS CSI driver already installed (DaemonSet found)")
		return nil
	}

	// Check if the deployment exists instead
	_, err = m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "efs-csi-controller", metav1.GetOptions{})
	if err == nil {
		log.Println("✅ EFS CSI driver already installed (Deployment found)")
		return nil
	}

	// Apply the EFS CSI driver using kubectl/oc apply
	manifestURL := "https://github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7"

	log.Printf("🔧 Applying EFS CSI driver manifests from: %s", manifestURL)

	// Use oc if available (OpenShift), otherwise use kubectl
	cmd := "kubectl"
	if m.isOpenShiftCluster(ctx) {
		cmd = "oc"
	}

	// Run the installation command
	installCmd := exec.Command(cmd, "apply", "-k", manifestURL)

	log.Printf("🔧 Running: %s apply -k %s", cmd, manifestURL)

	// Execute the command with context timeout
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	installCmd = exec.CommandContext(cmdCtx, cmd, "apply", "-k", manifestURL)

	output, err := installCmd.CombinedOutput()
	if err != nil {
		log.Printf("❌ Failed to install EFS CSI driver: %v", err)
		log.Printf("Command output: %s", string(output))
		return fmt.Errorf("failed to install EFS CSI driver: %w\nOutput: %s", err, string(output))
	}

	log.Printf("✅ EFS CSI driver installation command completed")
	log.Printf("Installation output: %s", string(output))

	return nil
}

func (m *Manager) waitForEFSCSIDriver(ctx context.Context) error {
	log.Println("⏳ Waiting for EFS CSI driver pods to become ready...")

	// Multiple label selectors to check for EFS CSI driver components
	selectors := []struct {
		name     string
		selector string
	}{
		{"controller", "app=efs-csi-controller"},
		{"controller-alt", "app.kubernetes.io/name=aws-efs-csi-driver,app.kubernetes.io/component=controller"},
		{"node", "app=efs-csi-node"},
		{"node-alt", "app.kubernetes.io/name=aws-efs-csi-driver,app.kubernetes.io/component=node"},
	}

	maxWaitTime := 5 * time.Minute
	timeout := time.After(maxWaitTime)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			// Final attempt to get detailed status
			log.Println("❌ Timeout reached, checking final status...")
			m.logEFSCSIDriverStatus(ctx)
			return fmt.Errorf("EFS CSI driver did not become ready within %v", maxWaitTime)

		case <-ticker.C:
			readyFound := false
			totalPods := 0
			readyPods := 0

			for _, sel := range selectors {
				pods, err := m.clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
					LabelSelector: sel.selector,
				})
				if err != nil {
					log.Printf("⚠️ Failed to list %s pods: %v", sel.name, err)
					continue
				}

				if len(pods.Items) == 0 {
					continue
				}

				componentReady := 0
				for _, pod := range pods.Items {
					totalPods++
					if pod.Status.Phase == corev1.PodRunning {
						// Check if all containers are ready
						allContainersReady := true
						for _, containerStatus := range pod.Status.ContainerStatuses {
							if !containerStatus.Ready {
								allContainersReady = false
								break
							}
						}
						if allContainersReady {
							componentReady++
							readyPods++
						}
					}
				}

				if componentReady > 0 {
					log.Printf("✅ Found %d ready %s pod(s)", componentReady, sel.name)
					readyFound = true
				}
			}

			if readyFound && readyPods > 0 {
				log.Printf("✅ EFS CSI driver is ready (%d/%d pods running and ready)", readyPods, totalPods)
				return nil
			}

			if totalPods == 0 {
				log.Println("⏳ No EFS CSI driver pods found yet, waiting for installation to complete...")
			} else {
				log.Printf("⏳ EFS CSI driver pods found but not ready yet (%d/%d ready)", readyPods, totalPods)
			}
		}
	}
}

// logEFSCSIDriverStatus provides detailed status information for troubleshooting
func (m *Manager) logEFSCSIDriverStatus(ctx context.Context) {
	log.Println("📊 EFS CSI Driver Status Details:")

	// Check for any pods with EFS CSI related labels
	allPods, err := m.clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("❌ Failed to list all pods: %v", err)
		return
	}

	efsRelatedPods := 0
	for _, pod := range allPods.Items {
		// Check if pod name or labels suggest it's EFS CSI related
		isEFSRelated := strings.Contains(pod.Name, "efs-csi") ||
			strings.Contains(pod.Name, "aws-efs") ||
			strings.Contains(fmt.Sprintf("%v", pod.Labels), "efs")

		if isEFSRelated {
			efsRelatedPods++
			log.Printf("   Pod: %s, Phase: %s, Ready: %v",
				pod.Name, pod.Status.Phase, isPodReady(&pod))

			// Log container statuses for failed pods
			if pod.Status.Phase != corev1.PodRunning {
				for _, containerStatus := range pod.Status.ContainerStatuses {
					if containerStatus.State.Waiting != nil {
						log.Printf("     Container %s waiting: %s",
							containerStatus.Name, containerStatus.State.Waiting.Reason)
					}
					if containerStatus.State.Terminated != nil {
						log.Printf("     Container %s terminated: %s",
							containerStatus.Name, containerStatus.State.Terminated.Reason)
					}
				}
			}
		}
	}

	if efsRelatedPods == 0 {
		log.Println("   No EFS CSI driver pods found - installation may have failed")
		log.Println("   Manual installation command:")
		log.Println("   kubectl apply -k 'https://github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7'")
	}
}

// isPodReady checks if all containers in a pod are ready
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (m *Manager) restartEFSCSIController(ctx context.Context) error {
	// Restart EFS CSI controller deployment with retry logic for conflicts
	for attempt := 0; attempt < 3; attempt++ {
		deployment, err := m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "efs-csi-controller", metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get EFS CSI controller deployment: %w", err)
		}

		// Add restart annotation
		if deployment.Spec.Template.ObjectMeta.Annotations == nil {
			deployment.Spec.Template.ObjectMeta.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

		_, err = m.clientset.AppsV1().Deployments("kube-system").Update(ctx, deployment, metav1.UpdateOptions{})
		if err != nil {
			// Handle conflict errors by retrying
			if strings.Contains(err.Error(), "object has been modified") || strings.Contains(err.Error(), "conflict") {
				log.Printf("⚠️ Deployment conflict detected (attempt %d/3), retrying...", attempt+1)
				time.Sleep(time.Duration(attempt+1) * time.Second) // Exponential backoff
				continue
			}
			return fmt.Errorf("failed to restart EFS CSI controller: %w", err)
		}

		log.Println("🔄 Restarted EFS CSI controller")
		return nil
	}

	return fmt.Errorf("failed to restart EFS CSI controller after 3 attempts due to conflicts")
}

// fixEFSCSIController fixes common issues with the EFS CSI controller deployment
func (m *Manager) fixEFSCSIController(ctx context.Context) error {
	// The EFS CSI driver should work out of the box - no modifications needed
	log.Printf("🔧 EFS CSI controller deployment left as-is")
	return nil
}

// isOpenShiftCluster checks if the cluster is an OpenShift cluster by looking for the openshift-config namespace
func (m *Manager) isOpenShiftCluster(ctx context.Context) bool {
	_, err := m.clientset.CoreV1().Namespaces().Get(ctx, "openshift-config", metav1.GetOptions{})
	return err == nil
}

// configureAWSCredentials configures the EFS CSI controller to use AWS credentials secret
func (m *Manager) configureAWSCredentials(ctx context.Context) error {
	log.Println("🔍 Searching for AWS credentials secret...")

	// Find AWS credentials secret in various common locations
	secretInfo, err := m.findAWSCredentialsSecret(ctx)
	if err != nil {
		return fmt.Errorf("failed to find AWS credentials secret: %w", err)
	}

	log.Printf("✅ Found AWS credentials secret: %s in namespace %s", secretInfo.Name, secretInfo.Namespace)

	// Get the EFS CSI controller deployment
	deployment, err := m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "efs-csi-controller", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get EFS CSI controller deployment: %w", err)
	}

	// Find the correct container (try multiple possible names)
	containerNames := []string{"efs-plugin", "aws-efs-csi-driver", "efs-csi-driver", "csi-driver"}
	var targetContainerIndex = -1
	var targetContainerName string

	for i, container := range deployment.Spec.Template.Spec.Containers {
		for _, possibleName := range containerNames {
			if container.Name == possibleName {
				targetContainerIndex = i
				targetContainerName = container.Name
				break
			}
		}
		if targetContainerIndex != -1 {
			break
		}
	}

	if targetContainerIndex == -1 {
		// List all container names for debugging
		containerNamesList := make([]string, len(deployment.Spec.Template.Spec.Containers))
		for i, container := range deployment.Spec.Template.Spec.Containers {
			containerNamesList[i] = container.Name
		}
		return fmt.Errorf("no suitable EFS CSI container found. Available containers: %v, expected one of: %v",
			containerNamesList, containerNames)
	}

	log.Printf("🔧 Configuring AWS credentials for container: %s", targetContainerName)

	// Check if AWS credentials are already configured
	container := &deployment.Spec.Template.Spec.Containers[targetContainerIndex]
	hasAccessKey := false
	hasSecretKey := false
	hasRegion := false

	for _, env := range container.Env {
		switch env.Name {
		case "AWS_ACCESS_KEY_ID":
			hasAccessKey = true
		case "AWS_SECRET_ACCESS_KEY":
			hasSecretKey = true
		case "AWS_DEFAULT_REGION", "AWS_REGION":
			hasRegion = true
		}
	}

	// Add missing AWS credentials environment variables
	if !hasAccessKey {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretInfo.Name},
					Key:                  secretInfo.AccessKeyID,
				},
			},
		})
		log.Printf("✅ Added AWS_ACCESS_KEY_ID from secret %s/%s", secretInfo.Namespace, secretInfo.Name)
	}

	if !hasSecretKey {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretInfo.Name},
					Key:                  secretInfo.SecretAccessKey,
				},
			},
		})
		log.Printf("✅ Added AWS_SECRET_ACCESS_KEY from secret %s/%s", secretInfo.Namespace, secretInfo.Name)
	}

	// Add region if available and not already set
	if !hasRegion && secretInfo.Region != "" {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "AWS_DEFAULT_REGION",
			Value: secretInfo.Region,
		})
		log.Printf("✅ Added AWS_DEFAULT_REGION: %s", secretInfo.Region)
	}

	// If we're not in kube-system, we need to copy the secret or use a service account
	if secretInfo.Namespace != "kube-system" {
		if err := m.ensureAWSCredentialsInKubeSystem(ctx, secretInfo); err != nil {
			return fmt.Errorf("failed to ensure AWS credentials in kube-system: %w", err)
		}
	}

	// Update the deployment
	_, err = m.clientset.AppsV1().Deployments("kube-system").Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update EFS CSI controller deployment: %w", err)
	}

	log.Printf("✅ Successfully configured AWS credentials for EFS CSI controller")

	// Validate that the credentials actually work
	if err := m.validateAWSCredentialsForEFS(ctx, secretInfo); err != nil {
		log.Printf("⚠️ Warning: AWS credentials configured but validation failed: %v", err)
		log.Println("🔧 The EFS CSI driver may fail to provision volumes due to insufficient permissions")
		// Don't return error here - let the test PVC catch the issue with better error reporting
	} else {
		log.Printf("✅ AWS credentials validated successfully")
	}

	return nil
}

// validateAWSCredentialsForEFS validates that the configured AWS credentials have EFS permissions
func (m *Manager) validateAWSCredentialsForEFS(ctx context.Context, secretInfo *AWSSecretInfo) error {
	// Get the credentials from the secret
	secretObj, err := m.clientset.CoreV1().Secrets(secretInfo.Namespace).Get(ctx, secretInfo.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get AWS credentials secret for validation: %w", err)
	}

	accessKeyID := string(secretObj.Data[secretInfo.AccessKeyID])
	secretAccessKey := string(secretObj.Data[secretInfo.SecretAccessKey])

	if accessKeyID == "" || secretAccessKey == "" {
		return fmt.Errorf("AWS credentials are empty")
	}

	log.Println("🧪 Validating AWS credentials by testing EFS API access...")

	// Detect AWS region for the credentials
	region := secretInfo.Region
	if region == "" {
		if detectedRegion, err := m.detectAWSRegionFromCluster(ctx); err == nil {
			region = detectedRegion
			log.Printf("🔍 Detected AWS region from cluster: %s", region)
		} else {
			region = "us-east-1" // Default fallback
			log.Printf("⚠️ Could not detect AWS region, using default: %s", region)
		}
	}

	// Create AWS config with the credentials from the secret
	creds := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		return fmt.Errorf("failed to create AWS config: %w", err)
	}

	// Create EFS client
	efsClient := efs.NewFromConfig(cfg)

	// Test basic EFS permissions with a simple describe operation
	log.Printf("🔍 Testing EFS permissions in region %s...", region)

	// Create a context with timeout for the API call
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Try to describe file systems (read-only operation)
	input := &efs.DescribeFileSystemsInput{
		MaxItems: aws.Int32(1),
	}

	result, err := efsClient.DescribeFileSystems(testCtx, input)
	if err != nil {
		// Check for specific error types
		if strings.Contains(err.Error(), "UnauthorizedOperation") ||
			strings.Contains(err.Error(), "AccessDenied") ||
			strings.Contains(err.Error(), "InvalidUserID.NotFound") {
			return fmt.Errorf("AWS credentials do not have EFS permissions: %w", err)
		}
		if strings.Contains(err.Error(), "InvalidAccessKeyId") {
			return fmt.Errorf("AWS access key ID is invalid: %w", err)
		}
		if strings.Contains(err.Error(), "SignatureDoesNotMatch") {
			return fmt.Errorf("AWS secret access key is invalid: %w", err)
		}
		if strings.Contains(err.Error(), "TokenRefreshRequired") {
			return fmt.Errorf("AWS credentials expired or require refresh: %w", err)
		}

		// For other errors, provide a warning but don't fail completely
		log.Printf("⚠️ Warning: EFS API test failed (may be network/service issue): %v", err)
		return fmt.Errorf("EFS API validation failed: %w", err)
	}

	// Success! Log some useful information
	if result != nil && result.FileSystems != nil {
		log.Printf("✅ AWS credentials validated successfully")
		log.Printf("🔍 Found %d EFS filesystem(s) accessible with these credentials", len(result.FileSystems))

		// Log available filesystems for debugging
		for _, fs := range result.FileSystems {
			if fs.FileSystemId != nil {
				log.Printf("   - EFS ID: %s", *fs.FileSystemId)
			}
		}
	} else {
		log.Printf("✅ AWS credentials validated (EFS access confirmed)")
	}

	log.Printf("✅ AWS credentials validated - basic EFS connectivity confirmed")
	return nil
}

// detectAWSRegionFromCluster attempts to detect the AWS region from cluster node information
func (m *Manager) detectAWSRegionFromCluster(ctx context.Context) (string, error) {
	// Get nodes and check for region in labels or provider ID
	nodes, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		// Check node labels for region
		if region, ok := node.Labels["topology.kubernetes.io/region"]; ok {
			return region, nil
		}

		// Extract region from provider ID (aws:///us-west-2a/i-1234567890abcdef0)
		if node.Spec.ProviderID != "" {
			re := regexp.MustCompile(`aws:///([a-z]{2}-[a-z]+-\d+)[a-z]/`)
			matches := re.FindStringSubmatch(node.Spec.ProviderID)
			if len(matches) >= 2 {
				return matches[1], nil
			}
		}

		// Extract region from node name pattern
		re := regexp.MustCompile(`\.([a-z]{2}-[a-z]+-\d+)\.compute\.internal`)
		matches := re.FindStringSubmatch(node.Name)
		if len(matches) >= 2 {
			return matches[1], nil
		}
	}

	return "", fmt.Errorf("could not detect AWS region from cluster")
}

// AWSSecretInfo holds information about found AWS credentials
type AWSSecretInfo struct {
	Name            string
	Namespace       string
	AccessKeyID     string
	SecretAccessKey string
	Region          string
}

// findAWSCredentialsSecret searches for AWS credentials in common locations
func (m *Manager) findAWSCredentialsSecret(ctx context.Context) (*AWSSecretInfo, error) {
	// Common locations and secret names for AWS credentials in OpenShift/Kubernetes
	searchLocations := []struct {
		namespace   string
		secretNames []string
	}{
		{"kube-system", []string{"aws-creds", "aws-credentials", "cloud-credentials"}},
		{"openshift-machine-api", []string{"aws-cloud-credentials", "aws-credentials"}},
		{"openshift-cloud-credential-operator", []string{"cloud-credential-operator-iam-ro-creds"}},
		{"openshift-cluster-api", []string{"aws-cloud-credentials"}},
		{"default", []string{"aws-creds", "aws-credentials"}},
	}

	for _, location := range searchLocations {
		// Check if namespace exists
		_, err := m.clientset.CoreV1().Namespaces().Get(ctx, location.namespace, metav1.GetOptions{})
		if err != nil {
			continue // Skip if namespace doesn't exist
		}

		for _, secretName := range location.secretNames {
			secret, err := m.clientset.CoreV1().Secrets(location.namespace).Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				continue // Try next secret name
			}

			// Try to extract credentials with different possible key names
			secretInfo := &AWSSecretInfo{
				Name:      secretName,
				Namespace: location.namespace,
			}

			// Common variations for access key ID
			for _, key := range []string{"aws_access_key_id", "AWS_ACCESS_KEY_ID", "access_key_id", "accessKeyId"} {
				if _, exists := secret.Data[key]; exists {
					secretInfo.AccessKeyID = key
					log.Printf("🔍 Found access key ID with key: %s", key)
					break
				}
			}

			// Common variations for secret access key
			for _, key := range []string{"aws_secret_access_key", "AWS_SECRET_ACCESS_KEY", "secret_access_key", "secretAccessKey"} {
				if _, exists := secret.Data[key]; exists {
					secretInfo.SecretAccessKey = key
					log.Printf("🔍 Found secret access key with key: %s", key)
					break
				}
			}

			// Optional region
			for _, key := range []string{"aws_region", "AWS_REGION", "region", "AWS_DEFAULT_REGION"} {
				if _, exists := secret.Data[key]; exists {
					secretInfo.Region = string(secret.Data[key])
					log.Printf("🔍 Found region with key: %s, value: %s", key, secretInfo.Region)
					break
				}
			}

			// Validate that we found the required credentials
			if secretInfo.AccessKeyID != "" && secretInfo.SecretAccessKey != "" {
				log.Printf("✅ Valid AWS credentials found in %s/%s", location.namespace, secretName)
				return secretInfo, nil
			} else {
				log.Printf("⚠️ Secret %s/%s exists but missing required keys (found accessKey: %v, secretKey: %v)",
					location.namespace, secretName, secretInfo.AccessKeyID != "", secretInfo.SecretAccessKey != "")
			}
		}
	}

	return nil, fmt.Errorf("no valid AWS credentials secret found. Searched namespaces: %v. Please ensure aws-creds secret exists with aws_access_key_id and aws_secret_access_key keys",
		getNamespaceList(searchLocations))
}

// ensureAWSCredentialsInKubeSystem copies AWS credentials to kube-system if needed
func (m *Manager) ensureAWSCredentialsInKubeSystem(ctx context.Context, sourceSecret *AWSSecretInfo) error {
	targetSecretName := "aws-creds"

	// Check if target secret already exists
	_, err := m.clientset.CoreV1().Secrets("kube-system").Get(ctx, targetSecretName, metav1.GetOptions{})
	if err == nil {
		log.Printf("✅ AWS credentials secret already exists in kube-system")
		return nil
	}

	// Get source secret
	sourceSecretObj, err := m.clientset.CoreV1().Secrets(sourceSecret.Namespace).Get(ctx, sourceSecret.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get source secret: %w", err)
	}

	// Create target secret in kube-system
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetSecretName,
			Namespace: "kube-system",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sbd-operator",
				"sbd.medik8s.io/purpose":       "efs-csi-credentials",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"aws_access_key_id":     sourceSecretObj.Data[sourceSecret.AccessKeyID],
			"aws_secret_access_key": sourceSecretObj.Data[sourceSecret.SecretAccessKey],
		},
	}

	// Add region if available
	if sourceSecret.Region != "" {
		targetSecret.Data["aws_region"] = []byte(sourceSecret.Region)
	}

	_, err = m.clientset.CoreV1().Secrets("kube-system").Create(ctx, targetSecret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create AWS credentials secret in kube-system: %w", err)
	}

	log.Printf("✅ Copied AWS credentials from %s/%s to kube-system/%s",
		sourceSecret.Namespace, sourceSecret.Name, targetSecretName)

	// Update the secretInfo to point to the new location
	sourceSecret.Name = targetSecretName
	sourceSecret.Namespace = "kube-system"
	sourceSecret.AccessKeyID = "aws_access_key_id"
	sourceSecret.SecretAccessKey = "aws_secret_access_key"

	return nil
}

// getNamespaceList extracts namespace names from search locations
func getNamespaceList(locations []struct {
	namespace   string
	secretNames []string
}) []string {
	namespaces := make([]string, len(locations))
	for i, loc := range locations {
		namespaces[i] = loc.namespace
	}
	return namespaces
}

// detectEFSCSICapabilities checks if the EFS CSI driver supports standard NFS mounting (efs-mount)
// Most EFS CSI driver versions only support efs-ap (EFS utils mounting)
func (m *Manager) detectEFSCSICapabilities(ctx context.Context) bool {
	// For now, default to EFS utils mounting (efs-ap) since most EFS CSI drivers only support this
	// In the future, this could be enhanced to:
	// 1. Check the EFS CSI driver version
	// 2. Query CSI driver capabilities
	// 3. Attempt a test StorageClass creation with efs-mount

	log.Printf("🔍 Detecting EFS CSI driver capabilities...")

	// Check if there are any existing StorageClasses using efs-mount successfully
	storageClasses, err := m.clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("⚠️ Could not list StorageClasses to detect EFS capabilities: %v", err)
		return false
	}

	for _, sc := range storageClasses.Items {
		if sc.Provisioner == "efs.csi.aws.com" {
			if provisioningMode, exists := sc.Parameters["provisioningMode"]; exists {
				if provisioningMode == "efs-mount" {
					log.Printf("✅ Found existing StorageClass using efs-mount, EFS CSI driver supports standard NFS")
					return true
				}
			}
		}
	}

	log.Printf("📋 No efs-mount StorageClasses found, defaulting to EFS utils mounting (efs-ap)")
	return false
}
