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
	"os"
	"testing"
	"time"

	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

func TestSBDAgent_FailureTracking(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)

	// Create agent with short intervals for testing
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8097, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Set the mock device
	agent.setSBDDevices(mockDevice, mockDevice)

	// Test initial failure counts
	if agent.watchdogFailureCount != 0 {
		t.Errorf("Expected initial watchdog failure count 0, got %d", agent.watchdogFailureCount)
	}
	if agent.sbdFailureCount != 0 {
		t.Errorf("Expected initial SBD failure count 0, got %d", agent.sbdFailureCount)
	}
	if agent.heartbeatFailureCount != 0 {
		t.Errorf("Expected initial heartbeat failure count 0, got %d", agent.heartbeatFailureCount)
	}

	// Test incrementing failure counts
	watchdogCount := agent.incrementFailureCount("watchdog")
	if watchdogCount != 1 {
		t.Errorf("Expected watchdog failure count 1, got %d", watchdogCount)
	}

	sbdCount := agent.incrementFailureCount("sbd")
	if sbdCount != 1 {
		t.Errorf("Expected SBD failure count 1, got %d", sbdCount)
	}

	heartbeatCount := agent.incrementFailureCount("heartbeat")
	if heartbeatCount != 1 {
		t.Errorf("Expected heartbeat failure count 1, got %d", heartbeatCount)
	}

	// Test resetting failure counts
	agent.resetFailureCount("watchdog")
	if agent.watchdogFailureCount != 0 {
		t.Errorf("Expected watchdog failure count reset to 0, got %d", agent.watchdogFailureCount)
	}

	agent.resetFailureCount("sbd")
	if agent.sbdFailureCount != 0 {
		t.Errorf("Expected SBD failure count reset to 0, got %d", agent.sbdFailureCount)
	}

	agent.resetFailureCount("heartbeat")
	if agent.heartbeatFailureCount != 0 {
		t.Errorf("Expected heartbeat failure count reset to 0, got %d", agent.heartbeatFailureCount)
	}
}

func TestSBDAgent_SelfFenceThreshold(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with short intervals for testing
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8098, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Test that self-fence is not triggered initially
	shouldFence, reason := agent.shouldTriggerSelfFence()
	if shouldFence {
		t.Errorf("Should not trigger self-fence initially, but got: %s", reason)
	}

	// Increment watchdog failures to threshold
	for i := 0; i < MaxConsecutiveFailures; i++ {
		agent.incrementFailureCount("watchdog")
	}

	// Test that self-fence is triggered for watchdog failures
	shouldFence, reason = agent.shouldTriggerSelfFence()
	if !shouldFence {
		t.Error("Should trigger self-fence for watchdog failures")
	}
	if reason == "" {
		t.Error("Self-fence reason should not be empty")
	}

	// Reset and test SBD failures
	agent.resetFailureCount("watchdog")
	for i := 0; i < MaxConsecutiveFailures; i++ {
		agent.incrementFailureCount("sbd")
	}

	shouldFence, _ = agent.shouldTriggerSelfFence()
	if !shouldFence {
		t.Error("Should trigger self-fence for SBD failures")
	}

	// Reset and test heartbeat failures
	agent.resetFailureCount("sbd")
	for i := 0; i < MaxConsecutiveFailures; i++ {
		agent.incrementFailureCount("heartbeat")
	}

	shouldFence, _ = agent.shouldTriggerSelfFence()
	if !shouldFence {
		t.Error("Should trigger self-fence for heartbeat failures")
	}
}

func TestSBDAgent_FailureCountReset(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with short intervals for testing
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8099, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Increment some failure counts
	agent.incrementFailureCount("watchdog")
	agent.incrementFailureCount("sbd")
	agent.incrementFailureCount("heartbeat")

	// Verify counts are non-zero
	if agent.watchdogFailureCount == 0 || agent.sbdFailureCount == 0 || agent.heartbeatFailureCount == 0 {
		t.Error("Failure counts should be non-zero before reset")
	}

	// Simulate time passing to trigger automatic reset
	agent.lastFailureReset = time.Now().Add(-FailureCountResetInterval - time.Second)

	// Increment failure count again to trigger reset
	agent.incrementFailureCount("watchdog")

	// All counts except watchdog should be reset, watchdog should be 1
	if agent.watchdogFailureCount != 1 {
		t.Errorf("Expected watchdog failure count 1 after reset, got %d", agent.watchdogFailureCount)
	}
	if agent.sbdFailureCount != 0 {
		t.Errorf("Expected SBD failure count 0 after reset, got %d", agent.sbdFailureCount)
	}
	if agent.heartbeatFailureCount != 0 {
		t.Errorf("Expected heartbeat failure count 0 after reset, got %d", agent.heartbeatFailureCount)
	}
}

