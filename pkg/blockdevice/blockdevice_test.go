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

package blockdevice

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// testingInterface defines the common interface between *testing.T and *testing.B
type testingInterface interface {
	Helper()
	TempDir() string
	Fatalf(format string, args ...interface{})
}

// setupTestDevice creates a temporary file to simulate a block device for testing
func setupTestDevice(t testingInterface, size int64) (string, func()) {
	t.Helper()

	// Create a temporary directory
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "test-device")

	// Create a test file with specific size
	file, err := os.Create(testPath)
	if err != nil {
		t.Fatalf("Failed to create test device file: %v", err)
	}

	// Initialize the file with zeros to the specified size
	if size > 0 {
		if err := file.Truncate(size); err != nil {
			t.Fatalf("Failed to set test device size: %v", err)
		}
	}

	if err := file.Close(); err != nil {
		t.Fatalf("Failed to close test device file: %v", err)
	}

	// Return path and cleanup function
	return testPath, func() {
		_ = os.RemoveAll(tempDir)
	}
}

func TestOpen(t *testing.T) {
	tests := []struct {
		name        string
		setupDevice bool
		devicePath  string
		expectError bool
		errorSubstr string
	}{
		{
			name:        "valid device path",
			setupDevice: true,
			expectError: false,
		},
		{
			name:        "empty device path",
			setupDevice: false,
			devicePath:  "",
			expectError: true,
			errorSubstr: "device path cannot be empty",
		},
		{
			name:        "non-existent device path",
			setupDevice: false,
			devicePath:  "/non/existent/device",
			expectError: true,
			errorSubstr: "failed to open block device",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var testPath string
			var cleanup func()

			if tt.setupDevice {
				testPath, cleanup = setupTestDevice(t, 1024)
				defer cleanup()
			} else {
				testPath = tt.devicePath
			}

			device, err := Open(testPath)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorSubstr) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorSubstr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if device == nil {
				t.Errorf("Expected device to be non-nil")
				return
			}

			// Verify device properties
			if device.Path() != testPath {
				t.Errorf("Expected device path %q, got %q", testPath, device.Path())
			}

			if device.IsClosed() {
				t.Errorf("Expected device to be open")
			}

			// Clean up
			if err := device.Close(); err != nil {
				t.Errorf("Failed to close device: %v", err)
			}
		})
	}
}

func TestReadAt(t *testing.T) {
	// Create test device with known data
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	// Write test data to the file
	testData := []byte("Hello, World! This is test data for block device operations.")
	file, err := os.OpenFile(testPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("Failed to open test file for writing: %v", err)
	}
	if _, err := file.Write(testData); err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}
	_ = file.Close()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	tests := []struct {
		name        string
		bufSize     int
		offset      int64
		expectError bool
		expectData  string
		errorSubstr string
	}{
		{
			name:       "read from beginning",
			bufSize:    13,
			offset:     0,
			expectData: "Hello, World!",
		},
		{
			name:       "read from middle",
			bufSize:    4,
			offset:     7,
			expectData: "Worl",
		},
		{
			name:       "read beyond data (should get partial)",
			bufSize:    5,
			offset:     int64(len(testData) - 5),
			expectData: "ions.",
		},
		{
			name:        "negative offset",
			bufSize:     10,
			offset:      -1,
			expectError: true,
			errorSubstr: "negative offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, tt.bufSize)
			n, err := device.ReadAt(buf, tt.offset)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorSubstr) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorSubstr, err)
				}
				return
			}

			if err != nil && err != io.EOF {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			actualData := string(buf[:n])
			if actualData != tt.expectData {
				t.Errorf("Expected data %q, got %q", tt.expectData, actualData)
			}
		})
	}
}

func TestReadAtClosedDevice(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}

	// Close the device
	if err := device.Close(); err != nil {
		t.Fatalf("Failed to close device: %v", err)
	}

	// Try to read from closed device
	buf := make([]byte, 10)
	_, err = device.ReadAt(buf, 0)
	if err == nil {
		t.Errorf("Expected error when reading from closed device")
	} else if !strings.Contains(err.Error(), "is closed") {
		t.Errorf("Expected error to contain 'is closed', got: %v", err)
	}
}

