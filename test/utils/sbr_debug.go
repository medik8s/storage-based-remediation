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

// Package utils provides debugging utilities for SBR node mapping and device inspection
//
// Usage Examples:
//
//	// Print node mapping to stdout
//	err := testClients.NodeMapSummary("sbr-agent-pod-name", "sbr-system", "")
//
//	// Save node mapping to file
//	err := testClients.NodeMapSummary("sbr-agent-pod-name", "sbr-system", "node-mapping.txt")
//
//	// Print SBR device info to stdout
//	err := testClients.SBRDeviceSummary("sbr-agent-pod-name", "sbr-system", "")
//
//	// Save SBR device info to file
//	err := testClients.SBRDeviceSummary("sbr-agent-pod-name", "sbr-system", "sbr-device.txt")
package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/inspection"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

const (
	// notAvailableText represents a value that is not available or unknown
	notAvailableText = "N/A"
)

// SBRNodeSummary represents a summary of SBR node information for display purposes
type SBRNodeSummary struct {
	NodeID    uint16
	Timestamp time.Time
	Sequence  uint64
	Type      string
	HasData   bool
}

// InspectFromPod uses the same read-only diagnostic command as administrators.
func (tc *TestClients) InspectFromPod(podName, namespace string) (*inspection.Report, error) {
	pod := &corev1.Pod{}
	if err := tc.Client.Get(tc.Context, client.ObjectKey{Name: podName, Namespace: namespace}, pod); err != nil {
		return nil, fmt.Errorf("get inspection pod %s: %w", podName, err)
	}
	path, err := inspectionDeviceFromPod(pod)
	if err != nil {
		return nil, fmt.Errorf("inspect pod %s: %w", podName, err)
	}
	stdout, stderr, err := tc.execInPodContainer(podName, namespace, "sbr-agent",
		[]string{"sbr-agent", "inspect", "--sbr-device", path, "-o", "json"})
	if err != nil {
		return nil, fmt.Errorf("inspect pod %s: %w; stderr: %s; stdout: %s", podName, err, stderr, stdout)
	}
	var report inspection.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, fmt.Errorf("decode inspection from pod %s: %w", podName, err)
	}
	if report.SchemaVersion != 1 || (report.Mode != "fs" && report.Mode != "block") || len(report.Errors) != 0 {
		return nil, fmt.Errorf("invalid or incomplete inspection from pod %s: %+v", podName, report)
	}
	return &report, nil
}

// Read the operator-configured path rather than host /proc/1/cmdline.
func inspectionDeviceFromPod(pod *corev1.Pod) (string, error) {
	for _, container := range pod.Spec.Containers {
		if container.Name != "sbr-agent" {
			continue
		}
		var path string
		for i := 0; i < len(container.Args); i++ {
			arg := container.Args[i]
			if strings.HasPrefix(arg, "--sbr-device=") {
				path = strings.TrimPrefix(arg, "--sbr-device=")
			} else if arg == "--sbr-device" {
				if i+1 >= len(container.Args) {
					return "", fmt.Errorf("sbr-agent has no value for --sbr-device")
				}
				i++
				path = container.Args[i]
			}
		}
		if path == "" {
			return "", fmt.Errorf("sbr-agent has no configured --sbr-device")
		}
		return path, nil
	}
	return "", fmt.Errorf("pod has no sbr-agent container")
}

// GetNodeMapFromPod extracts the current node mapping in either storage mode.
func (tc *TestClients) GetNodeMapFromPod(podName, namespace string) (*sbdprotocol.NodeMapTable, error) {
	report, err := tc.InspectFromPod(podName, namespace)
	if err != nil {
		return nil, err
	}
	if report.NodeMap == nil {
		return nil, fmt.Errorf("node map is %s", report.NodeMapStatus)
	}
	return report.NodeMap, nil
}

func inspectionSummaries(slots []inspection.Slot) []SBRNodeSummary {
	result := make([]SBRNodeSummary, 0, len(slots))
	for _, slot := range slots {
		summary := SBRNodeSummary{NodeID: slot.NodeID, Sequence: slot.Sequence, Type: slot.Type, HasData: true}
		if slot.Timestamp != nil {
			summary.Timestamp = *slot.Timestamp
		}
		result = append(result, summary)
	}
	return result
}

// GetSBRDeviceInfoFromPod extracts validated heartbeat slots through the CLI.
func (tc *TestClients) GetSBRDeviceInfoFromPod(podName, namespace string) ([]SBRNodeSummary, error) {
	report, err := tc.InspectFromPod(podName, namespace)
	if err != nil {
		return nil, err
	}
	return inspectionSummaries(report.Heartbeats), nil
}

