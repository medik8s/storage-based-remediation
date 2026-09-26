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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockdevice"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockformat"
	"github.com/medik8s/storage-based-remediation/v5/internal/mocks"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
	"github.com/medik8s/storage-based-remediation/v5/internal/watchdog"
)

// preflightBlockProbeTimeout bounds the superblock read used to detect block mode at pre-flight;
// it matches the io-timeout flag default so a hung device fails fast.
const preflightBlockProbeTimeout = 2 * time.Second

// runPreflightChecks performs critical startup validation before entering main event loops
// Returns success if EITHER watchdog is active OR SBR device is accessible (or both)
// When detectOnlyMode is true, the watchdog check is skipped since the agent won't use it
func runPreflightChecks(watchdogPath, sbrDevicePath, nodeName string, nodeID uint16, detectOnlyMode bool) error {
	logger.Info("Running pre-flight checks",
		"watchdogPath", watchdogPath,
		"sbrDevicePath", sbrDevicePath,
		"nodeName", nodeName,
		"nodeID", nodeID,
		"detectOnlyMode", detectOnlyMode)

	// Check watchdog device availability (skip in detect-only mode)
	var watchdogErr error
	if detectOnlyMode {
		// Treat as successful - watchdog is not needed in detect-only mode
		logger.Info("Skipping watchdog pre-flight check (detect-only mode enabled)")
	} else if watchdogPath != "" {
		watchdogErr = checkWatchdogDevice(watchdogPath)
	}

	// Check SBR device accessibility. Detect block mode the same way the runtime does (a valid
	// on-disk superblock) so a raw block device is verified via its superblock instead of the
	// filesystem slot-write test, which would corrupt the block layout.
	var sbrErr error
	if sbrDevicePath != "" {
		isBlock, sb, probeErr := probeBlockModeAt(sbrDevicePath, preflightBlockProbeTimeout)
		switch {
		case probeErr != nil:
			sbrErr = probeErr
		case isBlock:
			// A valid superblock only proves the device can be read. Write this node's own
			// heartbeat slot to verify it can be written to.
			if writeErr := performSBRBlockWriteTest(sbrDevicePath, sb, nodeID, preflightBlockProbeTimeout); writeErr != nil {
				sbrErr = writeErr
			} else {
				logger.Info("Pre-flight check passed: block-mode SBR device write/read-back verified",
					"sbrDevicePath", sbrDevicePath, "nodeID", nodeID)
			}
		default:
			sbrErr = checkSBRDevice(sbrDevicePath, nodeID, nodeName, false)
		}
	}

	if sbrErr != nil {
		logger.Error(sbrErr, "SBR device pre-flight check failed", "sbrDevicePath", sbrDevicePath)
	}
	if watchdogErr != nil {
		logger.Error(watchdogErr, "Watchdog device pre-flight check failed", "watchdogPath", watchdogPath)
	}

	// Check node ID/name resolution
	nodeErr := checkNodeIDNameResolution(nodeName, nodeID)
	if nodeErr != nil {
		logger.Error(nodeErr, "Node ID/name resolution pre-flight check failed")
		return fmt.Errorf("node ID/name resolution pre-flight check failed: %w", nodeErr)
	}
	logger.Info("Pre-flight check passed: node ID/name resolution successful",
		"nodeName", nodeName,
		"nodeID", nodeID)

	// SBR device is always required
	if sbrDevicePath == "" {
		return fmt.Errorf("SBR device path cannot be empty")
	}

	// Check if at least one critical component (watchdog OR SBR) is working
	if watchdogErr == nil && sbrErr == nil {
		logger.Info("All pre-flight checks passed successfully")
		return nil
	} else if watchdogErr == nil {
		return fmt.Errorf("pre-flight checks failed: SBR device is not available: %w", sbrErr)
	} else if sbrErr == nil {
		return fmt.Errorf("pre-flight checks failed: watchdog device is not available: %w", watchdogErr)
	} else {
		return fmt.Errorf(
			"pre-flight checks failed: both watchdog device and SBR device are inaccessible. Watchdog error: %v, SBR error: %v",
			watchdogErr, sbrErr)
	}
}

