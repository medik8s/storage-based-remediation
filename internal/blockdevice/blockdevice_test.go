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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sys/unix"
)

// syncOpener opens block devices without O_DIRECT for unit tests.
type syncOpener struct{}

func (syncOpener) Open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_SYNC, 0)
}

func init() {
	// Production opens with O_DIRECT so reads bypass the page cache on real block
	// devices. Unit tests use regular temp files (tmpfs) with small, unaligned
	// ReadAt/WriteAt calls, which return EINVAL under O_DIRECT on many kernels
	// (notably CI). Use syncOpener so tests exercise I/O, timeout, and retry
	// logic without a real block device or 512-byte-aligned I/O.
	DeviceOpener = syncOpener{}
}

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
	_ = device.Close()
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

func TestOpenBuffered(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	// OpenBuffered should work the same as OpenWithTimeout but use
	// bufferedOpener (no O_DIRECT). Since unit tests already override
	// DeviceOpener, we verify the function signature and I/O work.
	device, err := OpenBuffered(devicePath, 5*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("OpenBuffered failed: %v", err)
	}
	defer device.Close()

	// Write and read back
	testData := []byte("buffered test data")
	n, err := device.WriteAt(testData, 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("expected to write %d bytes, wrote %d", len(testData), n)
	}

	readBuf := make([]byte, len(testData))
	n, err = device.ReadAt(readBuf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if string(readBuf[:n]) != string(testData) {
		t.Errorf("read data mismatch: expected %q, got %q", testData, readBuf[:n])
	}
}

func TestOpenBufferedInvokesFadvise(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	device, err := OpenBuffered(devicePath, 5*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("OpenBuffered failed: %v", err)
	}
	defer device.Close()

	// Verify OpenBuffered configured the production fadvise
	if device.fadvise == nil {
		t.Fatal("expected fadvise to be configured for OpenBuffered device")
	}

	// Inject a recording fadvise to verify ReadAt calls it
	var calls []fadviseCall
	device.fadvise = func(fd int, offset int64, length int64, advice int) error {
		calls = append(calls, fadviseCall{fd: fd, offset: offset, length: length, advice: advice})
		return nil
	}

	// Write then read — fadvise should be called on the read
	testData := []byte("hello")
	if _, err := device.WriteAt(testData, 100); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	buf := make([]byte, len(testData))
	if _, err := device.ReadAt(buf, 100); err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 fadvise call, got %d", len(calls))
	}

	c := calls[0]

	// Verify the advice is FADV_DONTNEED
	if c.advice != unix.FADV_DONTNEED {
		t.Errorf("fadvise advice: got %d, want FADV_DONTNEED (%d)", c.advice, unix.FADV_DONTNEED)
	}

	// Verify the range is page-aligned
	pageSize := int64(os.Getpagesize())
	if c.offset%pageSize != 0 {
		t.Errorf("fadvise offset %d is not page-aligned (page size %d)", c.offset, pageSize)
	}
	if c.length%pageSize != 0 {
		t.Errorf("fadvise length %d is not page-aligned (page size %d)", c.length, pageSize)
	}
	// The aligned range must cover the original [100, 105) range
	if c.offset > 100 {
		t.Errorf("fadvise offset %d does not cover requested offset 100", c.offset)
	}
	if c.offset+c.length < 105 {
		t.Errorf("fadvise range [%d, %d) does not cover requested end 105",
			c.offset, c.offset+c.length)
	}
}

func TestOpenWithTimeoutDoesNotInvokeFadvise(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	device, err := OpenWithTimeout(devicePath, 5*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("OpenWithTimeout failed: %v", err)
	}
	defer device.Close()

	// O_DIRECT devices must have fadvise=nil so ReadAt skips cache eviction
	if device.fadvise != nil {
		t.Fatal("expected fadvise=nil for OpenWithTimeout device")
	}

	// Verify ReadAt works without fadvise (no panic, no error)
	buf := make([]byte, 10)
	if _, err := device.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt failed on non-buffered device: %v", err)
	}
}

