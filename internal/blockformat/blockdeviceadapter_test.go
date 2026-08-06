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

package blockformat

import (
	"bytes"
	"io"
	"testing"
)

// Compile-time interface assertion: BlockDeviceAdapter must satisfy the
// same interface shape as mocks.BlockDeviceInterface. We duplicate the
// interface here to avoid importing the mocks package from library code.
var _ interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	Path() string
	IsClosed() bool
} = (*BlockDeviceAdapter)(nil)

// mockClosable satisfies ClosableDevice for testing.
type mockClosable struct {
	path       string
	closed     bool
	closeCount int
}

func (m *mockClosable) Close() error { m.closed = true; m.closeCount++; return nil }
func (m *mockClosable) Path() string { return m.path }
func (m *mockClosable) IsClosed() bool { return m.closed }

func TestBlockDeviceAdapterReadWriteViaOffset(t *testing.T) {
	raw := &mockNoSync{data: make([]byte, 8192)}
	offset := NewOffsetDevice(raw, 4096, 4096)
	backing := &mockClosable{path: "/dev/test"}
	closer := NewSharedCloser(backing)
	adapter := NewBlockDeviceAdapter(offset, closer)

	data := []byte("test data from adapter")
	n, err := adapter.WriteAt(data, 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(data) {
		t.Fatalf("expected %d bytes written, got %d", len(data), n)
	}

	// Verify write went to physical offset 4096
	if !bytes.Equal(raw.data[4096:4096+len(data)], data) {
		t.Error("data not written at expected physical offset")
	}

	// Read back via adapter
	buf := make([]byte, len(data))
	n, err = adapter.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if !bytes.Equal(buf[:n], data) {
		t.Errorf("read mismatch: expected %q, got %q", data, buf[:n])
	}
}

func TestBlockDeviceAdapterDelegatesLifecycle(t *testing.T) {
	raw := &mockNoSync{data: make([]byte, 4096)}
	offset := NewOffsetDevice(raw, 0, 4096)
	backing := &mockClosable{path: "/dev/sdb1"}
	closer := NewSharedCloser(backing)
	adapter := NewBlockDeviceAdapter(offset, closer)

	if adapter.Path() != "/dev/sdb1" {
		t.Errorf("Path() = %q, want /dev/sdb1", adapter.Path())
	}

	if adapter.IsClosed() {
		t.Error("IsClosed() should be false before Close()")
	}

	if err := adapter.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	if !adapter.IsClosed() {
		t.Error("IsClosed() should be true after Close()")
	}

	if !backing.closed {
		t.Error("backing device not closed")
	}
}

func TestBlockDeviceAdapterSyncDelegates(t *testing.T) {
	raw := &mockNoSync{data: make([]byte, 4096)}
	offset := NewOffsetDevice(raw, 0, 4096)
	backing := &mockClosable{path: "/dev/test"}
	closer := NewSharedCloser(backing)
	adapter := NewBlockDeviceAdapter(offset, closer)

	// mockNoSync has no Sync method, so OffsetDevice.Sync() is a no-op
	if err := adapter.Sync(); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}
}

func TestBlockDeviceAdaptersShareClose(t *testing.T) {
	raw := &mockNoSync{data: make([]byte, 8192)}
	backing := &mockClosable{path: "/dev/shared"}
	closer := NewSharedCloser(backing)

	heartbeatOffset := NewOffsetDevice(raw, 0, 4096)
	fenceOffset := NewOffsetDevice(raw, 4096, 4096)

	heartbeatAdapter := NewBlockDeviceAdapter(heartbeatOffset, closer)
	fenceAdapter := NewBlockDeviceAdapter(fenceOffset, closer)

	// Close heartbeat first
	if err := heartbeatAdapter.Close(); err != nil {
		t.Fatalf("heartbeat Close() failed: %v", err)
	}

	// Fence adapter should still report closed (shared backing)
	if !fenceAdapter.IsClosed() {
		t.Error("fence adapter should report closed after heartbeat close")
	}

	// Second close should be a no-op (not panic or error)
	if err := fenceAdapter.Close(); err != nil {
		t.Fatalf("fence Close() should succeed (no-op), got: %v", err)
	}

	// Backing device Close() called exactly once
	if backing.closeCount != 1 {
		t.Errorf("backing Close() called %d times, want 1", backing.closeCount)
	}
}

func TestNewBlockDeviceAdapterPanicsOnNilOffset(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil OffsetDevice")
		}
	}()
	backing := &mockClosable{path: "/dev/test"}
	closer := NewSharedCloser(backing)
	NewBlockDeviceAdapter(nil, closer)
}

func TestNewBlockDeviceAdapterPanicsOnNilBacking(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil SharedCloser")
		}
	}()
	raw := &mockNoSync{data: make([]byte, 4096)}
	offset := NewOffsetDevice(raw, 0, 4096)
	NewBlockDeviceAdapter(offset, nil)
}

func TestNewSharedCloserPanicsOnNilDevice(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil device")
		}
	}()
	NewSharedCloser(nil)
}
