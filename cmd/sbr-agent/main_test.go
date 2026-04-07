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
	"context"
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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/pkg/blockdevice"
	mocks "github.com/medik8s/storage-based-remediation/pkg/mocks"
	"github.com/medik8s/storage-based-remediation/pkg/sbdprotocol"
	testutils "github.com/medik8s/storage-based-remediation/test/utils"
)

const (
	// Test constants
	nonExistentWatchdogPath = "/non/existent/watchdog"
)

// failingRemediationGetClient wraps a client.Client and returns a fixed error for Get
// when the object is StorageBasedRemediation and the key matches the given namespace/name.
// Used to simulate API unreachable in tests.
type failingRemediationGetClient struct {
	delegate      client.Client
	failNamespace string
	failName      string
	err           error
}

func (c *failingRemediationGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*medik8sv1alpha1.StorageBasedRemediation); ok && key.Namespace == c.failNamespace && key.Name == c.failName {
		return c.err
	}
	return c.delegate.Get(ctx, key, obj, opts...)
}

func (c *failingRemediationGetClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.delegate.List(ctx, list, opts...)
}

func (c *failingRemediationGetClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return c.delegate.Create(ctx, obj, opts...)
}

func (c *failingRemediationGetClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.delegate.Delete(ctx, obj, opts...)
}

func (c *failingRemediationGetClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return c.delegate.Update(ctx, obj, opts...)
}

func (c *failingRemediationGetClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.delegate.Patch(ctx, obj, patch, opts...)
}

func (c *failingRemediationGetClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.delegate.DeleteAllOf(ctx, obj, opts...)
}

func (c *failingRemediationGetClient) Status() client.SubResourceWriter {
	return c.delegate.Status()
}

func (c *failingRemediationGetClient) SubResource(subResource string) client.SubResourceClient {
	return c.delegate.SubResource(subResource)
}

func (c *failingRemediationGetClient) Scheme() *runtime.Scheme {
	return c.delegate.Scheme()
}

func (c *failingRemediationGetClient) RESTMapper() meta.RESTMapper {
	return c.delegate.RESTMapper()
}

func (c *failingRemediationGetClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.delegate.GroupVersionKindFor(obj)
}

func (c *failingRemediationGetClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return c.delegate.IsObjectNamespaced(obj)
}

var _ client.Client = &failingRemediationGetClient{}

// createTestSBRAgent creates a test SBR agent with mock devices and temporary SBR files
func createTestSBRAgent(t *testing.T, metricsPort int) (
	*SBRAgent, *mocks.MockWatchdog, *mocks.MockBlockDevice, func()) {
	return createTestSBRAgentWithFileLocking(t, "test-node", metricsPort, true)
}

func createManagerPrefix() string {
	return strconv.Itoa(time.Now().Nanosecond())
}

// createTestSBRAgentWithFileLocking creates a test SBR agent with configurable file locking
func createTestSBRAgentWithFileLocking(t *testing.T, nodeName string, metricsPort int, fileLockingEnabled bool) (
	*SBRAgent, *mocks.MockWatchdog, *mocks.MockBlockDevice, func()) {

	// Create temporary SBR device files (both heartbeat and fence)
	tmpDir := t.TempDir()
	sbrPath := tmpDir + "/test-sbr"
	fencePath := sbrPath + "-fence"

	mockWatchdog := mocks.NewMockWatchdog(tmpDir + "/watchdog")
	mockHeartbeatDevice := mocks.NewMockBlockDevice(sbrPath, 1024*1024)
	mockFenceDevice := mocks.NewMockBlockDevice(fencePath, 1024*1024)

	// Create both heartbeat and fence device files
	if err := os.WriteFile(sbrPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBR heartbeat device: %v", err)
	}
	if err := os.WriteFile(fencePath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBR fence device: %v", err)
	}

	agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, nodeName, "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", metricsPort,
		10*time.Minute, fileLockingEnabled, 2*time.Second,
		testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix(), false)
	if err != nil {
		t.Fatalf("Failed to create SBR agent: %v", err)
	}

	// Set mock devices to override the real file-based devices
	agent.setSBRDevices(mockHeartbeatDevice, mockFenceDevice)

	var once sync.Once
	cleanup := func() { once.Do(func() { _ = agent.Stop() }) }
	t.Cleanup(cleanup)

	return agent, mockWatchdog, mockHeartbeatDevice, cleanup
}

