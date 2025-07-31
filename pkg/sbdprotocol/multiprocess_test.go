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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// MultiProcessTestConfig holds configuration for multi-process tests
type MultiProcessTestConfig struct {
	NumNodes           int           `json:"numNodes"`
	TestDuration       time.Duration `json:"testDuration"`
	HeartbeatInterval  time.Duration `json:"heartbeatInterval"`
	SharedDevicePath   string        `json:"sharedDevicePath"`
	FileLockingEnabled bool          `json:"fileLockingEnabled"`
	NodeName           string        `json:"nodeName"`
	NodeID             uint16        `json:"nodeID"`
	ClusterName        string        `json:"clusterName"`
}

// MultiProcessTestResult holds results from a multi-process test
type MultiProcessTestResult struct {
	NodeID               uint16         `json:"nodeID"`
	NodeName             string         `json:"nodeName"`
	HeartbeatsSent       int            `json:"heartbeatsSent"`
	HeartbeatsReceived   map[uint16]int `json:"heartbeatsReceived"`
	CoordinationStrategy string         `json:"coordinationStrategy"`
	FileLockAcquisitions int            `json:"fileLockAcquisitions"`
	FileLockFailures     int            `json:"fileLockFailures"`
	WriteOperations      int            `json:"writeOperations"`
	ReadOperations       int            `json:"readOperations"`
	Errors               []string       `json:"errors"`
	Duration             time.Duration  `json:"duration"`
}

// TestMultiProcessHeartbeatCoordination tests heartbeat coordination across multiple processes
func TestMultiProcessHeartbeatCoordination(t *testing.T) {
	// Check if we're running as a subprocess
	if os.Getenv("SBD_MULTIPROCESS_TEST") == "true" {
		// We're running as a subprocess - run the node process
		config := MultiProcessTestConfig{}
		result, err := runNodeProcess(config)
		if err != nil {
			t.Fatalf("Node process failed: %v", err)
		}
		t.Logf("Node process completed: %+v", result)
		return
	}

	if testing.Short() {
		t.Skip("Skipping multi-process test in short mode")
	}

	tests := []struct {
		name               string
		numNodes           int
		testDuration       time.Duration
		fileLockingEnabled bool
		expectedStrategy   string
	}{
		{
			name:               "3-node cluster with file locking",
			numNodes:           3,
			testDuration:       5 * time.Second,
			fileLockingEnabled: true,
			expectedStrategy:   "file-locking",
		},
		{
			name:               "5-node cluster with jitter coordination",
			numNodes:           5,
			testDuration:       5 * time.Second,
			fileLockingEnabled: false,
			expectedStrategy:   "jitter-only",
		},
		{
			name:               "2-node cluster stress test",
			numNodes:           2,
			testDuration:       10 * time.Second,
			fileLockingEnabled: true,
			expectedStrategy:   "file-locking",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary shared device file
			tmpDir := t.TempDir()
			sharedDevice := filepath.Join(tmpDir, "shared_sbd_device")

			// Initialize shared device with appropriate size
			deviceSize := int64(SBD_SLOT_SIZE * (SBD_MAX_NODES + 1))
			if err := createSharedDevice(sharedDevice, deviceSize); err != nil {
				t.Fatalf("Failed to create shared device: %v", err)
			}

			// Run multi-process test
			results, err := runMultiProcessTest(t, MultiProcessTestConfig{
				NumNodes:           tt.numNodes,
				TestDuration:       tt.testDuration,
				HeartbeatInterval:  500 * time.Millisecond,
				SharedDevicePath:   sharedDevice,
				FileLockingEnabled: tt.fileLockingEnabled,
				ClusterName:        "test-cluster",
			})

			if err != nil {
				t.Fatalf("Multi-process test failed: %v", err)
			}

			// Validate results
			validateMultiProcessResults(t, results, tt.expectedStrategy, tt.numNodes)
		})
	}
}

