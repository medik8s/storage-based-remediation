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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
)

// Standard test buffers — no O_DIRECT in tests, so make([]byte) is fine.
var (
	testIOBuffer   = make([]byte, BlockSectorSize)
	testZeroBuffer = make([]byte, 1024*1024) // 1 MB
)

func createTempDevice(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test-device")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create temp device: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()
	return path
}

func openDevice(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		t.Fatalf("failed to open device: %v", err)
	}
	return f
}

func TestInitDeviceWritesValidSuperblock(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize)
	f := openDevice(t, path)
	defer f.Close()

	err := InitDevice(f, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard())
	if err != nil {
		t.Fatalf("InitDevice failed: %v", err)
	}

	// Read back and validate superblock
	sector := make([]byte, BlockSectorSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}

	sb, err := UnmarshalSuperblock(sector)
	if err != nil {
		t.Fatalf("superblock invalid after init: %v", err)
	}
	if err := sb.Validate(); err != nil {
		t.Fatalf("superblock Validate failed: %v", err)
	}
}

func TestInitDeviceIdempotent(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize)
	f := openDevice(t, path)
	defer f.Close()

	// First init
	if err := InitDevice(f, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard()); err != nil {
		t.Fatalf("first InitDevice failed: %v", err)
	}

	// Read superblock after first init
	sector1 := make([]byte, BlockSectorSize)
	if _, err := f.ReadAt(sector1, 0); err != nil {
		t.Fatalf("failed to read superblock after first init: %v", err)
	}

	// Write a marker in the heartbeat region to detect unwanted zeroing
	marker := []byte("MARKER_DATA_1234")
	if _, err := f.WriteAt(marker, BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	// Second init — should be a no-op
	if err := InitDevice(f, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard()); err != nil {
		t.Fatalf("second InitDevice failed: %v", err)
	}

	// Verify superblock is unchanged
	sector2 := make([]byte, BlockSectorSize)
	if _, err := f.ReadAt(sector2, 0); err != nil {
		t.Fatalf("failed to read superblock after second init: %v", err)
	}
	if !bytes.Equal(sector1, sector2) {
		t.Error("superblock was modified on second init — idempotency broken")
	}

	// Verify marker survived (device was not re-zeroed)
	readBack := make([]byte, len(marker))
	if _, err := f.ReadAt(readBack, BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	if !bytes.Equal(readBack, marker) {
		t.Error("marker was destroyed on second init — device was re-zeroed")
	}
}

func TestInitDeviceRejectsUndersizedDevice(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize-1)
	f := openDevice(t, path)
	defer f.Close()

	err := InitDevice(f, BlockMinDeviceSize-1, testIOBuffer, testZeroBuffer, logr.Discard())
	if err == nil {
		t.Fatal("expected error for undersized device, got nil")
	}
}

func TestInitDeviceZeroesLayout(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize)
	f := openDevice(t, path)
	defer f.Close()

	// Fill with non-zero data
	junk := bytes.Repeat([]byte{0xAA}, int(BlockSectorSize))
	for off := int64(0); off < BlockMinDeviceSize; off += BlockSectorSize {
		if _, err := f.WriteAt(junk, off); err != nil {
			t.Fatalf("failed to write junk at %d: %v", off, err)
		}
	}

	if err := InitDevice(f, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard()); err != nil {
		t.Fatalf("InitDevice failed: %v", err)
	}

	// Verify non-superblock regions are zeroed
	buf := make([]byte, BlockSectorSize)
	zero := make([]byte, BlockSectorSize)

	// Check a heartbeat slot (should be zero)
	if _, err := f.ReadAt(buf, BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to read heartbeat region: %v", err)
	}
	if !bytes.Equal(buf, zero) {
		t.Error("heartbeat region was not zeroed")
	}

	// Check a fence slot (should be zero)
	if _, err := f.ReadAt(buf, BlockFenceRegionOffset); err != nil {
		t.Fatalf("failed to read fence region: %v", err)
	}
	if !bytes.Equal(buf, zero) {
		t.Error("fence region was not zeroed")
	}

	// Check node map A (should be zero)
	if _, err := f.ReadAt(buf, BlockNodeMapAOffset); err != nil {
		t.Fatalf("failed to read node map A: %v", err)
	}
	if !bytes.Equal(buf, zero) {
		t.Error("node map A was not zeroed")
	}

	// But superblock should NOT be zero
	if _, err := f.ReadAt(buf, BlockSuperblockOffset); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}
	if bytes.Equal(buf, zero) {
		t.Error("superblock should not be all zeroes after init")
	}
}

