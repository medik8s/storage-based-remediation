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

// Package utils provides debugging utilities for SBD node mapping and device inspection
//
// Usage Examples:
//
//	// Print node mapping to stdout
//	err := testClients.NodeMapSummary("sbd-agent-pod-name", "sbd-system", "")
//
//	// Save node mapping to file
//	err := testClients.NodeMapSummary("sbd-agent-pod-name", "sbd-system", "node-mapping.txt")
//
//	// Print SBD device info to stdout
//	err := testClients.SBDDeviceSummary("sbd-agent-pod-name", "sbd-system", "")
//
//	// Save SBD device info to file
//	err := testClients.SBDDeviceSummary("sbd-agent-pod-name", "sbd-system", "sbd-device.txt")
package utils

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

// SBDNodeSummary represents a summary of SBD node information for display purposes
type SBDNodeSummary struct {
	NodeID    uint16
	Timestamp time.Time
	Sequence  uint64
	Type      string
	HasData   bool
}

// GetNodeMapFromPod extracts the current node mapping from an SBD agent pod
func (tc *TestClients) GetNodeMapFromPod(podName, namespace string) (*sbdprotocol.NodeMapTable, error) {
	// Execute command to read node mapping file
	sbdNodeMappingPath := fmt.Sprintf("%s/%s%s", agent.SharedStorageSBDDeviceDirectory, agent.SharedStorageSBDDeviceFile, agent.SharedStorageNodeMappingSuffix)

	cmd := []string{"cat", sbdNodeMappingPath}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read node mapping from pod %s: %v, stderr: %s", podName, err, stderr)
	}

	return parseNodeMapping([]byte(stdout))
}

// GetSBDDeviceInfoFromPod extracts SBD device information from an SBD agent pod
func (tc *TestClients) GetSBDDeviceInfoFromPod(podName, namespace string) ([]SBDNodeSummary, error) {
	// Execute command to read SBD device content
	// Read first 255 slots (255 * 512 bytes = 130560 bytes)

	sbdDevicePath := fmt.Sprintf("%s/%s", agent.SharedStorageSBDDeviceDirectory, agent.SharedStorageSBDDeviceFile)

	cmd := []string{"dd", "if=" + sbdDevicePath, "bs=512", "count=255", "status=none"}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read SBD device from pod %s: %v, stderr: %s", podName, err, stderr)
	}

	slots, err := parseSBDDevice([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SBD device: %w", err)
	}

	return slots, nil
}

// GetFenceDeviceInfoFromPod extracts fence device information from an SBD agent pod
func (tc *TestClients) GetFenceDeviceInfoFromPod(podName, namespace string) ([]SBDNodeSummary, error) {
	// Execute command to read fence device content
	// Read first 255 slots (255 * 512 bytes = 130560 bytes)

	fenceDevicePath := fmt.Sprintf("%s/%s%s", agent.SharedStorageSBDDeviceDirectory, agent.SharedStorageSBDDeviceFile, agent.SharedStorageFenceDeviceSuffix)

	cmd := []string{"dd", "if=" + fenceDevicePath, "bs=512", "count=255", "status=none"}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read fence device from pod %s: %v, stderr: %s", podName, err, stderr)
	}

	slots, err := parseSBDDevice([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("failed to parse fence device: %w", err)
	}

	return slots, nil
}

// PrintNodeMap prints the node mapping summary to stdout
func PrintNodeMap(nodeMapTable *sbdprotocol.NodeMapTable) {
	fmt.Printf("=== Node Mapping Summary ===\n")
	fmt.Printf("Cluster name: %s\n", nodeMapTable.ClusterName)
	fmt.Printf("Version: %d\n", nodeMapTable.Version)
	fmt.Printf("Last update: %s\n", nodeMapTable.LastUpdate)
	fmt.Printf("Checksum: %d\n", nodeMapTable.Checksum)
	fmt.Printf("Entries: %d\n", len(nodeMapTable.Entries))
	fmt.Printf("Node usage: %v\n", nodeMapTable.NodeUsage)

	if len(nodeMapTable.Entries) == 0 {
		fmt.Printf("No active node mappings found.\n")
		return
	}

	fmt.Printf("%-6s %-30s %-20s\n", "NodeID", "Node Name", "Last Seen")
	fmt.Printf("%-6s %-30s %-20s\n", "------", "---------", "---------")

	for _, entry := range nodeMapTable.Entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-6d %-30s %-20s\n", entry.NodeID, entry.NodeName, lastSeenStr)
	}
	fmt.Printf("\n")
}