// TestMultiProcessFileLockingContention tests file locking under contention
func TestMultiProcessFileLockingContention(t *testing.T) {
	// Check if we're running as a subprocess
	if os.Getenv("SBD_MULTIPROCESS_TEST") == "true" {
		// We're running as a subprocess - run the node process
		config := MultiProcessTestConfig{}
		result, err := runNodeProcess(config)
		if err != nil {
			t.Fatalf("Node process failed: %v", err)
		}
		t.Logf("Node process completed: %+v", result)
		return
	}

	if testing.Short() {
		t.Skip("Skipping multi-process test in short mode")
	}

	// Create temporary shared device file
	tmpDir := t.TempDir()
	sharedDevice := filepath.Join(tmpDir, "shared_sbd_device")
	deviceSize := int64(SBD_SLOT_SIZE * (SBD_MAX_NODES + 1))
	if err := createSharedDevice(sharedDevice, deviceSize); err != nil {
		t.Fatalf("Failed to create shared device: %v", err)
	}

	// Test with high contention - many nodes, frequent writes
	results, err := runMultiProcessTest(t, MultiProcessTestConfig{
		NumNodes:           10,
		TestDuration:       8 * time.Second,
		HeartbeatInterval:  100 * time.Millisecond, // High frequency for contention
		SharedDevicePath:   sharedDevice,
		FileLockingEnabled: true,
		ClusterName:        "contention-test",
	})

	if err != nil {
		t.Fatalf("Multi-process contention test failed: %v", err)
	}

	// Validate that file locking worked under contention
	validateFileLockingContention(t, results)
}

// TestMultiProcessMessageProtocol tests SBD message protocol across processes
func TestMultiProcessMessageProtocol(t *testing.T) {
	// Check if we're running as a subprocess
	if os.Getenv("SBD_MULTIPROCESS_TEST") == "true" {
		// We're running as a subprocess - run the node process
		config := MultiProcessTestConfig{}
		result, err := runNodeProcess(config)
		if err != nil {
			t.Fatalf("Node process failed: %v", err)
		}
		t.Logf("Node process completed: %+v", result)
		return
	}

	if testing.Short() {
		t.Skip("Skipping multi-process test in short mode")
	}

	// Create temporary shared device file
	tmpDir := t.TempDir()
	sharedDevice := filepath.Join(tmpDir, "shared_sbd_device")
	deviceSize := int64(SBD_SLOT_SIZE * (SBD_MAX_NODES + 1))
	if err := createSharedDevice(sharedDevice, deviceSize); err != nil {
		t.Fatalf("Failed to create shared device: %v", err)
	}

	// Test message protocol with multiple nodes
	results, err := runMultiProcessTest(t, MultiProcessTestConfig{
		NumNodes:           4,
		TestDuration:       6 * time.Second,
		HeartbeatInterval:  300 * time.Millisecond,
		SharedDevicePath:   sharedDevice,
		FileLockingEnabled: true,
		ClusterName:        "protocol-test",
	})

	if err != nil {
		t.Fatalf("Multi-process protocol test failed: %v", err)
	}

	// Validate message protocol worked correctly
	validateMessageProtocol(t, results, sharedDevice)
}

// TestMultiProcessNodeSlotAssignment tests hash-based node slot assignment
func TestMultiProcessNodeSlotAssignment(t *testing.T) {
	// Check if we're running as a subprocess
	if os.Getenv("SBD_MULTIPROCESS_TEST") == "true" {
		// We're running as a subprocess - run the node process
		config := MultiProcessTestConfig{}
		result, err := runNodeProcess(config)
		if err != nil {
			t.Fatalf("Node process failed: %v", err)
		}
		t.Logf("Node process completed: %+v", result)
		return
	}

	if testing.Short() {
		t.Skip("Skipping multi-process test in short mode")
	}

	// Create temporary shared device file
	tmpDir := t.TempDir()
	sharedDevice := filepath.Join(tmpDir, "shared_sbd_device")
	deviceSize := int64(SBD_SLOT_SIZE * (SBD_MAX_NODES + 1))
	if err := createSharedDevice(sharedDevice, deviceSize); err != nil {
		t.Fatalf("Failed to create shared device: %v", err)
	}

	// Test with nodes that have different names to verify hash-based assignment
	nodeNames := []string{"worker-1", "worker-2", "master-1", "worker-3", "master-2"}
	results := make([]*MultiProcessTestResult, len(nodeNames))

	for i, nodeName := range nodeNames {
		result, err := runSingleNodeTest(t, MultiProcessTestConfig{
			NumNodes:           1,
			TestDuration:       2 * time.Second,
			HeartbeatInterval:  500 * time.Millisecond,
			SharedDevicePath:   sharedDevice,
			FileLockingEnabled: true,
			NodeName:           nodeName,
			ClusterName:        "slot-test",
		})

		if err != nil {
			t.Fatalf("Single node test failed for %s: %v", nodeName, err)
		}

		results[i] = result
	}

	// Validate that different nodes got different slots
	validateSlotAssignment(t, results, nodeNames)
}

