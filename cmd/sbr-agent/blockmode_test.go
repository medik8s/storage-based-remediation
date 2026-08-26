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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/internal/blockformat"
	"github.com/medik8s/storage-based-remediation/internal/sbdprotocol"
)

// createBlockModeDevice creates a temp file with a valid V1 superblock.
func createBlockModeDevice(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "block-device")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create test device: %v", err)
	}
	if err := f.Truncate(blockformat.BlockMinDeviceSize); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()

	// Initialize with superblock
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}
	return path
}

// createFilesystemModeDevice creates a temp file without a superblock.
func createFilesystemModeDevice(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fs-device")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create test device: %v", err)
	}
	if err := f.Truncate(blockformat.BlockMinDeviceSize); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()
	return path
}

func TestProbeBlockMode_ValidSuperblock(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	isBlock, sb, err := agent.probeBlockMode()
	if err != nil {
		t.Fatalf("probeBlockMode failed: %v", err)
	}
	if !isBlock {
		t.Fatal("expected block mode detection for device with superblock")
	}
	if sb == nil {
		t.Fatal("expected non-nil superblock")
	}
	if sb.HeartbeatRegOffset != blockformat.BlockHeartbeatRegionOffset {
		t.Errorf("heartbeat offset = %d, want %d", sb.HeartbeatRegOffset, blockformat.BlockHeartbeatRegionOffset)
	}
}

func TestProbeBlockMode_NoSuperblock(t *testing.T) {
	initTestLogger(t)
	path := createFilesystemModeDevice(t)

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	isBlock, sb, err := agent.probeBlockMode()
	if err != nil {
		t.Fatalf("probeBlockMode failed: %v", err)
	}
	if isBlock {
		t.Fatal("expected filesystem mode for device without superblock")
	}
	if sb != nil {
		t.Fatal("expected nil superblock for filesystem mode")
	}
}

func TestProbeBlockMode_DirectoryPath(t *testing.T) {
	initTestLogger(t)
	dir := t.TempDir()

	agent := &SBRAgent{
		heartbeatDevicePath: dir,
		ioTimeout:           30 * time.Second,
	}

	// A directory path should gracefully fall back to filesystem mode
	isBlock, _, err := agent.probeBlockMode()
	if err != nil {
		t.Fatalf("probeBlockMode failed: %v", err)
	}
	if isBlock {
		t.Fatal("expected filesystem mode for directory path")
	}
}

func TestProbeBlockMode_CorruptMagic(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	// Corrupt the magic bytes
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open: %v", err)
	}
	if _, err := f.WriteAt([]byte("BROKEN"), 0); err != nil {
		t.Fatalf("failed to corrupt magic: %v", err)
	}
	f.Close()

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	// Corrupt magic means no valid superblock — should be filesystem mode
	isBlock, _, err := agent.probeBlockMode()
	if err != nil {
		t.Fatalf("probeBlockMode should not error on corrupt magic: %v", err)
	}
	if isBlock {
		t.Fatal("expected filesystem mode for device with corrupt magic")
	}
}

func TestProbeBlockMode_SmallFile(t *testing.T) {
	initTestLogger(t)
	// Create a file smaller than the superblock — ReadAt returns io.EOF.
	path := filepath.Join(t.TempDir(), "tiny-file")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create: %v", err)
	}
	if err := f.Truncate(64); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	f.Close()

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	// Small file should gracefully fall back to filesystem mode, not error
	isBlock, _, err := agent.probeBlockMode()
	if err != nil {
		t.Fatalf("probeBlockMode should not error on small file: %v", err)
	}
	if isBlock {
		t.Fatal("expected filesystem mode for small file")
	}
}

func TestProbeBlockMode_ValidMagicInvalidLayout(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	// Modify a layout field to make Validate() fail but keep magic/version/CRC valid
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open: %v", err)
	}
	sector := make([]byte, blockformat.BlockSuperblockSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatalf("failed to read: %v", err)
	}
	sb, err := blockformat.UnmarshalSuperblock(sector)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	sb.HeartbeatRegOffset = 999999 // invalid offset
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	f.Close()

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	// Valid superblock but invalid layout — should error
	_, _, err = agent.probeBlockMode()
	if err == nil {
		t.Fatal("expected error for valid superblock with invalid layout")
	}
}

func TestProbeBlockMode_NonExistentPath(t *testing.T) {
	initTestLogger(t)

	agent := &SBRAgent{
		heartbeatDevicePath: "/nonexistent/device/path",
		ioTimeout:           30 * time.Second,
	}

	// Non-existent path that is not a directory — should error (not silently fall back)
	_, _, err := agent.probeBlockMode()
	if err == nil {
		t.Fatal("expected error for non-existent device path")
	}
}

