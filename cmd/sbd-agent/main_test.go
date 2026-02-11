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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/client-go/rest"

	mocks "github.com/medik8s/sbd-operator/pkg/mocks"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	testutils "github.com/medik8s/sbd-operator/test/utils"
)

const (
	// Test constants
	nonExistentWatchdogPath = "/non/existent/watchdog"
)

// createTestSBDAgent creates a test SBD agent with mock devices and temporary SBD files
func createTestSBDAgent(t *testing.T, metricsPort int) (
	*SBDAgent, *mocks.MockWatchdog, *mocks.MockBlockDevice, func()) {
	return createTestSBDAgentWithFileLocking(t, "test-node", metricsPort, true)
}

func createManagerPrefix() string {
	return strconv.Itoa(time.Now().Nanosecond())
}

// createTestSBDAgentWithFileLocking creates a test SBD agent with configurable file locking
func createTestSBDAgentWithFileLocking(t *testing.T, nodeName string, metricsPort int, fileLockingEnabled bool) (
	*SBDAgent, *mocks.MockWatchdog, *mocks.MockBlockDevice, func()) {

	// Create temporary SBD device files (both heartbeat and fence)
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	fencePath := sbdPath + "-fence"

	mockWatchdog := mocks.NewMockWatchdog(tmpDir + "/watchdog")
	mockHeartbeatDevice := mocks.NewMockBlockDevice(sbdPath, 1024*1024)
	mockFenceDevice := mocks.NewMockBlockDevice(fencePath, 1024*1024)

	// Create both heartbeat and fence device files
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD heartbeat device: %v", err)
	}
	if err := os.WriteFile(fencePath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD fence device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, nodeName, "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", metricsPort,
		10*time.Minute, fileLockingEnabled, 2*time.Second,
		testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix())
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}

	// Set mock devices to override the real file-based devices
	agent.setSBDDevices(mockHeartbeatDevice, mockFenceDevice)

	var once sync.Once
	cleanup := func() { once.Do(func() { _ = agent.Stop() }) }
	t.Cleanup(cleanup)

	return agent, mockWatchdog, mockHeartbeatDevice, cleanup
}

// TestWatchdogClosedWhenShutdownSignalReceived verifies that when a shutdown signal
// (SIGTERM) is received, the agent closes the watchdog so the node does not reboot
// on uninstall. See docs/RCA-watchdog-reboot-on-uninstall.md.
//
// This test is expected to FAIL until the bug is fixed: today the main loop blocks
// in Start() and never reads the signal, so Stop() is never called and the watchdog
// is never closed.
//
// This test uses its own agent setup (no t.Cleanup) so the run loop goroutine can
// stay blocked without triggering double Stop() or panics; only this test is affected.
func TestWatchdogClosedWhenShutdownSignalReceived(t *testing.T) {
	// Inline agent creation for this test only: same as createTestSBDAgentWithFileLocking
	// but without t.Cleanup(cleanup), so we do not call Stop() on test exit.
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	fencePath := sbdPath + "-fence"
	mockWatchdog := mocks.NewMockWatchdog(tmpDir + "/watchdog")
	mockHeartbeatDevice := mocks.NewMockBlockDevice(sbdPath, 1024*1024)
	mockFenceDevice := mocks.NewMockBlockDevice(fencePath, 1024*1024)
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD heartbeat device: %v", err)
	}
	if err := os.WriteFile(fencePath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD fence device: %v", err)
	}
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 555,
		10*time.Minute, true, 2*time.Second,
		testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix())
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	agent.setSBDDevices(mockHeartbeatDevice, mockFenceDevice)

	sigChan := make(chan os.Signal, 1)
	runLoopStarted := make(chan struct{})
	// Same run loop as main(): Start() then <-sigChan then Stop().
	// With current code, Start() blocks forever (context not cancelled on signal),
	// so the signal is never read and Stop() is never called.
	go func() {
		close(runLoopStarted)
		_ = agent.RunUntilShutdown(sigChan)
		<-sigChan
		_ = agent.Stop()
	}()

	<-runLoopStarted
	time.Sleep(500 * time.Millisecond)

	sigChan <- syscall.SIGTERM
	time.Sleep(2 * time.Second)

	if !mockWatchdog.IsClosed() {
		t.Error("watchdog was not closed after shutdown signal; node would reboot on uninstall (see docs/RCA-watchdog-reboot-on-uninstall.md)")
	}
}