// createSharedDevice creates a shared device file for testing
func createSharedDevice(path string, size int64) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create device file: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Write zeros to initialize the device
	zeros := make([]byte, size)
	if _, err := file.Write(zeros); err != nil {
		return fmt.Errorf("failed to initialize device: %w", err)
	}

	return file.Sync()
}

// runMultiProcessTest runs a multi-process test with the given configuration
func runMultiProcessTest(t *testing.T, config MultiProcessTestConfig) ([]*MultiProcessTestResult, error) {
	var wg sync.WaitGroup
	results := make([]*MultiProcessTestResult, config.NumNodes)
	errors := make([]error, config.NumNodes)

	for i := 0; i < config.NumNodes; i++ {
		wg.Add(1)
		go func(nodeIndex int) {
			defer wg.Done()

			nodeConfig := config
			nodeConfig.NodeName = fmt.Sprintf("node-%d", nodeIndex+1)
			nodeConfig.NodeID = uint16(nodeIndex + 1)

			result, err := runSingleNodeTest(t, nodeConfig)
			results[nodeIndex] = result
			errors[nodeIndex] = err
		}(i)
	}

	wg.Wait()

	// Check for errors
	for i, err := range errors {
		if err != nil {
			return nil, fmt.Errorf("node %d failed: %w", i+1, err)
		}
	}

	return results, nil
}

// runSingleNodeTest runs a single node test process
func runSingleNodeTest(t *testing.T, config MultiProcessTestConfig) (*MultiProcessTestResult, error) {
	// Check if we're running as a subprocess
	if os.Getenv("SBD_MULTIPROCESS_TEST") == "true" {
		return runNodeProcess(config)
	}

	// We're the parent process - spawn a subprocess
	return spawnNodeProcess(t, config)
}

// spawnNodeProcess spawns a subprocess to run a node test
func spawnNodeProcess(t *testing.T, config MultiProcessTestConfig) (*MultiProcessTestResult, error) {
	// Create temporary config file
	configData, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, fmt.Sprintf("config-%s.json", config.NodeName))
	if err := ioutil.WriteFile(configFile, configData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write config file: %w", err)
	}

	// Create result file path
	resultFile := filepath.Join(tmpDir, fmt.Sprintf("result-%s.json", config.NodeName))

	// Spawn subprocess
	cmd := exec.Command(os.Args[0], "-test.run="+t.Name())
	cmd.Env = append(os.Environ(),
		"SBD_MULTIPROCESS_TEST=true",
		"SBD_CONFIG_FILE="+configFile,
		"SBD_RESULT_FILE="+resultFile,
	)

	// Start the process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start subprocess: %w", err)
	}

	// Wait for completion with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timeout := config.TestDuration + 30*time.Second // Extra time for setup/teardown
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("subprocess failed: %w", err)
		}
	case <-time.After(timeout):
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("Failed to terminate subprocess: %v", err)
		}
		return nil, fmt.Errorf("subprocess timed out after %v", timeout)
	}

	// Read result file
	resultData, err := ioutil.ReadFile(resultFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read result file: %w", err)
	}

	var result MultiProcessTestResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// runNodeProcess runs the actual node process logic