// PrintSBDDevice prints the SBD device summary to stdout
func PrintSBDDevice(slots []SBDNodeSummary) {
	fmt.Printf("=== SBD Device Summary ===\n")
	fmt.Printf("Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		fmt.Printf("No active SBD slots found.\n")
		return
	}

	fmt.Printf("%-8s %-12s %-20s %-10s\n", "NodeID", "Type", "Timestamp", "Sequence")
	fmt.Printf("%-8s %-12s %-20s %-10s\n", "------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		fmt.Printf("%-8d %-12s %-20s %-10d\n",
			slot.NodeID, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Printf("\n")
}

// PrintFenceDevice prints the fence device summary to stdout
func PrintFenceDevice(slots []SBDNodeSummary) {
	fmt.Printf("=== Fence Device Summary ===\n")
	fmt.Printf("Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		fmt.Printf("No active fence slots found.\n")
		return
	}

	fmt.Printf("%-8s %-12s %-20s %-10s\n", "NodeID", "Type", "Timestamp", "Sequence")
	fmt.Printf("%-8s %-12s %-20s %-10s\n", "------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		fmt.Printf("%-8d %-12s %-20s %-10d\n",
			slot.NodeID, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Printf("\n")
}

// SaveNodeMapToFile saves the node mapping summary to a file
func SaveNodeMapToFile(nodeMapTable *sbdprotocol.NodeMapTable, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	fmt.Fprintf(file, "=== Node Mapping Summary ===\n")
	fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "Cluster name: %s\n", nodeMapTable.ClusterName)
	fmt.Fprintf(file, "Version: %d\n", nodeMapTable.Version)
	fmt.Fprintf(file, "Last update: %s\n", nodeMapTable.LastUpdate)
	fmt.Fprintf(file, "Checksum: %d\n", nodeMapTable.Checksum)
	fmt.Fprintf(file, "Entries: %d\n", len(nodeMapTable.Entries))
	fmt.Fprintf(file, "Node usage: %v\n", nodeMapTable.NodeUsage)

	if len(nodeMapTable.Entries) == 0 {
		fmt.Fprintf(file, "No active node mappings found.\n")
		return nil
	}

	fmt.Fprintf(file, "%-6s %-30s %-20s\n", "NodeID", "Node Name", "Last Seen")
	fmt.Fprintf(file, "%-6s %-30s %-20s\n", "------", "---------", "---------")

	for _, entry := range nodeMapTable.Entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(file, "%-6d %-30s %-20s\n", entry.NodeID, entry.NodeName, lastSeenStr)
	}
	fmt.Fprintf(file, "\n")

	return nil
}

// SaveSBDDeviceToFile saves the SBD device summary to a file
func SaveSBDDeviceToFile(slots []SBDNodeSummary, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	fmt.Fprintf(file, "=== SBD Device Summary ===\n")
	fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		fmt.Fprintf(file, "No active SBD slots found.\n")
		return nil
	}

	fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "NodeID", "Node Name", "Type", "Timestamp", "Sequence")
	fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "------", "---------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		nodeNameStr := "Unknown" // We don't have node name in the SBD message
		fmt.Fprintf(file, "%-8d %-30s %-12s %-20s %-10d\n",
			slot.NodeID, nodeNameStr, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Fprintf(file, "\n")

	return nil
}

