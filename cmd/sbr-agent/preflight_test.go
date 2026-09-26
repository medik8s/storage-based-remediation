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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mocks "github.com/medik8s/storage-based-remediation/v5/internal/mocks"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

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
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, false)
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
	err := runPreflightChecks(watchdogPath, "", "test-node", 1, false)
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
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, false)
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
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, false)
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
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, false)
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
	err := runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, false)
	if err == nil {
		t.Error("Expected pre-flight checks to fail with both watchdog and SBR device missing, but they succeeded")
		return
	}

	// Should mention both failures
	if !strings.Contains(err.Error(), "both watchdog device and SBR device are inaccessible") {
		t.Errorf("Expected error about both devices being inaccessible, but got: %v", err)
	}
}

// TestPreflightChecks_DetectOnlyMode tests that watchdog check is skipped in detect-only mode
func TestPreflightChecks_DetectOnlyMode(t *testing.T) {
	// Initialize logger for tests
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create temporary SBR device file
	tmpDir := t.TempDir()
	sbrPath := filepath.Join(tmpDir, "sbr-device")
	sbrFile, err := os.Create(sbrPath)
	if err != nil {
		t.Fatalf("Failed to create mock SBR file: %v", err)
	}
	_ = sbrFile.Close()

	// Use non-existent watchdog path - this would normally fail
	watchdogPath := nonExistentWatchdogPath

	// Test pre-flight checks with detect-only mode enabled
	// Should PASS even though watchdog device is missing, because:
	// 1. detect-only mode skips watchdog check
	// 2. SBR device is accessible
	err = runPreflightChecks(watchdogPath, sbrPath, "test-node", 1, true)
	if err != nil {
		t.Errorf("Expected pre-flight checks to succeed in detect-only mode with missing watchdog, but got error: %v", err)
	}
}

func TestPerformSBRFenceReadWriteTest_PreservesLiveSlots(t *testing.T) {
	device := mocks.NewMockBlockDevice("fence", 2*(sbdprotocol.SBD_MAX_NODES+1)*sbdprotocol.SBD_SLOT_SIZE)
	live := make([]byte, (sbdprotocol.SBD_MAX_NODES+1)*sbdprotocol.SBD_SLOT_SIZE)
	for nodeID := uint16(1); nodeID <= sbdprotocol.SBD_MAX_NODES; nodeID++ {
		message, err := sbdprotocol.MarshalFence(sbdprotocol.NewFence(1, nodeID, 42, sbdprotocol.FENCE_REASON_MANUAL))
		if err != nil {
			t.Fatal(err)
		}
		copy(live[int(nodeID)*sbdprotocol.SBD_SLOT_SIZE:], message)
	}
	if _, err := device.WriteAt(live, 0); err != nil {
		t.Fatal(err)
	}

	for _, nodeID := range []uint16{1, 2, sbdprotocol.SBD_MAX_NODES} {
		t.Run(fmt.Sprint(nodeID), func(t *testing.T) {
			if err := performSBRFenceReadWriteTest(device, nodeID); err != nil {
				t.Fatal(err)
			}
			actual := make([]byte, len(live))
			if _, err := device.ReadAt(actual, 0); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, live) {
				t.Fatal("fence probe changed live fence slots")
			}
		})
	}

	// Each probe must remain intact after the other nodes have probed.
	for _, nodeID := range []uint16{1, 2, sbdprotocol.SBD_MAX_NODES} {
		data := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
		offset := int64(sbdprotocol.SBD_MAX_NODES+nodeID) * sbdprotocol.SBD_SLOT_SIZE
		if _, err := device.ReadAt(data, offset); err != nil {
			t.Fatal(err)
		}
		message, err := sbdprotocol.UnmarshalFence(data)
		if err != nil {
			t.Fatal(err)
		}
		if message.TargetNodeID != nodeID {
			t.Fatalf("probe for node %d was overwritten by node %d", nodeID, message.TargetNodeID)
		}
	}
}
