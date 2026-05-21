//go:build linux

package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// TestReadTimeoutFromSysfsFile tests the sysfs timeout parsing logic, including edge cases.
// This function validates the timeout value read from /sys/class/watchdog/*/timeout,
// ensuring proper error handling for invalid formats, missing files, and edge cases.
func TestReadTimeoutFromSysfsFile(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() (string, func())
		expectError bool
		errorMsg    string
	}{
		// Valid timeout cases
		{
			name: "valid timeout",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("30"), 0644)
				return testFile, func() {}
			},
			expectError: false,
		},

		// File not found
		{
			name: "file not found",
			setup: func() (string, func()) {
				return "/nonexistent/" + SysfsTimeoutFile, func() {}
			},
			expectError: true,
		},

		// Invalid timeout values (<=0 check at lines 47-49 in watchdog_linux.go)
		{
			name: "zero timeout value",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("0"), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "invalid timeout value",
		},
		{
			name: "negative timeout value",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("-10"), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "invalid timeout value",
		},

		// Parsing errors (non-numeric values - lines 42-45 in watchdog_linux.go)
		{
			name: "non-numeric timeout value",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("invalid"), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "failed to parse timeout value",
		},
		{
			name: "empty timeout file",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte(""), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "failed to parse timeout value",
		},
		{
			name: "whitespace only timeout file",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("   \n  "), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "failed to parse timeout value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFile, cleanup := tt.setup()
			defer cleanup()

			wd := &Watchdog{
				logger: logr.Discard(),
			}

			_, err := wd.readTimeoutFromSysfsFile(testFile, SysfsWatchdog0)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
