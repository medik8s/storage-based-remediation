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
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math"
	"math/big"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockdevice"
)

// Node map buffer envelope field sizes (within the 64 KB region).
const (
	nmGenSize          = 8                                                         // uint64 generation counter
	nmUUIDSize         = 16                                                        // WriterUUID
	nmPayloadLenSize   = 4                                                         // uint32 payload length
	nmPayloadLenOffset = nmGenSize + nmUUIDSize                                    // 24
	nmPayloadOffset    = nmPayloadLenOffset + nmPayloadLenSize                     // 28
	nmCRCSize          = 4                                                         // CRC32 at end of region
	nmMaxPayloadSize   = int(BlockNodeMapRegionSize) - nmPayloadOffset - nmCRCSize // 65504 bytes

	// Write-verify timing constants.
	verifyDelayMin    = 1500 * time.Millisecond
	verifyDelayMax    = 2500 * time.Millisecond
	conflictJitterMin = 50 * time.Millisecond
	conflictJitterMax = 200 * time.Millisecond
)

// bufferState holds the parsed state of one node map buffer.
type bufferState struct {
	valid      bool
	generation uint64
	writerUUID [nmUUIDSize]byte
	payload    []byte
	readErr    error // non-nil if the buffer could not be read from the device
}

// BlockNodeMapStore implements the NodeMapStore interface using double-buffered
// regions on a block device. It uses the write-verify protocol for optimistic
// conflict detection as specified in the format doc.
//
// The store operates on two 64 KB buffers (A and B) at fixed offsets on the
// block device. Each buffer has the format:
//
//	[Generation(8)] [WriterUUID(16)] [PayloadLen(4)] [Payload(var)] [pad] [CRC32(4)]
//
// The CRC32 covers bytes 0 through RegionSize-5 (65532 bytes).
type BlockNodeMapStore struct {
	bufA   *OffsetDevice
	bufB   *OffsetDevice
	logger logr.Logger
	// mu protects the reusable I/O buffers. It also serializes Load and Save
	// because the buffers are shared between operations.
	mu       sync.Mutex
	readBuf  []byte // pre-allocated, aligned to DirectIOAlignment, 64 KiB
	writeBuf []byte // pre-allocated, aligned to DirectIOAlignment, 64 KiB
}

// NewBlockNodeMapStore creates a BlockNodeMapStore operating on the given device.
// The device must be a DeviceReadWriterAt with regions at the standard offsets.
// Read and write buffers are pre-allocated with O_DIRECT-compatible alignment.
func NewBlockNodeMapStore(dev DeviceReadWriterAt, logger logr.Logger) *BlockNodeMapStore {
	return &BlockNodeMapStore{
		bufA:     NewOffsetDevice(dev, BlockNodeMapAOffset, BlockNodeMapRegionSize),
		bufB:     NewOffsetDevice(dev, BlockNodeMapBOffset, BlockNodeMapRegionSize),
		logger:   logger,
		readBuf:  blockdevice.DirectIOAlloc(int(BlockNodeMapRegionSize)),
		writeBuf: blockdevice.DirectIOAlloc(int(BlockNodeMapRegionSize)),
	}
}

// Load reads both node map buffers and returns the payload from the buffer
// with the highest valid generation. If both buffers are invalid and appear
// to be a freshly zeroed device, returns fs.ErrNotExist. If both are
// corrupted with non-zero generations, returns an error.
func (s *BlockNodeMapStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stateA := s.readBuffer(s.bufA, "A")
	stateB := s.readBuffer(s.bufB, "B")

	// I/O errors must not be confused with empty/corrupt buffers
	if stateA.readErr != nil && stateB.readErr != nil {
		return nil, fmt.Errorf("failed to read both node map buffers: A: %v, B: %v",
			stateA.readErr, stateB.readErr)
	}
	if stateA.readErr != nil && !stateB.valid {
		return nil, fmt.Errorf("buffer A unreadable and buffer B invalid: %w", stateA.readErr)
	}
	if stateB.readErr != nil && !stateA.valid {
		return nil, fmt.Errorf("buffer B unreadable and buffer A invalid: %w", stateB.readErr)
	}

	if stateA.valid && stateB.valid && stateA.generation == stateB.generation {
		s.logger.Info("both buffers have equal generation; possible protocol violation",
			"generation", stateA.generation)
	}

	if !stateA.valid && !stateB.valid {
		// Both invalid — check if this is a fresh device or corruption
		if stateA.generation == 0 && stateB.generation == 0 {
			return nil, fmt.Errorf("no valid node map on device: %w", fs.ErrNotExist)
		}
		// Non-zero generation with invalid CRC indicates torn write or corruption
		return nil, fmt.Errorf("both node map buffers corrupted (genA=%d, genB=%d); "+
			"requires reinitialization via Init Job", stateA.generation, stateB.generation)
	} else if stateA.valid && (!stateB.valid || stateA.generation >= stateB.generation) {
		s.logger.V(1).Info("Load: using buffer A", "genA", stateA.generation)
		return stateA.payload, nil
	} else {
		s.logger.V(1).Info("Load: using buffer B", "genB", stateB.generation)
		return stateB.payload, nil
	}
}

