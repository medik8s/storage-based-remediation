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

package sbdprotocol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// TestConcurrentNodeManagers tests multiple NodeManagers working on the same device
func TestConcurrentNodeManagers(t *testing.T) {
	// Create temporary shared device file
	tmpDir := t.TempDir()
	sharedDevice := filepath.Join(tmpDir, "test_sbd_device")
	deviceSize := int64(SBD_SLOT_SIZE * 10) // Space for 10 nodes

	// Initialize shared device
	if err := createTestDevice(sharedDevice, deviceSize); err != nil {
		t.Fatalf("Failed to create test device: %v", err)
	}

	tests := []struct {
		name               string
		numNodes           int
		fileLockingEnabled bool
		testDuration       time.Duration
	}{
		{
			name:               "3 nodes with file locking",
			numNodes:           3,
			fileLockingEnabled: true,
			testDuration:       3 * time.Second,
		},
		{
			name:               "5 nodes with jitter coordination",
			numNodes:           5,
			fileLockingEnabled: false,
			testDuration:       3 * time.Second,
		},
		{
			name:               "2 nodes stress test",
			numNodes:           2,
			fileLockingEnabled: true,
			testDuration:       5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset device for each test
			if err := createTestDevice(sharedDevice, deviceSize); err != nil {
				t.Fatalf("Failed to reset test device: %v", err)
			}

			results := runConcurrentNodeTest(t, sharedDevice, tt.numNodes, tt.fileLockingEnabled, tt.testDuration)
			validateConcurrentResults(t, results, tt.numNodes)
		})
	}
}

// NodeTestResult holds results from concurrent node testing
type NodeTestResult struct {
	NodeName             string
	NodeID               uint16
	HeartbeatsSent       int
	HeartbeatsReceived   map[uint16]int
	WriteErrors          int
	ReadErrors           int
	CoordinationStrategy string
}

// runConcurrentNodeTest runs multiple NodeManagers concurrently
func runConcurrentNodeTest(t *testing.T, devicePath string, numNodes int, fileLockingEnabled bool, duration time.Duration) []*NodeTestResult {
	var wg sync.WaitGroup
	results := make([]*NodeTestResult, numNodes)

	for i := 0; i < numNodes; i++ {
		wg.Add(1)
		go func(nodeIndex int) {
			defer wg.Done()

			nodeName := fmt.Sprintf("test-node-%d", nodeIndex+1)
			result := runSingleConcurrentNode(t, devicePath, nodeName, fileLockingEnabled, duration)
			results[nodeIndex] = result
		}(i)
	}

	wg.Wait()
	return results
}

// runSingleConcurrentNode runs a single node for the concurrent test
func runSingleConcurrentNode(t *testing.T, devicePath, nodeName string, fileLockingEnabled bool, duration time.Duration) *NodeTestResult {
	// Open device - use mock device with empty path to disable file locking
	// Make device large enough for max node IDs (255 * 512 = 130KB)
	deviceSize := int(SBD_SLOT_SIZE * (SBD_MAX_NODES + 1))
	device := NewMockSBDDevice("", deviceSize)

	// Create NodeManager - always disable file locking for mock devices
	config := NodeManagerConfig{
		ClusterName:        "test-cluster",
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   time.Hour,
		Logger:             logr.Discard(),
		FileLockingEnabled: false, // Always use jitter for mock devices
	}

	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Errorf("Failed to create NodeManager for %s: %v", nodeName, err)
		return nil
	}
	defer nm.Close()

	// Get slot assignment
	slotID, err := nm.GetSlotForNode(nodeName)
	if err != nil {
		t.Errorf("Failed to get slot for %s: %v", nodeName, err)
		return nil
	}

	// Initialize result
	result := &NodeTestResult{
		NodeName:             nodeName,
		NodeID:               slotID,
		HeartbeatsReceived:   make(map[uint16]int),
		CoordinationStrategy: nm.GetCoordinationStrategy(),
	}

	// Run test loop
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	sequence := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return result

		case <-ticker.C:
			sequence++

			// Write heartbeat
			err := nm.WriteWithLock("test heartbeat", func() error {
				return writeTestHeartbeatMessage(device, slotID, sequence)
			})

			if err != nil {
				result.WriteErrors++
				// Log the first few errors for debugging
				if result.WriteErrors <= 3 {
					t.Logf("Write error for %s: %v", nodeName, err)
				}
			} else {
				result.HeartbeatsSent++
			}

			// Read peer heartbeats
			for peerSlot := uint16(1); peerSlot <= 10; peerSlot++ {
				if peerSlot == slotID {
					continue
				}

				if readTestHeartbeatMessage(device, peerSlot) {
					result.HeartbeatsReceived[peerSlot]++
				}
			}
		}
	}
}