// GetFenceDeviceInfoFromPod extracts validated fence slots through the CLI.
func (tc *TestClients) GetFenceDeviceInfoFromPod(podName, namespace string) ([]SBRNodeSummary, error) {
	report, err := tc.InspectFromPod(podName, namespace)
	if err != nil {
		return nil, err
	}
	return inspectionSummaries(report.Fences), nil
}

// PrintNodeMap prints the node mapping summary to stdout
func PrintNodeMap(nodeMapTable *sbdprotocol.NodeMapTable) {
	ginkgo.GinkgoWriter.Printf("\n=== Node Mapping Summary ===\n")
	ginkgo.GinkgoWriter.Printf("Cluster name: %s\n", nodeMapTable.ClusterName)
	ginkgo.GinkgoWriter.Printf("Version: %d\n", nodeMapTable.Version)
	ginkgo.GinkgoWriter.Printf("Last update: %s\n", nodeMapTable.LastUpdate)
	ginkgo.GinkgoWriter.Printf("Checksum: %d\n", nodeMapTable.Checksum)
	ginkgo.GinkgoWriter.Printf("Entries: %d\n", len(nodeMapTable.Entries))

	if len(nodeMapTable.Entries) == 0 {
		ginkgo.GinkgoWriter.Printf("No active node mappings found.\n")
		return
	}

	ginkgo.GinkgoWriter.Printf("%-6s %-30s %-20s\n", "NodeID", "Node Name", "Last Seen")
	ginkgo.GinkgoWriter.Printf("%-6s %-30s %-20s\n", "------", "---------", "---------")

	for _, entry := range nodeMapTable.Entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		ginkgo.GinkgoWriter.Printf("%-6d %-30s %-20s\n", entry.NodeID, entry.NodeName, lastSeenStr)
	}
	ginkgo.GinkgoWriter.Printf("\n")
}

// PrintSBRDevice prints the SBR device summary to stdout
func PrintSBRDevice(slots []SBRNodeSummary) {
	ginkgo.GinkgoWriter.Printf("\n=== SBR Device Summary ===\n")
	ginkgo.GinkgoWriter.Printf("Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		ginkgo.GinkgoWriter.Printf("No active SBR slots found.\n")
		return
	}

	ginkgo.GinkgoWriter.Printf("%-8s %-12s %-20s %-10s\n", "NodeID", "Type", "Timestamp", "Sequence")
	ginkgo.GinkgoWriter.Printf("%-8s %-12s %-20s %-10s\n", "------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := notAvailableText
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		ginkgo.GinkgoWriter.Printf("%-8d %-12s %-20s %-10d\n",
			slot.NodeID, slot.Type, timestampStr, slot.Sequence)
	}
	ginkgo.GinkgoWriter.Printf("\n")
}

// PrintFenceDevice prints the fence device summary to stdout
func PrintFenceDevice(slots []SBRNodeSummary) {
	ginkgo.GinkgoWriter.Printf("\n=== Fence Device Summary ===\n")
	ginkgo.GinkgoWriter.Printf("Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		ginkgo.GinkgoWriter.Printf("No active fence slots found.\n")
		return
	}

	ginkgo.GinkgoWriter.Printf("%-8s %-12s %-20s %-10s\n", "NodeID", "Type", "Timestamp", "Sequence")
	ginkgo.GinkgoWriter.Printf("%-8s %-12s %-20s %-10s\n", "------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := notAvailableText
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		ginkgo.GinkgoWriter.Printf("%-8d %-12s %-20s %-10d\n",
			slot.NodeID, slot.Type, timestampStr, slot.Sequence)
	}
	ginkgo.GinkgoWriter.Printf("\n")
}

// SaveNodeMapToFile saves the node mapping summary to a file
func SaveNodeMapToFile(nodeMapTable *sbdprotocol.NodeMapTable, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer func() { _ = file.Close() }()

	_, _ = fmt.Fprintf(file, "=== Node Mapping Summary ===\n")
	_, _ = fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(file, "Cluster name: %s\n", nodeMapTable.ClusterName)
	_, _ = fmt.Fprintf(file, "Version: %d\n", nodeMapTable.Version)
	_, _ = fmt.Fprintf(file, "Last update: %s\n", nodeMapTable.LastUpdate)
	_, _ = fmt.Fprintf(file, "Checksum: %d\n", nodeMapTable.Checksum)
	_, _ = fmt.Fprintf(file, "Entries: %d\n", len(nodeMapTable.Entries))
	_, _ = fmt.Fprintf(file, "Node usage: %v\n", nodeMapTable.NodeUsage)

	if len(nodeMapTable.Entries) == 0 {
		_, _ = fmt.Fprintf(file, "No active node mappings found.\n")
		return nil
	}

	_, _ = fmt.Fprintf(file, "%-6s %-30s %-20s\n", "NodeID", "Node Name", "Last Seen")
	_, _ = fmt.Fprintf(file, "%-6s %-30s %-20s\n", "------", "---------", "---------")

	for _, entry := range nodeMapTable.Entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		_, _ = fmt.Fprintf(file, "%-6d %-30s %-20s\n", entry.NodeID, entry.NodeName, lastSeenStr)
	}
	_, _ = fmt.Fprintf(file, "\n")

	return nil
}

