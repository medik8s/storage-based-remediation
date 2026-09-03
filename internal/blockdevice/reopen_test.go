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
	"io"
	"os"
	"os/exec"
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

// TestReopenDeviceFcntlLockingProbe verifies that fcntl locking is probed at
// startup and enabled on local filesystems (which support POSIX locks).
// Note: this only proves local locking works, not distributed NFS locking.
func TestReopenDeviceFcntlLockingProbe(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Local filesystems (tmpfs, ext4, xfs) support fcntl locking,
	// so the probe should have enabled it
	if dev.fcntlFlock == nil {
		t.Error("expected fcntl locking to be enabled on local filesystem")
	}
}

// TestReopenDeviceFcntlLockOnRead verifies that ReadAt acquires a shared lock.
// The lock is released implicitly when the read fd is closed (no explicit
// unlock on the read path).
func TestReopenDeviceFcntlLockOnRead(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Write data so reads succeed
	if _, err := dev.WriteAt([]byte("fcntl read lock test"), 0); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Track lock calls
	type lockCall struct {
		cmd      int
		lockType int16
		start    int64
		length   int64
	}
	var calls []lockCall
	var callsMu sync.Mutex

	dev.fcntlFlock = func(fd uintptr, cmd int, lk *unix.Flock_t) error {
		callsMu.Lock()
		calls = append(calls, lockCall{
			cmd:      cmd,
			lockType: lk.Type,
			start:    lk.Start,
			length:   lk.Len,
		})
		callsMu.Unlock()
		return nil
	}

	buf := make([]byte, 20)
	if _, err := dev.ReadAt(buf, 100); err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}

	callsMu.Lock()
	defer callsMu.Unlock()

	// Read path: lock only, no explicit unlock (fd close releases it)
	if len(calls) != 1 {
		t.Fatalf("expected 1 fcntl call (lock only, unlock via close), got %d", len(calls))
	}

	if calls[0].cmd != unix.F_SETLKW {
		t.Errorf("lock call: expected F_SETLKW (%d), got %d", unix.F_SETLKW, calls[0].cmd)
	}
	if calls[0].lockType != unix.F_RDLCK {
		t.Errorf("lock call: expected F_RDLCK (%d), got %d", unix.F_RDLCK, calls[0].lockType)
	}
	if calls[0].start != 100 {
		t.Errorf("lock call: expected start=100, got %d", calls[0].start)
	}
	if calls[0].length != 20 {
		t.Errorf("lock call: expected length=20, got %d", calls[0].length)
	}
}

// TestReopenDeviceFcntlLockOnWrite verifies that WriteAt acquires and releases
// an exclusive lock.
func TestReopenDeviceFcntlLockOnWrite(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	type lockCall struct {
		cmd      int
		lockType int16
		start    int64
		length   int64
	}
	var calls []lockCall
	var callsMu sync.Mutex

	dev.fcntlFlock = func(fd uintptr, cmd int, lk *unix.Flock_t) error {
		callsMu.Lock()
		calls = append(calls, lockCall{
			cmd:      cmd,
			lockType: lk.Type,
			start:    lk.Start,
			length:   lk.Len,
		})
		callsMu.Unlock()
		return nil
	}

	data := []byte("exclusive write test")
	if _, err := dev.WriteAt(data, 512); err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	callsMu.Lock()
	defer callsMu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 fcntl calls (lock + unlock), got %d", len(calls))
	}

	// First call: acquire exclusive lock
	if calls[0].lockType != unix.F_WRLCK {
		t.Errorf("lock call: expected F_WRLCK (%d), got %d", unix.F_WRLCK, calls[0].lockType)
	}
	if calls[0].start != 512 {
		t.Errorf("lock call: expected start=512, got %d", calls[0].start)
	}
	if calls[0].length != int64(len(data)) {
		t.Errorf("lock call: expected length=%d, got %d", len(data), calls[0].length)
	}

	// Second call: release lock
	if calls[1].lockType != unix.F_UNLCK {
		t.Errorf("unlock call: expected F_UNLCK (%d), got %d", unix.F_UNLCK, calls[1].lockType)
	}
}

// TestReopenDeviceFcntlDisabled verifies that when fcntl is nil (unsupported
// filesystem), reads and writes work normally without locking.
func TestReopenDeviceFcntlDisabled(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Disable locking to simulate unsupported filesystem
	dev.fcntlFlock = nil

	testData := []byte("no locking test data")
	n, err := dev.WriteAt(testData, 0)
	if err != nil {
		t.Fatalf("WriteAt without locking failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("wrote %d bytes, expected %d", n, len(testData))
	}

	buf := make([]byte, len(testData))
	n, err = dev.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt without locking failed: %v", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Errorf("data mismatch: expected %q, got %q", testData, buf[:n])
	}
}

