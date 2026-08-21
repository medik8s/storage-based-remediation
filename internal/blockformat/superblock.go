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

// Package blockformat defines the on-disk binary format for SBR Block mode.
// It provides layout constants, superblock serialization, and validation.
package blockformat

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// Block device layout constants.
// All offsets and sizes are in bytes, aligned to 4 KB sectors.
const (
	BlockSectorSize int64 = 4096
	BlockMaxNodes   int64 = 255

	BlockSuperblockOffset int64 = 0
	BlockSuperblockSize   int64 = 4096

	BlockNodeMapAOffset    int64 = 4096
	BlockNodeMapBOffset    int64 = 69632
	BlockNodeMapRegionSize int64 = 65536 // 16 sectors per buffer

	BlockHeartbeatRegionOffset int64 = 135168
	BlockFenceRegionOffset     int64 = 1179648

	BlockSlotSize int64 = 4096 // per-slot I/O granularity

	// BlockMinDeviceSize is the minimum device size required for the full layout.
	// BlockFenceRegionOffset + (BlockMaxNodes * BlockSlotSize)
	BlockMinDeviceSize int64 = 2224128
)

// Superblock field sizes and offsets within the 4 KB sector.
const (
	superblockMagicSize   = 6
	superblockVersionSize = 2
	superblockFieldSize   = 8 // uint64 for each offset/length field
	superblockFlagsSize   = 4
	superblockCRCSize     = 4

	// SuperblockDataSize is the number of bytes covered by the CRC (everything before it).
	SuperblockDataSize = 76
	// SuperblockTotalSize is the wire size of the superblock header (data + CRC).
	SuperblockTotalSize = 80
)

// Superblock flags (Flags field bitmask).
const (
	// FlagQuiesce signals agents to stop heartbeating and enter a safe
	// paused state. Set by the controller or a migration job during
	// operator-orchestrated format migration (e.g. V1 → V2).
	// Agents detect this via periodic superblock re-reads.
	FlagQuiesce uint32 = 1 << 0
)

var (
	// SuperblockMagic identifies a valid SBR block device.
	SuperblockMagic = [superblockMagicSize]byte{'S', 'B', 'R', 'B', 'L', 'K'}

	// SuperblockVersion is the current format version.
	SuperblockVersion uint16 = 1
)

// Superblock is the self-describing header at sector 0 of an SBR block device.
// It is written once by the init job and is almost immutable thereafter —
// only the Flags field may be modified during operator-orchestrated migration.
type Superblock struct {
	Magic              [6]byte
	Version            uint16
	NodeMapAOffset     int64
	NodeMapALength     int64
	NodeMapBOffset     int64
	NodeMapBLength     int64
	HeartbeatRegOffset int64
	HeartbeatRegLength int64
	FenceRegOffset     int64
	FenceRegLength     int64
	Flags              uint32
	CRC                uint32
}

// NewV1Superblock creates a version 1 superblock with the standard layout.
func NewV1Superblock() *Superblock {
	return &Superblock{
		Magic:              SuperblockMagic,
		Version:            SuperblockVersion,
		NodeMapAOffset:     BlockNodeMapAOffset,
		NodeMapALength:     BlockNodeMapRegionSize,
		NodeMapBOffset:     BlockNodeMapBOffset,
		NodeMapBLength:     BlockNodeMapRegionSize,
		HeartbeatRegOffset: BlockHeartbeatRegionOffset,
		HeartbeatRegLength: BlockMaxNodes * BlockSlotSize,
		FenceRegOffset:     BlockFenceRegionOffset,
		FenceRegLength:     BlockMaxNodes * BlockSlotSize,
		Flags:              0,
	}
}

// Marshal serializes the superblock to an 80-byte wire format (little-endian).
// The CRC32 is computed over bytes 0–75 and written at bytes 76–79.
func (s *Superblock) Marshal() ([]byte, error) {
	buf := make([]byte, SuperblockTotalSize)
	off := 0

	copy(buf[off:off+superblockMagicSize], s.Magic[:])
	off += superblockMagicSize

	binary.LittleEndian.PutUint16(buf[off:off+superblockVersionSize], s.Version)
	off += superblockVersionSize

	putInt64 := func(v int64) {
		binary.LittleEndian.PutUint64(buf[off:off+superblockFieldSize], uint64(v))
		off += superblockFieldSize
	}

	putInt64(s.NodeMapAOffset)
	putInt64(s.NodeMapALength)
	putInt64(s.NodeMapBOffset)
	putInt64(s.NodeMapBLength)
	putInt64(s.HeartbeatRegOffset)
	putInt64(s.HeartbeatRegLength)
	putInt64(s.FenceRegOffset)
	putInt64(s.FenceRegLength)

	binary.LittleEndian.PutUint32(buf[off:off+superblockFlagsSize], s.Flags)
	off += superblockFlagsSize

	// CRC covers bytes 0 through SuperblockDataSize-1
	crc := crc32.ChecksumIEEE(buf[:SuperblockDataSize])
	binary.LittleEndian.PutUint32(buf[off:off+superblockCRCSize], crc)

	return buf, nil
}