// TestWatchdogClosedWhenShutdownSignalReceived verifies that when a shutdown signal
// (SIGTERM) is received, the agent closes the watchdog so the node does not reboot
// on uninstall. See docs/RCA-watchdog-reboot-on-uninstall.md.
func TestWatchdogClosedWhenShutdownSignalReceived(t *testing.T) {
	// Inline agent creation for this test only: same as createTestSBRAgentWithFileLocking
	// but without t.Cleanup(cleanup), so we do not call Stop() on test exit.
	tmpDir := t.TempDir()
	sbrPath := tmpDir + "/test-sbr"
	fencePath := sbrPath + "-fence"
	mockWatchdog := mocks.NewMockWatchdog(tmpDir + "/watchdog")
	mockHeartbeatDevice := mocks.NewMockBlockDevice(sbrPath, 1024*1024)
	mockFenceDevice := mocks.NewMockBlockDevice(fencePath, 1024*1024)
	if err := os.WriteFile(sbrPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBR heartbeat device: %v", err)
	}
	if err := os.WriteFile(fencePath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBR fence device: %v", err)
	}
	agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 555,
		10*time.Minute, true, 2*time.Second,
		testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix(), false)
	if err != nil {
		t.Fatalf("Failed to create SBR agent: %v", err)
	}
	agent.setSBRDevices(mockHeartbeatDevice, mockFenceDevice)

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
	monitor := newPeerMonitor(30, 1, nil, logger)

	// Initially no peers
	if count := monitor.getHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers initially, got %d", count)
	}

	// Update a peer
	monitor.updatePeer(2, 1000, 1)

	// Should have one healthy peer
	if count := monitor.getHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer, got %d", count)
	}

	// Get peer status
	peers := monitor.getPeerStatus()
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
	monitor := newPeerMonitor(1, 1, nil, logger) // 1 second timeout

	// Update a peer
	monitor.updatePeer(2, 1000, 1)

	// Should be healthy initially
	if count := monitor.getHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer initially, got %d", count)
	}

	// Wait for timeout
	time.Sleep(1100 * time.Millisecond)

	// Check liveness
	monitor.checkPeerLiveness()

	// Should now be unhealthy
	if count := monitor.getHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers after timeout, got %d", count)
	}

	// Update peer again
	monitor.updatePeer(2, 2000, 2)

	// Should be healthy again
	if count := monitor.getHealthyPeerCount(); count != 1 {
		t.Errorf("Expected 1 healthy peer after update, got %d", count)
	}
}

func TestPeerMonitor_SequenceValidation(t *testing.T) {
	logger := logr.Discard()
	monitor := newPeerMonitor(30, 1, nil, logger)

	// Update a peer with sequence 5
	monitor.updatePeer(2, 1000, 5)

	peers := monitor.getPeerStatus()
	peer := peers[2]
	if peer.LastSequence != 5 {
		t.Errorf("Expected sequence 5, got %d", peer.LastSequence)
	}

	// Update with older sequence (should be ignored)
	monitor.updatePeer(2, 1000, 3)

	peers = monitor.getPeerStatus()
	peer = peers[2]
	if peer.LastSequence != 5 {
		t.Errorf("Expected sequence to remain 5, got %d", peer.LastSequence)
	}

	// Update with newer sequence (should be accepted)
	monitor.updatePeer(2, 1000, 7)

	peers = monitor.getPeerStatus()
	peer = peers[2]
	if peer.LastSequence != 7 {
		t.Errorf("Expected sequence 7, got %d", peer.LastSequence)
	}
}

func TestSBRAgent_ReadPeerHeartbeat(t *testing.T) {

	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
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
	peers := agent.peerMonitor.getPeerStatus()
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

func TestSBRAgent_ReadPeerHeartbeat_InvalidMessage(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
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
	peers := agent.peerMonitor.getPeerStatus()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers, got %d", len(peers))
	}
}

func TestSBRAgent_ReadPeerHeartbeat_DeviceError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
	defer cleanup()

	// Configure device to fail reads
	mockDevice.SetFailRead(true)

	// Should return error when device read fails
	err := agent.readPeerHeartbeat(2)
	if err == nil {
		t.Error("Expected error when device read fails")
	}
}

func TestSBRAgent_ReadPeerHeartbeat_NodeIDMismatch(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
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
	peers := agent.peerMonitor.getPeerStatus()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers due to node ID mismatch, got %d", len(peers))
	}
}

func TestSBRAgent_PeerMonitorLoop_Integration(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
	defer cleanup()

	// reduce default setting in order to speed the test
	agent.peerMonitor.sbrTimeoutSeconds = 2

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
	peers := agent.peerMonitor.getPeerStatus()
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
	if count := agent.peerMonitor.getHealthyPeerCount(); count != 2 {
		for peerID, peer := range peers {
			t.Errorf("Peer %d: %+v", peerID, peer)
		}
		t.Errorf("Expected 2 healthy peers, got %d", count)
	}

	// Wait for peers to become unhealthy (1 second timeout + check interval)
	heartbeatInterval := time.Duration(agent.peerMonitor.sbrTimeoutSeconds) / 2 * time.Second
	timeout := heartbeatInterval * MaxConsecutiveFailures
	time.Sleep(timeout + time.Second)

	// Should now have 0 healthy peers
	if count := agent.peerMonitor.getHealthyPeerCount(); count != 0 {
		t.Errorf("Expected 0 healthy peers after %vs timeout, got %d", agent.peerMonitor.sbrTimeoutSeconds, count)
		for peerID, peer := range peers {
			t.Errorf("Peer %d: %+v", peerID, peer)
		}
	}
}

func TestSBRAgent_NewSBRAgent(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8081)
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
	_, err := NewSBRAgentWithWatchdog(invalidWatchdog, "/dev/invalid-sbr", "", "test-cluster", 0, 0, 0, 0, 0, 0,
		"invalid", 8087, 10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(t),
		&rest.Config{}, createManagerPrefix(), false)
	if err == nil {
		t.Error("Expected error for invalid configuration")
	}
}

func TestSBRAgent_WriteHeartbeatToSBR(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8081)
	defer cleanup()
	// Write heartbeat
	err := agent.writeHeartbeatToSBR()
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

