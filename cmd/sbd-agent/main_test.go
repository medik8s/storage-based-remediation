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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

// MockBlockDevice is a mock implementation of BlockDevice for testing
type MockBlockDevice struct {
	data      []byte
	path      string
	closed    bool
	failRead  bool
	failWrite bool
	failSync  bool
	mutex     sync.RWMutex
}

// NewMockBlockDevice creates a new mock block device with the specified size
func NewMockBlockDevice(path string, size int) *MockBlockDevice {
	return &MockBlockDevice{
		data: make([]byte, size),
		path: path,
	}
}

func (m *MockBlockDevice) ReadAt(p []byte, off int64) (int, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.closed {
		return 0, errors.New("device is closed")
	}

	if m.failRead {
		return 0, errors.New("mock read failure")
	}

	if off < 0 || off >= int64(len(m.data)) {
		return 0, errors.New("offset out of range")
	}

	n := copy(p, m.data[off:])
	return n, nil
}

func (m *MockBlockDevice) WriteAt(p []byte, off int64) (int, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return 0, errors.New("device is closed")
	}

	if m.failWrite {
		return 0, errors.New("mock write failure")
	}

	if off < 0 || off >= int64(len(m.data)) {
		return 0, errors.New("offset out of range")
	}

	// Ensure we don't write beyond the buffer
	end := off + int64(len(p))
	if end > int64(len(m.data)) {
		end = int64(len(m.data))
	}

	n := copy(m.data[off:end], p)
	return n, nil
}

func (m *MockBlockDevice) Sync() error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.closed {
		return errors.New("device is closed")
	}

	if m.failSync {
		return errors.New("mock sync failure")
	}

	return nil
}

func (m *MockBlockDevice) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.closed = true
	return nil
}

func (m *MockBlockDevice) Path() string {
	return m.path
}

func (m *MockBlockDevice) IsClosed() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.closed
}

// SetFailRead configures the mock to fail read operations
func (m *MockBlockDevice) SetFailRead(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failRead = fail
}

// SetFailWrite configures the mock to fail write operations
func (m *MockBlockDevice) SetFailWrite(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failWrite = fail
}

// SetFailSync configures the mock to fail sync operations
func (m *MockBlockDevice) SetFailSync(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failSync = fail
}

// GetData returns a copy of the current data buffer
func (m *MockBlockDevice) GetData() []byte {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make([]byte, len(m.data))
	copy(result, m.data)
	return result
}

// WritePeerHeartbeat writes a heartbeat message to a specific peer slot for testing
func (m *MockBlockDevice) WritePeerHeartbeat(nodeID uint16, timestamp uint64, sequence uint64) error {
	// Create heartbeat message
	header := sbdprotocol.NewHeartbeat(nodeID, sequence)
	header.Timestamp = timestamp
	heartbeatMsg := sbdprotocol.SBDHeartbeatMessage{Header: header}

	// Marshal the message
	msgBytes, err := sbdprotocol.MarshalHeartbeat(heartbeatMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat message: %w", err)
	}

	// Calculate slot offset for this node
	slotOffset := int64(nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write heartbeat message to the designated slot
	_, err = m.WriteAt(msgBytes, slotOffset)
	return err
}

// WriteFenceMessage writes a fence message to a specific slot for testing
func (m *MockBlockDevice) WriteFenceMessage(nodeID, targetNodeID uint16, sequence uint64, reason uint8) error {
	// Create fence message header
	header := sbdprotocol.NewFence(nodeID, targetNodeID, sequence, reason)
	fenceMsg := sbdprotocol.SBDFenceMessage{
		Header:       header,
		TargetNodeID: targetNodeID,
		Reason:       reason,
	}

	// Marshal the fence message
	msgBytes, err := sbdprotocol.MarshalFence(fenceMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal fence message: %w", err)
	}

	// Calculate slot offset for the target node (where the fence message is written)
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to the designated slot
	_, err = m.WriteAt(msgBytes, slotOffset)
	return err
}

// MockWatchdog is a mock implementation of WatchdogInterface for testing
type MockWatchdog struct {
	path      string
	petCount  int
	closed    bool
	failPet   bool
	failClose bool
	mutex     sync.RWMutex
}

// NewMockWatchdog creates a new mock watchdog
func NewMockWatchdog(path string) *MockWatchdog {
	return &MockWatchdog{
		path: path,
	}
}

func (m *MockWatchdog) Pet() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return errors.New("watchdog is closed")
	}

	if m.failPet {
		return errors.New("mock pet failure")
	}

	m.petCount++
	return nil
}

func (m *MockWatchdog) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failClose {
		return errors.New("mock close failure")
	}

	m.closed = true
	return nil
}

func (m *MockWatchdog) Path() string {
	return m.path
}

