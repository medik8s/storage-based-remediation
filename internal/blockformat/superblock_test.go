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
	"encoding/binary"
	"testing"
)

func TestNewV1Superblock(t *testing.T) {
	sb := NewV1Superblock()

	if sb.Magic != SuperblockMagic {
		t.Errorf("expected magic %q, got %q", string(SuperblockMagic[:]), string(sb.Magic[:]))
	}
	if sb.Version != 1 {
		t.Errorf("expected version 1, got %d", sb.Version)
	}
	if sb.NodeMapAOffset != BlockNodeMapAOffset {
		t.Errorf("expected NodeMapA offset %d, got %d", BlockNodeMapAOffset, sb.NodeMapAOffset)
	}
	if sb.NodeMapBOffset != BlockNodeMapBOffset {
		t.Errorf("expected NodeMapB offset %d, got %d", BlockNodeMapBOffset, sb.NodeMapBOffset)
	}
	if sb.HeartbeatRegOffset != BlockHeartbeatRegionOffset {
		t.Errorf("expected heartbeat offset %d, got %d", BlockHeartbeatRegionOffset, sb.HeartbeatRegOffset)
	}
	if sb.FenceRegOffset != BlockFenceRegionOffset {
		t.Errorf("expected fence offset %d, got %d", BlockFenceRegionOffset, sb.FenceRegOffset)
	}
	if sb.Flags != 0 {
		t.Errorf("expected flags 0, got %d", sb.Flags)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	sb := NewV1Superblock()

	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if len(data) != SuperblockTotalSize {
		t.Fatalf("expected %d bytes, got %d", SuperblockTotalSize, len(data))
	}

	parsed, err := UnmarshalSuperblock(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Magic != sb.Magic {
		t.Errorf("magic mismatch after round-trip")
	}
	if parsed.Version != sb.Version {
		t.Errorf("version mismatch: expected %d, got %d", sb.Version, parsed.Version)
	}
	if parsed.NodeMapAOffset != sb.NodeMapAOffset {
		t.Errorf("NodeMapA offset mismatch: expected %d, got %d", sb.NodeMapAOffset, parsed.NodeMapAOffset)
	}
	if parsed.NodeMapALength != sb.NodeMapALength {
		t.Errorf("NodeMapA length mismatch: expected %d, got %d", sb.NodeMapALength, parsed.NodeMapALength)
	}
	if parsed.NodeMapBOffset != sb.NodeMapBOffset {
		t.Errorf("NodeMapB offset mismatch: expected %d, got %d", sb.NodeMapBOffset, parsed.NodeMapBOffset)
	}
	if parsed.NodeMapBLength != sb.NodeMapBLength {
		t.Errorf("NodeMapB length mismatch: expected %d, got %d", sb.NodeMapBLength, parsed.NodeMapBLength)
	}
	if parsed.HeartbeatRegOffset != sb.HeartbeatRegOffset {
		t.Errorf("heartbeat offset mismatch: expected %d, got %d", sb.HeartbeatRegOffset, parsed.HeartbeatRegOffset)
	}
	if parsed.HeartbeatRegLength != sb.HeartbeatRegLength {
		t.Errorf("heartbeat length mismatch: expected %d, got %d", sb.HeartbeatRegLength, parsed.HeartbeatRegLength)
	}
	if parsed.FenceRegOffset != sb.FenceRegOffset {
		t.Errorf("fence offset mismatch: expected %d, got %d", sb.FenceRegOffset, parsed.FenceRegOffset)
	}
	if parsed.FenceRegLength != sb.FenceRegLength {
		t.Errorf("fence length mismatch: expected %d, got %d", sb.FenceRegLength, parsed.FenceRegLength)
	}
	if parsed.Flags != sb.Flags {
		t.Errorf("flags mismatch: expected %d, got %d", sb.Flags, parsed.Flags)
	}
}

func TestUnmarshalRejectsBadMagic(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Corrupt the magic
	data[0] = 'X'

	_, err = UnmarshalSuperblock(data)
	if err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}

func TestUnmarshalRejectsBadVersion(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Set version to 99
	binary.LittleEndian.PutUint16(data[6:8], 99)

	_, err = UnmarshalSuperblock(data)
	if err == nil {
		t.Fatal("expected error for bad version, got nil")
	}
}

func TestUnmarshalRejectsBadCRC(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Corrupt a data byte (not the CRC itself) to cause CRC mismatch
	data[10] ^= 0xFF

	_, err = UnmarshalSuperblock(data)
	if err == nil {
		t.Fatal("expected error for bad CRC, got nil")
	}
}

func TestUnmarshalRejectsTruncatedData(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Truncate to less than SuperblockTotalSize
	for _, size := range []int{0, 1, 40, SuperblockTotalSize - 1} {
		_, err = UnmarshalSuperblock(data[:size])
		if err == nil {
			t.Errorf("expected error for %d-byte input, got nil", size)
		}
	}
}

func TestUnmarshalAcceptsExtraData(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Pad with zeroes (simulates reading a full 4 KB sector)
	padded := make([]byte, BlockSectorSize)
	copy(padded, data)

	parsed, err := UnmarshalSuperblock(padded)
	if err != nil {
		t.Fatalf("Unmarshal with extra data failed: %v", err)
	}
	if parsed.Version != 1 {
		t.Errorf("expected version 1, got %d", parsed.Version)
	}
}

func TestUnmarshalRejectsZeroedData(t *testing.T) {
	data := make([]byte, SuperblockTotalSize)

	_, err := UnmarshalSuperblock(data)
	if err == nil {
		t.Fatal("expected error for all-zeroes superblock, got nil")
	}
}

func TestIsValidSuperblock(t *testing.T) {
	sb := NewV1Superblock()
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if !IsValidSuperblock(data) {
		t.Error("expected valid superblock to return true")
	}

	// Corrupt it
	data[0] = 'X'
	if IsValidSuperblock(data) {
		t.Error("expected corrupted superblock to return false")
	}

	// All zeroes
	if IsValidSuperblock(make([]byte, SuperblockTotalSize)) {
		t.Error("expected zeroed data to return false")
	}

	// Valid CRC but wrong layout lengths — IsValidSuperblock calls Validate
	bad := NewV1Superblock()
	bad.HeartbeatRegLength = 999
	badData, err := bad.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if IsValidSuperblock(badData) {
		t.Error("expected IsValidSuperblock to reject valid-CRC superblock with wrong layout lengths")
	}
}

func TestValidate(t *testing.T) {
	sb := NewV1Superblock()
	if err := sb.Validate(); err != nil {
		t.Errorf("valid superblock failed Validate: %v", err)
	}

	// Tamper with offsets
	bad := NewV1Superblock()
	bad.NodeMapAOffset = 999
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong NodeMapA offset")
	}

	bad = NewV1Superblock()
	bad.HeartbeatRegOffset = 0
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong heartbeat offset")
	}

	bad = NewV1Superblock()
	bad.FenceRegOffset = 0
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong fence offset")
	}

	// Length checks
	bad = NewV1Superblock()
	bad.NodeMapALength = 999
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong NodeMapA length")
	}

	bad = NewV1Superblock()
	bad.NodeMapBLength = 999
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong NodeMapB length")
	}

	bad = NewV1Superblock()
	bad.HeartbeatRegLength = 999
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong heartbeat length")
	}

	bad = NewV1Superblock()
	bad.FenceRegLength = 999
	if err := bad.Validate(); err == nil {
		t.Error("expected Validate to fail for wrong fence length")
	}
}