// TestPeerMonitor tests the peer monitoring functionality
func TestPeerMonitor(t *testing.T) {
	logger := logr.Discard()
	monitor := NewPeerMonitor(30, 1, nil, logger)

	// Initially no peers
	if count := monitor.GetHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers initially, got %d", count)
	}

	// Update a peer
	monitor.UpdatePeer(2, 1000, 1)

	// Should have one healthy peer
	if count := monitor.GetHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer, got %d", count)
	}

	// Get peer status
	peers := monitor.GetPeerStatus()
	if len(peers) != 1 {
		t.Errorf("Expected 1 peer in status map, got %d", len(peers))
	}

	peer, exists := peers[2]
	if !exists {
		t.Error("Expected peer 2 to exist in status map")
	}

	if peer.NodeID != 2 {
		t.Errorf("Expected peer NodeID 2, got %d", peer.NodeID)
	}

	if peer.LastTimestamp != 1000 {
		t.Errorf("Expected peer timestamp 1000, got %d", peer.LastTimestamp)
	}

	if peer.LastSequence != 1 {
		t.Errorf("Expected peer sequence 1, got %d", peer.LastSequence)
	}

	if !peer.IsHealthy {
		t.Error("Expected peer to be healthy")
	}
}

func TestPeerMonitor_Liveness(t *testing.T) {
	logger := logr.Discard()
	monitor := NewPeerMonitor(1, 1, nil, logger) // 1 second timeout

	// Update a peer
	monitor.UpdatePeer(2, 1000, 1)

	// Should be healthy initially
	if count := monitor.GetHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer initially, got %d", count)
	}

	// Wait for timeout
	time.Sleep(1100 * time.Millisecond)

	// Check liveness
	monitor.CheckPeerLiveness()

	// Should now be unhealthy
	if count := monitor.GetHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers after timeout, got %d", count)
	}

	// Update peer again
	monitor.UpdatePeer(2, 2000, 2)

	// Should be healthy again
	if count := monitor.GetHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer after update, got %d", count)
	}
}

func TestPeerMonitor_SequenceValidation(t *testing.T) {
	logger := logr.Discard()
	monitor := NewPeerMonitor(30, 1, nil, logger)

	// Update a peer with sequence 5
	monitor.UpdatePeer(2, 1000, 5)

	peers := monitor.GetPeerStatus()
	peer := peers[2]
	if peer.LastSequence != 5 {
		t.Errorf("Expected sequence 5, got %d", peer.LastSequence)
	}

	// Update with older sequence (should be ignored)
	monitor.UpdatePeer(2, 1000, 3)

	peers = monitor.GetPeerStatus()
	peer = peers[2]
	if peer.LastSequence != 5 {
		t.Errorf("Expected sequence to remain 5, got %d", peer.LastSequence)
	}

	// Update with newer sequence (should be accepted)
	monitor.UpdatePeer(2, 1000, 7)

	peers = monitor.GetPeerStatus()
	peer = peers[2]
	if peer.LastSequence != 7 {
		t.Errorf("Expected sequence 7, got %d", peer.LastSequence)
	}
}

func TestSBDAgent_ReadPeerHeartbeat(t *testing.T) {

	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Initially, should return no error for empty slot
	err := agent.readPeerHeartbeat(2)
	if err != nil {
		t.Errorf("Expected no error for empty slot, got: %v", err)
	}

	// Write a heartbeat message from peer node 2
	timestamp := uint64(time.Now().UnixNano())
	sequence := uint64(100)
	err = mockDevice.WritePeerHeartbeat(2, timestamp, sequence)
	if err != nil {
		t.Fatalf("Failed to write peer heartbeat: %v", err)
	}

	// Now reading should succeed and update peer status
	err = agent.readPeerHeartbeat(2)
	if err != nil {
		t.Errorf("Failed to read peer heartbeat: %v", err)
	}

	// Check that peer was updated
	peers := agent.peerMonitor.GetPeerStatus()
	if len(peers) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(peers))
	}

	peer, exists := peers[2]
	if !exists {
		t.Error("Expected peer 2 to exist")
	} else {
		if peer.LastTimestamp != timestamp {
			t.Errorf("Expected timestamp %d, got %d", timestamp, peer.LastTimestamp)
		}
		if peer.LastSequence != sequence {
			t.Errorf("Expected sequence %d, got %d", sequence, peer.LastSequence)
		}
	}
}

func TestSBDAgent_ReadPeerHeartbeat_InvalidMessage(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Write invalid data to peer slot
	invalidData := []byte("invalid message data")
	slotOffset := int64(2) * sbdprotocol.SBD_SLOT_SIZE
	_, err := mockDevice.WriteAt(invalidData, slotOffset)
	if err != nil {
		t.Fatalf("Failed to write invalid data: %v", err)
	}

	// Should handle invalid data gracefully
	err = agent.readPeerHeartbeat(2)
	if err != nil {
		t.Errorf("Expected no error for invalid data, got: %v", err)
	}

	// Should not have created a peer entry
	peers := agent.peerMonitor.GetPeerStatus()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers, got %d", len(peers))
	}
}

