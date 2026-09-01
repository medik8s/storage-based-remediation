/*
Copyright 2026.

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
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"golang.org/x/sys/unix"
)

func TestNewReopenDevice(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	if dev.Path() != path {
		t.Errorf("expected path %q, got %q", path, dev.Path())
	}
	if dev.IsClosed() {
		t.Error("expected device to be open")
	}
}

func TestNewReopenDeviceEmptyPath(t *testing.T) {
	_, err := NewReopenDevice("", logr.Discard())
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReopenDeviceWriteAndRead(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	testData := []byte("hello reopen device")
	n, err := dev.WriteAt(testData, 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("wrote %d bytes, expected %d", n, len(testData))
	}

	buf := make([]byte, len(testData))
	n, err = dev.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Errorf("read data mismatch: expected %q, got %q", testData, buf[:n])
	}
}

func TestReopenDeviceReadsExternalModification(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Write initial data via the device
	testData := []byte("initial data")
	if _, err := dev.WriteAt(testData, 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Modify the file externally (on a local filesystem this proves the
	// reopen path sees subsequent modifications; on NFS it would exercise
	// CTO revalidation, but that requires an integration test)
	externalData := []byte("modified!!")
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("external open failed: %v", err)
	}
	if _, err := f.WriteAt(externalData, 0); err != nil {
		t.Fatalf("external write failed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("external close failed: %v", err)
	}

	// ReadAt should see the externally modified data because it reopens
	buf := make([]byte, len(externalData))
	n, err := dev.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if string(buf[:n]) != string(externalData) {
		t.Errorf("expected externally modified data %q, got %q", externalData, buf[:n])
	}
}

func TestReopenDeviceClosedOperations(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}

	if err := dev.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !dev.IsClosed() {
		t.Error("expected device to be closed")
	}

	// Double close should be safe
	if err := dev.Close(); err != nil {
		t.Errorf("double Close should not error: %v", err)
	}

	// Operations on closed device should fail
	buf := make([]byte, 10)
	if _, err := dev.ReadAt(buf, 0); err == nil {
		t.Error("ReadAt on closed device should fail")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should mention 'closed', got: %v", err)
	}

	if _, err := dev.WriteAt(buf, 0); err == nil {
		t.Error("WriteAt on closed device should fail")
	}

	if err := dev.Sync(); err == nil {
		t.Error("Sync on closed device should fail")
	}
}

func TestReopenDeviceNegativeOffset(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	buf := make([]byte, 10)
	if _, err := dev.ReadAt(buf, -1); err == nil {
		t.Error("expected error for negative read offset")
	}
	if _, err := dev.WriteAt(buf, -1); err == nil {
		t.Error("expected error for negative write offset")
	}
}

func TestReopenDeviceConcurrentSafety(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Write initial data
	if _, err := dev.WriteAt([]byte("concurrent test data!"), 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Concurrent reads and writes should not race
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			buf := make([]byte, 20)
			_, _ = dev.ReadAt(buf, 0)
		}()
		go func() {
			defer wg.Done()
			_, _ = dev.WriteAt([]byte("concurrent test data!"), 0)
		}()
	}
	wg.Wait()

	// Verify device is still functional
	if dev.IsClosed() {
		t.Error("device should still be open after concurrent operations")
	}
}

func TestReopenDeviceInvokesFadvise(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Write data so reads succeed
	if _, err := dev.WriteAt([]byte("fadvise test data!!!"), 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Replace fadvise with a counter
	var calls atomic.Int32
	dev.fadvise = func(fd int, offset int64, length int64, advice int) error {
		if advice != unix.FADV_DONTNEED {
			t.Errorf("expected FADV_DONTNEED (%d), got %d", unix.FADV_DONTNEED, advice)
		}
		calls.Add(1)
		return nil
	}

	// Each ReadAt should invoke fadvise once
	buf := make([]byte, 20)
	for i := 0; i < 3; i++ {
		if _, err := dev.ReadAt(buf, 0); err != nil {
			t.Fatalf("ReadAt %d failed: %v", i, err)
		}
	}

	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 fadvise calls, got %d", got)
	}
}

func TestReopenDeviceFadviseContinuesOnError(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	if _, err := dev.WriteAt([]byte("fadvise error test!!"), 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// fadvise failure should not prevent the read from succeeding
	dev.fadvise = func(fd int, offset int64, length int64, advice int) error {
		return fmt.Errorf("injected fadvise error")
	}

	buf := make([]byte, 20)
	n, err := dev.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt should succeed despite fadvise error: %v", err)
	}
	if n != 20 {
		t.Errorf("expected 20 bytes, got %d", n)
	}
}
