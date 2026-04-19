package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/medik8s/storage-based-remediation/pkg/agent"
	"github.com/medik8s/storage-based-remediation/test/utils"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		fmt.Printf("Usage: %s <namespace> [sbrconfig-name]\n\n", os.Args[0])
		fmt.Println("This tool validates SBR device file consistency across all agent pods.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Printf("  %s default                    # Check all SBR agents in default namespace\n", os.Args[0])
		fmt.Printf("  %s default test-sbr-config   # Check agents for specific StorageBasedRemediationConfig\n", os.Args[0])
		fmt.Println()
		fmt.Println("The tool performs the following validations:")
		fmt.Println("  • Discovers all SBR agent pods in the namespace")
		fmt.Println("  • Extracts SBR device files from each pod")
		fmt.Println("  • Compares checksums across all pods")
		fmt.Println("  • Tests for real-time consistency and cache coherency")
		fmt.Println("  • Analyzes node mapping and slot assignments")
		fmt.Println("  • Performs timed consistency checks")
		fmt.Println()
		os.Exit(1)
	}

	var namespace string
	if len(os.Args) > 1 {
		namespace = os.Args[1]
	}
	var sbrConfigName string
	if len(os.Args) > 2 {
		sbrConfigName = os.Args[2]
	}

	fmt.Printf("🔍 SBR Device Consistency Validator\n")
	fmt.Printf("===================================\n")
	if namespace != "" {
		fmt.Printf("Namespace: %s\n", namespace)
	}
	if sbrConfigName != "" {
		fmt.Printf("StorageBasedRemediationConfig: %s\n", sbrConfigName)
	}
	fmt.Printf("Time: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	// Create Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		log.Fatalf("Failed to load kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	testClients := &utils.TestClients{}

	// If sbrConfigName is empty, find the first StorageBasedRemediationConfig in the namespace and use it
	if sbrConfigName == "" {
		fmt.Printf("No StorageBasedRemediationConfig name provided, discovering first StorageBasedRemediationConfig in namespace %q...\n", namespace)
		sbrConfigs, err := getStorageBasedRemediationConfigs(namespace)
		if err != nil {
			log.Fatalf("Failed to list StorageBasedRemediationConfigs in namespace %q: %v", namespace, err)
		}
		if len(sbrConfigs) == 0 {
			log.Fatalf("No StorageBasedRemediationConfig resources found in namespace %q", namespace)
		}
		sbrConfigName = sbrConfigs[0]
		fmt.Printf("Using StorageBasedRemediationConfig: %s in namespace %s\n", sbrConfigName, namespace)
	}

	// Discover SBR agent pods
	fmt.Printf("🚀 Discovering SBR agent pods...\n")
	pods, err := getSBRAgentPods(clientset, namespace, sbrConfigName)
	if err != nil {
		log.Fatalf("Failed to get SBR agent pods: %v", err)
	}

	if len(pods) == 0 {
		log.Fatalf("No SBR agent pods found in namespace %s", namespace)
	}

	fmt.Printf("Found %d SBR agent pods:\n", len(pods))
	for i, pod := range pods {
		fmt.Printf("  %d. %s\n", i+1, pod)
	}
	fmt.Println()

	// Phase 1: Initial consistency check
	fmt.Printf("📊 Phase 1: Initial Device File Analysis\n")
	fmt.Printf("========================================\n")

	// podName -> {sbr-device: checksum, fence-device: checksum, node-mapping: checksum}
	checksums := make(map[string]map[string]string)

	for _, podName := range pods {
		fmt.Printf("Analyzing pod: %s\n", podName)
		checksums[podName] = make(map[string]string)

		// Get checksums for each file type
		if checksum, err := getFileChecksum(podName, namespace, "sbr-device"); err == nil {
			checksums[podName]["sbr-device"] = checksum
		} else {
			fmt.Printf("  ⚠️  Failed to get sbr-device checksum: %v\n", err)
		}

		if checksum, err := getFileChecksum(podName, namespace, "fence-device"); err == nil {
			checksums[podName]["fence-device"] = checksum
		} else {
			fmt.Printf("  ⚠️  Failed to get fence-device checksum: %v\n", err)
		}

		if checksum, err := getFileChecksum(podName, namespace, "node-mapping"); err == nil {
			checksums[podName]["node-mapping"] = checksum
		} else {
			fmt.Printf("  ⚠️  Failed to get node-mapping checksum: %v\n", err)
		}
	}

	// Analyze consistency
	fmt.Printf("\n📋 Consistency Analysis:\n")
	analyzeConsistency(checksums)

	// Phase 2: Detailed slot analysis
	fmt.Printf("\n📊 Phase 2: Detailed Slot Analysis\n")
	fmt.Printf("==================================\n")

	for _, podName := range pods {
		fmt.Printf("--- Pod: %s ---\n", podName)

		fmt.Printf("Node Mapping:\n")
		if err := testClients.NodeMapSummary(podName, namespace, ""); err != nil {
			fmt.Printf("Failed to get node mapping: %v\n", err)
		}

		fmt.Printf("SBR Device Slots:\n")
		if err := testClients.SBRDeviceSummary(podName, namespace, ""); err != nil {
			fmt.Printf("Failed to get SBR device info: %v\n", err)
		}

		fmt.Printf("Fence Device Slots:\n")
		if err := testClients.FenceDeviceSummary(podName, namespace, ""); err != nil {
			fmt.Printf("Failed to get fence device info: %v\n", err)
		}

		fmt.Println()
	}

	// Phase 3: Timed consistency check
	fmt.Printf("📊 Phase 3: Timed Consistency Check\n")
	fmt.Printf("===================================\n")
	fmt.Printf("Monitoring SBR device changes over 30 seconds...\n\n")

	timedConsistencyCheck(pods, namespace)

	// Phase 4: Cache coherency test
	fmt.Printf("\n📊 Phase 4: Cache Coherency Validation\n")
	fmt.Printf("=====================================\n")

	if len(pods) > 0 {
		// Test storage configuration on the first pod
		if err := testClients.ValidateStorageConfiguration(pods[0], namespace); err != nil {
			fmt.Printf("❌ Storage configuration validation failed: %v\n", err)
		}
	}

	fmt.Printf("\n🎉 SBR Consistency Analysis Complete!\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("📝 Summary:\n")
	fmt.Printf("• Analyzed %d SBR agent pods\n", len(pods))
	fmt.Printf("• Checked device file consistency across all pods\n")
	fmt.Printf("• Performed real-time monitoring of SBR operations\n")
	fmt.Printf("• Validated storage configuration and cache coherency\n")
	fmt.Printf("\n💡 Next Steps:\n")
	fmt.Printf("• Review any consistency warnings above\n")
	fmt.Printf("• Monitor SBR agent logs for coordination issues\n")
	fmt.Printf("• Ensure all nodes can see each other's heartbeats\n")
}

// getKubeConfig loads the Kubernetes configuration
func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster config first
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}

	// Fall back to kubeconfig file
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}