func TestWriteAt(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	tests := []struct {
		name        string
		data        string
		offset      int64
		expectError bool
		errorSubstr string
	}{
		{
			name:   "write to beginning",
			data:   "Hello, World!",
			offset: 0,
		},
		{
			name:   "write to middle",
			data:   "Test",
			offset: 100,
		},
		{
			name:   "overwrite existing data",
			data:   "Overwrite",
			offset: 0,
		},
		{
			name:        "negative offset",
			data:        "Test",
			offset:      -1,
			expectError: true,
			errorSubstr: "negative offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.data)
			n, err := device.WriteAt(data, tt.offset)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorSubstr) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorSubstr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if n != len(data) {
				t.Errorf("Expected to write %d bytes, wrote %d", len(data), n)
			}

			// Verify the data was written correctly
			buf := make([]byte, len(data))
			readN, err := device.ReadAt(buf, tt.offset)
			if err != nil && err != io.EOF {
				t.Errorf("Failed to read back written data: %v", err)
			}

			if readN != len(data) {
				t.Errorf("Expected to read %d bytes, read %d", len(data), readN)
			}

			if string(buf[:readN]) != tt.data {
				t.Errorf("Expected to read back %q, got %q", tt.data, string(buf[:readN]))
			}
		})
	}
}

func TestWriteAtClosedDevice(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}

	// Close the device
	if err := device.Close(); err != nil {
		t.Fatalf("Failed to close device: %v", err)
	}

	// Try to write to closed device
	data := []byte("test")
	_, err = device.WriteAt(data, 0)
	if err == nil {
		t.Errorf("Expected error when writing to closed device")
	} else if !strings.Contains(err.Error(), "is closed") {
		t.Errorf("Expected error to contain 'is closed', got: %v", err)
	}
}

func TestSync(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Write some data
	data := []byte("Test data for sync")
	if _, err := device.WriteAt(data, 0); err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}

	// Test sync operation
	if err := device.Sync(); err != nil {
		t.Errorf("Sync operation failed: %v", err)
	}
}

func TestSyncClosedDevice(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}

	// Close the device
	if err := device.Close(); err != nil {
		t.Fatalf("Failed to close device: %v", err)
	}

	// Try to sync closed device
	err = device.Sync()
	if err == nil {
		t.Errorf("Expected error when syncing closed device")
	} else if !strings.Contains(err.Error(), "is closed") {
		t.Errorf("Expected error to contain 'is closed', got: %v", err)
	}
}

func TestClose(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}

	// Device should be open initially
	if device.IsClosed() {
		t.Errorf("Expected device to be open initially")
	}

	// Close the device
	if err := device.Close(); err != nil {
		t.Errorf("Failed to close device: %v", err)
	}

	// Device should be closed now
	if !device.IsClosed() {
		t.Errorf("Expected device to be closed after Close()")
	}

	// Closing again should be safe (no-op)
	if err := device.Close(); err != nil {
		t.Errorf("Second close should not return error: %v", err)
	}

	// Device should still be closed
	if !device.IsClosed() {
		t.Errorf("Expected device to remain closed after second Close()")
	}
}

func TestDeviceString(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}

	// Test string representation when open
	str := device.String()
	if !strings.Contains(str, testPath) {
		t.Errorf("Expected string to contain device path %q, got: %s", testPath, str)
	}
	if !strings.Contains(str, "open") {
		t.Errorf("Expected string to contain 'open', got: %s", str)
	}

	// Close device and test string representation when closed
	device.Close()
	str = device.String()
	if !strings.Contains(str, testPath) {
		t.Errorf("Expected string to contain device path %q, got: %s", testPath, str)
	}
	if !strings.Contains(str, "closed") {
		t.Errorf("Expected string to contain 'closed', got: %s", str)
	}
}

func TestDevicePath(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 1024)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	if device.Path() != testPath {
		t.Errorf("Expected device path %q, got %q", testPath, device.Path())
	}
}

func TestReadWriteIntegration(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 2048)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Test data with different patterns
	testCases := []struct {
		data   string
		offset int64
	}{
		{"Pattern 1: Beginning of device", 0},
		{"Pattern 2: Middle section", 512},
		{"Pattern 3: Near end", 1900},
	}

	// Write all test patterns
	for _, tc := range testCases {
		data := []byte(tc.data)
		n, err := device.WriteAt(data, tc.offset)
		if err != nil {
			t.Errorf("Failed to write at offset %d: %v", tc.offset, err)
		}
		if n != len(data) {
			t.Errorf("Expected to write %d bytes at offset %d, wrote %d", len(data), tc.offset, n)
		}
	}

	// Sync to ensure data is written
	if err := device.Sync(); err != nil {
		t.Errorf("Failed to sync: %v", err)
	}

	// Read back and verify all test patterns
	for _, tc := range testCases {
		buf := make([]byte, len(tc.data))
		n, err := device.ReadAt(buf, tc.offset)
		if err != nil && err != io.EOF {
			t.Errorf("Failed to read at offset %d: %v", tc.offset, err)
		}
		if n != len(tc.data) {
			t.Errorf("Expected to read %d bytes at offset %d, read %d", len(tc.data), tc.offset, n)
		}
		if string(buf[:n]) != tc.data {
			t.Errorf("Data mismatch at offset %d: expected %q, got %q", tc.offset, tc.data, string(buf[:n]))
		}
	}
}