func TestInitDeviceRejectsTooSmallBuffers(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize)
	f := openDevice(t, path)
	defer f.Close()

	smallBuf := make([]byte, 100)
	validBuf := make([]byte, BlockSectorSize)

	err := InitDevice(f, BlockMinDeviceSize, smallBuf, validBuf, logr.Discard())
	if err == nil {
		t.Fatal("expected error for too-small ioBuffer")
	}

	err = InitDevice(f, BlockMinDeviceSize, validBuf, smallBuf, logr.Discard())
	if err == nil {
		t.Fatal("expected error for too-small zeroBuffer")
	}
}

// mockSyncDevice tracks whether Sync was called.
type mockSyncDevice struct {
	data   []byte
	synced bool
}

func (m *mockSyncDevice) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, m.data[off:])
	return n, nil
}

func (m *mockSyncDevice) WriteAt(p []byte, off int64) (int, error) {
	n := copy(m.data[off:], p)
	return n, nil
}

func (m *mockSyncDevice) Sync() error {
	m.synced = true
	return nil
}

func TestInitDeviceCallsSync(t *testing.T) {
	dev := &mockSyncDevice{data: make([]byte, BlockMinDeviceSize)}

	err := InitDevice(dev, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard())
	if err != nil {
		t.Fatalf("InitDevice failed: %v", err)
	}
	if !dev.synced {
		t.Fatal("expected InitDevice to call Sync")
	}
}

// mockSyncErrorDevice returns an error from Sync.
type mockSyncErrorDevice struct {
	data []byte
}

func (m *mockSyncErrorDevice) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, m.data[off:])
	return n, nil
}

func (m *mockSyncErrorDevice) WriteAt(p []byte, off int64) (int, error) {
	n := copy(m.data[off:], p)
	return n, nil
}

func (m *mockSyncErrorDevice) Sync() error {
	return fmt.Errorf("simulated sync failure")
}

func TestInitDeviceSyncError(t *testing.T) {
	dev := &mockSyncErrorDevice{data: make([]byte, BlockMinDeviceSize)}

	err := InitDevice(dev, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard())
	if err == nil {
		t.Fatal("expected error when Sync fails")
	}
}

func TestInitDeviceRejectsUnknownVersion(t *testing.T) {
	path := createTempDevice(t, BlockMinDeviceSize)
	f := openDevice(t, path)
	defer f.Close()

	// Write a superblock with valid magic but unknown version (99)
	sb := NewV1Superblock()
	sb.Version = 99
	// Marshal manually to bypass version validation — we need raw bytes
	// with correct magic but wrong version and a valid-looking sector
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	// Overwrite the version field (bytes 6-7) with version 99
	data[6] = 99
	data[7] = 0
	// Write the raw sector (CRC will be wrong but magic is correct)
	sector := make([]byte, BlockSectorSize)
	copy(sector, data)
	if _, err := f.WriteAt(sector, 0); err != nil {
		t.Fatalf("failed to write fake superblock: %v", err)
	}

	// InitDevice should refuse to overwrite
	err = InitDevice(f, BlockMinDeviceSize, testIOBuffer, testZeroBuffer, logr.Discard())
	if err == nil {
		t.Fatal("expected error when device has SBR magic with unknown version")
	}
}

func TestInitDeviceLargerDevice(t *testing.T) {
	// Device larger than minimum — should work fine
	size := BlockMinDeviceSize * 2
	path := createTempDevice(t, size)
	f := openDevice(t, path)
	defer f.Close()

	if err := InitDevice(f, size, testIOBuffer, testZeroBuffer, logr.Discard()); err != nil {
		t.Fatalf("InitDevice failed on larger device: %v", err)
	}

	sector := make([]byte, BlockSectorSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}
	if !IsValidSuperblock(sector) {
		t.Error("superblock invalid on larger device")
	}
}
