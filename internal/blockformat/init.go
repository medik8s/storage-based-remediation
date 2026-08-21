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
	"fmt"
	"io"

	"github.com/go-logr/logr"
)

// DeviceReadWriterAt combines io.ReaderAt and io.WriterAt for init operations.
type DeviceReadWriterAt interface {
	io.ReaderAt
	io.WriterAt
}

// InitDevice initializes a block device with a V1 superblock.
// It is idempotent: if a valid superblock already exists, it returns nil
// without modifying the device.
//
// To support O_DIRECT, the caller must provide page-aligned memory buffers.
//   - ioBuffer: a 4 KB page-aligned buffer used for reading/writing the superblock.
//   - zeroBuffer: a larger page-aligned buffer (e.g. 1 MB) used for zeroing the layout.
//     Larger buffers reduce the number of synchronous network round-trips on
//     network-backed storage (Ceph RBD).
//
// Both buffers must be at least BlockSectorSize (4096) bytes. The caller is
// responsible for ensuring page alignment when the device is opened with
// O_DIRECT. In tests using regular files (no O_DIRECT), standard
// make([]byte) allocations are sufficient.
func InitDevice(dev DeviceReadWriterAt, deviceSize int64, ioBuffer, zeroBuffer []byte, logger logr.Logger) error {
	if deviceSize < BlockMinDeviceSize {
		return fmt.Errorf("device too small: %d bytes, need at least %d",
			deviceSize, BlockMinDeviceSize)
	}
	if len(ioBuffer) < int(BlockSectorSize) {
		return fmt.Errorf("ioBuffer too small: need at least %d bytes, got %d",
			BlockSectorSize, len(ioBuffer))
	}
	if len(zeroBuffer) < int(BlockSectorSize) {
		return fmt.Errorf("zeroBuffer too small: need at least %d bytes, got %d",
			BlockSectorSize, len(zeroBuffer))
	}

	// Check for existing valid superblock (idempotency).
	// Clear ioBuffer before reading to avoid stale data.
	clear(ioBuffer[:BlockSectorSize])
	n, err := dev.ReadAt(ioBuffer[:BlockSectorSize], BlockSuperblockOffset)
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read superblock sector: %w", err)
	}

	if n >= SuperblockTotalSize {
		if IsValidSuperblock(ioBuffer[:n]) {
			logger.Info("device already initialized, skipping")
			return nil
		}
		// Check if the sector has valid magic but failed full validation
		// (e.g. unknown version). This prevents accidental downgrade of a
		// newer-version device — the operator must explicitly reformat.
		if n >= superblockMagicSize {
			var magic [superblockMagicSize]byte
			copy(magic[:], ioBuffer[:superblockMagicSize])
			if magic == SuperblockMagic {
				return fmt.Errorf("device has SBR magic but unrecognized format — refusing to overwrite (possible newer version); reformat explicitly if intended")
			}
		}
	}

	// Zero the layout region using the caller-provided buffer.
	// Using a large buffer (e.g. 1 MB) minimizes synchronous I/O round-trips
	// on network storage.
	clear(zeroBuffer)
	chunkSize := int64(len(zeroBuffer))
	logger.Info("zeroing device layout region", "bytes", BlockMinDeviceSize, "chunkSize", chunkSize)

	for off := int64(0); off < BlockMinDeviceSize; off += chunkSize {
		remaining := BlockMinDeviceSize - off
		writeSize := chunkSize
		if remaining < writeSize {
			writeSize = remaining
		}
		if _, err := dev.WriteAt(zeroBuffer[:writeSize], off); err != nil {
			return fmt.Errorf("failed to zero device at offset %d: %w", off, err)
		}
	}

	// Write V1 superblock into the caller-provided ioBuffer.
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal superblock: %w", err)
	}

	// Clear and copy into ioBuffer to preserve its page-aligned address.
	clear(ioBuffer[:BlockSectorSize])
	copy(ioBuffer, data)

	if _, err := dev.WriteAt(ioBuffer[:BlockSectorSize], BlockSuperblockOffset); err != nil {
		return fmt.Errorf("failed to write superblock: %w", err)
	}

	// Ensure the superblock is durable and visible to agents on other nodes
	// before the controller proceeds to create the DaemonSet. O_SYNC provides
	// per-syscall durability, but an explicit Sync() flushes any storage
	// backend write-back caches (e.g. Ceph OSD).
	if s, ok := dev.(Syncer); ok {
		if err := s.Sync(); err != nil {
			return fmt.Errorf("failed to sync device after superblock write: %w", err)
		}
	}

	logger.Info("device initialized with V1 superblock")
	return nil
}