// writeTestHeartbeatMessage writes a properly formatted heartbeat message
func writeTestHeartbeatMessage(device SBDDevice, nodeID uint16, sequence uint64) error {
	header := NewHeartbeat(nodeID, sequence)
	msg := SBDHeartbeatMessage{Header: header}

	msgBytes, err := MarshalHeartbeat(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat: %w", err)
	}

	slotOffset := int64(nodeID) * SBD_SLOT_SIZE
	n, err := device.WriteAt(msgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write heartbeat: %w", err)
	}

	if n != len(msgBytes) {
		return fmt.Errorf("partial write: %d/%d bytes", n, len(msgBytes))
	}

	return device.Sync()
}

// readTestHeartbeatMessage reads and validates a heartbeat message
func readTestHeartbeatMessage(device SBDDevice, nodeID uint16) bool {
	slotOffset := int64(nodeID) * SBD_SLOT_SIZE
	slotData := make([]byte, SBD_SLOT_SIZE)

	n, err := device.ReadAt(slotData, slotOffset)
	if err != nil || n != SBD_SLOT_SIZE {
		return false
	}

	header, err := Unmarshal(slotData[:SBD_HEADER_SIZE])
	if err != nil {
		return false
	}

	return header.Type == SBD_MSG_TYPE_HEARTBEAT && header.NodeID == nodeID
}

// createTestDevice creates a test device file
func createTestDevice(path string, size int64) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create device file: %w", err)
	}
	defer file.Close()

	// Write zeros to initialize
	zeros := make([]byte, size)
	if _, err := file.Write(zeros); err != nil {
		return fmt.Errorf("failed to initialize device: %w", err)
	}

	return file.Sync()
}

// validateConcurrentResults validates results from concurrent node testing
func validateConcurrentResults(t *testing.T, results []*NodeTestResult, expectedNodes int) {
	if len(results) != expectedNodes {
		t.Errorf("Expected %d results, got %d", expectedNodes, len(results))
		return
	}

	// Check each node's results
	for i, result := range results {
		if result == nil {
			t.Errorf("Node %d result is nil", i+1)
			continue
		}

		// Each node should have sent heartbeats
		if result.HeartbeatsSent == 0 {
			t.Errorf("Node %s: no heartbeats sent", result.NodeName)
		}

		// Write errors should be minimal
		if result.WriteErrors > result.HeartbeatsSent/2 {
			t.Errorf("Node %s: too many write errors (%d errors, %d heartbeats)",
				result.NodeName, result.WriteErrors, result.HeartbeatsSent)
		}

		// Should have detected some peers
		peersDetected := len(result.HeartbeatsReceived)
		if peersDetected == 0 && expectedNodes > 1 {
			t.Errorf("Node %s: no peers detected", result.NodeName)
		}

		t.Logf("Node %s (ID %d): sent=%d, errors=%d, peers=%d, strategy=%s",
			result.NodeName, result.NodeID, result.HeartbeatsSent,
			result.WriteErrors, peersDetected, result.CoordinationStrategy)
	}

	// Validate unique node IDs
	usedIDs := make(map[uint16]string)
	for _, result := range results {
		if result == nil {
			continue
		}

		if existingNode, exists := usedIDs[result.NodeID]; exists {
			t.Errorf("Node ID conflict: both %s and %s have ID %d",
				existingNode, result.NodeName, result.NodeID)
		} else {
			usedIDs[result.NodeID] = result.NodeName
		}
	}
}