func TestSBDAgent_ReadPeerHeartbeat_DeviceError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Configure device to fail reads
	mockDevice.SetFailRead(true)

	// Should return error when device read fails
	err := agent.readPeerHeartbeat(2)
	if err == nil {
		t.Error("Expected error when device read fails")
	}
}

func TestSBDAgent_ReadPeerHeartbeat_NodeIDMismatch(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Write a heartbeat message from node 5 in node 2's slot (mismatch)
	timestamp := uint64(time.Now().UnixNano())
	sequence := uint64(100)

	// Create heartbeat with wrong node ID
	header := sbdprotocol.NewHeartbeat(5, sequence) // Node 5's message
	header.Timestamp = timestamp
	heartbeatMsg := sbdprotocol.SBDHeartbeatMessage{Header: header}
	msgBytes, err := sbdprotocol.MarshalHeartbeat(heartbeatMsg)
	if err != nil {
		t.Fatalf("Failed to marshal heartbeat: %v", err)
	}

	// Write to node 2's slot
	slotOffset := int64(2) * sbdprotocol.SBD_SLOT_SIZE
	_, err = mockDevice.WriteAt(msgBytes, slotOffset)
	if err != nil {
		t.Fatalf("Failed to write heartbeat: %v", err)
	}

	// Should handle node ID mismatch gracefully
	err = agent.readPeerHeartbeat(2)
	if err != nil {
		t.Errorf("Expected no error for node ID mismatch, got: %v", err)
	}

	// Should not have created a peer entry due to mismatch
	peers := agent.peerMonitor.GetPeerStatus()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers due to node ID mismatch, got %d", len(peers))
	}
}

func TestSBDAgent_PeerMonitorLoop_Integration(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// reduce default setting in order to speed the test
	agent.peerMonitor.sbdTimeoutSeconds = 2

	// Write heartbeats for multiple peers
	err := mockDevice.WritePeerHeartbeat(2, 12345, 1)
	if err != nil {
		t.Fatalf("Failed to write peer 2 heartbeat: %v", err)
	}

	err = mockDevice.WritePeerHeartbeat(3, 12346, 1)
	if err != nil {
		t.Fatalf("Failed to write peer 3 heartbeat: %v", err)
	}

	// Start the peer monitor loop
	go agent.peerMonitorLoop()

	// Wait for a few check cycles
	time.Sleep(agent.peerCheckInterval * 2)

	// Check that peers were discovered
	peers := agent.peerMonitor.GetPeerStatus()
	if len(peers) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(peers))
	}

	// Refresh the heartbeats
	err = mockDevice.WritePeerHeartbeat(2, 12345, 1)
	if err != nil {
		t.Fatalf("Failed to write peer 2 heartbeat: %v", err)
	}

	err = mockDevice.WritePeerHeartbeat(3, 12346, 1)
	if err != nil {
		t.Fatalf("Failed to write peer 3 heartbeat: %v", err)
	}

	// Check healthy peer count
	if count := agent.peerMonitor.GetHealthyPeerCount(); count != 2 {
		for peerID, peer := range peers {
			t.Errorf("Peer %d: %+v", peerID, peer)
		}
		t.Errorf("Expected 2 healthy peers, got %d", count)
	}

	// Wait for peers to become unhealthy (1 second timeout + check interval)
	heartbeatInterval := time.Duration(agent.peerMonitor.sbdTimeoutSeconds) / 2 * time.Second
	timeout := heartbeatInterval * MaxConsecutiveFailures
	time.Sleep(timeout + time.Second)

	// Should now have 0 healthy peers
	if count := agent.peerMonitor.GetHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers after %vs timeout, got %d", agent.peerMonitor.sbdTimeoutSeconds, count)
		for peerID, peer := range peers {
			t.Errorf("Peer %d: %+v", peerID, peer)
		}
	}
}

func TestSBDAgent_NewSBDAgent(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Verify configuration
	if agent.nodeName != "test-node" {
		t.Errorf("Expected node name 'test-node', got '%s'", agent.nodeName)
	}
	// NodeID is now assigned by hash-based NodeManager, so verify it's within valid range
	if agent.nodeID < 1 || agent.nodeID > 255 {
		t.Errorf("Expected node ID in range [1, 255], got %d", agent.nodeID)
	}
	if agent.petInterval != 1*time.Second {
		t.Errorf("Expected pet interval 1s, got %v", agent.petInterval)
	}

	// Test invalid configurations
	invalidWatchdog := mocks.NewMockWatchdog("")
	_, err := NewSBDAgentWithWatchdog(invalidWatchdog, "/dev/invalid-sbd", "", "test-cluster", 0, 0, 0, 0, 0, 0,
		"invalid", 8087, 10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(t),
		&rest.Config{}, createManagerPrefix())
	if err == nil {
		t.Error("Expected error for invalid configuration")
	}
}

