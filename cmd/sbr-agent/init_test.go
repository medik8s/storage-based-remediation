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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/internal/blockformat"
)

func createInitTestDevice(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "init-test-device")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create test device: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()
	return path
}

func TestRunInitSuccess(t *testing.T) {
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)

	err := runInit(path, 30*time.Second, logr.Discard())
	if err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Verify superblock was written
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open device: %v", err)
	}
	defer f.Close()

	sector := make([]byte, blockformat.BlockSectorSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}

	sb, err := blockformat.UnmarshalSuperblock(sector)
	if err != nil {
		t.Fatalf("superblock invalid after runInit: %v", err)
	}
	if err := sb.Validate(); err != nil {
		t.Fatalf("superblock Validate failed: %v", err)
	}
}

func TestRunInitIdempotent(t *testing.T) {
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)

	// First init
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("first runInit failed: %v", err)
	}

	// Read superblock after first init
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open device: %v", err)
	}

	sector1 := make([]byte, blockformat.BlockSectorSize)
	if _, err := f.ReadAt(sector1, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}

	// Write marker in heartbeat region
	marker := []byte("INIT_TEST_MARKER")
	if _, err := f.WriteAt(marker, blockformat.BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}
	f.Close()

	// Second init — should be no-op
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("second runInit failed: %v", err)
	}

	// Verify marker survived
	f, err = os.Open(path)
	if err != nil {
		t.Fatalf("failed to reopen device: %v", err)
	}
	defer f.Close()

	readBack := make([]byte, len(marker))
	if _, err := f.ReadAt(readBack, blockformat.BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	if !bytes.Equal(readBack, marker) {
		t.Error("marker destroyed on second init — device was re-zeroed")
	}
}

func TestRunInitEmptyDevicePath(t *testing.T) {
	err := runInit("", 30*time.Second, logr.Discard())
	if err == nil {
		t.Fatal("expected error for empty device path")
	}
}

func TestRunInitNonExistentDevice(t *testing.T) {
	err := runInit("/non/existent/device", 30*time.Second, logr.Discard())
	if err == nil {
		t.Fatal("expected error for non-existent device")
	}
}

func TestRunInitUndersizedDevice(t *testing.T) {
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize-1)

	err := runInit(path, 30*time.Second, logr.Discard())
	if err == nil {
		t.Fatal("expected error for undersized device")
	}
}

func TestRunInitZeroesExistingData(t *testing.T) {
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)

	// Fill with junk
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open device: %v", err)
	}
	junk := bytes.Repeat([]byte{0xBB}, 4096)
	for off := int64(0); off < blockformat.BlockMinDeviceSize; off += 4096 {
		if _, err := f.WriteAt(junk, off); err != nil {
			t.Fatalf("failed to write junk at %d: %v", off, err)
		}
	}
	f.Close()

	// Run init
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Verify heartbeat region is zeroed
	f, err = os.Open(path)
	if err != nil {
		t.Fatalf("failed to reopen device: %v", err)
	}
	defer f.Close()

	buf := make([]byte, 4096)
	if _, err := f.ReadAt(buf, blockformat.BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("failed to read heartbeat region: %v", err)
	}
	if !bytes.Equal(buf, make([]byte, 4096)) {
		t.Error("heartbeat region not zeroed after init")
	}

	// But superblock should be valid
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}
	if !blockformat.IsValidSuperblock(buf) {
		t.Error("superblock invalid after init on junk-filled device")
	}
}

func TestCheckSBRDevice_BlockModeValidSuperblock(t *testing.T) {
	logger = logr.Discard()

	// Create a device with a valid superblock
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// blockModeExpected=true: should verify superblock and succeed
	if err := checkSBRDevice(path, 1, "test-node", true); err != nil {
		t.Fatalf("checkSBRDevice(blockModeExpected=true) failed: %v", err)
	}

	// Verify the superblock is still valid (not overwritten by slot write)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open: %v", err)
	}
	defer f.Close()
	buf := make([]byte, blockformat.BlockSuperblockSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("failed to read: %v", err)
	}
	if !blockformat.IsValidSuperblock(buf) {
		t.Error("superblock corrupted after checkSBRDevice")
	}
}

func TestCheckSBRDevice_BlockModeNoSuperblockErrors(t *testing.T) {
	logger = logr.Discard()

	// Create a device WITHOUT a superblock (zeroed)
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)

	// blockModeExpected=true but no superblock: must error, not fall back
	err := checkSBRDevice(path, 1, "test-node", true)
	if err == nil {
		t.Fatal("expected checkSBRDevice to error when block mode expected but no superblock found")
	}
	if !strings.Contains(err.Error(), "no valid superblock found") {
		t.Errorf("expected error about missing superblock, got: %v", err)
	}
}

func TestCheckSBRDevice_BlockModeCorruptSuperblockErrors(t *testing.T) {
	logger = logr.Discard()

	// Create a device with a valid superblock, then corrupt the CRC
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Corrupt the CRC (byte 76 of the superblock)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open: %v", err)
	}
	crcBuf := make([]byte, 1)
	if _, err := f.ReadAt(crcBuf, 76); err != nil {
		t.Fatalf("failed to read CRC byte: %v", err)
	}
	crcBuf[0] ^= 0xFF
	if _, err := f.WriteAt(crcBuf, 76); err != nil {
		t.Fatalf("failed to write corrupted CRC: %v", err)
	}
	f.Close()

	// Verify magic is still present but superblock is now invalid
	f2, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to reopen: %v", err)
	}
	magicBuf := make([]byte, blockformat.BlockSuperblockSize)
	if _, err := f2.ReadAt(magicBuf, 0); err != nil {
		t.Fatalf("failed to read: %v", err)
	}
	f2.Close()
	if !blockformat.HasSuperblockMagic(magicBuf) {
		t.Fatal("test setup: expected magic to be present after CRC corruption")
	}
	if blockformat.IsValidSuperblock(magicBuf) {
		t.Fatal("test setup: expected superblock to be invalid after CRC corruption")
	}

	// blockModeExpected=true: must error on corrupt superblock
	err = checkSBRDevice(path, 1, "test-node", true)
	if err == nil {
		t.Fatal("expected checkSBRDevice to reject device with SBR magic but invalid superblock")
	}
	if !strings.Contains(err.Error(), "invalid superblock") {
		t.Errorf("expected error about invalid superblock, got: %v", err)
	}
}

func TestCheckSBRDevice_FilesystemModeDoesSlotWrite(t *testing.T) {
	logger = logr.Discard()

	// Create a device WITHOUT a superblock (filesystem mode)
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)

	// blockModeExpected=false: should succeed via the slot write path
	if err := checkSBRDevice(path, 1, "test-node", false); err != nil {
		t.Fatalf("checkSBRDevice(blockModeExpected=false) failed: %v", err)
	}
}

func TestCheckSBRDevice_FilesystemModeIgnoresSuperblock(t *testing.T) {
	logger = logr.Discard()

	// Create a device WITH a valid superblock
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// blockModeExpected=false: should NOT probe superblock, runs slot write
	// test directly (which will overwrite superblock region — that's expected
	// since the caller explicitly said this is not a block device)
	if err := checkSBRDevice(path, 1, "test-node", false); err != nil {
		t.Fatalf("checkSBRDevice(blockModeExpected=false) failed on device with superblock: %v", err)
	}
}