// SaveFenceDeviceToFile saves the fence device summary to a file
func SaveFenceDeviceToFile(slots []SBDNodeSummary, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	fmt.Fprintf(file, "=== Fence Device Summary ===\n")
	fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		fmt.Fprintf(file, "No active fence slots found.\n")
		return nil
	}

	fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "NodeID", "Node Name", "Type", "Timestamp", "Sequence")
	fmt.Fprintf(file, "%-8s %-30s %-12s %-20s %-10s\n", "------", "---------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		nodeNameStr := "Unknown" // We don't have node name in the fence message
		fmt.Fprintf(file, "%-8d %-30s %-12s %-20s %-10d\n",
			slot.NodeID, nodeNameStr, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Fprintf(file, "\n")

	return nil
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
		fmt.Printf("Node mapping summary saved to: %s\n", outputFile)
	} else {
		PrintNodeMap(nodeMapTable)
	}

	return nil
}

// SBDDeviceSummary gets SBD device info from a pod and either prints or saves it
func (tc *TestClients) SBDDeviceSummary(podName, namespace, outputFile string) error {
	slots, err := tc.GetSBDDeviceInfoFromPod(podName, namespace)
	if err != nil {
		return err
	}

	if outputFile != "" {
		if err := SaveSBDDeviceToFile(slots, outputFile); err != nil {
			return fmt.Errorf("failed to save SBD device info to file: %w", err)
		}
		fmt.Printf("SBD device summary saved to: %s\n", outputFile)
	} else {
		PrintSBDDevice(slots)
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
		fmt.Printf("Fence device summary saved to: %s\n", outputFile)
	} else {
		PrintFenceDevice(slots)
	}

	return nil
}

// ValidateStorageConfiguration validates that storage is properly configured for SBD
func (tc *TestClients) ValidateStorageConfiguration(podName, namespace string) error {
	fmt.Printf("=== Validating Storage Configuration for SBD ===\n")

	// Check mount information
	fmt.Println("--- NFS Mount Information ---")
	mountInfo, err := tc.getMountInfo(podName, namespace)
	if err != nil {
		return fmt.Errorf("failed to get mount info: %w", err)
	}

	// Parse and validate mount options
	if err := validateNFSMountOptions(mountInfo); err != nil {
		return fmt.Errorf("mount options validation failed: %w", err)
	}

	// Test file locking behavior
	fmt.Println("--- File Locking Test ---")
	if err := tc.testFileLocking(podName, namespace); err != nil {
		return fmt.Errorf("file locking test failed: %w", err)
	}

	// Test cache coherency
	fmt.Println("--- Cache Coherency Test ---")
	if err := tc.testCacheCoherency(podName, namespace); err != nil {
		return fmt.Errorf("cache coherency test failed: %w", err)
	}

	fmt.Printf("✅ Storage configuration validation passed\n\n")
	return nil
}

// getMountInfo retrieves mount information for the SBD storage path
func (tc *TestClients) getMountInfo(podName, namespace string) (string, error) {
	cmd := []string{"sh", "-c", "mount | grep /dev/sbd"}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get mount info: %v, stderr: %s", err, stderr)
	}
	return stdout, nil
}

// validateNFSMountOptions validates that mount options include required cache coherency settings
func validateNFSMountOptions(mountInfo string) error {
	fmt.Printf("Mount info: %s\n", mountInfo)

	requiredOptions := []string{"cache=none", "sync"}
	recommendedOptions := []string{"local_lock=none"}

	missing := []string{}
	for _, option := range requiredOptions {
		if !strings.Contains(mountInfo, option) {
			missing = append(missing, option)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("❌ Missing required NFS mount options: %v. These are required for SBD cache coherency", missing)
	}

	fmt.Printf("✅ Required mount options present: %v\n", requiredOptions)

	// Check recommended options
	missingRec := []string{}
	for _, option := range recommendedOptions {
		if !strings.Contains(mountInfo, option) {
			missingRec = append(missingRec, option)
		}
	}

	if len(missingRec) > 0 {
		fmt.Printf("⚠️  Missing recommended options: %v\n", missingRec)
	} else {
		fmt.Printf("✅ Recommended mount options present: %v\n", recommendedOptions)
	}

	return nil
}

// testFileLocking tests that file locking is working correctly across the shared storage
func (tc *TestClients) testFileLocking(podName, namespace string) error {
	testFile := "/dev/sbd/test-lock-file"

	// Create a test file with flock
	cmd := []string{"sh", "-c", fmt.Sprintf("echo 'test' > %s && flock -x %s sleep 1", testFile, testFile)}
	_, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return fmt.Errorf("file locking test failed: %v, stderr: %s", err, stderr)
	}

	// Clean up test file
	cleanupCmd := []string{"rm", "-f", testFile}
	_, _, _ = tc.execInPod(podName, namespace, cleanupCmd)

	fmt.Printf("✅ File locking test passed\n")
	return nil
}

