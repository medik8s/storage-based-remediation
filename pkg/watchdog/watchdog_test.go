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

package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/sbd-operator/pkg/retry"
)

// TestNew tests the New function with various scenarios
func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		setup       func() (string, func())
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty path",
			path:        "",
			expectError: true,
			errorMsg:    "watchdog device path cannot be empty",
		},
		{
			name:        "non-existent path",
			path:        "/dev/non-existent-watchdog",
			expectError: true,
			errorMsg:    "failed to open watchdog device",
		},
		{
			name: "valid path with mock file",
			setup: func() (string, func()) {
				// Create a temporary file to simulate a watchdog device
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				return tmpFile, func() {
					os.Remove(tmpFile)
				}
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			var cleanup func()

			if tt.setup != nil {
				path, cleanup = tt.setup()
				defer cleanup()
			} else {
				path = tt.path
			}

			wd, err := New(path)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if wd == nil {
				t.Error("Expected watchdog instance but got nil")
				return
			}

			// Verify watchdog properties
			if !wd.IsOpen() {
				t.Error("Expected watchdog to be open")
			}

			if wd.Path() != path {
				t.Errorf("Expected path %s, got %s", path, wd.Path())
			}

			// Clean up
			if err := wd.Close(); err != nil {
				t.Errorf("Failed to close watchdog: %v", err)
			}
		})
	}
}

// TestPet tests the Pet method with various scenarios
func TestPet(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() *Watchdog
		expectError bool
		errorMsg    string
	}{
		{
			name: "pet closed watchdog",
			setup: func() *Watchdog {
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				wd, err := New(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create watchdog: %v", err)
				}

				// Close the watchdog to test petting a closed device
				wd.Close()
				return wd
			},
			expectError: true,
			errorMsg:    "watchdog device is not open",
		},
		{
			name: "pet with nil file descriptor",
			setup: func() *Watchdog {
				// Create a watchdog with nil file descriptor
				return &Watchdog{
					file:   nil,
					path:   "/test/path",
					isOpen: true, // Mark as open but with nil file
				}
			},
			expectError: true,
			errorMsg:    "watchdog file descriptor is nil",
		},
		{
			name: "pet valid watchdog (ioctl fails, write-based fallback succeeds)",
			setup: func() *Watchdog {
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				wd, err := New(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create watchdog: %v", err)
				}
				return wd
			},
			expectError: false, // Should succeed with write-based fallback
		},
		{
			name: "pet read-only watchdog file (both ioctl and write fail)",
			setup: func() *Watchdog {
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog_readonly")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				// Change to read-only
				err = os.Chmod(tmpFile, 0444)
				if err != nil {
					t.Fatalf("Failed to change file permissions: %v", err)
				}

				// Open as read-only (this will fail for watchdog, but for test purposes)
				file, err = os.OpenFile(tmpFile, os.O_RDONLY, 0)
				if err != nil {
					t.Fatalf("Failed to open read-only file: %v", err)
				}

				wd := &Watchdog{
					file:   file,
					path:   tmpFile,
					isOpen: true,
					logger: logr.Discard(),
					retryConfig: retry.Config{
						MaxRetries:   1,
						InitialDelay: 1 * time.Millisecond,
					},
				}
				return wd
			},
			expectError: true,
			errorMsg:    "failed to pet watchdog", // Both ioctl and write should fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := tt.setup()
			defer func() {
				if wd.IsOpen() {
					wd.Close()
				}
			}()

			err := wd.Pet()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestClose tests the Close method with various scenarios
func TestClose(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() *Watchdog
		expectError bool
		errorMsg    string
	}{
		{
			name: "close already closed watchdog",
			setup: func() *Watchdog {
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				wd, err := New(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create watchdog: %v", err)
				}

				// Close it once first
				wd.Close()
				return wd
			},
			expectError: false, // Should not error on double close
		},
		{
			name: "close watchdog with nil file",
			setup: func() *Watchdog {
				return &Watchdog{
					file:   nil,
					path:   "/test/path",
					isOpen: true,
				}
			},
			expectError: false,
		},
		{
			name: "close valid watchdog",
			setup: func() *Watchdog {
				tmpDir := t.TempDir()
				tmpFile := filepath.Join(tmpDir, "mock_watchdog")
				file, err := os.Create(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create mock file: %v", err)
				}
				file.Close()

				wd, err := New(tmpFile)
				if err != nil {
					t.Fatalf("Failed to create watchdog: %v", err)
				}
				return wd
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := tt.setup()

			err := wd.Close()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}

			// After closing, watchdog should not be open
			if wd.IsOpen() {
				t.Error("Expected watchdog to be closed after Close()")
			}
		})
	}
}