func TestSBDAgent_WriteHeartbeatToSBD(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()
	// Write heartbeat
	err := agent.writeHeartbeatToSBD()
	if err != nil {
		t.Fatalf("Failed to write heartbeat: %v", err)
	}

	// Verify the heartbeat was written to the correct slot
	// Use the actual assigned nodeID from the agent (not hardcoded 5)
	slotOffset := int64(agent.nodeID) * sbdprotocol.SBD_SLOT_SIZE
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	n, err := mockDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		t.Fatalf("Failed to read slot data: %v", err)
	}
	if n != sbdprotocol.SBD_SLOT_SIZE {
		t.Fatalf("Expected to read %d bytes, got %d", sbdprotocol.SBD_SLOT_SIZE, n)
	}

	// Unmarshal and verify the message
	header, err := sbdprotocol.Unmarshal(slotData[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		t.Fatalf("Failed to unmarshal heartbeat header: %v", err)
	}

	if header.NodeID != agent.nodeID {
		t.Errorf("Expected node ID %d, got %d", agent.nodeID, header.NodeID)
	}
	if header.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Expected heartbeat message type, got %d", header.Type)
	}
	if header.Sequence != 1 {
		t.Errorf("Expected sequence 1, got %d", header.Sequence)
	}
}

func TestSBDAgent_WriteHeartbeatToSBD_DeviceError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8089)
	defer cleanup()

	// Configure device to fail writes
	mockDevice.SetFailWrite(true)

	// Should return error when device write fails
	err := agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected error when device write fails")
	}
}

func TestSBDAgent_WriteHeartbeatToSBD_SyncError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBDAgent(t, 8090)
	defer cleanup()

	// Configure device to fail sync
	mockDevice.SetFailSync(true)

	// Should return error when device sync fails
	err := agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected error when device sync fails")
	}
}

func TestSBDAgent_SBDHealthStatus(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8091)
	defer cleanup()

	// Initially should be false
	if agent.isSBDHealthy() {
		t.Error("Expected SBD to be initially unhealthy")
	}

	// Set healthy
	agent.setSBDHealthy(true)
	if !agent.isSBDHealthy() {
		t.Error("Expected SBD to be healthy after setting")
	}

	// Set unhealthy
	agent.setSBDHealthy(false)
	if agent.isSBDHealthy() {
		t.Error("Expected SBD to be unhealthy after setting")
	}
}

func TestSBDAgent_HeartbeatSequence(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Get initial sequence numbers
	seq1 := agent.getNextHeartbeatSequence()
	seq2 := agent.getNextHeartbeatSequence()
	seq3 := agent.getNextHeartbeatSequence()

	// Should increment
	if seq1 != 1 {
		t.Errorf("Expected first sequence to be 1, got %d", seq1)
	}
	if seq2 != 2 {
		t.Errorf("Expected second sequence to be 2, got %d", seq2)
	}
	if seq3 != 3 {
		t.Errorf("Expected third sequence to be 3, got %d", seq3)
	}
}

func TestEnvironmentVariables(t *testing.T) {
	// Test getNodeNameFromEnv
	t.Run("getNodeNameFromEnv", func(t *testing.T) {
		// Clear environment
		_ = os.Unsetenv("NODE_NAME")
		_ = os.Unsetenv("HOSTNAME")
		_ = os.Unsetenv("NODENAME")

		// Set NODE_NAME
		_ = os.Setenv("NODE_NAME", "test-env-node")
		defer func() { _ = os.Unsetenv("NODE_NAME") }()

		nodeName := getNodeNameFromEnv()
		if nodeName != "test-env-node" {
			t.Errorf("Expected 'test-env-node', got '%s'", nodeName)
		}
	})

	// Test getNodeIDFromEnv
	t.Run("getNodeIDFromEnv", func(t *testing.T) {
		// Clear environment
		_ = os.Unsetenv("SBD_NODE_ID")
		_ = os.Unsetenv("NODE_ID")

		// Set SBD_NODE_ID
		_ = os.Setenv("SBD_NODE_ID", "5")
		defer func() { _ = os.Unsetenv("SBD_NODE_ID") }()

		nodeID := getNodeIDFromEnv()
		if nodeID != 5 {
			t.Errorf("Expected 5, got %d", nodeID)
		}
	})

	// Test getSBDTimeoutFromEnv
	t.Run("getSBDTimeoutFromEnv", func(t *testing.T) {
		// Clear environment
		_ = os.Unsetenv("SBD_TIMEOUT_SECONDS")
		_ = os.Unsetenv("SBD_TIMEOUT")

		// Set SBD_TIMEOUT_SECONDS
		_ = os.Setenv("SBD_TIMEOUT_SECONDS", "60")
		defer func() { _ = os.Unsetenv("SBD_TIMEOUT_SECONDS") }()

		timeout := getSBDTimeoutFromEnv()
		if timeout != 60 {
			t.Errorf("Expected 60, got %d", timeout)
		}
	})
}

