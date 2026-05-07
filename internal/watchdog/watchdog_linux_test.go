//go:build linux

package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// TestReadTimeoutFromSysfsFile tests the sysfs timeout parsing logic
func TestReadTimeoutFromSysfsFile(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() (string, func())
		expectError bool
		errorMsg    string
	}{
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
		{
			name: "file not found",
			setup: func() (string, func()) {
				return "/nonexistent/" + SysfsTimeoutFile, func() {}
			},
			expectError: true,
		},
		{
			name: "invalid timeout value",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, SysfsTimeoutFile)
				_ = os.WriteFile(testFile, []byte("0"), 0644)
				return testFile, func() {}
			},
			expectError: true,
			errorMsg:    "invalid timeout value",
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