func TestInitializeBlockModeDevices(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	sb := blockformat.NewV1Superblock()
	if err := agent.initializeBlockModeDevices(sb); err != nil {
		t.Fatalf("initializeBlockModeDevices failed: %v", err)
	}

	if !agent.blockMode {
		t.Error("expected blockMode to be true")
	}
	if agent.rawDevice == nil {
		t.Error("expected rawDevice to be set")
	}
	if agent.deviceCloser == nil {
		t.Error("expected deviceCloser to be set")
	}
	if agent.heartbeatDevice == nil {
		t.Error("expected heartbeatDevice to be set")
	}
	if agent.fenceDevice == nil {
		t.Error("expected fenceDevice to be set")
	}

	// Verify heartbeat writes go to the correct physical offset
	data := []byte("heartbeat test")
	padded := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	copy(padded, data)
	n, err := agent.heartbeatDevice.WriteAt(padded, 0)
	if err != nil {
		t.Fatalf("heartbeat write failed: %v", err)
	}
	if n != len(padded) {
		t.Fatalf("expected %d bytes, got %d", len(padded), n)
	}
	if err := agent.heartbeatDevice.Sync(); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Read back from raw device at heartbeat region offset to verify
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open raw file: %v", err)
	}
	defer f.Close()
	raw := make([]byte, len(data))
	if _, err := f.ReadAt(raw, blockformat.BlockHeartbeatRegionOffset); err != nil {
		t.Fatalf("raw read failed: %v", err)
	}
	if !bytes.Equal(raw, data) {
		t.Errorf("data at physical heartbeat offset: expected %q, got %q", data, raw)
	}

	// Clean up
	agent.heartbeatDevice.Close()
}

func TestInitializeBlockModeDevices_QuiesceFlag(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
	}

	quiescedSB := blockformat.NewV1Superblock()
	quiescedSB.Flags |= blockformat.FlagQuiesce

	err := agent.initializeBlockModeDevices(quiescedSB)
	if err == nil {
		t.Fatal("expected error when quiesce flag is set")
	}

	// Verify state was NOT set on failure (point 6)
	if agent.blockMode {
		t.Error("blockMode should not be set after failed initialization")
	}
	if agent.rawDevice != nil {
		t.Error("rawDevice should not be set after failed initialization")
	}
}

func TestInitializeSBRDevices_BlockModeDetection(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		fenceDevicePath:     path + "-fence", // won't be used in block mode
		ioTimeout:           30 * time.Second,
	}

	if err := agent.initializeSBRDevices(); err != nil {
		t.Fatalf("initializeSBRDevices failed: %v", err)
	}

	if !agent.blockMode {
		t.Error("expected blockMode to be true for device with superblock")
	}

	agent.heartbeatDevice.Close()
}

func TestInitializeSBRDevices_FilesystemModeFallback(t *testing.T) {
	initTestLogger(t)
	// Create two separate device files (filesystem mode)
	dir := t.TempDir()
	hbPath := filepath.Join(dir, "sbr-device")
	fencePath := filepath.Join(dir, "sbr-device-fence")

	for _, p := range []string{hbPath, fencePath} {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("failed to create %s: %v", p, err)
		}
		if err := f.Truncate(blockformat.BlockMinDeviceSize); err != nil {
			t.Fatalf("failed to truncate %s: %v", p, err)
		}
		f.Close()
	}

	agent := &SBRAgent{
		heartbeatDevicePath: hbPath,
		fenceDevicePath:     fencePath,
		ioTimeout:           30 * time.Second,
	}

	if err := agent.initializeSBRDevices(); err != nil {
		t.Fatalf("initializeSBRDevices failed: %v", err)
	}

	if agent.blockMode {
		t.Error("expected filesystem mode for device without superblock")
	}

	agent.heartbeatDevice.Close()
	agent.fenceDevice.Close()
}