// getStorageBasedRemediationConfigs lists StorageBasedRemediationConfig resources in the given namespace
func getStorageBasedRemediationConfigs(namespace string) ([]string, error) {
	config, err := getKubeConfig()
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	sbrConfigGVR := schema.GroupVersionResource{
		Group:    "storage-based-remediation.medik8s.io",
		Version:  "v1alpha1",
		Resource: "storagebasedremediationconfigs",
	}

	var list *unstructured.UnstructuredList
	if namespace == "" {
		// Search all namespaces
		list, err = dynamicClient.Resource(sbrConfigGVR).List(context.TODO(), metav1.ListOptions{})
	} else {
		// Search specific namespace
		list, err = dynamicClient.Resource(sbrConfigGVR).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
	}
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}

	return names, nil
}

// getSBRAgentPods discovers SBR agent pods in the given namespace
func getSBRAgentPods(clientset *kubernetes.Clientset, namespace, sbrConfigName string) ([]string, error) {
	var labelSelector string
	if sbrConfigName != "" {
		labelSelector = fmt.Sprintf("sbrconfig=%s", sbrConfigName)
	} else {
		labelSelector = "app.kubernetes.io/name=sbr-agent"
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	var podNames []string
	for _, pod := range pods.Items {
		if pod.Status.Phase == "Running" {
			podNames = append(podNames, pod.Name)
		}
	}

	return podNames, nil
}

// getFileChecksum gets the checksum of a specific SBR file from a pod
func getFileChecksum(podName, namespace, fileType string) (string, error) {
	var filePath string
	switch fileType {
	case "sbr-device":
		filePath = fmt.Sprintf("%s/%s", agent.SharedStorageSBRDeviceDirectory, agent.SharedStorageSBRDeviceFile)
	case "fence-device":
		filePath = fmt.Sprintf("%s/%s%s", agent.SharedStorageSBRDeviceDirectory, agent.SharedStorageSBRDeviceFile, agent.SharedStorageFenceDeviceSuffix)
	case "node-mapping":
		filePath = fmt.Sprintf("%s/%s%s", agent.SharedStorageSBRDeviceDirectory, agent.SharedStorageSBRDeviceFile, agent.SharedStorageNodeMappingSuffix)
	default:
		return "", fmt.Errorf("unknown file type: %s", fileType)
	}

	cmd := []string{"md5sum", filePath}
	stdout, stderr, err := execInPod(podName, namespace, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get checksum: %v, stderr: %s", err, stderr)
	}

	// Parse md5sum output (format: "checksum  filename")
	parts := strings.Fields(stdout)
	if len(parts) > 0 {
		return parts[0], nil
	}

	return "", fmt.Errorf("invalid md5sum output: %s", stdout)
}

// analyzeConsistency analyzes checksum consistency across pods
func analyzeConsistency(checksums map[string]map[string]string) {
	fileTypes := []string{"sbr-device", "fence-device", "node-mapping"}

	for _, fileType := range fileTypes {
		fmt.Printf("\n%s consistency:\n", fileType)

		// Collect all checksums for this file type
		fileChecksums := make(map[string][]string) // checksum -> list of pods
		for podName, podChecksums := range checksums {
			if checksum, exists := podChecksums[fileType]; exists {
				fileChecksums[checksum] = append(fileChecksums[checksum], podName)
			}
		}

		if len(fileChecksums) == 0 {
			fmt.Printf("  ❌ No data available\n")
			continue
		}

		if len(fileChecksums) == 1 {
			fmt.Printf("  ✅ Consistent across all pods\n")
			for checksum, pods := range fileChecksums {
				fmt.Printf("    Checksum: %s (pods: %v)\n", checksum, pods)
			}
		} else {
			fmt.Printf("  ⚠️  INCONSISTENT - %d different checksums:\n", len(fileChecksums))
			for checksum, pods := range fileChecksums {
				fmt.Printf("    %s: %v\n", checksum, pods)
			}

			if fileType == "sbr-device" || fileType == "fence-device" {
				fmt.Printf("    💡 Note: Different checksums for %s may indicate cache coherency issues\n", fileType)
				fmt.Printf("       Each pod may be seeing different versions of the shared file due to NFS caching\n")
			}
		}
	}
}

// timedConsistencyCheck monitors consistency over time
func timedConsistencyCheck(pods []string, namespace string) {
	interval := 5 * time.Second
	duration := 30 * time.Second
	iterations := int(duration / interval)

	for i := 0; i < iterations; i++ {
		fmt.Printf("Check %d/%d (time: %s)\n", i+1, iterations, time.Now().Format("15:04:05"))

		for _, podName := range pods {
			checksum, err := getFileChecksum(podName, namespace, "sbr-device")
			if err != nil {
				fmt.Printf("  %s: ERROR - %v\n", podName, err)
			} else {
				fmt.Printf("  %s: %s\n", podName, checksum[:8]+"...")
			}
		}

		if i < iterations-1 {
			fmt.Printf("Waiting %v...\n\n", interval)
			time.Sleep(interval)
		}
	}
}

// execInPod executes a command in a pod and returns stdout, stderr, and error
func execInPod(podName, namespace string, command []string) (string, string, error) {
	// Build kubectl exec command
	args := []string{"exec", "-n", namespace, podName, "--"}
	args = append(args, command...)

	cmd := exec.Command("kubectl", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", stderr.String(), fmt.Errorf("kubectl exec failed: %w", err)
	}

	return stdout.String(), stderr.String(), nil
}