// GetPetCount returns the number of times Pet() was called
func (m *MockWatchdog) GetPetCount() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.petCount
}

// SetFailPet configures the mock to fail pet operations
func (m *MockWatchdog) SetFailPet(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failPet = fail
}

// TestPeerMonitor tests the peer monitoring functionality
func TestPeerMonitor(t *testing.T) {
	logger := logr.Discard()
	monitor := NewPeerMonitor(30, 1, logger)

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
	monitor := NewPeerMonitor(1, 1, logger) // 1 second timeout

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
	monitor := NewPeerMonitor(30, 1, logger)

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
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path to avoid opening real device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8081, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device after creation
	agent.setSBDDevice(mockDevice)

	// Initially, should return no error for empty slot
	err = agent.readPeerHeartbeat(2)
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
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path to avoid opening real device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8082, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device after creation
	agent.setSBDDevice(mockDevice)

	// Write invalid data to peer slot
	invalidData := []byte("invalid message data")
	slotOffset := int64(2) * sbdprotocol.SBD_SLOT_SIZE
	_, err = mockDevice.WriteAt(invalidData, slotOffset)
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
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path to avoid opening real device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8083, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device after creation
	agent.setSBDDevice(mockDevice)

	// Configure device to fail reads
	mockDevice.SetFailRead(true)

	// Should return error when device read fails
	err = agent.readPeerHeartbeat(2)
	if err == nil {
		t.Error("Expected error when device read fails")
	}
}

func TestSBDAgent_ReadPeerHeartbeat_NodeIDMismatch(t *testing.T) {
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path to avoid opening real device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8084, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device after creation
	agent.setSBDDevice(mockDevice)

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
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	// Create agent with short check interval for testing and empty SBD device path
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1, 30*time.Second, 5*time.Second, 15*time.Second, 100*time.Millisecond, 1, "panic", 8085, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device after creation
	agent.setSBDDevice(mockDevice)

	// Write heartbeats for multiple peers
	err = mockDevice.WritePeerHeartbeat(2, 12345, 1)
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
	time.Sleep(300 * time.Millisecond)

	// Check that peers were discovered
	peers := agent.peerMonitor.GetPeerStatus()
	if len(peers) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(peers))
	}

	if _, exists := peers[2]; !exists {
		t.Error("Expected peer 2 to be discovered")
	}

	if _, exists := peers[3]; !exists {
		t.Error("Expected peer 3 to be discovered")
	}

	// Check healthy peer count
	if count := agent.peerMonitor.GetHealthyPeerCount(); count != 2 {
		t.Errorf("Expected 2 healthy peers, got %d", count)
	}

	// Wait for peers to become unhealthy (1 second timeout + check interval)
	time.Sleep(1200 * time.Millisecond)

	// Should now have 0 healthy peers
	if count := agent.peerMonitor.GetHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers after timeout, got %d", count)
	}
}

func TestSBDAgent_NewSBDAgent(t *testing.T) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")

	// Test successful creation
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8086, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Verify configuration
	if agent.nodeName != "test-node" {
		t.Errorf("Expected node name 'test-node', got '%s'", agent.nodeName)
	}
	if agent.nodeID != 1 {
		t.Errorf("Expected node ID 1, got %d", agent.nodeID)
	}
	if agent.petInterval != 1*time.Second {
		t.Errorf("Expected pet interval 1s, got %v", agent.petInterval)
	}

	// Test invalid configurations
	invalidWatchdog := NewMockWatchdog("")
	_, err = NewSBDAgentWithWatchdog(invalidWatchdog, "", "", "test-cluster", 0, 0, 0, 0, 0, 0, "invalid", 8087, 10*time.Minute, true)
	if err == nil {
		t.Error("Expected error for invalid configuration")
	}
}

func TestSBDAgent_WriteHeartbeatToSBD(t *testing.T) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB

	// Create agent and set mock device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 5,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8088, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	agent.setSBDDevice(mockDevice)

	// Write heartbeat
	err = agent.writeHeartbeatToSBD()
	if err != nil {
		t.Fatalf("Failed to write heartbeat: %v", err)
	}

	// Verify the heartbeat was written to the correct slot
	slotOffset := int64(5) * sbdprotocol.SBD_SLOT_SIZE
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

	if header.NodeID != 5 {
		t.Errorf("Expected node ID 5, got %d", header.NodeID)
	}
	if header.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Expected heartbeat message type, got %d", header.Type)
	}
	if header.Sequence != 1 {
		t.Errorf("Expected sequence 1, got %d", header.Sequence)
	}
}

func TestSBDAgent_WriteHeartbeatToSBD_DeviceError(t *testing.T) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB

	// Create agent and set mock device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8089, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	agent.setSBDDevice(mockDevice)

	// Configure device to fail writes
	mockDevice.SetFailWrite(true)

	// Should return error when device write fails
	err = agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected error when device write fails")
	}
}