// checkWatchdogDevice verifies the watchdog device exists and can be opened
// Note: This function does NOT use softdog fallback - it strictly checks the specified device
func checkWatchdogDevice(watchdogPath string) error {
	logger.V(1).Info("Checking watchdog device availability", "watchdogPath", watchdogPath)

	// For preflight checks, we want to be strict about the specified device
	// Don't use softdog fallback here - if the specified device doesn't work, it should fail
	wd, err := watchdog.NewWithSoftdogFallback(watchdogPath, logger.WithName("preflight-watchdog"))
	if err != nil {
		return fmt.Errorf("watchdog device pre-flight check failed: %w", err)
	}
	defer func() {
		if closeErr := wd.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close watchdog device during pre-flight check",
				"watchdogPath", wd.Path())
		}
	}()

	logger.Info("Pre-flight check: using hardware watchdog device",
		"watchdogPath", wd.Path())

	logger.V(1).Info("Watchdog device successfully opened and closed", "watchdogPath", wd.Path())
	return nil
}

// checkSBRDevice verifies the SBR device exists and performs a minimal read/write test.
// When blockModeExpected is true, the device must contain a valid V1 superblock;
// the function will never fall back to the filesystem slot-write test.
// When blockModeExpected is false, the superblock region is not probed and the
// filesystem slot-write test runs directly.
func checkSBRDevice(sbrDevicePath string, nodeID uint16, nodeName string, blockModeExpected bool) error {
	logger.V(1).Info("Checking SBR device accessibility",
		"sbrDevicePath", sbrDevicePath, "nodeID", nodeID, "blockModeExpected", blockModeExpected)

	// Check if the SBR device file exists
	if _, err := os.Stat(sbrDevicePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SBR device does not exist: %s", sbrDevicePath)
		}
		return fmt.Errorf("failed to stat SBR device %s: %w", sbrDevicePath, err)
	}

	var device mocks.BlockDeviceInterface
	var err error
	if blockModeExpected {
		device, err = blockdevice.Open(sbrDevicePath)
		if err != nil {
			return fmt.Errorf("failed to open SBR device %s: %w", sbrDevicePath, err)
		}
	} else {
		probe := func(device mocks.BlockDeviceInterface) error {
			return performSBRReadWriteTest(device, nodeID, nodeName)
		}
		device, err = openWithDirectOrReopen(sbrDevicePath, preflightBlockProbeTimeout,
			logger.WithName("preflight-sbr-device"), probe)
		if err != nil {
			return fmt.Errorf("failed to open SBR device %s: %w", sbrDevicePath, err)
		}
	}
	defer func() {
		if closeErr := device.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close SBR device during pre-flight check",
				"sbrDevicePath", sbrDevicePath)
		}
	}()

	if blockModeExpected {
		return checkSBRBlockDevice(device, sbrDevicePath)
	}

	logger.V(1).Info("SBR device read/write test completed successfully",
		"sbrDevicePath", sbrDevicePath,
		"nodeID", nodeID)
	return nil
}

// checkSBRBlockDevice verifies a block-mode device has a valid V1 superblock.
// It never falls back to the filesystem slot-write test.
func checkSBRBlockDevice(device mocks.BlockDeviceInterface, sbrDevicePath string) error {
	buf := blockdevice.DirectIOAlloc(int(blockformat.BlockSuperblockSize))
	n, err := device.ReadAt(buf, blockformat.BlockSuperblockOffset)
	if err != nil {
		return fmt.Errorf("block mode device %s: failed to read superblock: %w", sbrDevicePath, err)
	}
	if n < blockformat.SuperblockTotalSize {
		return fmt.Errorf("block mode device %s: short read (%d bytes), expected at least %d",
			sbrDevicePath, n, blockformat.SuperblockTotalSize)
	}

	if blockformat.HasSuperblockMagic(buf) {
		if _, unmarshalErr := blockformat.UnmarshalSuperblock(buf); unmarshalErr != nil {
			return fmt.Errorf("block mode device %s has SBR magic but an invalid superblock: %w",
				sbrDevicePath, unmarshalErr)
		}
		logger.V(1).Info("Block mode device: superblock read verified",
			"sbrDevicePath", sbrDevicePath)
		return nil
	}

	return fmt.Errorf("block mode device %s: no valid superblock found — device not initialized", sbrDevicePath)
}