func runNodeProcess(config MultiProcessTestConfig) (*MultiProcessTestResult, error) {
	// Get config and result file paths from environment
	configFile := os.Getenv("SBD_CONFIG_FILE")
	resultFile := os.Getenv("SBD_RESULT_FILE")

	if configFile != "" {
		// Read config from file
		configData, err := ioutil.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := json.Unmarshal(configData, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	// Create shared device interface
	device, err := NewFileSBDDevice(config.SharedDevicePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open shared device: %w", err)
	}
	defer func() { _ = device.Close() }()

	// Create node manager
	nmConfig := NodeManagerConfig{
		ClusterName:        config.ClusterName,
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   time.Hour,
		Logger:             logr.Discard(),
		FileLockingEnabled: config.FileLockingEnabled,
	}

	nodeManager, err := NewNodeManager(device, nmConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create node manager: %w", err)
	}
	defer func() { _ = nodeManager.Close() }()

	// Get or assign slot for this node
	slotID, err := nodeManager.GetNodeIDForNode(config.NodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get slot for node: %w", err)
	}

	// Initialize result tracking
	result := &MultiProcessTestResult{
		NodeID:               slotID,
		NodeName:             config.NodeName,
		HeartbeatsReceived:   make(map[uint16]int),
		CoordinationStrategy: nodeManager.GetCoordinationStrategy(),
		Errors:               []string{},
	}

	// Run test for specified duration
	ctx, cancel := context.WithTimeout(context.Background(), config.TestDuration)
	defer cancel()

	startTime := time.Now()
	ticker := time.NewTicker(config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(startTime)

			// Write result to file if specified
			if resultFile != "" {
				resultData, err := json.Marshal(result)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal result: %w", err)
				}
				if err := ioutil.WriteFile(resultFile, resultData, 0644); err != nil {
					return nil, fmt.Errorf("failed to write result file: %w", err)
				}
			}

			return result, nil

		case <-ticker.C:
			// Send heartbeat
			err := nodeManager.WriteWithLock("heartbeat", func() error {
				return writeTestHeartbeat(device, slotID, uint64(result.HeartbeatsSent+1))
			})

			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("heartbeat write failed: %v", err))
				result.FileLockFailures++
			} else {
				result.HeartbeatsSent++
				result.WriteOperations++
				result.FileLockAcquisitions++
			}

			// Read peer heartbeats
			for peerSlot := uint16(1); peerSlot <= SBD_MAX_NODES; peerSlot++ {
				if peerSlot == slotID {
					continue
				}

				if readTestHeartbeat(device, peerSlot) {
					result.HeartbeatsReceived[peerSlot]++
					result.ReadOperations++
				}
			}
		}
	}
}

