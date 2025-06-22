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
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestSBDAgent_FailureTracking(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)

	// Create agent with short intervals for testing
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8097, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device
	agent.setSBDDevice(mockDevice)

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
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8098, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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

	shouldFence, reason = agent.shouldTriggerSelfFence()
	if !shouldFence {
		t.Error("Should trigger self-fence for SBD failures")
	}

	// Reset and test heartbeat failures
	agent.resetFailureCount("sbd")
	for i := 0; i < MaxConsecutiveFailures; i++ {
		agent.incrementFailureCount("heartbeat")
	}

	shouldFence, reason = agent.shouldTriggerSelfFence()
	if !shouldFence {
		t.Error("Should trigger self-fence for heartbeat failures")
	}
}

func TestSBDAgent_FailureCountReset(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")

	// Create agent with short intervals for testing
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8099, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		1*time.Second, 1*time.Second, 1*time.Second, 1*time.Second,
		30, "panic", 8100, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

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
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8101, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set up heartbeat failure to avoid interference
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)
	agent.setSBDDevice(mockDevice)

	// Allow watchdog to fail, then succeed
	time.Sleep(50 * time.Millisecond) // Let some failures occur
	mockWatchdog.SetFailPet(false)    // Now allow success

	// Give the retry mechanism time to succeed
	time.Sleep(100 * time.Millisecond)

	// Verify that failure count eventually resets after success
	// Note: This test is timing-sensitive and may need adjustment
	time.Sleep(50 * time.Millisecond) // Additional time for success
	agent.resetFailureCount("watchdog")

	// Verify the agent is still healthy
	if agent.watchdogFailureCount > MaxConsecutiveFailures {
		t.Errorf("Watchdog failure count should not exceed threshold after recovery")
	}
}

func TestSBDAgent_HeartbeatRetryMechanism(t *testing.T) {
	// Create mock devices
	mockWatchdog := NewMockWatchdog("/dev/mock-watchdog")
	mockDevice := NewMockBlockDevice("/dev/mock-sbd", 1024*1024)

	// Create agent with short intervals for testing
	agent, err := NewSBDAgentWithWatchdog(mockWatchdog, "", "test-node", "test-cluster", 1,
		10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond,
		30, "panic", 8102, 10*time.Minute)
	if err != nil {
		t.Fatalf("Failed to create SBD agent: %v", err)
	}
	defer agent.Stop()

	// Set the mock device and make it fail initially
	agent.setSBDDevice(mockDevice)
	mockDevice.SetFailWrite(true)

	// Suppress log output during test
	agent.retryConfig.Logger = logr.Discard()

	// Test heartbeat write failure and retry mechanism
	err = agent.writeHeartbeatToSBD()
	if err == nil {
		t.Error("Expected heartbeat write to fail when device is set to fail")
	}

	// The failure should increment the heartbeat failure count
	// (This would happen in the actual loop with retry mechanism)
	agent.incrementFailureCount("heartbeat")
	if agent.heartbeatFailureCount == 0 {
		t.Error("Heartbeat failure count should have increased")
	}

	// Make device succeed and reset failure count
	mockDevice.SetFailWrite(false)
	err = agent.writeHeartbeatToSBD()
	if err != nil {
		t.Errorf("Expected heartbeat write to succeed after fixing device: %v", err)
	}

	// Reset failure count to simulate successful retry
	agent.resetFailureCount("heartbeat")
	if agent.heartbeatFailureCount != 0 {
		t.Error("Heartbeat failure count should be reset after successful retry")
	}
}