// Benchmark tests
func BenchmarkSBDAgent_WriteHeartbeat(b *testing.B) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")
	mockDevice := mocks.NewMockBlockDevice("/dev/mock-sbd", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	// Create temporary SBD device file
	tmpDir := b.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		b.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080,
		10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(b), &rest.Config{}, createManagerPrefix())
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	agent.setSBDDevices(mockDevice, mockDevice)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := agent.writeHeartbeatToSBD(); err != nil {
			b.Fatalf("Failed to write heartbeat: %v", err)
		}
	}
}

func BenchmarkSBDAgent_ReadPeerHeartbeat(b *testing.B) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")
	mockDevice := mocks.NewMockBlockDevice("/dev/mock-sbd", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	// Create temporary SBD device file
	tmpDir := b.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		b.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080,
		10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(b), &rest.Config{}, createManagerPrefix())
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	agent.setSBDDevices(mockDevice, mockDevice)

	// Write a peer heartbeat
	err = mockDevice.WritePeerHeartbeat(2, 12345, 1)
	if err != nil {
		b.Fatalf("Failed to write peer heartbeat: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := agent.readPeerHeartbeat(2); err != nil {
			b.Fatalf("Failed to read peer heartbeat: %v", err)
		}
	}
}

func BenchmarkPeerMonitor_UpdatePeer(b *testing.B) {
	logger := logr.Discard()
	monitor := NewPeerMonitor(30, 1, nil, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.UpdatePeer(2, uint64(i), uint64(i))
	}
}

func TestSBDAgent_ReadOwnSlotForFenceMessage(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Get the fence device (different from heartbeat device)
	mockFenceDevice := agent.fenceDevice.(*mocks.MockBlockDevice)

	// Initially, no fence message should be found
	err := agent.readOwnSlotForFenceMessage()
	if err != nil {
		t.Errorf("Expected no error for empty slot, got: %v", err)
	}

	// Write a fence message targeting this node to the FENCE device
	// Use the actual assigned nodeID from the agent (not hardcoded 3)
	err = mockFenceDevice.WriteFenceMessage(2, agent.nodeID, 100, sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT)
	if err != nil {
		t.Fatalf("Failed to write fence message: %v", err)
	}

	// Reading own slot should detect fence message and trigger self-fence
	// We expect this to panic, so we'll catch it
	defer func() {
		if r := recover(); r != nil {
			// Expected panic due to self-fencing
			if !strings.Contains(fmt.Sprintf("%v", r), "Self-fencing:") {
				t.Errorf("Expected self-fencing panic, got: %v", r)
			}
		} else {
			t.Error("Expected panic due to self-fencing, but no panic occurred")
		}
	}()

	// This should trigger self-fencing and panic
	_ = agent.readOwnSlotForFenceMessage()
	// Should not reach here due to panic
	t.Error("Expected function to panic due to self-fencing")
}

func TestSBDAgent_ReadOwnSlotForFenceMessage_WrongTarget(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Get the fence device (different from heartbeat device)
	mockFenceDevice := agent.fenceDevice.(*mocks.MockBlockDevice)

	// Write a fence message targeting a different node to the fence device
	err := mockFenceDevice.WriteFenceMessage(2, 5, 100, sbdprotocol.FENCE_REASON_MANUAL)
	if err != nil {
		t.Fatalf("Failed to write fence message: %v", err)
	}

	// Reading own slot should not trigger self-fence (wrong target)
	err = agent.readOwnSlotForFenceMessage()
	if err != nil {
		t.Errorf("Expected no error for fence message targeting different node, got: %v", err)
	}

	// Verify self-fence was not triggered
	if agent.isSelfFenceDetected() {
		t.Error("Expected self-fence not to be triggered for wrong target")
	}
}

func TestSBDAgent_SelfFenceStatus(t *testing.T) {
	agent, _, _, cleanup := createTestSBDAgent(t, 8095)
	defer cleanup()

	// Initially should not be self-fenced
	if agent.isSelfFenceDetected() {
		t.Error("Expected self-fence to be initially false")
	}

	// Set self-fence detected
	agent.setSelfFenceDetected(true)
	if !agent.isSelfFenceDetected() {
		t.Error("Expected self-fence to be true after setting")
	}

	// Reset self-fence
	agent.setSelfFenceDetected(false)
	if agent.isSelfFenceDetected() {
		t.Error("Expected self-fence to be false after resetting")
	}
}