func TestQuiesceCheckLoop_ExitsOnQuiesce(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agentCtx, agentCancel := context.WithCancel(context.Background())

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		ioTimeout:           30 * time.Second,
		heartbeatInterval:   100 * time.Millisecond, // fast for testing
		ctx:                 agentCtx,
		cancel:              agentCancel,
		blockMode:           true,
	}

	// Initialize block mode devices to get rawDevice
	sb := blockformat.NewV1Superblock()
	if err := agent.initializeBlockModeDevices(sb); err != nil {
		t.Fatalf("initializeBlockModeDevices failed: %v", err)
	}

	// Start quiesce check loop in a goroutine
	done := make(chan struct{})
	go func() {
		agent.quiesceCheckLoop()
		close(done)
	}()

	// Set quiesce flag on the device
	time.Sleep(50 * time.Millisecond) // let loop start
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open device: %v", err)
	}
	sector := make([]byte, blockformat.BlockSuperblockSize)
	if _, err := f.ReadAt(sector, 0); err != nil {
		t.Fatalf("failed to read superblock: %v", err)
	}
	sbRead, err := blockformat.UnmarshalSuperblock(sector)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	sbRead.Flags |= blockformat.FlagQuiesce
	data, err := sbRead.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatalf("failed to write quiesced superblock: %v", err)
	}
	f.Close()

	// Wait for the loop to detect quiesce and exit
	select {
	case <-done:
		// Success — loop exited
	case <-time.After(3 * time.Second):
		agentCancel()
		t.Fatal("quiesce check loop did not exit in time")
	}

	// Verify context was cancelled
	select {
	case <-agentCtx.Done():
		// OK
	default:
		t.Error("expected agent context to be cancelled after quiesce detection")
	}

	agent.deviceCloser.Close()
}

func TestInitializeNodeManagers_BlockModeUsesBlockNodeMapStore(t *testing.T) {
	initTestLogger(t)
	path := createBlockModeDevice(t)

	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	agent := &SBRAgent{
		heartbeatDevicePath: path,
		fenceDevicePath:     path + "-fence",
		ioTimeout:           30 * time.Second,
		nodeName:            "test-node",
		nodeID:              1,
		staleNodeTimeout:    1 * time.Hour,
		ctx:                 agentCtx,
		cancel:              agentCancel,
	}

	// Initialize devices in block mode
	sb := blockformat.NewV1Superblock()
	if err := agent.initializeBlockModeDevices(sb); err != nil {
		t.Fatalf("initializeBlockModeDevices failed: %v", err)
	}

	// Initialize node managers — should use BlockNodeMapStore
	if err := agent.initializeNodeManagers("test-cluster", true); err != nil {
		t.Fatalf("initializeNodeManagers failed: %v", err)
	}

	if agent.nodeManager == nil {
		t.Fatal("expected nodeManager to be set")
	}

	// Verify node ID was assigned
	if agent.nodeID == 0 {
		t.Error("expected nodeID to be assigned")
	}

	agent.heartbeatDevice.Close()
}

// initTestLogger initializes the global logger for tests that need it.
func initTestLogger(t *testing.T) {
	t.Helper()
	logger = logr.Discard()
}

func TestSlotOffset_BlockMode(t *testing.T) {
	agent := &SBRAgent{blockMode: true}

	tests := []struct {
		nodeID uint16
		want   int64
	}{
		{1, 0},                                 // first usable slot starts at offset 0
		{2, blockformat.BlockSlotSize},         // second slot
		{254, 253 * blockformat.BlockSlotSize}, // near end
		{255, 254 * blockformat.BlockSlotSize}, // last valid nodeID — must fit
	}

	for _, tc := range tests {
		got := agent.slotOffset(tc.nodeID)
		if got != tc.want {
			t.Errorf("slotOffset(%d) = %d, want %d", tc.nodeID, got, tc.want)
		}
	}
}

func TestSlotOffset_FilesystemMode(t *testing.T) {
	agent := &SBRAgent{blockMode: false}

	tests := []struct {
		nodeID uint16
		want   int64
	}{
		{1, sbdprotocol.SBD_SLOT_SIZE},
		{2, 2 * sbdprotocol.SBD_SLOT_SIZE},
		{255, 255 * sbdprotocol.SBD_SLOT_SIZE},
	}

	for _, tc := range tests {
		got := agent.slotOffset(tc.nodeID)
		if got != tc.want {
			t.Errorf("slotOffset(%d) = %d, want %d", tc.nodeID, got, tc.want)
		}
	}
}

func TestSlotOffset_BlockMode_MaxNodeID_FitsInRegion(t *testing.T) {
	agent := &SBRAgent{blockMode: true}

	// nodeID 255 (SBD_MAX_NODES) must produce an offset that, plus one
	// full slot read, stays within the heartbeat region.
	maxOffset := agent.slotOffset(sbdprotocol.SBD_MAX_NODES)
	endByte := maxOffset + blockformat.BlockSlotSize
	regionSize := blockformat.BlockMaxNodes * blockformat.BlockSlotSize

	if endByte > regionSize {
		t.Errorf("nodeID %d: offset %d + slot %d = %d exceeds region %d",
			sbdprotocol.SBD_MAX_NODES, maxOffset, blockformat.BlockSlotSize,
			endByte, regionSize)
	}
}
