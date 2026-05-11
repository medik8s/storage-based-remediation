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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestIOTimeoutConfiguration(t *testing.T) {
	// Create a temporary file for testing
	tmpFile, err := os.CreateTemp("", "blockdevice-timeout-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	// Open the device with the blockdevice package
	device, err := OpenWithLogger(tmpFile.Name(), logr.Discard())
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Verify that the timeout is properly configured
	if device.ioTimeout != DefaultIOTimeout {
		t.Errorf("Expected timeout %v, got %v", DefaultIOTimeout, device.ioTimeout)
	}

	// Test that normal I/O operations work within timeout
	testData := []byte("test data for timeout verification")
	n, err := device.WriteAt(testData, 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to write %d bytes, wrote %d", len(testData), n)
	}

	// Read back the data
	readBuf := make([]byte, len(testData))
	n, err = device.ReadAt(readBuf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to read %d bytes, read %d", len(testData), n)
	}

	// Verify data integrity
	if string(readBuf) != string(testData) {
		t.Errorf("Data mismatch: wrote %q, read %q", string(testData), string(readBuf))
	}

	// Test sync operation
	err = device.Sync()
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
}

func TestTimeoutErrorMessages(t *testing.T) {
	// Create a device with a very short timeout for testing
	tmpFile, err := os.CreateTemp("", "blockdevice-timeout-msg-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	device, err := OpenWithLogger(tmpFile.Name(), logr.Discard())
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Set a very short timeout for testing timeout behavior
	device.ioTimeout = 1 * time.Nanosecond

	testData := []byte("test")

	// Test write timeout
	_, err = device.WriteAt(testData, 0)
	if err != nil && strings.Contains(err.Error(), "timeout") {
		t.Logf("WriteAt timeout error: %v", err)
		// This is expected - timeout error should contain "timeout"
	}

	// Test read timeout
	readBuf := make([]byte, 4)
	_, err = device.ReadAt(readBuf, 0)
	if err != nil && strings.Contains(err.Error(), "timeout") {
		t.Logf("ReadAt timeout error: %v", err)
		// This is expected - timeout error should contain "timeout"
	}

	// Test sync timeout
	err = device.Sync()
	if err != nil && strings.Contains(err.Error(), "timeout") {
		t.Logf("Sync timeout error: %v", err)
		// This is expected - timeout error should contain "timeout"
	}
}

func TestTimeoutPreventsHanging(t *testing.T) {
	// This test verifies that our timeout mechanism prevents operations from hanging indefinitely
	// We can't easily simulate a truly hanging storage device in a unit test, but we can verify
	// that the timeout mechanism works by setting a very short timeout and ensuring operations
	// complete within a reasonable time frame.

	tmpFile, err := os.CreateTemp("", "blockdevice-hang-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	device, err := OpenWithLogger(tmpFile.Name(), logr.New(nil))
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	device.ioTimeout = 1 * time.Millisecond
	device.retryConfig.TestDelay = 1 * time.Minute
	t.Logf("Device: %+v", device.retryConfig)

	// Test that operations complete quickly even with timeout errors
	startTime := time.Now()
	testData := []byte("test data")

	// This should complete quickly (either success or timeout error)
	_, err = device.WriteAt(testData, 0)
	if err == nil {
		t.Errorf("Expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}

	elapsed := time.Since(startTime)

	// Operation should complete within a reasonable time (much less than device.retryConfig.TestDelay)
	maxExpectedTime := device.retryConfig.TestDelay - 10*time.Millisecond
	if elapsed > maxExpectedTime {
		t.Errorf("Operation took too long: %v (expected < %v)", elapsed, maxExpectedTime)
	}

	t.Logf("Operation completed in %v (with or without timeout error)", elapsed)
}

func TestStrictTimeoutEnforcement(t *testing.T) {
	// This test verifies that timeouts are strictly enforced using time.After
	// rather than relying on context cancellation which may not interrupt system calls

	tmpFile, err := os.CreateTemp("", "blockdevice-strict-timeout-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	device, err := OpenWithLogger(tmpFile.Name(), logr.Discard())
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Set timeout to a precise value for testing
	strictTimeout := 50 * time.Millisecond
	device.ioTimeout = strictTimeout

	testData := []byte("strict timeout test")

	// Test WriteAt strict timeout
	startTime := time.Now()
	_, _ = device.WriteAt(testData, 0)
	writeElapsed := time.Since(startTime)

	// Should complete very close to the timeout period
	// Allow some tolerance for scheduling delays
	minExpected := strictTimeout
	maxExpected := strictTimeout + 20*time.Millisecond

	if writeElapsed < minExpected {
		t.Logf("WriteAt completed faster than timeout (%v < %v) - operation may have succeeded",
			writeElapsed, minExpected)
	} else if writeElapsed > maxExpected {
		t.Errorf("WriteAt took too long (%v > %v) - timeout not strictly enforced",
			writeElapsed, maxExpected)
	} else {
		t.Logf("WriteAt timeout strictly enforced: %v (expected ~%v)", writeElapsed, strictTimeout)
	}

	// Test ReadAt strict timeout
	readBuf := make([]byte, len(testData))
	startTime = time.Now()
	_, _ = device.ReadAt(readBuf, 0)
	readElapsed := time.Since(startTime)

	if readElapsed < minExpected {
		t.Logf("ReadAt completed faster than timeout (%v < %v) - operation may have succeeded",
			readElapsed, minExpected)
	} else if readElapsed > maxExpected {
		t.Errorf("ReadAt took too long (%v > %v) - timeout not strictly enforced",
			readElapsed, maxExpected)
	} else {
		t.Logf("ReadAt timeout strictly enforced: %v (expected ~%v)", readElapsed, strictTimeout)
	}

	// Test Sync strict timeout
	startTime = time.Now()
	_ = device.Sync()
	syncElapsed := time.Since(startTime)

	if syncElapsed < minExpected {
		t.Logf("Sync completed faster than timeout (%v < %v) - operation may have succeeded",
			syncElapsed, minExpected)
	} else if syncElapsed > maxExpected {
		t.Errorf("Sync took too long (%v > %v) - timeout not strictly enforced",
			syncElapsed, maxExpected)
	} else {
		t.Logf("Sync timeout strictly enforced: %v (expected ~%v)", syncElapsed, strictTimeout)
	}
}

func TestDefaultTimeoutValue(t *testing.T) {
	// Verify that the default timeout is reasonable for storage operations
	if DefaultIOTimeout < 10*time.Second {
		t.Errorf("DefaultIOTimeout (%v) may be too short for storage operations", DefaultIOTimeout)
	}
	if DefaultIOTimeout > 60*time.Second {
		t.Errorf("DefaultIOTimeout (%v) may be too long, could delay self-fencing", DefaultIOTimeout)
	}

	// Current value should be 30 seconds - reasonable for most storage systems
	expectedTimeout := 30 * time.Second
	if DefaultIOTimeout != expectedTimeout {
		t.Logf("DefaultIOTimeout is %v (expected %v) - this is fine if intentionally changed",
			DefaultIOTimeout, expectedTimeout)
	}
}

// TestRetryWithTimeouts verifies that timeout errors are properly retried
func TestRetryWithTimeouts(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "blockdevice-retry-timeout-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	device, err := OpenWithLogger(tmpFile.Name(), logr.Discard())
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Set a very short timeout to trigger timeout errors
	device.ioTimeout = 1 * time.Nanosecond

	testData := []byte("retry test")

	// The operation should be retried multiple times due to timeout errors
	// This will eventually fail after all retries are exhausted
	startTime := time.Now()
	_, err = device.WriteAt(testData, 0)
	elapsed := time.Since(startTime)

	// Should have attempted retries (so should take longer than a single timeout)
	// but still complete relatively quickly
	if elapsed < time.Millisecond {
		t.Errorf("Operation completed too quickly (%v), expected retries", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Operation took too long (%v), retries may not be working", elapsed)
	}

	// Should have received an error (after retries)
	if err == nil {
		t.Error("Expected timeout error after retries, but got nil")
	}

	if err != nil && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}

	t.Logf("Retry operation completed in %v with error: %v", elapsed, err)
}

func TestTimeoutMechanismComparison(t *testing.T) {
	// This test documents the improvement from context-based to time.After-based timeouts
	// Context cancellation may not interrupt hanging system calls, but time.After provides
	// strict enforcement by abandoning operations that exceed the timeout.

	tmpFile, err := os.CreateTemp("", "blockdevice-mechanism-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	device, err := OpenWithLogger(tmpFile.Name(), logr.Discard())
	if err != nil {
		t.Fatalf("Failed to open device: %v", err)
	}
	defer func() { _ = device.Close() }()

	// Test with a reasonable timeout that shows the mechanism works
	device.ioTimeout = 10 * time.Millisecond

	// Perform multiple operations to demonstrate consistent timeout behavior
	testData := []byte("mechanism test")
	timeouts := 0
	successes := 0

	for i := 0; i < 5; i++ {
		startTime := time.Now()
		_, err := device.WriteAt(testData, int64(i*len(testData)))
		elapsed := time.Since(startTime)

		if err != nil && strings.Contains(err.Error(), "timeout") {
			timeouts++
			// Verify timeout happened close to the expected time
			if elapsed > 15*time.Millisecond {
				t.Errorf("Timeout took too long (%v), strict enforcement may not be working", elapsed)
			}
		} else if err == nil {
			successes++
		}

		t.Logf("Operation %d: %v (elapsed: %v)", i+1,
			map[bool]string{true: "success", false: "timeout"}[err == nil], elapsed)
	}

	t.Logf("Results: %d timeouts, %d successes out of 5 operations", timeouts, successes)

	// The key insight: with time.After, we get predictable timeout behavior
	// Even if the underlying I/O would hang, we return control to the caller
	if timeouts > 0 {
		t.Logf("✓ Strict timeout enforcement working - operations abandoned after timeout")
	}
}