func TestSBDAgent_WatchdogLoop_WithSelfFence(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create mock watchdog and SBD device
	agent, mockWatchdog, _, cleanup := createTestSBDAgent(t, 8081)
	defer cleanup()

	// Set self-fence detected
	agent.setSelfFenceDetected(true)

	// Start the watchdog loop in a goroutine
	go agent.watchdogLoop()

	// Wait a bit to ensure the loop starts
	time.Sleep(100 * time.Millisecond)

	// Stop the agent
	agent.cancel()

	// Wait a bit for the loop to stop
	time.Sleep(100 * time.Millisecond)

	// Verify watchdog was not pet when self-fence is detected
	if mockWatchdog.GetPetCount() > 0 {
		t.Errorf("Expected watchdog not to be pet when self-fence detected, but it was pet %d times",
			mockWatchdog.GetPetCount())
	}
}

// TestPreflightChecks_Success tests successful pre-flight checks
func TestPreflightChecks_Success(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary files for testing
	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	sbdPath := filepath.Join(tmpDir, "sbd")

	// Create mock watchdog file
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	// Create mock SBD device file with sufficient size
	sbdFile, err := os.Create(sbdPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBD file: %v", err)
	}
	// Write enough data for SBD slots
	data := make([]byte, 1024*1024) // 1MB
	_, _ = sbdFile.Write(data)
	_ = sbdFile.Close()

	// Test successful pre-flight checks
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1)
	if err != nil {
		t.Errorf("Expected pre-flight checks to succeed, but got error: %v", err)
	}
}

// TestPreflightChecks_WatchdogMissing tests pre-flight checks with missing watchdog device
func TestPreflightChecks_WatchdogMissing(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Use non-existent watchdog path
	watchdogPath := nonExistentWatchdogPath

	// Test pre-flight checks with missing watchdog device and no SBD device
	// This should fail because SBD device is always required now
	err := runPreflightChecks(watchdogPath, "", "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with empty SBD device path, but they succeeded")
		return
	}

	// Should mention that SBD device path cannot be empty
	if !strings.Contains(err.Error(), "SBD device path cannot be empty") {
		t.Errorf("Expected error about empty SBD device path, but got: %v", err)
	}
}

// TestPreflightChecks_SBDMissing tests pre-flight checks with missing SBD device
func TestPreflightChecks_SBDMissing(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary watchdog file
	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	// Use non-existent SBD path
	sbdPath := "/non/existent/sbd"

	// Test pre-flight checks with missing SBD device but working watchdog
	// This should now PASS because watchdog is available (either/or logic)
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1)
	if err == nil {
		t.Errorf("Expected pre-flight checks to fail with working watchdog and missing SBD device")
	}
}

// TestPreflightChecks_WatchdogOnlyMode tests pre-flight checks in watchdog-only mode
func TestPreflightChecks_RequireSBDDevice(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary watchdog file
	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	// Empty SBD path should now fail (no more watchdog-only mode)
	sbdPath := ""

	// Test pre-flight checks with empty SBD path should fail
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with empty SBD path, but they succeeded")
	}

	if !strings.Contains(err.Error(), "SBD device path cannot be empty") {
		t.Errorf("Expected error about empty SBD device path, but got: %v", err)
	}
}

// TestPreflightChecks_InvalidNodeName tests pre-flight checks with invalid node names
func TestPreflightChecks_InvalidNodeName(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary watchdog file
	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	testCases := []struct {
		name     string
		nodeName string
		nodeID   uint16
		errorMsg string
	}{
		{
			name:     "empty node name",
			nodeName: "",
			nodeID:   1,
			errorMsg: "node name is empty",
		},
		{
			name:     "node name too long",
			nodeName: strings.Repeat("a", MaxNodeNameLength+1),
			nodeID:   1,
			errorMsg: "node name too long",
		},
		{
			name:     "node name with control characters",
			nodeName: "test\x00node",
			nodeID:   1,
			errorMsg: "invalid character",
		},
		{
			name:     "invalid node ID zero",
			nodeName: "test-node",
			nodeID:   0,
			errorMsg: "out of valid range",
		},
		{
			name:     "invalid node ID too high",
			nodeName: "test-node",
			nodeID:   256, // Assuming SBD_MAX_NODES is 255
			errorMsg: "out of valid range",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := runPreflightChecks(watchdogPath, "", tc.nodeName, tc.nodeID)
			if err == nil {
				t.Errorf("Expected pre-flight checks to fail for %s, but they succeeded", tc.name)
				return
			}

			if !strings.Contains(err.Error(), tc.errorMsg) {
				t.Errorf("Expected error containing '%s', but got: %v", tc.errorMsg, err)
			}
		})
	}
}