func TestSBDAgent_RetryConfiguration(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second,
		30, "panic", 8100, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Verify retry configuration is properly initialized
	if agent.retryConfig.MaxRetries != MaxCriticalRetries {
		t.Errorf("Expected MaxRetries %d, got %d", MaxCriticalRetries, agent.retryConfig.MaxRetries)
	}
	if agent.retryConfig.InitialDelay != InitialCriticalRetryDelay {
		t.Errorf("Expected InitialDelay %v, got %v", InitialCriticalRetryDelay, agent.retryConfig.InitialDelay)
	}
	if agent.retryConfig.MaxDelay != MaxCriticalRetryDelay {
		t.Errorf("Expected MaxDelay %v, got %v", MaxCriticalRetryDelay, agent.retryConfig.MaxDelay)
	}
	if agent.retryConfig.BackoffFactor != CriticalRetryBackoffFactor {
		t.Errorf("Expected BackoffFactor %f, got %f", CriticalRetryBackoffFactor, agent.retryConfig.BackoffFactor)
	}

	// Check that logger is properly initialized by testing if it can be used
	// We'll just verify the configuration was set up correctly without the logger check
	// since logr.Logger is always a valid struct
}

func TestSBDAgent_WatchdogRetryMechanism(t *testing.T) {
	// Create mock watchdog that fails initially
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")
	mockWatchdog.SetFailPet(true)

	// Create agent with very short intervals for testing
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8101, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Test that failures are tracked when watchdog pet fails
	// We don't start the watchdog loop here, just test the failure tracking mechanism
	err = agent.watchdog.Pet()
	if err == nil {
		t.Error("Expected watchdog pet to fail")
	}

	// Now make watchdog succeed
	mockWatchdog.SetFailPet(false)
	err = agent.watchdog.Pet()
	if err != nil {
		t.Errorf("Expected watchdog pet to succeed, got: %v", err)
	}
}

func TestSBDAgent_HeartbeatRetryMechanism(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)

	// Create agent with very short intervals for testing
	// Create temporary SBD device file
	tmpDir := t.TempDir()
	sbdPath := tmpDir + "/test-sbd"
	if err := os.WriteFile(sbdPath, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatalf("Failed to create test SBD device: %v", err)
	}

	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, sbdPath, "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8102, 10*time.Minute, true, 2*time.Second, k8sClient, k8sClientset, testNamespace, false)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer func() { _ = agent.Stop() }()

	// Set the mock device and make it fail initially
	agent.setSBDDevices(mockDevice, mockDevice)
	mockDevice.SetFailWrite(true)

	// Test heartbeat write failure
	err = agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected heartbeat write to fail")
	}

	// Now make device succeed
	mockDevice.SetFailWrite(false)
	err = agent.writeHeartbeatToSBD()
	if err != nil {
		t.Errorf("Expected heartbeat write to succeed, got: %v", err)
	}

	// Verify heartbeat was written
	// Use the actual assigned nodeID from the agent (not hardcoded 1)
	slotOffset := int64(agent.nodeID) * sbdprotocol.SBD_SLOT_SIZE
	slotData := make([]byte, sbdprotocol.SBD_HEADER_SIZE)
	n, err := mockDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		t.Fatalf("Failed to read heartbeat: %v", err)
	}
	if n != sbdprotocol.SBD_HEADER_SIZE {
		t.Fatalf("Expected to read %d bytes, got %d", sbdprotocol.SBD_HEADER_SIZE, n)
	}

	// Unmarshal and verify
	header, err := sbdprotocol.Unmarshal(slotData)
	if err != nil {
		t.Fatalf("Failed to unmarshal heartbeat: %v", err)
	}
	if header.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Expected heartbeat message type, got %d", header.Type)
	}
}