// saveDeviceToFileGeneric is a generic helper function for saving device summaries to file
func saveDeviceToFileGeneric(slots []SBRNodeSummary, filename, deviceType, noSlotsMessage string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer func() { _ = file.Close() }()

	_, _ = fmt.Fprintf(file, "=== %s Summary ===\n", deviceType)
	_, _ = fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(file, "Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		_, _ = fmt.Fprintf(file, "%s\n", noSlotsMessage)
		return nil
	}

	_, _ = fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "NodeID", "Node Name", "Type", "Timestamp", "Sequence")
	_, _ = fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "------", "---------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := notAvailableText
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		nodeNameStr := "Unknown" // We don't have node name in the message
		_, _ = fmt.Fprintf(file, "%-8d %-30s %-12s %-20s %-10d\n",
			slot.NodeID, nodeNameStr, slot.Type, timestampStr, slot.Sequence)
	}
	_, _ = fmt.Fprintf(file, "\n")

	return nil
}

// SaveSBRDeviceToFile saves SBR device slots to a file for debugging
func SaveSBRDeviceToFile(slots []SBRNodeSummary, filename string) error {
	return saveDeviceToFileGeneric(slots, filename, "SBR Device", "No active SBR slots found.")
}

// SaveFenceDeviceToFile saves the fence device summary to a file
func SaveFenceDeviceToFile(slots []SBRNodeSummary, filename string) error {
	return saveDeviceToFileGeneric(slots, filename, "Fence Device", "No active fence slots found.")
}

// NodeMapSummary gets node mapping from a pod and either prints or saves it
func (tc *TestClients) NodeMapSummary(podName, namespace, outputFile string) error {
	nodeMapTable, err := tc.GetNodeMapFromPod(podName, namespace)
	if err != nil {
		return err
	}

	if outputFile != "" {
		if err := SaveNodeMapToFile(nodeMapTable, outputFile); err != nil {
			return fmt.Errorf("failed to save node map to file: %w", err)
		}
		ginkgo.GinkgoWriter.Printf("Node mapping summary saved to: %s\n", outputFile)
	} else {
		PrintNodeMap(nodeMapTable)
	}

	return nil
}

// SBRDeviceSummary gets SBR device info from a pod and either prints or saves it
func (tc *TestClients) SBRDeviceSummary(podName, namespace, outputFile string) error {
	slots, err := tc.GetSBRDeviceInfoFromPod(podName, namespace)
	if err != nil {
		return err
	}

	if outputFile != "" {
		if err := SaveSBRDeviceToFile(slots, outputFile); err != nil {
			return fmt.Errorf("failed to save SBR device info to file: %w", err)
		}
		ginkgo.GinkgoWriter.Printf("SBR device summary saved to: %s\n", outputFile)
	} else {
		PrintSBRDevice(slots)
	}

	return nil
}

// FenceDeviceSummary gets fence device info from a pod and either prints or saves it
func (tc *TestClients) FenceDeviceSummary(podName, namespace, outputFile string) error {
	slots, err := tc.GetFenceDeviceInfoFromPod(podName, namespace)
	if err != nil {
		return err
	}

	if outputFile != "" {
		if err := SaveFenceDeviceToFile(slots, outputFile); err != nil {
			return fmt.Errorf("failed to save fence device info to file: %w", err)
		}
		ginkgo.GinkgoWriter.Printf("Fence device summary saved to: %s\n", outputFile)
	} else {
		PrintFenceDevice(slots)
	}

	return nil
}

// ValidateStorageConfiguration validates that storage is properly configured for SBR
func (tc *TestClients) ValidateStorageConfiguration(podName, namespace string) error {
	ginkgo.GinkgoWriter.Printf("=== Validating Storage Configuration for SBR ===\n")

	// Check mount information
	ginkgo.GinkgoWriter.Printf("--- NFS Mount Information ---\n")
	mountInfo, err := tc.getMountInfo(podName, namespace)
	if err != nil {
		return fmt.Errorf("failed to get mount info: %w", err)
	}

	// Parse and validate mount options
	if err := validateNFSMountOptions(mountInfo); err != nil {
		return fmt.Errorf("mount options validation failed: %w", err)
	}

	// Test file locking behavior
	ginkgo.GinkgoWriter.Printf("--- File Locking Test ---\n")
	if err := tc.testFileLocking(podName, namespace); err != nil {
		return fmt.Errorf("file locking test failed: %w", err)
	}

	// Test cache coherency
	ginkgo.GinkgoWriter.Printf("--- Cache Coherency Test ---\n")
	if err := tc.testCacheCoherency(podName, namespace); err != nil {
		return fmt.Errorf("cache coherency test failed: %w", err)
	}

	ginkgo.GinkgoWriter.Printf("✅ Storage configuration validation passed\n\n")
	return nil
}

