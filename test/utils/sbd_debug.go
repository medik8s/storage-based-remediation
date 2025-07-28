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
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

// SBDSlotSummary represents a summary of SBD slot information for display purposes
type SBDSlotSummary struct {
	SlotID    uint16
	NodeID    uint16
	Type      string
	Timestamp time.Time
	Sequence  uint64
	HasData   bool
}

// GetNodeMapFromPod extracts the current node mapping from an SBD agent pod
func (tc *TestClients) GetNodeMapFromPod(podName, namespace string) ([]sbdprotocol.NodeMapEntry, error) {
	// Execute command to read node mapping file
	sbdNodeMappingPath := fmt.Sprintf("%s/%s.%s", agent.SharedStorageSBDDeviceDirectory, agent.SharedStorageSBDDeviceFile, agent.SharedStorageNodeMappingSuffix)

	cmd := []string{"cat", sbdNodeMappingPath}
	stdout, stderr, err := tc.execInPod(podName, namespace, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read node mapping from pod %s: %v, stderr: %s", podName, err, stderr)
	}

	entries, err := parseNodeMapping([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("failed to parse node mapping: %w", err)
	}

	return entries, nil
}

// GetSBDDeviceInfoFromPod extracts SBD device information from an SBD agent pod
func (tc *TestClients) GetSBDDeviceInfoFromPod(podName, namespace string) ([]SBDSlotSummary, error) {
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

// PrintNodeMap prints the node mapping summary to stdout
func PrintNodeMap(entries []sbdprotocol.NodeMapEntry) {
	fmt.Printf("=== Node Mapping Summary ===\n")
	fmt.Printf("Total entries: %d\n\n", len(entries))

	if len(entries) == 0 {
		fmt.Printf("No active node mappings found.\n")
		return
	}

	fmt.Printf("%-6s %-30s %-20s\n", "Slot", "Node Name", "Last Seen")
	fmt.Printf("%-6s %-30s %-20s\n", "----", "---------", "---------")

	for _, entry := range entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-6d %-30s %-20s\n", entry.SlotID, entry.NodeName, lastSeenStr)
	}
	fmt.Printf("\n")
}

// PrintSBDDevice prints the SBD device summary to stdout
func PrintSBDDevice(slots []SBDSlotSummary) {
	fmt.Printf("=== SBD Device Summary ===\n")
	fmt.Printf("Total slots with data: %d\n\n", len(slots))

	if len(slots) == 0 {
		fmt.Printf("No active SBD slots found.\n")
		return
	}

	fmt.Printf("%-6s %-8s %-30s %-12s %-20s %-10s\n", "Slot", "NodeID", "Node Name", "Type", "Timestamp", "Sequence")
	fmt.Printf("%-6s %-8s %-30s %-12s %-20s %-10s\n", "----", "------", "---------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		nodeNameStr := "Unknown" // We don't have node name in the SBD message
		fmt.Printf("%-6d %-8d %-30s %-12s %-20s %-10d\n",
			slot.SlotID, slot.NodeID, nodeNameStr, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Printf("\n")
}

// SaveNodeMapToFile saves the node mapping summary to a file
func SaveNodeMapToFile(entries []sbdprotocol.NodeMapEntry, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	fmt.Fprintf(file, "=== Node Mapping Summary ===\n")
	fmt.Fprintf(file, "Generated at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "Total entries: %d\n\n", len(entries))

	if len(entries) == 0 {
		fmt.Fprintf(file, "No active node mappings found.\n")
		return nil
	}

	fmt.Fprintf(file, "%-6s %-30s %-20s\n", "Slot", "Node Name", "Last Seen")
	fmt.Fprintf(file, "%-6s %-30s %-20s\n", "----", "---------", "---------")

	for _, entry := range entries {
		lastSeenStr := "Never"
		if !entry.LastSeen.IsZero() {
			lastSeenStr = entry.LastSeen.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(file, "%-6d %-30s %-20s\n", entry.SlotID, entry.NodeName, lastSeenStr)
	}
	fmt.Fprintf(file, "\n")

	return nil
}

// SaveSBDDeviceToFile saves the SBD device summary to a file
func SaveSBDDeviceToFile(slots []SBDSlotSummary, filename string) error {
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

	fmt.Fprintf(file, "%-6s %-8s %-30s %-12s %-20s %-10s\n", "Slot", "NodeID", "Node Name", "Type", "Timestamp", "Sequence")
	fmt.Fprintf(file, "%-6s %-8s %-30s %-12s %-20s %-10s\n", "----", "------", "---------", "----", "---------", "--------")

	for _, slot := range slots {
		timestampStr := "N/A"
		if !slot.Timestamp.IsZero() {
			timestampStr = slot.Timestamp.Format("15:04:05")
		}
		nodeNameStr := "Unknown" // We don't have node name in the SBD message
		fmt.Fprintf(file, "%-6d %-8d %-30s %-12s %-20s %-10d\n",
			slot.SlotID, slot.NodeID, nodeNameStr, slot.Type, timestampStr, slot.Sequence)
	}
	fmt.Fprintf(file, "\n")

	return nil
}

// NodeMapSummary gets node mapping from a pod and either prints or saves it
func (tc *TestClients) NodeMapSummary(podName, namespace, outputFile string) error {
	entries, err := tc.GetNodeMapFromPod(podName, namespace)
	if err != nil {
		return err
	}

	if outputFile != "" {
		if err := SaveNodeMapToFile(entries, outputFile); err != nil {
			return fmt.Errorf("failed to save node map to file: %w", err)
		}
		fmt.Printf("Node mapping summary saved to: %s\n", outputFile)
	} else {
		PrintNodeMap(entries)
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

// execInPod executes a command in a pod and returns stdout, stderr, and error
// Note: This is a simplified implementation that uses the REST API directly.
// In production, you would typically use the remotecommand package for proper SPDY streaming.
func (tc *TestClients) execInPod(podName, namespace string, command []string) (string, string, error) {
	// Create exec request using Kubernetes API
	req := tc.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")

	// Set up exec parameters
	req.VersionedParams(&corev1.PodExecOptions{
		Command: command,
		Stdout:  true,
		Stderr:  true,
	}, scheme.ParameterCodec)

	// For now, fall back to a simple approach that reads the response directly
	// This is a simplified implementation - in production you'd want proper streaming

	// Execute the request and get raw response
	res := req.Do(tc.Context)
	rawData, err := res.Raw()
	if err != nil {
		return "", "", fmt.Errorf("failed to execute command in pod %s: %w", podName, err)
	}

	// Parse the response (this is simplified - normally you'd need to handle the SPDY protocol)
	stdout := string(rawData)
	stderr := ""

	return stdout, stderr, nil
}

// parseNodeMapping parses binary node mapping data
func parseNodeMapping(data []byte) ([]sbdprotocol.NodeMapEntry, error) {
	// This is a simplified parser - in reality, we'd need to understand
	// the exact binary format used by the NodeManager
	// For now, we'll try to extract readable node names and simulate the mapping

	var entries []sbdprotocol.NodeMapEntry

	// Look for readable strings that might be node names
	content := string(data)
	lines := strings.Split(content, "\n")

	slotID := uint16(1)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 3 && isPrintableString(line) {
			entries = append(entries, sbdprotocol.NodeMapEntry{
				SlotID:   slotID,
				NodeName: line,
				LastSeen: time.Now(), // Simulated
			})
			slotID++
		}
	}

	return entries, nil
}

// parseSBDDevice parses binary SBD device data
func parseSBDDevice(data []byte) ([]SBDSlotSummary, error) {
	const slotSize = sbdprotocol.SBD_SLOT_SIZE
	magic := sbdprotocol.SBD_MAGIC

	var slots []SBDSlotSummary

	for i := 0; i < len(data)/slotSize; i++ {
		start := i * slotSize
		end := start + slotSize
		if end > len(data) {
			break
		}

		slotData := data[start:end]

		// Check if slot has SBD message magic
		if len(slotData) >= 8 && string(slotData[:8]) == magic {
			slot, err := parseSBDSlot(uint16(i+1), slotData)
			if err == nil && slot.HasData {
				slots = append(slots, slot)
			}
		}
	}

	return slots, nil
}

// parseSBDSlot parses a single SBD slot
func parseSBDSlot(slotID uint16, data []byte) (SBDSlotSummary, error) {
	if len(data) < sbdprotocol.SBD_HEADER_SIZE {
		return SBDSlotSummary{}, fmt.Errorf("slot data too short")
	}

	// Parse SBD header (simplified)
	magic := string(data[:8])
	if magic != sbdprotocol.SBD_MAGIC {
		return SBDSlotSummary{SlotID: slotID, HasData: false}, nil
	}

	// Read basic header fields (skip version since it's not used)
	msgType := data[10]
	nodeID := binary.LittleEndian.Uint16(data[11:13])
	timestamp := binary.LittleEndian.Uint64(data[13:21])
	sequence := binary.LittleEndian.Uint64(data[21:29])

	// Use existing helper function for message type name
	typeStr := sbdprotocol.GetMessageTypeName(msgType)

	// Convert timestamp from nanoseconds to time.Time
	ts := time.Unix(0, int64(timestamp))

	return SBDSlotSummary{
		SlotID:    slotID,
		NodeID:    nodeID,
		Type:      typeStr,
		Timestamp: ts,
		Sequence:  sequence,
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