func TestLayoutConstantsMatchSpec(t *testing.T) {
	// Verify constants match rwx-block-volume-format.md
	if BlockSectorSize != 4096 {
		t.Errorf("BlockSectorSize: expected 4096, got %d", BlockSectorSize)
	}
	if BlockSuperblockOffset != 0 {
		t.Errorf("BlockSuperblockOffset: expected 0, got %d", BlockSuperblockOffset)
	}
	if BlockSuperblockSize != 4096 {
		t.Errorf("BlockSuperblockSize: expected 4096, got %d", BlockSuperblockSize)
	}
	if BlockNodeMapAOffset != 4096 {
		t.Errorf("BlockNodeMapAOffset: expected 4096, got %d", BlockNodeMapAOffset)
	}
	if BlockNodeMapBOffset != 69632 {
		t.Errorf("BlockNodeMapBOffset: expected 69632, got %d", BlockNodeMapBOffset)
	}
	if BlockNodeMapRegionSize != 65536 {
		t.Errorf("BlockNodeMapRegionSize: expected 65536, got %d", BlockNodeMapRegionSize)
	}
	if BlockHeartbeatRegionOffset != 135168 {
		t.Errorf("BlockHeartbeatRegionOffset: expected 135168, got %d", BlockHeartbeatRegionOffset)
	}
	if BlockFenceRegionOffset != 1179648 {
		t.Errorf("BlockFenceRegionOffset: expected 1179648, got %d", BlockFenceRegionOffset)
	}
	if BlockSlotSize != 4096 {
		t.Errorf("BlockSlotSize: expected 4096, got %d", BlockSlotSize)
	}
	if BlockMinDeviceSize != 2224128 {
		t.Errorf("BlockMinDeviceSize: expected 2224128, got %d", BlockMinDeviceSize)
	}

	// Verify derived relationships
	expectedNodeMapB := BlockNodeMapAOffset + BlockNodeMapRegionSize
	if BlockNodeMapBOffset != expectedNodeMapB {
		t.Errorf("NodeMapB should follow NodeMapA: expected %d, got %d",
			expectedNodeMapB, BlockNodeMapBOffset)
	}

	expectedHBOffset := BlockNodeMapBOffset + BlockNodeMapRegionSize
	if BlockHeartbeatRegionOffset != expectedHBOffset {
		t.Errorf("heartbeat should follow NodeMapB: expected %d, got %d",
			expectedHBOffset, BlockHeartbeatRegionOffset)
	}

	expectedFenceOffset := BlockHeartbeatRegionOffset + BlockMaxNodes*BlockSlotSize
	if BlockFenceRegionOffset != expectedFenceOffset {
		t.Errorf("fence should follow heartbeat: expected %d, got %d",
			expectedFenceOffset, BlockFenceRegionOffset)
	}

	expectedMinSize := BlockFenceRegionOffset + BlockMaxNodes*BlockSlotSize
	if BlockMinDeviceSize != expectedMinSize {
		t.Errorf("min device size should be end of fence region: expected %d, got %d",
			expectedMinSize, BlockMinDeviceSize)
	}
}