// TestReopenDeviceFcntlLockFailureIsError verifies that when locking was
// enabled at startup (fcntlFlock != nil), a runtime lock failure returns an
// error rather than silently degrading. This distinguishes "locking
// unsupported" (detected at startup) from "locking unexpectedly failed"
// (runtime error that should not be masked).
func TestReopenDeviceFcntlLockFailureIsError(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	// Inject a failing fcntl — simulates runtime lock failure
	dev.fcntlFlock = func(fd uintptr, cmd int, lk *unix.Flock_t) error {
		if lk.Type == unix.F_UNLCK {
			return nil // unlock still works
		}
		return fmt.Errorf("injected fcntl error")
	}

	// WriteAt should fail because the lock cannot be acquired
	if _, err := dev.WriteAt([]byte("lock failure test!!"), 0); err == nil {
		t.Error("WriteAt should fail when lock acquisition fails")
	} else if !strings.Contains(err.Error(), "failed to acquire write lock") {
		t.Errorf("error should mention lock failure, got: %v", err)
	}

	// ReadAt should also fail
	buf := make([]byte, 20)
	if _, err := dev.ReadAt(buf, 0); err == nil {
		t.Error("ReadAt should fail when lock acquisition fails")
	} else if !strings.Contains(err.Error(), "failed to acquire read lock") {
		t.Errorf("error should mention lock failure, got: %v", err)
	}
}

// TestReopenDeviceZeroLengthSkipsLocking verifies that zero-length reads and
// writes skip locking entirely. This is critical because fcntl Len=0 means
// "lock from Start to EOF", not "lock zero bytes".
func TestReopenDeviceZeroLengthSkipsLocking(t *testing.T) {
	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	dev, err := NewReopenDevice(path, logr.Discard())
	if err != nil {
		t.Fatalf("NewReopenDevice failed: %v", err)
	}
	defer dev.Close()

	var lockCalls atomic.Int32
	dev.fcntlFlock = func(fd uintptr, cmd int, lk *unix.Flock_t) error {
		lockCalls.Add(1)
		return nil
	}

	// Zero-length read
	n, err := dev.ReadAt([]byte{}, 0)
	if err != nil {
		t.Errorf("zero-length ReadAt should succeed: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}

	// Zero-length write
	n, err = dev.WriteAt([]byte{}, 0)
	if err != nil {
		t.Errorf("zero-length WriteAt should succeed: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}

	// No lock calls should have been made
	if got := lockCalls.Load(); got != 0 {
		t.Errorf("expected 0 lock calls for zero-length I/O, got %d", got)
	}
}

// TestReopenDeviceCloseReleasesLock verifies that closing the read fd
// actually releases the POSIX lock at the OS level, not just at the mock
// level. This uses a real fcntl lock and a subprocess to verify cross-process
// lock behavior (POSIX locks are process-scoped, so lock contention only
// manifests between different processes, not between goroutines).
//
// The test:
// 1. Opens a file and acquires an exclusive lock
// 2. Spawns a subprocess that tries F_SETLK (non-blocking) on the same range
// 3. Verifies the subprocess is blocked (lock is held)
// 4. Closes the fd
// 5. Verifies the subprocess can now acquire the lock (lock was released)
func TestReopenDeviceCloseReleasesLock(t *testing.T) {
	// When run as the subprocess helper, try to acquire a lock and report
	if os.Getenv("SBR_LOCK_HELPER") == "1" {
		path := os.Getenv("SBR_LOCK_PATH")
		// Must open O_RDWR because F_WRLCK requires write access on the fd
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open failed: %v", err)
			os.Exit(2)
		}
		defer f.Close()

		lk := unix.Flock_t{
			Type:   unix.F_WRLCK,
			Whence: int16(io.SeekStart),
			Start:  0,
			Len:    10,
		}
		// F_SETLK (non-blocking): returns EAGAIN/EACCES if lock is held
		// by another process. Must not be EBADF (wrong fd mode).
		err = unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
		if err != nil {
			fmt.Print("LOCKED")
		} else {
			fmt.Print("FREE")
			// Release the lock we just acquired
			lk.Type = unix.F_UNLCK
			_ = unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
		}
		os.Exit(0)
	}

	path, cleanup := setupTestDevice(t, 4096)
	defer cleanup()

	// Step 1: acquire exclusive lock on the file
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}

	lk := unix.Flock_t{
		Type:   unix.F_WRLCK,
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    10,
	}
	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
		f.Close()
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Step 2: subprocess should see the lock is held
	helper := helperCmd(t, path)
	out, err := helper.Output()
	if err != nil {
		f.Close()
		t.Fatalf("helper failed: %v", err)
	}
	if string(out) != "LOCKED" {
		f.Close()
		t.Fatalf("expected subprocess to see LOCKED, got %q", out)
	}

	// Step 3: close the fd — should release the lock
	f.Close()

	// Step 4: subprocess should now see the lock is free
	helper2 := helperCmd(t, path)
	out2, err := helper2.Output()
	if err != nil {
		t.Fatalf("helper2 failed: %v", err)
	}
	if string(out2) != "FREE" {
		t.Errorf("expected subprocess to see FREE after close, got %q", out2)
	}
}

// helperCmd creates a subprocess that runs this test binary with
// SBR_LOCK_HELPER=1 to test cross-process lock behavior.
func helperCmd(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestReopenDeviceCloseReleasesLock")
	cmd.Env = append(os.Environ(),
		"SBR_LOCK_HELPER=1",
		"SBR_LOCK_PATH="+path,
	)
	return cmd
}