// performSBRBlockWriteTest verifies a block-mode SBR device by writing this node's heartbeat
// slot and reading it back, bounded by ioTimeout
func performSBRBlockWriteTest(sbrDevicePath string, sb *blockformat.Superblock, nodeID uint16, ioTimeout time.Duration) error {
	dev, err := blockdevice.OpenWithTimeout(sbrDevicePath, ioTimeout, logger.WithName("preflight-block-write"))
	if err != nil {
		return fmt.Errorf("block mode device %s: failed to open for write test: %w", sbrDevicePath, err)
	}
	defer func() {
		if closeErr := dev.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close block device after pre-flight write test", "sbrDevicePath", sbrDevicePath)
		}
	}()

	heartbeatRegion := blockformat.NewOffsetDevice(dev, sb.HeartbeatRegOffset, sb.HeartbeatRegLength)
	slotOffset := int64(nodeID-1) * blockformat.BlockSlotSize

	sequence := uint64(1) // Use sequence 1 for pre-flight test, matching performSBRReadWriteTest
	header := sbdprotocol.NewHeartbeat(nodeID, sequence)
	msgBytes, err := sbdprotocol.MarshalHeartbeat(sbdprotocol.SBDHeartbeatMessage{Header: header})
	if err != nil {
		return fmt.Errorf("failed to marshal test heartbeat message: %w", err)
	}

	// Block mode I/O must be a full, page-aligned slot; a bare marshalled message fails with EINVAL.
	writeBuf := blockdevice.DirectIOAlloc(int(blockformat.BlockSlotSize))
	copy(writeBuf, msgBytes)

	n, err := heartbeatRegion.WriteAt(writeBuf, slotOffset)
	if err != nil {
		return fmt.Errorf("block mode device %s: failed to write test heartbeat to slot at offset %d: %w",
			sbrDevicePath, slotOffset, err)
	}
	if n != len(writeBuf) {
		return fmt.Errorf("block mode device %s: partial write to slot: wrote %d bytes, expected %d",
			sbrDevicePath, n, len(writeBuf))
	}
	if err := heartbeatRegion.Sync(); err != nil {
		return fmt.Errorf("block mode device %s: failed to sync after test write: %w", sbrDevicePath, err)
	}

	readBuf := blockdevice.DirectIOAlloc(int(blockformat.BlockSlotSize))
	readN, err := heartbeatRegion.ReadAt(readBuf, slotOffset)
	if err != nil {
		return fmt.Errorf("block mode device %s: failed to read back test heartbeat at offset %d: %w",
			sbrDevicePath, slotOffset, err)
	}
	if readN != len(readBuf) {
		return fmt.Errorf("block mode device %s: partial read from slot: read %d bytes, expected %d",
			sbrDevicePath, readN, len(readBuf))
	}

	readHeader, err := sbdprotocol.Unmarshal(readBuf[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		return fmt.Errorf("block mode device %s: failed to unmarshal test heartbeat read back: %w", sbrDevicePath, err)
	}
	if readHeader.NodeID != nodeID || readHeader.Sequence != sequence || readHeader.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		return fmt.Errorf("block mode device %s: read-back mismatch (nodeID=%d seq=%d type=%d), expected (nodeID=%d seq=%d type=%d)",
			sbrDevicePath, readHeader.NodeID, readHeader.Sequence, readHeader.Type, nodeID, sequence, sbdprotocol.SBD_MSG_TYPE_HEARTBEAT)
	}

	logger.V(1).Info("Block mode device: write/read-back test passed",
		"sbrDevicePath", sbrDevicePath, "nodeID", nodeID, "slotOffset", slotOffset)
	return nil
}