func TestIsQuiesced(t *testing.T) {
	sb := NewV1Superblock()
	if sb.IsQuiesced() {
		t.Error("new V1 superblock should not be quiesced")
	}

	sb.Flags = FlagQuiesce
	if !sb.IsQuiesced() {
		t.Error("superblock with FlagQuiesce set should be quiesced")
	}

	// Verify quiesce flag survives marshal/unmarshal round-trip
	data, err := sb.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	parsed, err := UnmarshalSuperblock(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !parsed.IsQuiesced() {
		t.Error("quiesce flag lost after round-trip")
	}
	if parsed.Flags != FlagQuiesce {
		t.Errorf("expected flags %d, got %d", FlagQuiesce, parsed.Flags)
	}
}

func TestFlagQuiesceConstant(t *testing.T) {
	if FlagQuiesce != 1 {
		t.Errorf("FlagQuiesce should be bit 0 (value 1), got %d", FlagQuiesce)
	}
}

func TestMarshalDeterministic(t *testing.T) {
	sb := NewV1Superblock()

	data1, err := sb.Marshal()
	if err != nil {
		t.Fatalf("first Marshal failed: %v", err)
	}

	data2, err := sb.Marshal()
	if err != nil {
		t.Fatalf("second Marshal failed: %v", err)
	}

	if len(data1) != len(data2) {
		t.Fatalf("lengths differ: %d vs %d", len(data1), len(data2))
	}
	for i := range data1 {
		if data1[i] != data2[i] {
			t.Fatalf("byte %d differs: 0x%02x vs 0x%02x", i, data1[i], data2[i])
		}
	}
}

func TestHasSuperblockMagic(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{
			name:     "valid V1 superblock",
			data:     func() []byte { d, _ := NewV1Superblock().Marshal(); return d }(),
			expected: true,
		},
		{
			name:     "just the magic bytes",
			data:     SuperblockMagic[:],
			expected: true,
		},
		{
			name:     "magic with trailing garbage",
			data:     append(SuperblockMagic[:], make([]byte, 100)...),
			expected: true,
		},
		{
			name:     "empty data",
			data:     []byte{},
			expected: false,
		},
		{
			name:     "too short",
			data:     []byte("SBR"),
			expected: false,
		},
		{
			name:     "wrong magic",
			data:     []byte("NOTSBR"),
			expected: false,
		},
		{
			name:     "all zeros",
			data:     make([]byte, 80),
			expected: false,
		},
		{
			name: "valid magic but corrupted CRC (still has magic)",
			data: func() []byte {
				d, _ := NewV1Superblock().Marshal()
				d[76] ^= 0xFF // corrupt CRC
				return d
			}(),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasSuperblockMagic(tt.data)
			if got != tt.expected {
				t.Errorf("HasSuperblockMagic() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHasSuperblockMagicVsIsValidSuperblock(t *testing.T) {
	// A superblock with valid magic but corrupted CRC should:
	// - HasSuperblockMagic: true (magic is present)
	// - IsValidSuperblock: false (CRC is wrong)
	data, err := NewV1Superblock().Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	data[76] ^= 0xFF // corrupt CRC

	if !HasSuperblockMagic(data) {
		t.Error("expected HasSuperblockMagic=true for corrupted CRC with valid magic")
	}
	if IsValidSuperblock(data) {
		t.Error("expected IsValidSuperblock=false for corrupted CRC")
	}
}