// TestWatchdogProperties tests the IsOpen and Path methods
func TestWatchdogProperties(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "mock_watchdog")

	// Create a mock file
	file, err := os.Create(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create mock file: %v", err)
	}
	file.Close()

	wd, err := New(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create watchdog: %v", err)
	}

	// Test initial state
	if !wd.IsOpen() {
		t.Error("Expected new watchdog to be open")
	}

	if wd.Path() != tmpFile {
		t.Errorf("Expected path %s, got %s", tmpFile, wd.Path())
	}

	// Test after closing
	err = wd.Close()
	if err != nil {
		t.Fatalf("Failed to close watchdog: %v", err)
	}

	if wd.IsOpen() {
		t.Error("Expected watchdog to be closed after Close()")
	}

	// Path should still be available after closing
	if wd.Path() != tmpFile {
		t.Errorf("Expected path %s after close, got %s", tmpFile, wd.Path())
	}
}

// TestWatchdogLifecycle tests the complete lifecycle of a watchdog
func TestWatchdogLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "mock_watchdog")

	// Create a mock file
	file, err := os.Create(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create mock file: %v", err)
	}
	file.Close()

	// Create watchdog
	wd, err := New(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create watchdog: %v", err)
	}

	// Verify initial state
	if !wd.IsOpen() {
		t.Error("Expected new watchdog to be open")
	}

	// Try to pet (ioctl will fail on regular file, but write-based fallback should succeed)
	err = wd.Pet()
	if err != nil {
		t.Errorf("Expected pet to succeed with write-based fallback on regular file, got: %v", err)
	}

	// Close watchdog
	err = wd.Close()
	if err != nil {
		t.Errorf("Failed to close watchdog: %v", err)
	}

	// Verify closed state
	if wd.IsOpen() {
		t.Error("Expected watchdog to be closed")
	}

	// Try to pet closed watchdog
	err = wd.Pet()
	if err == nil {
		t.Error("Expected pet to fail on closed watchdog")
	}
	if !strings.Contains(err.Error(), "watchdog device is not open") {
		t.Errorf("Expected specific error message, got: %s", err.Error())
	}

	// Double close should not error
	err = wd.Close()
	if err != nil {
		t.Errorf("Double close should not error: %v", err)
	}
}

func TestFindWatchdogDevices(t *testing.T) {
	devices, err := findWatchdogDevices()
	if err != nil {
		t.Fatalf("findWatchdogDevices() failed: %v", err)
	}

	// Result should be a slice (may be empty on systems without watchdog devices)
	if devices == nil {
		t.Error("findWatchdogDevices() returned nil, expected empty slice")
	}

	// If we found devices, verify they exist and are character devices
	for _, device := range devices {
		info, err := os.Stat(device)
		if err != nil {
			t.Errorf("Device %s found by findWatchdogDevices() does not exist: %v", device, err)
			continue
		}

		if info.Mode()&os.ModeCharDevice == 0 {
			t.Errorf("Device %s found by findWatchdogDevices() is not a character device", device)
		}
	}

	t.Logf("Found %d watchdog devices: %v", len(devices), devices)
}

func TestIsModuleLoaded(t *testing.T) {
	// Test with a module that should exist on most Linux systems
	loaded := isModuleLoaded("kernel") // The kernel itself is not a module, so this should be false
	t.Logf("Module 'kernel' loaded: %v", loaded)

	// Test with a module that definitely doesn't exist
	loaded = isModuleLoaded("definitely_nonexistent_module_12345")
	if loaded {
		t.Error("isModuleLoaded() returned true for non-existent module")
	}
}

func TestNewWithSoftdogFallback_ValidPath(t *testing.T) {
	// Skip if running in CI/container where modprobe might not be available
	if os.Getenv("CI") == "true" || os.Getenv("CONTAINER") == "true" {
		t.Skip("Skipping softdog test in CI/container environment")
	}

	// Create a temporary directory for testing
	tempDir := t.TempDir()
	fakePath := filepath.Join(tempDir, "nonexistent_watchdog")

	logger := logr.Discard()

	// This should fail to open the fake path, but if no other watchdog devices exist,
	// it might try to load softdog. The behavior depends on the system state.
	wd, err := NewWithSoftdogFallback(fakePath, logger)

	if err != nil {
		// This is expected on systems where:
		// 1. Hardware watchdog devices exist, OR
		// 2. Softdog loading fails (no modprobe, no privileges, etc.)
		t.Logf("NewWithSoftdogFallback failed as expected: %v", err)
		return
	}

	// If we got here, softdog was loaded successfully
	defer wd.Close()

	if !wd.IsSoftdog() {
		t.Error("Expected watchdog to be marked as softdog")
	}

	if wd.Path() == "" {
		t.Error("Watchdog path should not be empty")
	}

	t.Logf("Successfully created softdog watchdog at path: %s", wd.Path())
}

func TestNewWithSoftdogFallback_EmptyPath(t *testing.T) {
	logger := logr.Discard()

	_, err := NewWithSoftdogFallback("", logger)
	if err == nil {
		t.Error("Expected error for empty path")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Expected error message to contain 'empty', got: %v", err)
	}
}