func TestSBRAgent_WriteHeartbeatToSBR_DeviceError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8089)
	defer cleanup()

	// Configure device to fail writes
	mockDevice.SetFailWrite(true)

	// Should return error when device write fails
	err := agent.writeHeartbeatToSBR()
	if err == nil {
		t.Error("Expected error when device write fails")
	}
}

func TestSBRAgent_WriteHeartbeatToSBR_SyncError(t *testing.T) {
	agent, _, mockDevice, cleanup := createTestSBRAgent(t, 8090)
	defer cleanup()

	// Configure device to fail sync
	mockDevice.SetFailSync(true)

	// Should return error when device sync fails
	err := agent.writeHeartbeatToSBR()
	if err == nil {
		t.Error("Expected error when device sync fails")
	}
}

func TestSBRAgent_SBRHealthStatus(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8091)
	defer cleanup()

	// Initially should be false
	if agent.isSBRHealthy() {
		t.Error("Expected SBR to be initially unhealthy")
	}

	// Set healthy
	agent.setSBRHealthy(true)
	if !agent.isSBRHealthy() {
		t.Error("Expected SBR to be healthy after setting")
	}

	// Set unhealthy
	agent.setSBRHealthy(false)
	if agent.isSBRHealthy() {
		t.Error("Expected SBR to be unhealthy after setting")
	}
}

func TestSBRAgent_HeartbeatSequence(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8081)
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
		_ = os.Unsetenv("SBR_NODE_ID")
		_ = os.Unsetenv("NODE_ID")

		// Set SBR_NODE_ID
		_ = os.Setenv("SBR_NODE_ID", "5")
		defer func() { _ = os.Unsetenv("SBR_NODE_ID") }()

		nodeID := getNodeIDFromEnv()
		if nodeID != 5 {
			t.Errorf("Expected 5, got %d", nodeID)
		}
	})

	// Test getSBRTimeoutFromEnv
	t.Run("getSBRTimeoutFromEnv", func(t *testing.T) {
		// Clear environment
		_ = os.Unsetenv("SBR_TIMEOUT_SECONDS")
		_ = os.Unsetenv("SBR_TIMEOUT")

		// Set SBR_TIMEOUT_SECONDS
		_ = os.Setenv("SBR_TIMEOUT_SECONDS", "60")
		defer func() { _ = os.Unsetenv("SBR_TIMEOUT_SECONDS") }()

		timeout := getSBRTimeoutFromEnv()
		if timeout != 60 {
			t.Errorf("Expected 60, got %d", timeout)
		}
	})
}

// Benchmark tests
func BenchmarkSBRAgent_WriteHeartbeat(b *testing.B) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")
	mockDevice := mocks.NewMockBlockDevice("/dev/mock-sbr", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	// Create temporary SBR device file
	tmpDir := b.TempDir()
	sbrPath := tmpDir + "/test-sbr"
	if err := os.WriteFile(sbrPath, make([]byte, 1024*1024), 0644); err != nil {
		b.Fatalf("Failed to create test SBR device: %v", err)
	}

	agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "test-node", "test-cluster", 1,
		30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080,
		10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(b), &rest.Config{}, createManagerPrefix(), false)
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	agent.setSBRDevices(mockDevice, mockDevice)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := agent.writeHeartbeatToSBR(); err != nil {
			b.Fatalf("Failed to write heartbeat: %v", err)
		}
	}
}

func BenchmarkSBRAgent_ReadPeerHeartbeat(b *testing.B) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")
	mockDevice := mocks.NewMockBlockDevice("/dev/mock-sbr", int(sbdprotocol.SBD_MAX_NODES*sbdprotocol.SBD_SLOT_SIZE))

	// Create temporary SBR device file
	tmpDir := b.TempDir()
	sbrPath := tmpDir + "/test-sbr"
	if err := os.WriteFile(sbrPath, make([]byte, 1024*1024), 0644); err != nil {
		b.Fatalf("Failed to create test SBR device: %v", err)
	}

	agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "test-node", "test-cluster", 1,
		30*time.Second, 5*time.Second, 15*time.Second, 5*time.Second, 30, "panic", 8080,
		10*time.Minute, true, 2*time.Second, testutils.NewFakeClient(b), &rest.Config{}, createManagerPrefix(), false)
	if err != nil {
		b.Fatalf("Failed to create agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	agent.setSBRDevices(mockDevice, mockDevice)

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

func BenchmarkPeerMonitor_updatePeer(b *testing.B) {
	logger := logr.Discard()
	monitor := newPeerMonitor(30, 1, nil, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.updatePeer(2, uint64(i), uint64(i))
	}
}

func TestSBRAgent_ReadOwnSlotForFenceMessage(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8081)
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

func TestSBRAgent_ReadOwnSlotForFenceMessage_WrongTarget(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8081)
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

func TestSBRAgent_SelfFenceStatus(t *testing.T) {
	agent, _, _, cleanup := createTestSBRAgent(t, 8095)
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

func TestSBRAgent_WatchdogLoop_WithSelfFence(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create mock watchdog and SBR device
	agent, mockWatchdog, _, cleanup := createTestSBRAgent(t, 8081)
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
	sbrPath := filepath.Join(tmpDir, "sbr")

	// Create mock watchdog file
	watchdogFile, err := os.Create(watchdogPath)
	if err != nil {
		t.Fatalf("Failed to create mock watchdog file: %v", err)
	}
	_ = watchdogFile.Close()

	// Create mock SBR device file with sufficient size
	sbrFile, err := os.Create(sbrPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBR file: %v", err)
	}
	// Write enough data for SBR slots
	data := make([]byte, 1024*1024) // 1MB
	_, _ = sbrFile.Write(data)
	_ = sbrFile.Close()

	// Test successful pre-flight checks
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1)
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

	// Test pre-flight checks with missing watchdog device and no SBR device
	// This should fail because SBR device is always required now
	err := runPreflightChecks(watchdogPath, "", "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with empty SBR device path, but they succeeded")
		return
	}

	// Should mention that SBR device path cannot be empty
	if !strings.Contains(err.Error(), "SBR device path cannot be empty") {
		t.Errorf("Expected error about empty SBR device path, but got: %v", err)
	}
}

// TestPreflightChecks_SBRMissing tests pre-flight checks with missing SBR device
func TestPreflightChecks_SBRMissing(t *testing.T) {
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

	// Use non-existent SBR path
	sbrPath := "/non/existent/sbr"

	// Test pre-flight checks with missing SBR device but working watchdog
	// This should now PASS because watchdog is available (either/or logic)
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1)
	if err == nil {
		t.Errorf("Expected pre-flight checks to fail with working watchdog and missing SBR device")
	}
}