func TestFadviseContinuesOnError(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	device, err := OpenBuffered(devicePath, 5*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("OpenBuffered failed: %v", err)
	}
	defer device.Close()

	// Write data first
	testData := []byte("test data")
	if _, err := device.WriteAt(testData, 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Inject a failing fadvise — read should still succeed
	device.fadvise = func(fd int, offset int64, length int64, advice int) error {
		return fmt.Errorf("simulated fadvise failure")
	}

	buf := make([]byte, len(testData))
	n, err := device.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt should succeed even when fadvise fails: %v", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Errorf("read data mismatch: expected %q, got %q", testData, buf[:n])
	}
}

func TestPageAlignedRange(t *testing.T) {
	pageSize := int64(os.Getpagesize())

	tests := []struct {
		name       string
		off        int64
		length     int64
		wantOff    int64
		wantLength int64
		wantErr    bool
	}{
		{
			name:       "already aligned",
			off:        0,
			length:     pageSize,
			wantOff:    0,
			wantLength: pageSize,
		},
		{
			name:       "offset mid-page",
			off:        100,
			length:     512,
			wantOff:    0,
			wantLength: pageSize,
		},
		{
			name:       "spans two pages",
			off:        pageSize - 10,
			length:     20,
			wantOff:    0,
			wantLength: 2 * pageSize,
		},
		{
			name:       "zero length at page boundary",
			off:        pageSize,
			length:     0,
			wantOff:    pageSize,
			wantLength: 0,
		},
		{
			name:       "large offset",
			off:        pageSize*100 + 123,
			length:     1,
			wantOff:    pageSize * 100,
			wantLength: pageSize,
		},
		{
			name:    "negative length",
			off:     0,
			length:  -1,
			wantErr: true,
		},
		{
			name:    "overflow off+length",
			off:     1<<62 + 1,
			length:  1<<62 + 1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOff, gotLen, err := pageAlignedRange(tt.off, tt.length)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotOff != tt.wantOff {
				t.Errorf("offset: got %d, want %d", gotOff, tt.wantOff)
			}
			if gotLen != tt.wantLength {
				t.Errorf("length: got %d, want %d", gotLen, tt.wantLength)
			}
			// Invariant: aligned range must cover original range
			if gotOff > tt.off {
				t.Errorf("aligned offset %d > original offset %d", gotOff, tt.off)
			}
			if tt.length > 0 && gotOff+gotLen < tt.off+tt.length {
				t.Errorf("aligned end %d < original end %d", gotOff+gotLen, tt.off+tt.length)
			}
			// Invariant: both must be page-aligned
			if gotOff%pageSize != 0 {
				t.Errorf("aligned offset %d not page-aligned", gotOff)
			}
			if gotLen > 0 && gotLen%pageSize != 0 {
				t.Errorf("aligned length %d not page-aligned", gotLen)
			}
		})
	}
}

type fadviseCall struct {
	fd     int
	offset int64
	length int64
	advice int
}

func TestOpenBufferedValidation(t *testing.T) {
	// Verify OpenBuffered shares the same validation as OpenWithTimeout
	_, err := OpenBuffered("", 5*time.Second, logr.Discard())
	if err == nil {
		t.Error("expected error for empty path")
	}

	_, err = OpenBuffered("/nonexistent", 0, logr.Discard())
	if err == nil {
		t.Error("expected error for zero timeout")
	}

	_, err = OpenBuffered("/nonexistent", 500*time.Millisecond, logr.Discard())
	if err == nil {
		t.Error("expected error for timeout too short")
	}
}

func TestBufferedOpenerNoODirect(t *testing.T) {
	// Verify that bufferedOpener creates a valid file handle (no O_DIRECT flag).
	// On temp files, O_DIRECT with unaligned buffers would fail; bufferedOpener
	// must work with any buffer alignment.
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	bo := bufferedOpener{}
	f, err := bo.Open(devicePath)
	if err != nil {
		t.Fatalf("bufferedOpener.Open failed: %v", err)
	}
	defer f.Close()

	// Write an unaligned buffer size (3 bytes) — would fail with O_DIRECT
	// on a real block device but must succeed with buffered I/O.
	_, err = f.WriteAt([]byte("abc"), 0)
	if err != nil {
		t.Fatalf("unaligned write via bufferedOpener failed: %v", err)
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
						_ = device.Close()
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

			_ = device.Close()
		})
	}
}