// TestCheckWatchdogDevice tests the watchdog device check function
func TestCheckWatchdogDevice(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Test with existing file
	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	err = checkWatchdogDevice(watchdogPath)
	if err != nil {
		t.Errorf("Expected watchdog device check to succeed, but got error: %v", err)
	}

	// Test with non-existent file
	nonExistentPath := filepath.Join(tmpDir, "non-existent")
	err = checkWatchdogDevice(nonExistentPath)
	if err == nil {
		t.Error("Expected watchdog device check to fail with non-existent file, but it succeeded")
	}

	// With softdog fallback, the error message will now include information about failed softdog loading
	// The exact error depends on system capabilities and whether softdog can be loaded
	expectedErrorSubstrings := []string{
		"watchdog device pre-flight check failed", // Main error type
		// Could be any of these depending on system state:
		// - "failed to load softdog module" (if modprobe fails)
		// - "does not exist" (if running in environment that doesn't try softdog)
		// - Other softdog-related errors
	}

	errorContainsExpected := false
	for _, substr := range expectedErrorSubstrings {
		if strings.Contains(err.Error(), substr) {
			errorContainsExpected = true
			break
		}
	}

	if !errorContainsExpected {
		t.Errorf("Expected error to contain one of %v, but got: %v", expectedErrorSubstrings, err)
	}
}

// TestCheckNodeIDNameResolution tests the node ID/name resolution check function
func TestCheckNodeIDNameResolution(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Test valid node name and ID
	err := checkNodeIDNameResolution("test-node", 1)
	if err != nil {
		t.Errorf("Expected node ID/name resolution to succeed, but got error: %v", err)
	}

	// Test empty node name
	err = checkNodeIDNameResolution("", 1)
	if err == nil {
		t.Error("Expected node ID/name resolution to fail with empty name, but it succeeded")
	}

	// Test node name too long
	longName := strings.Repeat("a", MaxNodeNameLength+1)
	err = checkNodeIDNameResolution(longName, 1)
	if err == nil {
		t.Error("Expected node ID/name resolution to fail with long name, but it succeeded")
	}

	// Test invalid node ID
	err = checkNodeIDNameResolution("test-node", 0)
	if err == nil {
		t.Error("Expected node ID/name resolution to fail with invalid node ID, but it succeeded")
	}
}

// TestPerformSBDReadWriteTest tests the SBD device read/write test function
func TestPerformSBDReadWriteTest(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Test with working mock device
	mockDevice := mocks.NewMockBlockDevice("/dev/sbd", 1024*1024) // 1MB device
	err := performSBDReadWriteTest(mockDevice, 1, "test-node")
	if err != nil {
		t.Errorf("Expected SBD read/write test to succeed, but got error: %v", err)
	}

	// Test with device that fails writes
	mockDevice.SetFailWrite(true)
	err = performSBDReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBD read/write test to fail with write failure, but it succeeded")
	}

	// Reset and test with device that fails reads
	mockDevice.SetFailWrite(false)
	mockDevice.SetFailRead(true)
	err = performSBDReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBD read/write test to fail with read failure, but it succeeded")
	}

	// Reset and test with device that fails sync
	mockDevice.SetFailRead(false)
	mockDevice.SetFailSync(true)
	err = performSBDReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBD read/write test to fail with sync failure, but it succeeded")
	}
}

func TestValidateWatchdogTiming(t *testing.T) {
	// Initialize logger for tests
	logger = logr.Discard()

	tests := []struct {
		name        string
		petInterval time.Duration
		wantErr     bool
		errContains string
	}{
		{
			name:        "valid pet interval - 15s (4x safety margin)",
			petInterval: 15 * time.Second,
			wantErr:     false,
		},
		{
			name:        "valid pet interval - 20s (3x safety margin, exactly at limit)",
			petInterval: 20 * time.Second,
			wantErr:     false,
		},
		{
			name:        "valid pet interval - 10s (6x safety margin)",
			petInterval: 10 * time.Second,
			wantErr:     false,
		},
		{
			name:        "valid pet interval - 1s (minimum allowed)",
			petInterval: 1 * time.Second,
			wantErr:     false,
		},
		{
			name:        "invalid pet interval - too long (21s)",
			petInterval: 21 * time.Second,
			wantErr:     true,
			errContains: "too close to watchdog timeout",
		},
		{
			name:        "invalid pet interval - too long (25s)",
			petInterval: 25 * time.Second,
			wantErr:     true,
			errContains: "too close to watchdog timeout",
		},
		{
			name:        "invalid pet interval - exactly at watchdog timeout (60s)",
			petInterval: 60 * time.Second,
			wantErr:     true,
			errContains: "too close to watchdog timeout",
		},
		{
			name:        "invalid pet interval - longer than watchdog timeout (90s)",
			petInterval: 90 * time.Second,
			wantErr:     true,
			errContains: "too close to watchdog timeout",
		},
		{
			name:        "invalid pet interval - too short (500ms)",
			petInterval: 500 * time.Millisecond,
			wantErr:     true,
			errContains: "very short",
		},
		{
			name:        "invalid pet interval - too short (100ms)",
			petInterval: 100 * time.Millisecond,
			wantErr:     true,
			errContains: "very short",
		},
		{
			name:        "invalid pet interval - zero",
			petInterval: 0,
			wantErr:     true,
			errContains: "very short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use 60 second watchdog timeout (the hardcoded value the tests were based on)
			watchdogTimeout := 60 * time.Second
			valid, warning := validateWatchdogTiming(tt.petInterval, watchdogTimeout)

			if tt.wantErr {
				if valid {
					t.Errorf("validateWatchdogTiming() expected error but got none")
					return
				}
				if tt.errContains != "" && !contains(warning, tt.errContains) {
					t.Errorf("validateWatchdogTiming() warning = %v, want warning containing %q", warning, tt.errContains)
				}
			} else {
				if !valid {
					t.Errorf("validateWatchdogTiming() unexpected warning = %v", warning)
				}
			}
		})
	}
}