// TestPreflightChecks_WatchdogOnlyMode tests pre-flight checks in watchdog-only mode
func TestPreflightChecks_RequireSBRDevice(t *testing.T) {
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

	// Empty SBR path should now fail (no more watchdog-only mode)
	sbrPath := ""

	// Test pre-flight checks with empty SBR path should fail
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with empty SBR path, but they succeeded")
	}

	if !strings.Contains(err.Error(), "SBR device path cannot be empty") {
		t.Errorf("Expected error about empty SBR device path, but got: %v", err)
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
			nodeID:   256, // Assuming SBR_MAX_NODES is 255
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

// TestPerformSBRReadWriteTest tests the SBR device read/write test function
func TestPerformSBRReadWriteTest(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Test with working mock device
	mockDevice := mocks.NewMockBlockDevice("/dev/sbr", 1024*1024) // 1MB device
	err := performSBRReadWriteTest(mockDevice, 1, "test-node")
	if err != nil {
		t.Errorf("Expected SBR read/write test to succeed, but got error: %v", err)
	}

	// Test with device that fails writes
	mockDevice.SetFailWrite(true)
	err = performSBRReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBR read/write test to fail with write failure, but it succeeded")
	}

	// Reset and test with device that fails reads
	mockDevice.SetFailWrite(false)
	mockDevice.SetFailRead(true)
	err = performSBRReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBR read/write test to fail with read failure, but it succeeded")
	}

	// Reset and test with device that fails sync
	mockDevice.SetFailRead(false)
	mockDevice.SetFailSync(true)
	err = performSBRReadWriteTest(mockDevice, 1, "test-node")
	if err == nil {
		t.Error("Expected SBR read/write test to fail with sync failure, but it succeeded")
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

func TestSBRAgent_FileLockingConfiguration(t *testing.T) {
	mockWatchdog := mocks.NewMockWatchdog("/dev/watchdog")

	// Test with file locking enabled
	t.Run("FileLockingEnabled", func(t *testing.T) {

		agent, _, _, cleanup := createTestSBRAgent(t, 8081)
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
		agent, _, _, cleanup := createTestSBRAgentWithFileLocking(t, "test-node", 8082, false)
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

	// Test SBR device is always required (no more watchdog-only mode)
	t.Run("SBRDeviceRequired", func(t *testing.T) {
		// Try to create agent with empty SBR device path should fail
		_, err := NewSBRAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
			1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, 30, "panic", 8202, 10*time.Minute, false,
			2*time.Second, testutils.NewFakeClient(t), &rest.Config{}, createManagerPrefix(), false)
		if err == nil {
			t.Error("Expected error when creating agent with empty SBR device path")
		}

		if !strings.Contains(err.Error(), "heartbeat device path cannot be empty") {
			t.Errorf("Expected error about empty heartbeat device path, but got: %v", err)
		}
	})
}

// TestPreflightChecks_SBROnlyMode tests pre-flight checks with working SBR device but failing watchdog
func TestPreflightChecks_SBROnlyMode(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary SBR device file with sufficient size
	tmpDir := t.TempDir()
	sbrPath := filepath.Join(tmpDir, "sbr")
	sbrFile, err := os.Create(sbrPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBR file: %v", err)
	}
	// Write enough data for SBR slots
	data := make([]byte, 1024*1024) // 1MB
	_, _ = sbrFile.Write(data)
	_ = sbrFile.Close()

	// Use non-existent watchdog path (should fail)
	watchdogPath := nonExistentWatchdogPath

	// Test pre-flight checks with missing watchdog device but working SBR device
	// This should PASS because SBR device is available (either/or logic)
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1)
	if err == nil {
		t.Errorf("Expected pre-flight checks to fail with working SBR device despite missing watchdog")
	}
}

// TestPreflightChecks_BothFailing tests pre-flight checks with both watchdog and SBR device failing
func TestPreflightChecks_BothFailing(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Use non-existent paths for both watchdog and SBR device
	watchdogPath := nonExistentWatchdogPath
	sbrPath := "/non/existent/sbr"

	// Test pre-flight checks with both watchdog and SBR device failing
	// This should FAIL because neither component is available
	err := runPreflightChecks(watchdogPath, sbrPath, "test-node", 1)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with both watchdog and SBR device missing, but they succeeded")
		return
	}

	// Should mention both failures
	if !strings.Contains(err.Error(), "both watchdog device and SBR device are inaccessible") {
		t.Errorf("Expected error about both devices being inaccessible, but got: %v", err)
	}
}