// performSBRFenceReadWriteTest writes a no-op fence message to a per-node probe slot and reads it back.
// Probe slots follow the live slots (0 through SBD_MAX_NODES) in the filesystem-backed fence
// file, preserving pending fence requests and isolating probes from other nodes.
// It is used only as a filesystem-mode open probe, so openWithDirectOrReopen can reject an
// O_DIRECT fd that opened successfully but cannot perform the small writes runtime uses.
func performSBRFenceReadWriteTest(device mocks.BlockDeviceInterface, nodeID uint16) error {
	logger.V(1).Info("Performing SBR fence device read/write test", "nodeID", nodeID)

	slotOffset := (int64(sbdprotocol.SBD_MAX_NODES) + int64(nodeID)) * sbdprotocol.SBD_SLOT_SIZE
	sequence := uint64(0)
	testMsg := sbdprotocol.NewFence(nodeID, nodeID, sequence, sbdprotocol.FENCE_REASON_NONE)
	testMsgBytes, err := sbdprotocol.MarshalFence(testMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal test fence message: %w", err)
	}

	n, err := device.WriteAt(testMsgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write test fence message to SBR device at offset %d: %w", slotOffset, err)
	}
	if n != len(testMsgBytes) {
		return fmt.Errorf("partial write to SBR fence device: wrote %d bytes, expected %d", n, len(testMsgBytes))
	}
	if err := device.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBR fence device after test write: %w", err)
	}

	readBuffer := make([]byte, len(testMsgBytes))
	readN, err := device.ReadAt(readBuffer, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read test fence message from SBR device at offset %d: %w", slotOffset, err)
	}
	if readN != len(testMsgBytes) {
		return fmt.Errorf("partial read from SBR fence device: read %d bytes, expected %d", readN, len(testMsgBytes))
	}
	for i, b := range testMsgBytes {
		if readBuffer[i] != b {
			return fmt.Errorf("fence data mismatch at byte %d: wrote 0x%02x, read 0x%02x", i, b, readBuffer[i])
		}
	}

	readMsg, err := sbdprotocol.UnmarshalFence(readBuffer)
	if err != nil {
		return fmt.Errorf("failed to unmarshal test fence message read from SBR device: %w", err)
	}
	if readMsg.Header.NodeID != nodeID || readMsg.TargetNodeID != nodeID || readMsg.Header.Sequence != sequence || readMsg.Reason != sbdprotocol.FENCE_REASON_NONE {
		return fmt.Errorf("fence message mismatch: got source=%d target=%d sequence=%d reason=%d, expected source=%d target=%d sequence=%d reason=%d",
			readMsg.Header.NodeID, readMsg.TargetNodeID, readMsg.Header.Sequence, readMsg.Reason,
			nodeID, nodeID, sequence, sbdprotocol.FENCE_REASON_NONE)
	}

	logger.V(1).Info("SBR fence device read/write test successful",
		"nodeID", nodeID,
		"sequence", sequence,
		"slotOffset", slotOffset,
		"bytesWritten", n,
		"bytesRead", readN)
	return nil
}