// getMountInfo retrieves mount information for the SBR storage path
func (tc *TestClients) getMountInfo(podName, namespace string) (string, error) {
	cmd := []string{"sh", "-c", "mount | grep /dev/sbr"}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get mount info: %v, stderr: %s", err, stderr)
	}
	return stdout, nil
}

// validateNFSMountOptions validates that mount options include required cache coherency settings
func validateNFSMountOptions(mountInfo string) error {
	ginkgo.GinkgoWriter.Printf("Mount info: %s\n", mountInfo)

	requiredOptions := []string{"cache=none", "sync"}
	recommendedOptions := []string{"local_lock=none"}

	missing := []string{}
	for _, option := range requiredOptions {
		if !strings.Contains(mountInfo, option) {
			missing = append(missing, option)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("❌ Missing required NFS mount options: %v. These are required for SBR cache coherency", missing)
	}

	ginkgo.GinkgoWriter.Printf("✅ Required mount options present: %v\n", requiredOptions)

	// Check recommended options
	missingRec := []string{}
	for _, option := range recommendedOptions {
		if !strings.Contains(mountInfo, option) {
			missingRec = append(missingRec, option)
		}
	}

	if len(missingRec) > 0 {
		ginkgo.GinkgoWriter.Printf("⚠️  Missing recommended options: %v\n", missingRec)
	} else {
		ginkgo.GinkgoWriter.Printf("✅ Recommended mount options present: %v\n", recommendedOptions)
	}

	return nil
}

// testFileLocking tests that file locking is working correctly across the shared storage
func (tc *TestClients) testFileLocking(podName, namespace string) error {
	testFile := "/dev/sbr/test-lock-file"

	// Create a test file with flock
	cmd := []string{"sh", "-c", fmt.Sprintf("echo 'test' > %s && flock -x %s sleep 1", testFile, testFile)}
	_, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return fmt.Errorf("file locking test failed: %v, stderr: %s", err, stderr)
	}

	// Clean up test file
	cleanupCmd := []string{"rm", "-f", testFile}
	_, _, _ = tc.execInPod(podName, namespace, cleanupCmd)

	ginkgo.GinkgoWriter.Printf("✅ File locking test passed\n")
	return nil
}

// testCacheCoherency tests that cache coherency is working by checking file modification visibility
func (tc *TestClients) testCacheCoherency(podName, namespace string) error {
	testFile := "/dev/sbr/test-cache-coherency"
	testContent := fmt.Sprintf("cache-test-%d", time.Now().Unix())

	// Write test content
	writeCmd := []string{"sh", "-c", fmt.Sprintf("echo '%s' > %s && sync", testContent, testFile)}
	_, stderr, err := tc.execInPod(podName, namespace, writeCmd)
	if err != nil {
		return fmt.Errorf("cache coherency write test failed: %v, stderr: %s", err, stderr)
	}

	// Read back immediately (should work with cache=none and sync)
	readCmd := []string{"cat", testFile}
	stdout, stderr, err := tc.execInPod(podName, namespace, readCmd)
	if err != nil {
		return fmt.Errorf("cache coherency read test failed: %v, stderr: %s", err, stderr)
	}

	if strings.TrimSpace(stdout) != testContent {
		return fmt.Errorf("cache coherency test failed: expected '%s', got '%s'", testContent, strings.TrimSpace(stdout))
	}

	// Clean up test file
	cleanupCmd := []string{"rm", "-f", testFile}
	_, _, _ = tc.execInPod(podName, namespace, cleanupCmd)

	ginkgo.GinkgoWriter.Printf("✅ Cache coherency test passed\n")
	return nil
}

// execInPod executes a command in a pod and returns stdout, stderr, and error
// Uses kubectl exec with system:admin privileges instead of REST API
func (tc *TestClients) execInPod(podName, namespace string, command []string) (string, string, error) {
	return tc.execInPodContainer(podName, namespace, "", command)
}

func (tc *TestClients) execInPodContainer(
	podName string,
	namespace string,
	container string,
	command []string,
) (string, string, error) {
	args := []string{"exec", "-n", namespace, podName}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--")
	args = append(args, command...)

	cmd := exec.Command("kubectl", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("kubectl exec failed: %w", err)
	}

	return stdout.String(), stderr.String(), nil
}