// Fence flow with real SBR agent (RunUntilShutdown). Uses the envtest and k8sClient
// from the Agent Suite (suite_test.go). Temp files + blockdevice populate the node table
// so the agent's node manager resolves slot IDs; setSBRDevices then uses mocks for I/O.
var _ = Describe("Fence flow with real SBR agent", func() {
	const (
		fenceFlowTargetNode   = "worker-2"
		fenceFlowSBRTimeout   = uint(2)
		fenceFlowMetricsPort  = 9655
		detectOnlyMetricsPort = 9656
	)

	// setupFenceFlowBase creates worker-2 node, temp dir with sbr/fence files, and node manager;
	// returns tmpDir, sbrPath, fencePath, worker1ID, worker2ID. Registers DeferCleanup for node and dir.
	const fenceFlowBasePrefix = "fence-flow-"
	setupFenceFlowBase := func() (tmpDir, sbrPath, fencePath string, worker1ID, worker2ID uint16) {
		By("Creating target node worker-2")
		workerNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fenceFlowTargetNode}}
		Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, workerNode) })

		By("Creating temp files for agent node manager slot table")
		var err error
		tmpDir, err = os.MkdirTemp("", fenceFlowBasePrefix)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

		sbrPath = filepath.Join(tmpDir, "sbr")
		fencePath = filepath.Join(tmpDir, "sbr-fence")
		Expect(os.WriteFile(sbrPath, make([]byte, 1024*1024), 0644)).To(Succeed())
		Expect(os.WriteFile(fencePath, make([]byte, 1024*1024), 0644)).To(Succeed())

		heartbeatDevice, err := blockdevice.OpenWithTimeout(sbrPath, 2*time.Second, logr.Discard())
		Expect(err).NotTo(HaveOccurred())
		nmConfig := sbdprotocol.NodeManagerConfig{
			ClusterName:        "test-cluster",
			SyncInterval:       30 * time.Second,
			StaleNodeTimeout:   10 * time.Minute,
			Logger:             logr.Discard(),
			FileLockingEnabled: true,
		}
		nm, err := sbdprotocol.NewNodeManager(heartbeatDevice, nmConfig)
		Expect(err).NotTo(HaveOccurred())
		worker1ID, err = nm.GetNodeIDForNode("worker-1")
		Expect(err).NotTo(HaveOccurred())
		worker2ID, err = nm.GetNodeIDForNode("worker-2")
		Expect(err).NotTo(HaveOccurred())
		Expect(heartbeatDevice.Close()).To(Succeed())
		return tmpDir, sbrPath, fencePath, worker1ID, worker2ID
	}

	// startFenceFlowAgent starts the agent in a goroutine and registers cleanup to send SIGTERM.
	startFenceFlowAgent := func(agent *SBRAgent) {
		sigChan := make(chan os.Signal, 1)
		agentErr := make(chan error, 1)
		go func() { agentErr <- agent.RunUntilShutdown(sigChan) }()
		DeferCleanup(func() {
			sigChan <- syscall.SIGTERM
			select {
			case <-agentErr:
			case <-time.After(15 * time.Second):
			}
		})
	}

	Context("when agent sets SBRStorageUnhealthy and controller reconciles", func() {
		It("should write fence message to device", func() {
			tmpDir, sbrPath, fencePath, worker1ID, worker2ID := setupFenceFlowBase()

			// Stale age for this test: (MaxConsecutiveFailures+1)*heartbeatInterval; heartbeatInterval is 1s
			oldStale := sbrUnhealthyConditionStaleAge
			sbrUnhealthyConditionStaleAge = time.Duration(MaxConsecutiveFailures+1) * time.Second
			DeferCleanup(func() { sbrUnhealthyConditionStaleAge = oldStale })

			By("Writing initial heartbeats for worker-1 and worker-2 on mock devices")
			mockHeartbeatDevice := mocks.NewMockBlockDevice("/tmp/fence-test-heartbeat", 1024*1024)
			mockFenceDevice := mocks.NewMockBlockDevice("/tmp/fence-test-fence", 1024*1024)
			ts := uint64(time.Now().UnixNano())
			for round := 0; round < 3; round++ {
				Expect(mockHeartbeatDevice.WritePeerHeartbeat(worker1ID, ts+uint64(round), uint64(round+1))).To(Succeed())
				Expect(mockHeartbeatDevice.WritePeerHeartbeat(worker2ID, ts+uint64(round), uint64(round+1))).To(Succeed())
			}

			By("Creating real SBR agent and starting RunUntilShutdown")
			mockWatchdog := mocks.NewMockWatchdog(filepath.Join(tmpDir, "watchdog"))
			agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "worker-1", "test-cluster", worker1ID,
				1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, fenceFlowSBRTimeout, "panic", fenceFlowMetricsPort,
				10*time.Minute, true, 2*time.Second,
				k8sClient, cfg, createManagerPrefix(), false)
			Expect(err).NotTo(HaveOccurred())
			agent.setSBRDevices(mockHeartbeatDevice, mockFenceDevice)
			startFenceFlowAgent(agent)

			By("Letting agent run a few peer loops so it sees both workers")
			Consistently(func(g Gomega) bool {
				node := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fenceFlowTargetNode}, node)).To(Succeed())
				return isConditionExist(node.Status.Conditions, medik8sv1alpha1.NodeConditionSBRStorageUnhealthy, corev1.ConditionTrue)
			}, 3*time.Second, 500*time.Millisecond).Should(BeFalse(), "agent should not set SBRStorageUnhealthy on worker-2")

			By("Simulating worker-2 agent stopping (zero out its slot)")
			slotOffset := int64(worker2ID) * sbdprotocol.SBD_SLOT_SIZE
			_, err = mockHeartbeatDevice.WriteAt(make([]byte, sbdprotocol.SBD_SLOT_SIZE), slotOffset)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for real agent to set SBRStorageUnhealthy on worker-2 (peer timeout ~7s)")
			Eventually(func(g Gomega) bool {
				node := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fenceFlowTargetNode}, node)).To(Succeed())
				return isConditionExist(node.Status.Conditions, medik8sv1alpha1.NodeConditionSBRStorageUnhealthy, corev1.ConditionTrue)
			}, 20*time.Second, 500*time.Millisecond).Should(BeTrue(), "agent should set SBRStorageUnhealthy on worker-2")

			By("Simulating NHC creating StorageBasedRemediation after observing the condition")
			sbr := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{Name: fenceFlowTargetNode, Namespace: "default"},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBRRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 300,
				},
			}
			Expect(k8sClient.Create(ctx, sbr)).To(Succeed())

			By("Verifying controller wrote fence message (controller uses temp devices, so read from fencePath)")
			fenceFile, err := os.Open(fencePath)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = fenceFile.Close() })

			Eventually(func() bool {
				for slotID := uint16(1); slotID <= sbdprotocol.SBD_MAX_NODES; slotID++ {
					slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
					n, err := fenceFile.ReadAt(slotData, int64(slotID)*sbdprotocol.SBD_SLOT_SIZE)
					if err != nil || n < sbdprotocol.SBD_HEADER_SIZE+3 {
						continue
					}
					fenceMsg, err := sbdprotocol.UnmarshalFence(slotData[:n])
					if err != nil {
						continue
					}
					if fenceMsg.Reason == sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT {
						return true
					}
				}
				return false
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(), "controller should write fence message with FENCE_REASON_HEARTBEAT_TIMEOUT")

			By("Waiting for SBRStorageUnhealthy condition to become Unknown (stale age elapsed)")
			Eventually(func(g Gomega) bool {
				node := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fenceFlowTargetNode}, node)).To(Succeed())
				return isConditionExist(node.Status.Conditions, medik8sv1alpha1.NodeConditionSBRStorageUnhealthy, corev1.ConditionUnknown)
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(), "agent should set SBRStorageUnhealthy to Unknown after stale age")

			By("Simulating worker-2 recovering (write heartbeats again)")
			ts2 := uint64(time.Now().UnixNano())
			for round := 0; round < 3; round++ {
				Expect(mockHeartbeatDevice.WritePeerHeartbeat(worker2ID, ts2+uint64(round), uint64(round+100))).To(Succeed())
			}

			By("Waiting for SBRStorageUnhealthy condition to become False (recovered)")
			Eventually(func(g Gomega) bool {
				node := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fenceFlowTargetNode}, node)).To(Succeed())
				return isConditionExist(node.Status.Conditions, medik8sv1alpha1.NodeConditionSBRStorageUnhealthy, corev1.ConditionFalse)
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(), "agent should set SBRStorageUnhealthy to False when peer recovers")
		})
	})

	Context("detect-only mode", func() {
		It("should emit SBRUnhealthyDetectOnly and not SelfFenceInitiated or SBRUnhealthyWatchdogTimeout when SBR is unhealthy", func() {
			tmpDir, sbrPath, _, worker1ID, _ := setupFenceFlowBase()

			By("Creating mock devices and making heartbeat writes fail so SBR becomes unhealthy")
			mockHeartbeatDevice := mocks.NewMockBlockDevice("/tmp/detect-only-heartbeat", 1024*1024)
			mockFenceDevice := mocks.NewMockBlockDevice("/tmp/detect-only-fence", 1024*1024)
			mockHeartbeatDevice.SetFailWrite(true)

			By("Creating mock event recorder and StorageBasedRemediationConfig object for events")
			mockRecorder := mocks.NewMockEventRecorder()
			recorderObject := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "detect-only-test", Namespace: "default"},
			}

			By("Creating real SBR agent in detect-only mode and overriding recorder")
			mockWatchdog := mocks.NewMockWatchdog(filepath.Join(tmpDir, "watchdog"))
			agent, err := NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "worker-1", "test-cluster", worker1ID,
				1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, fenceFlowSBRTimeout, "panic", detectOnlyMetricsPort,
				10*time.Minute, true, 2*time.Second,
				k8sClient, cfg, createManagerPrefix(), true)
			Expect(err).NotTo(HaveOccurred())
			agent.recorder = mockRecorder
			agent.recorderObject = recorderObject
			agent.setSBRDevices(mockHeartbeatDevice, mockFenceDevice)
			startFenceFlowAgent(agent)

			By("Running agent until SBR is marked unhealthy and watchdog loop emits detect-only event (~12s)")
			time.Sleep(12 * time.Second)

			By("Collecting events from mock recorder")
			events := mockRecorder.GetEvents()

			By("Verifying no remediation events: SelfFenceInitiated and SBRUnhealthyWatchdogTimeout must not be emitted")
			Expect(events).NotTo(ContainElement(HaveField(EventFieldReason, Equal(EventReasonSelfFenceInitiated))),
				"detect-only mode must not emit SelfFenceInitiated")
			Expect(events).NotTo(ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthyWatchdogTimeout))),
				"detect-only mode must not emit SBRUnhealthyWatchdogTimeout (watchdog disarmed)")

			By("Verifying SBRUnhealthyDetectOnly was emitted when SBR became unhealthy")
			Expect(events).To(
				ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthyDetectOnly))),
				"expected at least one SBRUnhealthyDetectOnly event when SBR unhealthy in detect-only mode")
		})
	})

	Context("SBR is unhealthy and not in detect-only mode", func() {
		const (
			fenceFlowUnhealthyMetricsPort = 9657
			fenceFlowUnhealthyPrefix      = "fence-flow-unhealthy-"
		)

		var (
			tmpDir                string
			sbrPath               string
			worker1ID, worker2ID  uint16
			mockHeartbeatDevice   *mocks.MockBlockDevice
			mockFenceDevice       *mocks.MockBlockDevice
			mockRecorder          *mocks.MockEventRecorder
			recorderObject        *medik8sv1alpha1.StorageBasedRemediationConfig
			agent                 *SBRAgent
			mockWatchdog          *mocks.MockWatchdog
			petCountWhenUnhealthy int
			controllerNamespace   string
		)

		// setupUnhealthyFenceFlow runs common setup: base env, devices, heartbeats, recorder, agent (with given client and reboot method), start, wait for healthy.
		setupUnhealthyFenceFlow := func(c client.Client, rebootMethod string) {
			tmpDir, sbrPath, _, worker1ID, worker2ID = setupFenceFlowBase()
			By("Writing initial heartbeats for worker-1 and worker-2 on mock devices")
			mockHeartbeatDevice = mocks.NewMockBlockDevice("/tmp/"+fenceFlowUnhealthyPrefix+"heartbeat", 1024*1024)
			mockFenceDevice = mocks.NewMockBlockDevice("/tmp/"+fenceFlowUnhealthyPrefix+"fence", 1024*1024)
			ts := uint64(time.Now().UnixNano())
			for round := 0; round < 3; round++ {
				Expect(mockHeartbeatDevice.WritePeerHeartbeat(worker1ID, ts+uint64(round), uint64(round+1))).To(Succeed())
				Expect(mockHeartbeatDevice.WritePeerHeartbeat(worker2ID, ts+uint64(round), uint64(round+1))).To(Succeed())
			}
			By("Creating mock event recorder and StorageBasedRemediationConfig for event verification")
			mockRecorder = mocks.NewMockEventRecorder()
			recorderObject = &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: fenceFlowUnhealthyPrefix + "config", Namespace: "default"},
			}
			By("Creating real SBR agent (not detect-only) and overriding recorder")
			mockWatchdog = mocks.NewMockWatchdog(filepath.Join(tmpDir, "watchdog"))
			var err error
			agent, err = NewSBRAgentWithWatchdog(mockWatchdog, sbrPath, "worker-1", "test-cluster", worker1ID,
				1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second, fenceFlowSBRTimeout, rebootMethod, fenceFlowUnhealthyMetricsPort,
				10*time.Minute, true, 2*time.Second,
				c, cfg, controllerNamespace, false)
			Expect(err).NotTo(HaveOccurred())
			agent.recorder = mockRecorder
			agent.recorderObject = recorderObject
			agent.setSBRDevices(mockHeartbeatDevice, mockFenceDevice)
			startFenceFlowAgent(agent)
			By("Waiting for agent to pet and SBR to be healthy (writes succeeding)")
			Eventually(func(g Gomega) {
				g.Expect(mockWatchdog.GetPetCount()).To(BeNumerically(">=", 1), "expected at least one pet when SBR healthy")
				g.Expect(agent.isSBRHealthy()).To(BeTrue(), "expected SBR to be healthy after successful writes")
			}, 15*time.Second, 500*time.Millisecond).Should(Succeed())
		}

		makeSBRUnhealthy := func() {
			By("Making heartbeat writes fail so SBR becomes unhealthy after MaxConsecutiveFailures")
			mockHeartbeatDevice.SetFailWrite(true)
			By("Waiting for SBR to become unhealthy (~7s at 1s heartbeat interval)")
			Eventually(func(g Gomega) {
				g.Expect(agent.isSBRHealthy()).To(BeFalse(), "expected SBR to be unhealthy after heartbeat write failures")
				petCountWhenUnhealthy = mockWatchdog.GetPetCount()
			}, 15*time.Second, 500*time.Millisecond).Should(Succeed())
		}

		When("no StorageBasedRemediation CR exists for this node", func() {
			BeforeEach(func() {
				controllerNamespace = createManagerPrefix()
				setupUnhealthyFenceFlow(k8sClient, "panic")
				makeSBRUnhealthy()
			})
			It("should pet watchdog and not trigger self-fence", func() {
				By("Verifying pet continues after SBR is unhealthy (no remediation CR -> agent still pets)")
				Eventually(func(g Gomega) {
					g.Expect(mockWatchdog.GetPetCount()).To(BeNumerically(">", petCountWhenUnhealthy),
						"expected at least one more pet after SBR became unhealthy (no CR path)")
				}, 5*time.Second, 500*time.Millisecond).Should(Succeed())
				By("Verifying fencing did not happen (no SelfFenceInitiated, no SBRUnhealthyWatchdogTimeout)")
				events := mockRecorder.GetEvents()
				Expect(events).NotTo(ContainElement(HaveField(EventFieldReason, Equal(EventReasonSelfFenceInitiated))),
					"fencing must not happen when no CR and agent pets watchdog")
				Expect(events).NotTo(ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthyWatchdogTimeout))),
					"should not emit SBRUnhealthyWatchdogTimeout when we pet to avoid reboot")
			})
		})

		When("StorageBasedRemediation CR exists for this node", func() {
			BeforeEach(func() {
				controllerNamespace = createManagerPrefix()
				By("Creating namespace for remediation CR (CR created before agent, CR created after SBR is healthy)")
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: controllerNamespace}}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })
				setupUnhealthyFenceFlow(k8sClient, RebootMethodNone)
				By("Creating StorageBasedRemediation CR for this node now that SBR is healthy (so agent will trigger self-fence when unhealthy)")
				sbr := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Namespace: controllerNamespace},
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
						Reason:         medik8sv1alpha1.SBRRemediationReasonHeartbeatTimeout,
						TimeoutSeconds: 300,
					},
				}
				Expect(k8sClient.Create(ctx, sbr)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, sbr) })
				makeSBRUnhealthy()
			})
			It("should trigger self-fence and not pet after unhealthy", func() {
				By("Verifying agent does not pet after SBR unhealthy when remediation CR exists (self-fence path)")
				Consistently(func(g Gomega) {
					g.Expect(mockWatchdog.GetPetCount()).To(Equal(petCountWhenUnhealthy),
						"expected no additional pets when SBR unhealthy and agent triggers self-fence")
				}, 5*time.Second, 500*time.Millisecond).Should(Succeed())
				By("Verifying SelfFenceInitiated was emitted (CR exists, trigger self-fence)")
				Expect(mockRecorder.GetEvents()).To(
					ContainElement(HaveField(EventFieldReason, Equal(EventReasonSelfFenceInitiated))),
					"expected SelfFenceInitiated when remediation CR exists and SBR unhealthy")
				By("Verifying SBRUnhealthyWatchdogTimeout was not emitted (self-fence path, not skip-pet path)")
				Expect(mockRecorder.GetEvents()).NotTo(
					ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthyWatchdogTimeout))),
					"should not emit SBRUnhealthyWatchdogTimeout when triggering self-fence for existing CR")
			})
		})

		When("remediation CR check fails (API error)", func() {
			BeforeEach(func() {
				controllerNamespace = createManagerPrefix()
				By("Wrapping k8s client to fail Get(StorageBasedRemediation) for this node (simulate API unreachable)")
				failingClient := &failingRemediationGetClient{
					delegate:      k8sClient,
					failNamespace: controllerNamespace,
					failName:      "worker-1",
					err:           fmt.Errorf("simulated API unreachable"),
				}
				setupUnhealthyFenceFlow(failingClient, RebootMethodNone)
				makeSBRUnhealthy()
			})
			It("should trigger self-fence and emit SBRUnhealthySkipPetAPIError", func() {
				By("Verifying agent does not pet after SBR unhealthy when API check fails (self-fence path)")
				Consistently(func(g Gomega) {
					g.Expect(mockWatchdog.GetPetCount()).To(Equal(petCountWhenUnhealthy),
						"expected no additional pets when SBR unhealthy and we trigger self-fence")
				}, 5*time.Second, 500*time.Millisecond).Should(Succeed())
				By("Verifying SelfFenceInitiated was emitted (fail-safe: when API check fails we trigger self-fence)")
				Expect(mockRecorder.GetEvents()).To(
					ContainElement(HaveField(EventFieldReason, Equal(EventReasonSelfFenceInitiated))),
					"expected SelfFenceInitiated when remediation CR check fails (fail-safe behavior)")
				By("Verifying SBRUnhealthySkipPetAPIError was emitted (on a tick we hit handleWatchdogTickSBRUnhealthy with API error before self-fence)")
				Expect(mockRecorder.GetEvents()).To(
					ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthySkipPetAPIError))),
					"expected SBRUnhealthySkipPetAPIError when remediation CR check fails")
				By("Verifying SBRUnhealthyWatchdogTimeout was not emitted (we use SBRUnhealthySkipPetAPIError for API failure path)")
				Expect(mockRecorder.GetEvents()).NotTo(
					ContainElement(HaveField(EventFieldReason, Equal(EventReasonSBRUnhealthyWatchdogTimeout))),
					"should not emit SBRUnhealthyWatchdogTimeout when we trigger self-fence on API error")
			})
		})
	})
})

func isConditionExist(conditions []corev1.NodeCondition, condType corev1.NodeConditionType, condStatus corev1.ConditionStatus) bool {
	for _, cond := range conditions {
		if cond.Type == condType {
			return cond.Status == condStatus
		}
	}
	return false
}
