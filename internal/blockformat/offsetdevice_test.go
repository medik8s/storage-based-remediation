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
	"os"
	"path/filepath"
	"testing"
)

func createFileDevice(t *testing.T, size int64) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "offset-test")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()

	f, err = os.OpenFile(path, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		t.Fatalf("failed to reopen: %v", err)
	}
	return f
}

func TestOffsetDeviceReadWriteTranslation(t *testing.T) {
	f := createFileDevice(t, 8192)
	defer f.Close()

	baseOffset := int64(4096)
	od := NewOffsetDevice(f, baseOffset, 4096)

	// Write at offset 0 in OffsetDevice → physical offset 4096
	data := []byte("hello offset device")
	n, err := od.WriteAt(data, 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(data) {
		t.Fatalf("expected %d bytes written, got %d", len(data), n)
	}

	// Read back via OffsetDevice
	buf := make([]byte, len(data))
	n, err = od.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if !bytes.Equal(buf[:n], data) {
		t.Errorf("data mismatch: expected %q, got %q", data, buf[:n])
	}

	// Verify physical location via raw file
	raw := make([]byte, len(data))
	if _, err := f.ReadAt(raw, baseOffset); err != nil {
		t.Fatalf("raw ReadAt failed: %v", err)
	}
	if !bytes.Equal(raw, data) {
		t.Errorf("physical data mismatch: expected %q, got %q", data, raw)
	}

	// Verify offset 0 (before baseOffset) is still zeroes
	before := make([]byte, len(data))
	if _, err := f.ReadAt(before, 0); err != nil {
		t.Fatalf("raw ReadAt at 0 failed: %v", err)
	}
	if !bytes.Equal(before, make([]byte, len(data))) {
		t.Error("data leaked before baseOffset")
	}
}

func TestOffsetDeviceBoundsCheck(t *testing.T) {
	f := createFileDevice(t, 8192)
	defer f.Close()

	od := NewOffsetDevice(f, 0, 100) // 100-byte region

	// Write within bounds
	data := make([]byte, 50)
	if _, err := od.WriteAt(data, 0); err != nil {
		t.Fatalf("write within bounds failed: %v", err)
	}

	// Write exactly at boundary
	if _, err := od.WriteAt(data, 50); err != nil {
		t.Fatalf("write at exact boundary failed: %v", err)
	}

	// Write exceeding bounds
	if _, err := od.WriteAt(data, 51); err == nil {
		t.Error("expected error for write exceeding region, got nil")
	}

	// Read exceeding bounds
	buf := make([]byte, 50)
	if _, err := od.ReadAt(buf, 51); err == nil {
		t.Error("expected error for read exceeding region, got nil")
	}
}

func TestOffsetDeviceUnbounded(t *testing.T) {
	f := createFileDevice(t, 8192)
	defer f.Close()

	// regionSize 0 = unbounded
	od := NewOffsetDevice(f, 1000, 0)

	data := []byte("unbounded write")
	if _, err := od.WriteAt(data, 5000); err != nil {
		t.Fatalf("unbounded write failed: %v", err)
	}

	buf := make([]byte, len(data))
	if _, err := od.ReadAt(buf, 5000); err != nil {
		t.Fatalf("unbounded read failed: %v", err)
	}
	if !bytes.Equal(buf, data) {
		t.Errorf("data mismatch: expected %q, got %q", data, buf)
	}
}

func TestOffsetDeviceNegativeOffset(t *testing.T) {
	f := createFileDevice(t, 4096)
	defer f.Close()

	od := NewOffsetDevice(f, 0, 4096)

	if _, err := od.ReadAt(make([]byte, 10), -1); err == nil {
		t.Error("expected error for negative read offset")
	}
	if _, err := od.WriteAt(make([]byte, 10), -1); err == nil {
		t.Error("expected error for negative write offset")
	}
}

func TestOffsetDeviceSyncDelegation(t *testing.T) {
	f := createFileDevice(t, 4096)
	defer f.Close()

	od := NewOffsetDevice(f, 0, 4096)

	// os.File implements Sync, so this should delegate
	if err := od.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
}

// mockNoSync is a DeviceReadWriterAt without Sync support.
type mockNoSync struct {
	data []byte
}

func (m *mockNoSync) ReadAt(p []byte, off int64) (int, error) {
	copy(p, m.data[off:])
	return len(p), nil
}

func (m *mockNoSync) WriteAt(p []byte, off int64) (int, error) {
	copy(m.data[off:], p)
	return len(p), nil
}

func TestNewOffsetDevicePanicsOnNegativeBaseOffset(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative baseOffset")
		}
	}()
	mock := &mockNoSync{data: make([]byte, 4096)}
	NewOffsetDevice(mock, -1, 0)
}

func TestNewOffsetDevicePanicsOnNegativeRegionSize(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative regionSize")
		}
	}()
	mock := &mockNoSync{data: make([]byte, 4096)}
	NewOffsetDevice(mock, 0, -1)
}

func TestOffsetDeviceSyncNoOp(t *testing.T) {
	mock := &mockNoSync{data: make([]byte, 4096)}
	od := NewOffsetDevice(mock, 0, 0)

	// Should not error even though device has no Sync
	if err := od.Sync(); err != nil {
		t.Fatalf("Sync on non-syncer should be no-op, got: %v", err)
	}
}