// testCacheCoherency tests that cache coherency is working by checking file modification visibility
func (tc *TestClients) testCacheCoherency(podName, namespace string) error {
	testFile := "/dev/sbd/test-cache-coherency"
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

	fmt.Printf("✅ Cache coherency test passed\n")
	return nil
}

// execInPod executes a command in a pod and returns stdout, stderr, and error
// Uses kubectl exec with system:admin privileges instead of REST API
func (tc *TestClients) execInPod(podName, namespace string, command []string) (string, string, error) {
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

// parseNodeMapping parses binary node mapping data
func parseNodeMapping(data []byte) (*sbdprotocol.NodeMapTable, error) {
	// This is a simplified parser - in reality, we'd need to understand
	// the exact binary format used by the NodeManager
	// For now, we'll try to extract readable node names and simulate the mapping

	// Unmarshal the data into a NodeMapTable
	nodeMapTable, err := sbdprotocol.UnmarshalNodeMapTable(data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal node mapping: %w", err)
	}

	return nodeMapTable, nil
}

// parseSBDDevice parses binary SBD device data
func parseSBDDevice(data []byte) ([]SBDNodeSummary, error) {
	const slotSize = sbdprotocol.SBD_SLOT_SIZE
	magic := sbdprotocol.SBD_MAGIC

	var slots []SBDNodeSummary

	for i := 0; i < len(data)/slotSize; i++ {
		start := i * slotSize
		end := start + slotSize
		if end > len(data) {
			break
		}

		slotData := data[start:end]

		// Check if slot has SBD message magic
		if len(slotData) >= 8 && string(slotData[:8]) == magic {
			slot, err := parseSBDSlot(uint16(i), slotData)
			if err == nil && slot.HasData {
				slots = append(slots, slot)
			} else {
				fmt.Printf("Failed to parse SBD slot: %v\n", err)
				return nil, fmt.Errorf("failed to parse SBD slot: %w", err)
			}
		}
	}

	return slots, nil
}

// parseSBDSlot parses a single SBD slot
func parseSBDSlot(nodeID uint16, data []byte) (SBDNodeSummary, error) {
	if len(data) < sbdprotocol.SBD_HEADER_SIZE {
		return SBDNodeSummary{}, fmt.Errorf("slot data too short")
	}

	// Parse SBD header (simplified)
	magic := string(data[:8])
	if magic != sbdprotocol.SBD_MAGIC {
		return SBDNodeSummary{NodeID: nodeID, HasData: false}, nil
	}

	// Read basic header fields (skip version since it's not used)
	msgType := data[10]
	if nodeID != binary.LittleEndian.Uint16(data[11:13]) {
		return SBDNodeSummary{}, fmt.Errorf("nodeID mismatch: %d != %d", nodeID, binary.LittleEndian.Uint16(data[11:13]))
	}

	timestamp := binary.LittleEndian.Uint64(data[13:21])
	sequence := binary.LittleEndian.Uint64(data[21:29])

	// Use existing helper function for message type name
	typeStr := sbdprotocol.GetMessageTypeName(msgType)

	// Convert timestamp from nanoseconds to time.Time
	ts := time.Unix(0, int64(timestamp))

	return SBDNodeSummary{
		NodeID:    nodeID,
		Timestamp: ts,
		Sequence:  sequence,
		Type:      typeStr,
		HasData:   true,
	}, nil
}

// isPrintableString checks if a string contains mostly printable characters
func isPrintableString(s string) bool {
	if len(s) == 0 {
		return false
	}

	printableCount := 0
	for _, r := range s {
		if r >= 32 && r <= 126 { // Printable ASCII range
			printableCount++
		}
	}

	// Consider it printable if more than 80% of characters are printable
	return float64(printableCount)/float64(len(s)) > 0.8
}