func TestConcurrentOperations(t *testing.T) {
	testPath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Test concurrent reads and writes
	done := make(chan bool, 4)

	// Concurrent writes
	for i := 0; i < 2; i++ {
		go func(id int) {
			defer func() { done <- true }()

			data := []byte(strings.Repeat("A", 100))
			offset := int64(id * 1000)

			for j := 0; j < 10; j++ {
				if _, err := device.WriteAt(data, offset+int64(j*100)); err != nil {
					t.Errorf("Concurrent write %d failed: %v", id, err)
					return
				}
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 2; i++ {
		go func(id int) {
			defer func() { done <- true }()

			buf := make([]byte, 100)
			offset := int64(id * 1000)

			for j := 0; j < 10; j++ {
				if _, err := device.ReadAt(buf, offset+int64(j*100)); err != nil && err != io.EOF {
					t.Errorf("Concurrent read %d failed: %v", id, err)
					return
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 4; i++ {
		<-done
	}
}

func BenchmarkReadAt(b *testing.B) {
	testPath, cleanup := setupTestDevice(b, 1024*1024) // 1MB device
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		b.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	buf := make([]byte, 4096) // 4KB buffer

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := int64(i % 256 * 4096) // Rotate through different offsets
		_, err := device.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			b.Fatalf("Read failed: %v", err)
		}
	}
}

func BenchmarkWriteAt(b *testing.B) {
	testPath, cleanup := setupTestDevice(b, 1024*1024) // 1MB device
	defer cleanup()

	device, err := Open(testPath)
	if err != nil {
		b.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	data := make([]byte, 4096) // 4KB buffer
	for i := range data {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := int64(i % 256 * 4096) // Rotate through different offsets
		_, err := device.WriteAt(data, offset)
		if err != nil {
			b.Fatalf("Write failed: %v", err)
		}
	}
}

func TestOpenWithTimeout(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	tests := []struct {
		name        string
		timeout     time.Duration
		expectError bool
		errorSubstr string
	}{
		{
			name:        "valid timeout - 30 seconds",
			timeout:     30 * time.Second,
			expectError: false,
		},
		{
			name:        "valid timeout - 5 seconds (minimum)",
			timeout:     5 * time.Second,
			expectError: false,
		},
		{
			name:        "valid timeout - 5 minutes",
			timeout:     5 * time.Minute,
			expectError: false,
		},
		{
			name:        "invalid timeout - zero",
			timeout:     0,
			expectError: true,
			errorSubstr: "I/O timeout must be positive",
		},
		{
			name:        "invalid timeout - negative",
			timeout:     -1 * time.Second,
			expectError: true,
			errorSubstr: "I/O timeout must be positive",
		},
		{
			name:        "invalid timeout - too short",
			timeout:     500 * time.Millisecond,
			expectError: true,
			errorSubstr: "I/O timeout",
		},
		{
			name:        "invalid timeout - too long",
			timeout:     15 * time.Minute,
			expectError: true,
			errorSubstr: "I/O timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device, err := OpenWithTimeout(devicePath, tt.timeout, logr.Discard())

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
					if device != nil {
						device.Close()
					}
					return
				}
				if tt.errorSubstr != "" && !strings.Contains(err.Error(), tt.errorSubstr) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorSubstr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if device == nil {
				t.Errorf("Expected valid device, got nil")
				return
			}

			// Verify the device works with custom timeout
			testData := []byte("test data")
			n, err := device.WriteAt(testData, 0)
			if err != nil {
				t.Errorf("Failed to write with custom timeout: %v", err)
			}
			if n != len(testData) {
				t.Errorf("Expected to write %d bytes, wrote %d", len(testData), n)
			}

			device.Close()
		})
	}
}