// writeTestHeartbeat writes a test heartbeat to the specified slot
func writeTestHeartbeat(device SBDDevice, nodeID uint16, sequence uint64) error {
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

// readTestHeartbeat reads and validates a heartbeat from the specified slot
func readTestHeartbeat(device SBDDevice, nodeID uint16) bool {
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

// validateMultiProcessResults validates the results from multi-process tests
func validateMultiProcessResults(t *testing.T, results []*MultiProcessTestResult, expectedStrategy string, numNodes int) {
	if len(results) != numNodes {
		t.Errorf("Expected %d results, got %d", numNodes, len(results))
		return
	}

	// Validate each node's results
	for i, result := range results {
		if result == nil {
			t.Errorf("Node %d result is nil", i+1)
			continue
		}

		// Check coordination strategy
		if result.CoordinationStrategy != expectedStrategy {
			t.Errorf("Node %d: expected coordination strategy %s, got %s",
				i+1, expectedStrategy, result.CoordinationStrategy)
		}

		// Check that heartbeats were sent
		if result.HeartbeatsSent == 0 {
			t.Errorf("Node %d: no heartbeats sent", i+1)
		}

		// Check for excessive errors
		if len(result.Errors) > result.HeartbeatsSent/2 {
			t.Errorf("Node %d: too many errors (%d errors, %d heartbeats)",
				i+1, len(result.Errors), result.HeartbeatsSent)
		}

		// Log results for debugging
		t.Logf("Node %d (%s): sent=%d, received=%d peers, strategy=%s, errors=%d",
			i+1, result.NodeName, result.HeartbeatsSent,
			len(result.HeartbeatsReceived), result.CoordinationStrategy, len(result.Errors))
	}

	// Validate cross-node communication
	validateCrossNodeCommunication(t, results)
}

// validateFileLockingContention validates file locking under high contention
func validateFileLockingContention(t *testing.T, results []*MultiProcessTestResult) {
	totalLockAcquisitions := 0
	totalLockFailures := 0

	for i, result := range results {
		if result == nil {
			continue
		}

		totalLockAcquisitions += result.FileLockAcquisitions
		totalLockFailures += result.FileLockFailures

		// Each node should have acquired locks successfully
		if result.FileLockAcquisitions == 0 {
			t.Errorf("Node %d: no successful lock acquisitions", i+1)
		}

		// Failure rate should be reasonable (less than 50%)
		if result.FileLockFailures > result.FileLockAcquisitions {
			t.Errorf("Node %d: excessive lock failures (%d failures, %d successes)",
				i+1, result.FileLockFailures, result.FileLockAcquisitions)
		}
	}

	t.Logf("Total lock acquisitions: %d, failures: %d (%.1f%% failure rate)",
		totalLockAcquisitions, totalLockFailures,
		float64(totalLockFailures)/float64(totalLockAcquisitions+totalLockFailures)*100)
}

// validateMessageProtocol validates SBD message protocol correctness
func validateMessageProtocol(t *testing.T, results []*MultiProcessTestResult, devicePath string) {
	// Open device to read final state
	device, err := NewFileSBDDevice(devicePath)
	if err != nil {
		t.Errorf("Failed to open device for validation: %v", err)
		return
	}
	defer func() { _ = device.Close() }()

	// Check that each node's slot contains valid data
	for _, result := range results {
		if result == nil {
			continue
		}

		slotOffset := int64(result.NodeID) * SBD_SLOT_SIZE
		slotData := make([]byte, SBD_SLOT_SIZE)

		n, err := device.ReadAt(slotData, slotOffset)
		if err != nil || n != SBD_SLOT_SIZE {
			t.Errorf("Failed to read slot %d: %v", result.NodeID, err)
			continue
		}

		header, err := Unmarshal(slotData[:SBD_HEADER_SIZE])
		if err != nil {
			t.Errorf("Failed to unmarshal header from slot %d: %v", result.NodeID, err)
			continue
		}

		// Validate message format
		if header.Type != SBD_MSG_TYPE_HEARTBEAT {
			t.Errorf("Slot %d: expected heartbeat message, got type %d", result.NodeID, header.Type)
		}

		if header.NodeID != result.NodeID {
			t.Errorf("Slot %d: node ID mismatch, expected %d, got %d",
				result.NodeID, result.NodeID, header.NodeID)
		}

		if header.Sequence == 0 {
			t.Errorf("Slot %d: sequence number is 0", result.NodeID)
		}
	}
}

// validateSlotAssignment validates hash-based slot assignment
func validateSlotAssignment(t *testing.T, results []*MultiProcessTestResult, nodeNames []string) {
	usedSlots := make(map[uint16]string)

	for i, result := range results {
		if result == nil {
			continue
		}

		nodeName := nodeNames[i]
		slotID := result.NodeID

		// Check that slot is within valid range
		if slotID < 1 || slotID > SBD_MAX_NODES {
			t.Errorf("Node %s: invalid slot ID %d", nodeName, slotID)
			continue
		}

		// Check for slot conflicts
		if existingNode, exists := usedSlots[slotID]; exists {
			t.Errorf("Slot conflict: both %s and %s assigned to slot %d",
				existingNode, nodeName, slotID)
		} else {
			usedSlots[slotID] = nodeName
		}

		t.Logf("Node %s assigned to slot %d", nodeName, slotID)
	}

	// Verify we have the expected number of unique slots
	if len(usedSlots) != len(nodeNames) {
		t.Errorf("Expected %d unique slots, got %d", len(nodeNames), len(usedSlots))
	}
}

// validateCrossNodeCommunication validates that nodes can see each other's heartbeats
func validateCrossNodeCommunication(t *testing.T, results []*MultiProcessTestResult) {
	// Create a map of node IDs to results for easy lookup
	nodeResults := make(map[uint16]*MultiProcessTestResult)
	for _, result := range results {
		if result != nil {
			nodeResults[result.NodeID] = result
		}
	}

	// Check that nodes received heartbeats from peers
	for nodeID, result := range nodeResults {
		peersDetected := len(result.HeartbeatsReceived)
		expectedPeers := len(nodeResults) - 1 // All nodes except self

		if peersDetected < expectedPeers/2 {
			t.Errorf("Node %d: detected only %d/%d peers", nodeID, peersDetected, expectedPeers)
		}

		// Check for reasonable heartbeat reception
		for peerID, count := range result.HeartbeatsReceived {
			if count == 0 {
				t.Errorf("Node %d: never received heartbeat from peer %d", nodeID, peerID)
			}
		}
	}
}

// FileSBDDevice is a simple file-based SBD device implementation for testing
type FileSBDDevice struct {
	file *os.File
	path string
}

// NewFileSBDDevice creates a new file-based SBD device
func NewFileSBDDevice(path string) (*FileSBDDevice, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	return &FileSBDDevice{
		file: file,
		path: path,
	}, nil
}

func (f *FileSBDDevice) ReadAt(p []byte, off int64) (int, error) {
	return f.file.ReadAt(p, off)
}

func (f *FileSBDDevice) WriteAt(p []byte, off int64) (int, error) {
	return f.file.WriteAt(p, off)
}

func (f *FileSBDDevice) Sync() error {
	return f.file.Sync()
}

func (f *FileSBDDevice) Close() error {
	if f.file != nil {
		return f.file.Close()
	}
	return nil
}

func (f *FileSBDDevice) Path() string {
	return f.path
}