func TestSBDAgent_WriteHeartbeatToSBD_SyncError(t *testing.T) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024) // 1MB

	// Create agent and set mock device
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8090, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	agent.setSBDDevice(mockDevice)

	// Configure device to fail sync
	mockDevice.SetFailSync(true)

	// Should return error when device sync fails
	err = agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected error when device sync fails")
	}
}

func TestSBDAgent_SBDHealthStatus(t *testing.T) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")

	// Create agent
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8091, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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
	mockWatchdog := NewMockWatchdog("/dev/watchdog")

	// Create agent
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8092, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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
		os.Unsetenv("NODE_NAME")
		os.Unsetenv("HOSTNAME")
		os.Unsetenv("NODENAME")

		// Set NODE_NAME
		os.Setenv("NODE_NAME", "test-env-node")
		defer os.Unsetenv("NODE_NAME")

		nodeName := getNodeNameFromEnv()
		if nodeName != "test-env-node" {
			t.Errorf("Expected 'test-env-node', got '%s'", nodeName)
		}
	})

	// Test getNodeIDFromEnv
	t.Run("getNodeIDFromEnv", func(t *testing.T) {
		// Clear environment
		os.Unsetenv("SBD_NODE_ID")
		os.Unsetenv("NODE_ID")

		// Set SBD_NODE_ID
		os.Setenv("SBD_NODE_ID", "5")
		defer os.Unsetenv("SBD_NODE_ID")

		nodeID := getNodeIDFromEnv()
		if nodeID != 5 {
			t.Errorf("Expected 5, got %d", nodeID)
		}
	})

	// Test getSBDTimeoutFromEnv
	t.Run("getSBDTimeoutFromEnv", func(t *testing.T) {
		// Clear environment
		os.Unsetenv("SBD_TIMEOUT_SECONDS")
		os.Unsetenv("SBD_TIMEOUT")

		// Set SBD_TIMEOUT_SECONDS
		os.Setenv("SBD_TIMEOUT_SECONDS", "60")
		defer os.Unsetenv("SBD_TIMEOUT_SECONDS")

		timeout := getSBDTimeoutFromEnv()
		if timeout != 60 {
			t.Errorf("Expected 60, got %d", timeout)
		}
	})
}

// Benchmark tests
func BenchmarkSBDAgent_WriteHeartbeat(b *testing.B) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1, 30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080, 10*time.Minute, true)
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer agent.Stop()

	agent.setSBDDevice(mockDevice)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := agent.writeHeartbeatToSBD(); err != nil {
			b.Fatalf("Failed to write heartbeat: %v", err)
		}
	}
}

func BenchmarkSBDAgent_ReadPeerHeartbeat(b *testing.B) {
	mockWatchdog := NewMockWatchdog("/dev/watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1, 30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080, 10*time.Minute, true)
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer agent.Stop()

	agent.setSBDDevice(mockDevice)

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
	monitor := NewPeerMonitor(30, 1, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.UpdatePeer(2, uint64(i), uint64(i))
	}
}

func TestSBDAgent_ReadOwnSlotForFenceMessage(t *testing.T) {
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 3,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8093, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device
	agent.setSBDDevice(mockDevice)

	// Initially, no fence message should be found
	err = agent.readOwnSlotForFenceMessage()
	if err != nil {
		t.Errorf("Expected no error for empty slot, got: %v", err)
	}

	// Write a fence message targeting this node
	err = mockDevice.WriteFenceMessage(2, 3, 100, sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT)
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
	err = agent.readOwnSlotForFenceMessage()
	// Should not reach here due to panic
	t.Error("Expected function to panic due to self-fencing")
}

func TestSBDAgent_ReadOwnSlotForFenceMessage_WrongTarget(t *testing.T) {
	// Create mock devices
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 3,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8094, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device
	agent.setSBDDevice(mockDevice)

	// Write a fence message targeting a different node
	err = mockDevice.WriteFenceMessage(2, 5, 100, sbdprotocol.FENCE_REASON_MANUAL)
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
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with empty SBD device path
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8095, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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
	mockWatchdog := NewMockWatchdog("/dev/watchdog0")
	mockSBDDevice := NewMockBlockDevice("/dev/sbd", 1024*1024) // 1MB device

	// Create SBD agent with empty SBD device path
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1, 5*time.Second, 10*time.Second, 5*time.Second, 10*time.Second, 30, "panic", 8096, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}

	// Set the mock SBD device
	agent.setSBDDevice(mockSBDDevice)

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
		t.Errorf("Expected watchdog not to be pet when self-fence detected, but it was pet %d times", mockWatchdog.GetPetCount())
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
	watchdogFile.Close()

	// Create mock SBD device file with sufficient size
	sbdFile, err := os.Create(sbdPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBD file: %v", err)
	}
	// Write enough data for SBD slots
	data := make([]byte, 1024*1024) // 1MB
	sbdFile.Write(data)
	sbdFile.Close()

	// Test successful pre-flight checks
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1, false)
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
	watchdogPath := "/non/existent/watchdog"

	// Test pre-flight checks with missing watchdog device
	err := runPreflightChecks(watchdogPath, "", "test-node", 1, false)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with missing watchdog, but they succeeded")
	}

	// With softdog fallback, the error message will now include information about failed softdog loading
	// or it might succeed if softdog can be loaded. The exact error depends on system capabilities.
	expectedErrorSubstrings := []string{
		"watchdog device pre-flight check failed", // Main error type
		// Could be any of these depending on system state:
		// - "failed to load softdog module" (if modprobe fails)
		// - "watchdog device does not exist" (if running in environment that doesn't try softdog)
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
	watchdogFile.Close()

	// Use non-existent SBD path
	sbdPath := "/non/existent/sbd"

	// Test pre-flight checks with missing SBD device
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1, false)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with missing SBD device, but they succeeded")
	}

	if !strings.Contains(err.Error(), "SBD device does not exist") {
		t.Errorf("Expected error about missing SBD device, but got: %v", err)
	}
}