// TestValidateBlockModeStorage_ODirectNotSet verifies that the F_GETFL check
// detects when O_DIRECT is not set on the file descriptor.
func TestValidateBlockModeStorage_ODirectNotSet(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	// Open without O_DIRECT — simulates a code bug where the open path
	// accidentally omits O_DIRECT.
	dev, err := OpenBuffered(devicePath, 5*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("OpenBuffered failed: %v", err)
	}
	defer dev.Close()

	err = DefaultStorageChecker.ValidateBlockModeStorage(dev.File(), devicePath)
	if err == nil {
		t.Fatal("should reject fd without O_DIRECT")
	}
	if !strings.Contains(err.Error(), "O_DIRECT is not set") {
		t.Fatalf("error should mention O_DIRECT, got: %v", err)
	}
}

// TestBlockModeFilesystemUnsupported_BlacklistedTypes verifies that the
// production blocklist rejects NFS, CIFS, and FUSE filesystem types.
func TestBlockModeFilesystemUnsupported_BlacklistedTypes(t *testing.T) {
	tests := []struct {
		name     string
		fsType   int64
		wantName string
	}{
		{"NFS", int64(unix.NFS_SUPER_MAGIC), "NFS"},
		{"CIFS", int64(unix.CIFS_SUPER_MAGIC), "CIFS"},
		{"FUSE", int64(unix.FUSE_SUPER_MAGIC), "FUSE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, unsupported := blockModeFilesystemUnsupported(tt.fsType)
			if !unsupported {
				t.Fatalf("expected %s (0x%x) to be unsupported", tt.name, tt.fsType)
			}
			if !strings.Contains(name, tt.wantName) {
				t.Fatalf("expected name to contain %q, got %q", tt.wantName, name)
			}
		})
	}
}

// TestBlockModeFilesystemUnsupported_NotBlacklistedTypes verifies that
// filesystem types not in the blocklist are allowed through. This does not
// imply these filesystems are validated for block-mode correctness.
func TestBlockModeFilesystemUnsupported_NotBlacklistedTypes(t *testing.T) {
	tests := []struct {
		name   string
		fsType int64
	}{
		{"ext4", int64(unix.EXT4_SUPER_MAGIC)},
		{"xfs", int64(unix.XFS_SUPER_MAGIC)},
		{"tmpfs", int64(unix.TMPFS_MAGIC)},
		{"btrfs", int64(unix.BTRFS_SUPER_MAGIC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, unsupported := blockModeFilesystemUnsupported(tt.fsType)
			if unsupported {
				t.Fatalf("%s (0x%x) should not be rejected", tt.name, tt.fsType)
			}
		})
	}
}

// TestValidateBlockModeStorage_SuccessfulPath verifies that the full checker
// accepts a file descriptor that has O_DIRECT set and is backed by a
// compatible filesystem. This exercises the sequential F_GETFL → fstatfs path.
func TestValidateBlockModeStorage_SuccessfulPath(t *testing.T) {
	devicePath, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	// Open with O_DIRECT via the production directOpener path.
	// On tmpfs the fstatfs check will see TMPFS_MAGIC, which is not in
	// the blocklist, so the full checker should accept it.
	//
	// Note: DeviceOpener is overridden to syncOpener in test init(), so
	// we call directOpener explicitly to get a real O_DIRECT fd.
	do := directOpener{}
	f, err := do.Open(devicePath)
	if err != nil {
		t.Skipf("O_DIRECT not supported on test filesystem, skipping: %v", err)
	}
	defer f.Close()

	err = DefaultStorageChecker.ValidateBlockModeStorage(f, devicePath)
	if err != nil {
		t.Fatalf("expected successful validation, got: %v", err)
	}
}