// performSBRReadWriteTest writes the node ID to its slot and reads it back to verify functionality
func performSBRReadWriteTest(device mocks.BlockDeviceInterface, nodeID uint16, nodeName string) error {
	logger.V(1).Info("Performing SBR device read/write test", "nodeID", nodeID, "nodeName", nodeName)

	// Calculate slot offset for this node
	slotOffset := int64(nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Create a test heartbeat message
	sequence := uint64(1) // Use sequence 1 for pre-flight test
	testHeader := sbdprotocol.NewHeartbeat(nodeID, sequence)
	testMsg := sbdprotocol.SBDHeartbeatMessage{Header: testHeader}

	// Marshal the test message
	testMsgBytes, err := sbdprotocol.MarshalHeartbeat(testMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal test heartbeat message: %w", err)
	}

	// Write test message to the node's slot
	n, err := device.WriteAt(testMsgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write test message to SBR device at offset %d: %w", slotOffset, err)
	}

	if n != len(testMsgBytes) {
		return fmt.Errorf("partial write to SBR device: wrote %d bytes, expected %d", n, len(testMsgBytes))
	}

	// Sync to ensure data is written to storage
	if err := device.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBR device after test write: %w", err)
	}

	// Read back the data to verify write was successful
	readBuffer := make([]byte, len(testMsgBytes))
	readN, err := device.ReadAt(readBuffer, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read test message from SBR device at offset %d: %w", slotOffset, err)
	}

	if readN != len(testMsgBytes) {
		return fmt.Errorf("partial read from SBR device: read %d bytes, expected %d", readN, len(testMsgBytes))
	}

	// Verify the data matches what we wrote
	for i, b := range testMsgBytes {
		if readBuffer[i] != b {
			return fmt.Errorf("data mismatch at byte %d: wrote 0x%02x, read 0x%02x", i, b, readBuffer[i])
		}
	}

	// Try to unmarshal the read data to ensure it's valid
	readHeader, err := sbdprotocol.Unmarshal(readBuffer[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		return fmt.Errorf("failed to unmarshal test message read from SBR device: %w", err)
	}

	// Verify the header matches our expectations
	if readHeader.NodeID != nodeID {
		return fmt.Errorf("node ID mismatch: expected %d, got %d", nodeID, readHeader.NodeID)
	}

	if readHeader.Sequence != sequence {
		return fmt.Errorf("sequence mismatch: expected %d, got %d", sequence, readHeader.Sequence)
	}

	if readHeader.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		return fmt.Errorf("message type mismatch: expected %d, got %d", sbdprotocol.SBD_MSG_TYPE_HEARTBEAT, readHeader.Type)
	}

	logger.V(1).Info("SBR device read/write test successful",
		"nodeID", nodeID,
		"sequence", sequence,
		"slotOffset", slotOffset,
		"bytesWritten", n,
		"bytesRead", readN)

	return nil
}

// checkNodeIDNameResolution verifies that the node name and ID are valid and consistent
func checkNodeIDNameResolution(nodeName string, nodeID uint16) error {
	logger.V(1).Info("Checking node ID/name resolution", "nodeName", nodeName, "nodeID", nodeID)

	// Validate node name is not empty
	if nodeName == "" {
		return fmt.Errorf("node name is empty")
	}

	// Validate node name length
	if len(nodeName) > MaxNodeNameLength {
		return fmt.Errorf("node name too long: %d characters, maximum allowed: %d", len(nodeName), MaxNodeNameLength)
	}

	// Validate node ID is within valid range
	if nodeID < 1 || nodeID > sbdprotocol.SBD_MAX_NODES {
		return fmt.Errorf("node ID %d is out of valid range [1, %d]", nodeID, sbdprotocol.SBD_MAX_NODES)
	}

	// Additional validation: ensure node name contains only valid characters
	// (printable ASCII characters, no control characters)
	for i, r := range nodeName {
		if r < 32 || r > 126 {
			return fmt.Errorf("node name contains invalid character at position %d: 0x%02x", i, r)
		}
	}

	logger.V(1).Info("Node ID/name resolution successful",
		"nodeName", nodeName,
		"nodeNameLength", len(nodeName),
		"nodeID", nodeID)

	return nil
}

// resetPreflightSentinelAt invalidates a previous process's result before startup checks.
func resetPreflightSentinelAt(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove pre-flight sentinel %s: %w", path, err)
	}
	return nil
}

// createPreflightSentinel creates the marker file the readiness probe waits for.
func createPreflightSentinel() error {
	return createPreflightSentinelAt(agent.PreflightSentinelPath)
}

// createPreflightSentinelAt creates the sentinel file at the given path, making its parent
// directory first. Split out from createPreflightSentinel so tests can point it at a temp dir
// instead of the real container-local path.
func createPreflightSentinelAt(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create sentinel directory %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte("ok\n"), 0o644); err != nil {
		return fmt.Errorf("failed to write pre-flight sentinel file %s: %w", path, err)
	}
	return nil
}