// TestPreflightChecks_WatchdogOnlyMode tests pre-flight checks in watchdog-only mode
func TestPreflightChecks_WatchdogOnlyMode(t *testing.T) {
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
	watchdogFile.Close()

	// Empty SBD path for watchdog-only mode
	sbdPath := ""

	// Test pre-flight checks in watchdog-only mode
	err = runPreflightChecks(watchdogPath, sbdPath, "test-node", 1, false)
	if err != nil {
		t.Errorf("Expected pre-flight checks to succeed in watchdog-only mode, but got error: %v", err)
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
	watchdogFile.Close()

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
			err := runPreflightChecks(watchdogPath, "", tc.nodeName, tc.nodeID, false)
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
	watchdogFile.Close()

	err = checkWatchdogDevice(watchdogPath, false)
	if err != nil {
		t.Errorf("Expected watchdog device check to succeed, but got error: %v", err)
	}

	// Test with non-existent file
	nonExistentPath := filepath.Join(tmpDir, "non-existent")
	err = checkWatchdogDevice(nonExistentPath, false)
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
	mockDevice := NewMockBlockDevice("/dev/sbd", 1024*1024) // 1MB device
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
			errContains: "too long for watchdog timeout",
		},
		{
			name:        "invalid pet interval - too long (25s)",
			petInterval: 25 * time.Second,
			wantErr:     true,
			errContains: "too long for watchdog timeout",
		},
		{
			name:        "invalid pet interval - exactly at watchdog timeout (60s)",
			petInterval: 60 * time.Second,
			wantErr:     true,
			errContains: "too long for watchdog timeout",
		},
		{
			name:        "invalid pet interval - longer than watchdog timeout (90s)",
			petInterval: 90 * time.Second,
			wantErr:     true,
			errContains: "too long for watchdog timeout",
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
	mockWatchdog := NewMockWatchdog("/dev/watchdog")

	// Test with file locking enabled
	t.Run("FileLockingEnabled", func(t *testing.T) {
		// Create a temporary SBD device file
		tmpDir := t.TempDir()
		sbdPath := tmpDir + "/test-sbd"

		// Create the file with some initial data
		if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
			t.Fatalf("Failed to create test SBD device: %v", err)
		}

		agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
			1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8200, 10*time.Minute, true)
		if err != nil {
			t.Fatalf("Failed to create SBD agent: %v", err)
		}
		defer agent.Stop()

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
		// Create a temporary SBD device file
		tmpDir := t.TempDir()
		sbdPath := tmpDir + "/test-sbd"

		// Create the file with some initial data
		if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
			t.Fatalf("Failed to create test SBD device: %v", err)
		}

		agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
			1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8201, 10*time.Minute, false)
		if err != nil {
			t.Fatalf("Failed to create SBD agent: %v", err)
		}
		defer agent.Stop()

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

	// Test watchdog-only mode (no SBD device)
	t.Run("WatchdogOnlyMode", func(t *testing.T) {
		agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
			1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8202, 10*time.Minute, false)
		if err != nil {
			t.Fatalf("Failed to create SBD agent: %v", err)
		}
		defer agent.Stop()

		// In watchdog-only mode, NodeManager should not be initialized
		if agent.nodeManager != nil {
			t.Error("Expected NodeManager to be nil in watchdog-only mode")
		}
	})
}