// Save writes node map data using the write-verify protocol:
//  1. Read both buffers, select the one with the highest valid generation
//  2. Write to the inactive buffer with generation+1 and a new WriterUUID
//  3. Sync the device
//  4. Wait a randomized delay (1.5–2.5s) for concurrent writes to land
//  5. Read back and verify generation + WriterUUID match
//
// On verification failure (concurrent write detected), Save returns a
// *ConflictError. The caller must handle retries by re-loading the current
// state, re-merging, and calling Save again with the updated data. Save
// does not retry internally because the caller's data becomes stale after
// a conflict — retrying with the same data would silently drop the
// concurrent writer's entries.
func (s *BlockNodeMapStore) Save(data []byte) error {
	if len(data) > nmMaxPayloadSize {
		return fmt.Errorf("payload too large: %d bytes, max %d", len(data), nmMaxPayloadSize)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveAttempt(data)
}

// ConflictError indicates a write-verify conflict: another writer
// modified the target buffer between the write and verify steps.
// The caller must re-load, re-merge, and retry with updated data.
type ConflictError struct {
	msg string
}

func (e *ConflictError) Error() string    { return e.msg }
func (e *ConflictError) IsConflict() bool { return true }

// IsConflictError reports whether the error is a write-verify conflict.
func IsConflictError(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

func (s *BlockNodeMapStore) saveAttempt(data []byte) error {
	// Step 1: Read both buffers to determine current state
	stateA := s.readBuffer(s.bufA, "A")
	stateB := s.readBuffer(s.bufB, "B")

	if stateA.readErr != nil || stateB.readErr != nil {
		return fmt.Errorf("failed to read node map buffers: A: %v, B: %v",
			stateA.readErr, stateB.readErr)
	}

	var activeGen uint64
	var targetBuf *OffsetDevice
	var targetName string

	if stateA.valid && stateB.valid && stateA.generation == stateB.generation {
		s.logger.Info("Save: both buffers have the same generation",
			"generation", stateA.generation)
	}

	if !stateA.valid && !stateB.valid {
		// First boot: both invalid
		if stateA.generation != 0 || stateB.generation != 0 {
			return fmt.Errorf("both buffers corrupted with non-zero generations (A=%d, B=%d); "+
				"requires reinitialization", stateA.generation, stateB.generation)
		}
		// First boot: write to buffer A with gen=1, using full write-verify
		s.logger.Info("Save: first boot, initializing buffer A")
		activeGen = 0
		targetBuf = s.bufA
		targetName = "A"
	} else if stateA.valid && (!stateB.valid || stateA.generation >= stateB.generation) {
		activeGen = stateA.generation
		targetBuf = s.bufB
		targetName = "B"
	} else {
		activeGen = stateB.generation
		targetBuf = s.bufA
		targetName = "A"
	}

	// Step 2: Write to inactive buffer
	if activeGen == math.MaxUint64 {
		return fmt.Errorf("generation counter overflow at %d; requires reinitialization", activeGen)
	}
	newGen := activeGen + 1
	writerUUID, err := generateWriterID()
	if err != nil {
		return err
	}

	marshalInto(s.writeBuf, newGen, writerUUID, data)
	s.logger.V(1).Info("Save: writing to inactive buffer",
		"target", targetName, "generation", newGen,
		"writerUUID", fmt.Sprintf("%x", writerUUID))

	if _, err := targetBuf.WriteAt(s.writeBuf, 0); err != nil {
		return fmt.Errorf("failed to write node map buffer %s: %w", targetName, err)
	}

	// Step 3: Sync
	if err := targetBuf.Sync(); err != nil {
		return fmt.Errorf("failed to sync after writing buffer %s: %w", targetName, err)
	}

	// Step 4: Wait
	delay := randomDuration(verifyDelayMin, verifyDelayMax)
	s.logger.V(1).Info("Save: waiting for verify delay", "delay", delay)
	time.Sleep(delay)

	// Step 5: Verify
	verifyState := s.readBuffer(targetBuf, targetName)
	if !verifyState.valid {
		return &ConflictError{msg: fmt.Sprintf("buffer %s invalid after write (CRC mismatch)", targetName)}
	}
	if verifyState.generation != newGen {
		return &ConflictError{msg: fmt.Sprintf("buffer %s generation mismatch: expected %d, got %d",
			targetName, newGen, verifyState.generation)}
	}
	if verifyState.writerUUID != writerUUID {
		return &ConflictError{msg: fmt.Sprintf("buffer %s WriterUUID mismatch: expected %x, got %x",
			targetName, writerUUID, verifyState.writerUUID)}
	}

	s.logger.V(1).Info("Save: write-verify succeeded", "buffer", targetName, "generation", newGen)
	return nil
}

// readBuffer reads and parses a 64 KB node map buffer.
// It uses the pre-allocated readBuf for O_DIRECT-aligned I/O.
func (s *BlockNodeMapStore) readBuffer(buf *OffsetDevice, name string) bufferState {
	raw := s.readBuf
	n, err := buf.ReadAt(raw, 0)
	if err != nil && err != io.EOF {
		s.logger.V(1).Info("readBuffer: read error", "buffer", name, "error", err)
		return bufferState{readErr: fmt.Errorf("read buffer %s: %w", name, err)}
	}
	if n != int(BlockNodeMapRegionSize) {
		s.logger.V(1).Info("readBuffer: short read", "buffer", name, "got", n, "expected", BlockNodeMapRegionSize)
		return bufferState{readErr: fmt.Errorf("short read buffer %s: got %d bytes, need %d", name, n, BlockNodeMapRegionSize)}
	}

	return parseBuffer(raw)
}

// parseBuffer parses a raw 64 KB buffer into a bufferState.
func parseBuffer(raw []byte) bufferState {
	if int64(len(raw)) < BlockNodeMapRegionSize {
		return bufferState{}
	}

	// Extract generation (even if CRC fails, for reinitialization guard)
	gen := binary.LittleEndian.Uint64(raw[0:nmGenSize])

	// Verify CRC (covers bytes 0 through RegionSize-5)
	dataEnd := int(BlockNodeMapRegionSize) - nmCRCSize
	expectedCRC := crc32.ChecksumIEEE(raw[:dataEnd])
	storedCRC := binary.LittleEndian.Uint32(raw[dataEnd : dataEnd+nmCRCSize])

	if expectedCRC != storedCRC {
		// Return generation for reinitialization guard even on CRC failure
		return bufferState{generation: gen}
	}

	// Parse header fields
	var uuid [nmUUIDSize]byte
	copy(uuid[:], raw[nmGenSize:nmGenSize+nmUUIDSize])

	payloadLen := binary.LittleEndian.Uint32(raw[nmPayloadLenOffset : nmPayloadLenOffset+nmPayloadLenSize])
	if int(payloadLen) > nmMaxPayloadSize {
		return bufferState{generation: gen}
	}

	// Generation 0 means "unused buffer" — never treat it as valid data,
	// even if someone crafts a buffer with correct CRC and gen=0.
	if gen == 0 {
		return bufferState{generation: 0}
	}

	payload := make([]byte, payloadLen)
	copy(payload, raw[nmPayloadOffset:nmPayloadOffset+int(payloadLen)])

	return bufferState{
		valid:      true,
		generation: gen,
		writerUUID: uuid,
		payload:    payload,
	}
}

// marshalInto writes node map fields into an existing buffer (must be
// at least BlockNodeMapRegionSize bytes). This allows using a
// pre-allocated, O_DIRECT-aligned buffer for production I/O.
func marshalInto(buf []byte, generation uint64, writerUUID [nmUUIDSize]byte, payload []byte) {
	clear(buf[:BlockNodeMapRegionSize])

	// Generation
	binary.LittleEndian.PutUint64(buf[0:nmGenSize], generation)

	// WriterUUID
	copy(buf[nmGenSize:nmGenSize+nmUUIDSize], writerUUID[:])

	// PayloadLength
	binary.LittleEndian.PutUint32(buf[nmPayloadLenOffset:nmPayloadLenOffset+nmPayloadLenSize], uint32(len(payload)))

	// Payload
	copy(buf[nmPayloadOffset:], payload)

	// CRC32 at the last 4 bytes, covering everything before it
	dataEnd := int(BlockNodeMapRegionSize) - nmCRCSize
	checksum := crc32.ChecksumIEEE(buf[:dataEnd])
	binary.LittleEndian.PutUint32(buf[dataEnd:dataEnd+nmCRCSize], checksum)
}

// marshalBuffer allocates and returns a new 64 KB buffer with the given fields.
// Used in tests; production code uses marshalInto with pre-allocated buffers.
func marshalBuffer(generation uint64, writerUUID [nmUUIDSize]byte, payload []byte) []byte {
	buf := make([]byte, BlockNodeMapRegionSize)
	marshalInto(buf, generation, writerUUID, payload)
	return buf
}

// generateWriterID generates a random 16-byte writer identifier for
// conflict detection. This is not an RFC4122 UUID — just random bytes.
func generateWriterID() ([nmUUIDSize]byte, error) {
	var id [nmUUIDSize]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("failed to generate writer ID: %w", err)
	}
	return id, nil
}

// randomDuration returns a random duration in [min, max].
func randomDuration(min, max time.Duration) time.Duration {
	diff := max - min
	if diff <= 0 {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(diff)+1))
	if err != nil {
		return min
	}
	return min + time.Duration(n.Int64())
}