func TestWatchdog_IsSoftdog(t *testing.T) {
	// Test with regular watchdog (should return false)
	wd := &Watchdog{
		path:      "/dev/test",
		isSoftdog: false,
	}

	if wd.IsSoftdog() {
		t.Error("Expected IsSoftdog() to return false for hardware watchdog")
	}

	// Test with softdog (should return true)
	wd.isSoftdog = true
	if !wd.IsSoftdog() {
		t.Error("Expected IsSoftdog() to return true for softdog")
	}
}

// TestLoadSoftdogModule_Integration tests the actual softdog loading functionality
// This test requires root privileges and will be skipped in most environments
func TestLoadSoftdogModule_Integration(t *testing.T) {
	// Skip if not running as root or in CI/container
	if os.Geteuid() != 0 {
		t.Skip("Skipping softdog module loading test (requires root privileges)")
	}

	if os.Getenv("CI") == "true" || os.Getenv("CONTAINER") == "true" {
		t.Skip("Skipping softdog test in CI/container environment")
	}

	logger := logr.Discard()

	// Try to load softdog module in normal mode
	err := loadSoftdogModule(false, logger)
	if err != nil {
		t.Logf("Failed to load softdog module (this may be expected): %v", err)
		return
	}

	// Verify the module was loaded
	if !isModuleLoaded(SoftdogModule) {
		t.Error("Softdog module should be loaded after loadSoftdogModule() succeeds")
	}

	t.Log("Successfully loaded softdog module")
}

// TestLoadSoftdogModule_TestMode tests the softdog loading with test mode enabled
func TestLoadSoftdogModule_TestMode(t *testing.T) {
	// Skip if not running as root or in CI/container
	if os.Geteuid() != 0 {
		t.Skip("Skipping softdog module loading test (requires root privileges)")
	}

	if os.Getenv("CI") == "true" || os.Getenv("CONTAINER") == "true" {
		t.Skip("Skipping softdog test in CI/container environment")
	}

	logger := logr.Discard()

	// Try to load softdog module in test mode
	err := loadSoftdogModule(true, logger)
	if err != nil {
		t.Logf("Failed to load softdog module in test mode (this may be expected): %v", err)
		return
	}

	// Verify the module was loaded
	if !isModuleLoaded(SoftdogModule) {
		t.Error("Softdog module should be loaded after loadSoftdogModule() succeeds")
	}

	t.Log("Successfully loaded softdog module in test mode")
}

// TestNewWithSoftdogFallbackAndTestMode tests the test mode functionality
func TestNewWithSoftdogFallbackAndTestMode(t *testing.T) {
	// Skip if running in CI/container where modprobe might not be available
	if os.Getenv("CI") == "true" || os.Getenv("CONTAINER") == "true" {
		t.Skip("Skipping softdog test in CI/container environment")
	}

	// Create a temporary directory for testing
	tempDir := t.TempDir()
	fakePath := filepath.Join(tempDir, "nonexistent_watchdog")

	logger := logr.Discard()

	// Test with test mode enabled
	wd, err := NewWithSoftdogFallbackAndTestMode(fakePath, true, logger)

	if err != nil {
		// This is expected on systems where:
		// 1. Hardware watchdog devices exist, OR
		// 2. Softdog loading fails (no modprobe, no privileges, etc.)
		t.Logf("NewWithSoftdogFallbackAndTestMode failed as expected: %v", err)
		return
	}

	// If we got here, softdog was loaded successfully
	defer wd.Close()

	if !wd.IsSoftdog() {
		t.Error("Expected watchdog to be marked as softdog")
	}

	if wd.Path() == "" {
		t.Error("Watchdog path should not be empty")
	}

	t.Logf("Successfully created softdog watchdog in test mode at path: %s", wd.Path())
}

// TestBuildNsenterArgs tests the nsenter argument construction
func TestBuildNsenterArgs(t *testing.T) {
	// Test with simple command
	args := buildNsenterArgs("cat", "/proc/modules")
	expected := []string{
		"--target", "1",
		"--mount",
		"--uts",
		"--ipc",
		"--net",
		"--pid",
		"--",
		"cat",
		"/proc/modules",
	}

	if len(args) != len(expected) {
		t.Errorf("Expected %d args, got %d: %v", len(expected), len(args), args)
	}

	for i, arg := range expected {
		if i >= len(args) || args[i] != arg {
			t.Errorf("Expected arg[%d] = %s, got %s", i, arg, args[i])
		}
	}

	// Test with command and multiple arguments
	args = buildNsenterArgs("modprobe", "softdog", "soft_margin=60", "soft_noboot=1")
	expectedCmd := []string{
		"--target", "1",
		"--mount",
		"--uts",
		"--ipc",
		"--net",
		"--pid",
		"--",
		"modprobe",
		"softdog",
		"soft_margin=60",
		"soft_noboot=1",
	}

	if len(args) != len(expectedCmd) {
		t.Errorf("Expected %d args for modprobe, got %d: %v", len(expectedCmd), len(args), args)
	}

	for i, arg := range expectedCmd {
		if i >= len(args) || args[i] != arg {
			t.Errorf("Expected modprobe arg[%d] = %s, got %s", i, arg, args[i])
		}
	}
}
