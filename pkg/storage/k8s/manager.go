package k8s

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

	// Install EFS CSI driver
	log.Println("🔧 Installing EFS CSI driver...")

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

	// Add IAM role annotation
	if serviceAccount.Annotations == nil {
		serviceAccount.Annotations = make(map[string]string)
	}
	serviceAccount.Annotations["eks.amazonaws.com/role-arn"] = roleARN

	// Update service account
	_, err = m.clientset.CoreV1().ServiceAccounts("kube-system").Update(ctx, serviceAccount, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update service account: %w", err)
	}

	log.Printf("🔗 Configured service account with IAM role: %s", roleARN)

	// Fix EFS CSI controller deployment if needed
	if err := m.fixEFSCSIController(ctx); err != nil {
		log.Printf("⚠️ Warning: failed to fix EFS CSI controller: %v", err)
	}

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
			"provisioningMode": "efs-ap",
			"fileSystemId":     efsID,
			"directoryPerms":   "0755",
			"gidRangeStart":    "1000",
			"gidRangeEnd":      "2000",
			"basePath":         "/sbd",
		},
		MountOptions: []string{
			"tls",
			"regional",
		},
		AllowVolumeExpansion: &[]bool{true}[0],
		VolumeBindingMode:    &[]storagev1.VolumeBindingMode{storagev1.VolumeBindingImmediate}[0],
	}

	_, err = m.clientset.StorageV1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create StorageClass: %w", err)
	}

	log.Printf("💾 Created StorageClass: %s", m.config.StorageClassName)
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
		return false, fmt.Errorf("test PVC failed to bind (final status: %s, last message: %s)", lastStatus, lastMessage)
	}

	return true, nil
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
	// This is a simplified version - in practice, you'd want to apply the full EFS CSI driver manifest
	// For now, we'll assume it's already installed or handle it externally
	log.Println("🔧 EFS CSI driver installation handled externally")
	return nil
}

func (m *Manager) waitForEFSCSIDriver(ctx context.Context) error {
	// Wait for EFS CSI driver pods to be ready
	for i := 0; i < 60; i++ { // Wait up to 5 minutes
		pods, err := m.clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: "app=efs-csi-controller",
		})
		if err != nil {
			return fmt.Errorf("failed to list EFS CSI controller pods: %w", err)
		}

		ready := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				ready++
			}
		}

		if ready > 0 {
			log.Printf("✅ EFS CSI driver is ready (%d pods running)", ready)
			return nil
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("EFS CSI driver did not become ready within timeout")
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
	// Retry logic for handling deployment conflicts
	for attempt := 0; attempt < 3; attempt++ {
		deployment, err := m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "efs-csi-controller", metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get EFS CSI controller deployment: %w", err)
		}

		// Check if the deployment has the socket directory issue
		needsUpdate := false

		// Find the efs-plugin container
		for i, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "efs-plugin" {
				// Check if there are multiple AWS_ROLE_ARN environment variables
				cleanEnvVars := []corev1.EnvVar{}
				roleARNSeen := false

				for _, env := range container.Env {
					if env.Name == "AWS_ROLE_ARN" {
						if !roleARNSeen {
							// Keep only the first AWS_ROLE_ARN (which should be empty to use IRSA)
							cleanEnvVars = append(cleanEnvVars, corev1.EnvVar{
								Name:  "AWS_ROLE_ARN",
								Value: "", // Empty to use IRSA
							})
							roleARNSeen = true
						}
						needsUpdate = true
					} else {
						cleanEnvVars = append(cleanEnvVars, env)
					}
				}

				if needsUpdate {
					deployment.Spec.Template.Spec.Containers[i].Env = cleanEnvVars
					log.Printf("🔧 Cleaned up duplicate AWS_ROLE_ARN environment variables")
				}

				// Ensure the socket directory volume mount is correct
				for j, mount := range container.VolumeMounts {
					if mount.Name == "socket-dir" && mount.MountPath != "/var/lib/csi/sockets/pluginproxy/" {
						deployment.Spec.Template.Spec.Containers[i].VolumeMounts[j].MountPath = "/var/lib/csi/sockets/pluginproxy/"
						needsUpdate = true
						log.Printf("🔧 Fixed socket directory mount path")
					}
				}
				break
			}
		}

		// Add an init container to ensure the socket directory exists
		hasInitContainer := false
		for _, initContainer := range deployment.Spec.Template.Spec.InitContainers {
			if initContainer.Name == "socket-dir-init" {
				hasInitContainer = true
				break
			}
		}

		if !hasInitContainer {
			initContainer := corev1.Container{
				Name:  "socket-dir-init",
				Image: "busybox:1.35",
				Command: []string{
					"sh", "-c",
					"mkdir -p /var/lib/csi/sockets/pluginproxy && chmod 755 /var/lib/csi/sockets/pluginproxy",
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/var/lib/csi/sockets/pluginproxy/",
					},
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:  &[]int64{0}[0],
					RunAsGroup: &[]int64{0}[0],
				},
			}
			deployment.Spec.Template.Spec.InitContainers = append(deployment.Spec.Template.Spec.InitContainers, initContainer)
			needsUpdate = true
			log.Printf("🔧 Added init container to create socket directory")
		}

		if needsUpdate {
			_, err = m.clientset.AppsV1().Deployments("kube-system").Update(ctx, deployment, metav1.UpdateOptions{})
			if err != nil {
				// Handle conflict errors by retrying
				if strings.Contains(err.Error(), "object has been modified") || strings.Contains(err.Error(), "conflict") {
					log.Printf("⚠️ Deployment conflict detected during fix (attempt %d/3), retrying...", attempt+1)
					time.Sleep(time.Duration(attempt+1) * time.Second) // Exponential backoff
					continue
				}
				return fmt.Errorf("failed to update EFS CSI controller deployment: %w", err)
			}
			log.Printf("🔧 Fixed EFS CSI controller deployment")
		}

		return nil // Success or no update needed
	}

	return fmt.Errorf("failed to fix EFS CSI controller deployment after 3 attempts due to conflicts")
}