// contains checks if a string contains a substring (helper function)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > len(substr) && containsSubstring(s, substr)))
}

// containsSubstring is a simple substring search
func containsSubstring(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSBDAgent_FileLockingConfiguration(t *testing.T) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")

	// Test with file locking enabled
	t.Run("FileLockingEnabled", func(t *testing.T) {

		agent, _, _, cleanup := createTestSBDAgent(t, 8081)
		defer cleanup()

		// Verify file locking is enabled via NodeManager
		if agent.nodeManager == nil {
			t.Error("Expected NodeManager to be initialized")
		} else if !agent.nodeManager.IsFileLockingEnabled() {
			t.Error("Expected file locking to be enabled")
		}

		// Verify coordination strategy
		if agent.nodeManager != nil {
			strategy := agent.nodeManager.GetCoordinationStrategy()
			if strategy != "file-locking" && strategy != "jitter-fallback" {
				t.Errorf("Expected file-locking or jitter-fallback strategy, got: %s", strategy)
			}
		}
	})

	// Test with file locking disabled
	t.Run("FileLockingDisabled", func(t *testing.T) {
		agent, _, _, cleanup := createTestSBDAgentWithFileLocking(t, "test-node", 8082, false)
		defer cleanup()

		// Verify file locking is disabled via NodeManager
		if agent.nodeManager == nil {
			t.Error("Expected NodeManager to be initialized")
		} else if agent.nodeManager.IsFileLockingEnabled() {
			t.Error("Expected file locking to be disabled")
		}

		// Verify coordination strategy
		if agent.nodeManager != nil {
			strategy := agent.nodeManager.GetCoordinationStrategy()
			if strategy != "jitter-only" {
				t.Errorf("Expected jitter-only strategy, got: %s", strategy)
			}
		}
	})

	// Test SBD device is always required (no more watchdog-only mode)
	t.Run("SBDDeviceRequired", func(t *testing.T) {
		// Try to create agent with empty SBD device path should fail
		_, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
			1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8202, 10*time.Minute, false,
			2*time.Second, testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix())
		if err == nil {
			t.Error("Expected error when creating agent with empty SBD device path")
		}

		if !strings.Contains(err.Error(), "heartbeat device path cannot be empty") {
			t.Errorf("Expected error about empty heartbeat device path, but got: %v", err)
		}
	})
}

// TestPreflightChecks_SBDOnlyMode tests pre-flight checks with working SBD device but failing watchdog
func TestPreflightChecks_SBDOnlyMode(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary SBD device file with sufficient size
	tmpDir := t.TempDir()
	sbdPath := filepath.Join(tmpDir, "sbd")
	sbdFile, err := os.Create(sbdPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBD file: %v", err)
	}
	// Write enough data for SBD slots
	data := make([]byte, 1024*1024) // 1MB
	_, _ = sbdFile.Write(data)
	_ = sbdFile.Close()

	// Use non-existent watchdog path (should fail)
	watchdogPath := nonExistentWatchdogPath

	// Test pre-flight checks with missing watchdog device but working SBD device
	// This should PASS because SBD device is available (either/or logic)
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1)
	if err == nil {
		t.Errorf("Expected pre-flight checks to fail with working SBD device despite missing watchdog")
	}
}

// TestPreflightChecks_BothFailing tests pre-flight checks with both watchdog and SBD device failing
func TestPreflightChecks_BothFailing(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Use non-existent paths for both watchdog and SBD device
	watchdogPath := nonExistentWatchdogPath
	sbdPath := "/non/existent/sbd"

	// Test pre-flight checks with both watchdog and SBD device failing
	// This should FAIL because neither component is available
	err := runPreflightChecks(watchdogPath, sbdPath, "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with both watchdog and SBD device missing, but they succeeded")
		return
	}

	// Should mention both failures
	if !strings.Contains(err.Error(), "both watchdog device and SBD device are inaccessible") {
		t.Errorf("Expected error about both devices being inaccessible, but got: %v", err)
	}
}