// UnmarshalSuperblock deserializes an 80-byte wire format into a Superblock.
// It validates magic, version, and CRC.
func UnmarshalSuperblock(data []byte) (*Superblock, error) {
	if len(data) < SuperblockTotalSize {
		return nil, fmt.Errorf("superblock data too short: need %d bytes, got %d",
			SuperblockTotalSize, len(data))
	}

	s := &Superblock{}
	off := 0

	copy(s.Magic[:], data[off:off+superblockMagicSize])
	off += superblockMagicSize

	if s.Magic != SuperblockMagic {
		return nil, fmt.Errorf("invalid superblock magic: expected %q, got %q",
			string(SuperblockMagic[:]), string(s.Magic[:]))
	}

	s.Version = binary.LittleEndian.Uint16(data[off : off+superblockVersionSize])
	off += superblockVersionSize

	if s.Version != SuperblockVersion {
		return nil, fmt.Errorf("unsupported superblock version: expected %d, got %d",
			SuperblockVersion, s.Version)
	}

	getInt64 := func() int64 {
		v := int64(binary.LittleEndian.Uint64(data[off : off+superblockFieldSize]))
		off += superblockFieldSize
		return v
	}

	s.NodeMapAOffset = getInt64()
	s.NodeMapALength = getInt64()
	s.NodeMapBOffset = getInt64()
	s.NodeMapBLength = getInt64()
	s.HeartbeatRegOffset = getInt64()
	s.HeartbeatRegLength = getInt64()
	s.FenceRegOffset = getInt64()
	s.FenceRegLength = getInt64()

	s.Flags = binary.LittleEndian.Uint32(data[off : off+superblockFlagsSize])
	off += superblockFlagsSize

	s.CRC = binary.LittleEndian.Uint32(data[off : off+superblockCRCSize])

	expectedCRC := crc32.ChecksumIEEE(data[:SuperblockDataSize])
	if s.CRC != expectedCRC {
		return nil, fmt.Errorf("superblock CRC mismatch: expected 0x%08x, got 0x%08x",
			expectedCRC, s.CRC)
	}

	return s, nil
}

// Validate checks that a superblock matches the expected V1 layout.
func (s *Superblock) Validate() error {
	if s.Magic != SuperblockMagic {
		return fmt.Errorf("invalid magic: %q", string(s.Magic[:]))
	}
	if s.Version != SuperblockVersion {
		return fmt.Errorf("unsupported version: %d", s.Version)
	}
	if s.NodeMapAOffset != BlockNodeMapAOffset {
		return fmt.Errorf("unexpected NodeMapA offset: %d", s.NodeMapAOffset)
	}
	if s.NodeMapBOffset != BlockNodeMapBOffset {
		return fmt.Errorf("unexpected NodeMapB offset: %d", s.NodeMapBOffset)
	}
	if s.HeartbeatRegOffset != BlockHeartbeatRegionOffset {
		return fmt.Errorf("unexpected heartbeat region offset: %d", s.HeartbeatRegOffset)
	}
	if s.NodeMapALength != BlockNodeMapRegionSize {
		return fmt.Errorf("unexpected NodeMapA length: %d", s.NodeMapALength)
	}
	if s.NodeMapBLength != BlockNodeMapRegionSize {
		return fmt.Errorf("unexpected NodeMapB length: %d", s.NodeMapBLength)
	}
	if s.HeartbeatRegLength != BlockMaxNodes*BlockSlotSize {
		return fmt.Errorf("unexpected heartbeat region length: %d", s.HeartbeatRegLength)
	}
	if s.FenceRegOffset != BlockFenceRegionOffset {
		return fmt.Errorf("unexpected fence region offset: %d", s.FenceRegOffset)
	}
	if s.FenceRegLength != BlockMaxNodes*BlockSlotSize {
		return fmt.Errorf("unexpected fence region length: %d", s.FenceRegLength)
	}
	return nil
}

// IsValidSuperblock checks if the given data contains a valid superblock
// with correct V1 layout fields. Useful for idempotency checks.
func IsValidSuperblock(data []byte) bool {
	sb, err := UnmarshalSuperblock(data)
	if err != nil {
		return false
	}
	return sb.Validate() == nil
}

// HasSuperblockMagic checks if the given data starts with the SBR superblock
// magic bytes. This is used to detect block-format devices even when the
// superblock is invalid (e.g. bad CRC, unknown version), preventing
// destructive operations on a partially-written or incompatible device.
func HasSuperblockMagic(data []byte) bool {
	if len(data) < superblockMagicSize {
		return false
	}
	var magic [superblockMagicSize]byte
	copy(magic[:], data[:superblockMagicSize])
	return magic == SuperblockMagic
}

// IsQuiesced returns true if the quiesce flag is set, indicating that
// agents should stop heartbeating and enter a safe paused state.
func (s *Superblock) IsQuiesced() bool {
	return s.Flags&FlagQuiesce != 0
}
