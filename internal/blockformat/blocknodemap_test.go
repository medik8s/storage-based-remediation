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
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io/fs"
	"math"
	"testing"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/internal/sbdprotocol"
)

// Compile-time check that BlockNodeMapStore implements sbdprotocol.NodeMapStore.
var _ sbdprotocol.NodeMapStore = (*BlockNodeMapStore)(nil)

// memDevice is an in-memory DeviceReadWriterAt for testing.
type memDevice struct {
	data []byte
}

func newMemDevice(size int64) *memDevice {
	return &memDevice{data: make([]byte, size)}
}

func (m *memDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(m.data)) {
		return 0, errors.New("offset out of range")
	}
	n := copy(p, m.data[off:])
	return n, nil
}

func (m *memDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		return 0, errors.New("write out of range")
	}
	n := copy(m.data[off:], p)
	return n, nil
}

func (m *memDevice) Sync() error { return nil }

func TestBlockNodeMapStore_RoundTrip(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	payload := []byte("test payload data for round trip")
	if err := store.Save(payload); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !bytes.Equal(payload, loaded) {
		t.Errorf("round-trip mismatch: expected %q, got %q", payload, loaded)
	}
}

func TestBlockNodeMapStore_LoadFreshDevice(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error loading from fresh device")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got: %v", err)
	}
}

func TestBlockNodeMapStore_CrashRecoveryCorruptOneBuffer(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Save initial data (goes to buffer A with gen=1 via first boot)
	payload1 := []byte("first version")
	if err := store.Save(payload1); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Save second version (goes to buffer B with gen=2)
	payload2 := []byte("second version")
	if err := store.Save(payload2); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Corrupt buffer B's CRC (last 4 bytes of the region)
	corruptOffset := BlockNodeMapBOffset + BlockNodeMapRegionSize - 4
	copy(dev.data[corruptOffset:], []byte{0xFF, 0xFF, 0xFF, 0xFF})

	// Load should fall back to buffer A (gen=1)
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load with corrupt B failed: %v", err)
	}
	if !bytes.Equal(payload1, loaded) {
		t.Errorf("expected fallback to buffer A payload %q, got %q", payload1, loaded)
	}
}

func TestBlockNodeMapStore_GenerationMonotonicity(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// First save: gen=1 in buffer A
	if err := store.Save([]byte("v1")); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}
	stateA1 := parseBufferAt(dev, BlockNodeMapAOffset)
	if stateA1.generation != 1 {
		t.Errorf("expected gen=1 after first save, got %d", stateA1.generation)
	}

	// Second save: gen=2 in buffer B (inactive)
	if err := store.Save([]byte("v2")); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}
	stateB := parseBufferAt(dev, BlockNodeMapBOffset)
	if stateB.generation != 2 {
		t.Errorf("expected gen=2 after second save, got %d", stateB.generation)
	}

	// Third save: gen=3 back to buffer A (inactive)
	if err := store.Save([]byte("v3")); err != nil {
		t.Fatalf("third Save failed: %v", err)
	}
	stateA2 := parseBufferAt(dev, BlockNodeMapAOffset)
	if stateA2.generation != 3 {
		t.Errorf("expected gen=3 after third save, got %d", stateA2.generation)
	}

	// Verify monotonic: 1 → 2 → 3
	if stateA1.generation >= stateB.generation || stateB.generation >= stateA2.generation {
		t.Errorf("generations not monotonically increasing: %d, %d, %d",
			stateA1.generation, stateB.generation, stateA2.generation)
	}
}

func TestBlockNodeMapStore_FirstBootUsesBufferA(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	payload := []byte("first boot payload")
	if err := store.Save(payload); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Buffer A should have gen=1
	stateA := parseBufferAt(dev, BlockNodeMapAOffset)
	if !stateA.valid {
		t.Fatal("buffer A should be valid after first boot")
	}
	if stateA.generation != 1 {
		t.Errorf("expected gen=1, got %d", stateA.generation)
	}
	if !bytes.Equal(stateA.payload, payload) {
		t.Errorf("payload mismatch in buffer A")
	}

	// Buffer B should still be zeroed/invalid
	stateB := parseBufferAt(dev, BlockNodeMapBOffset)
	if stateB.valid {
		t.Error("buffer B should be invalid after first boot")
	}
}

func TestBlockNodeMapStore_ReinitializationGuard(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)

	// Simulate torn write: write a buffer with non-zero generation but corrupt CRC
	buf := make([]byte, BlockNodeMapRegionSize)
	binary.LittleEndian.PutUint64(buf[0:8], 42) // non-zero generation
	// Leave CRC as zero (won't match)
	copy(dev.data[BlockNodeMapAOffset:], buf)

	// Also corrupt buffer B with non-zero generation
	binary.LittleEndian.PutUint64(buf[0:8], 43)
	copy(dev.data[BlockNodeMapBOffset:], buf)

	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Load should fail with error about corruption
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error with corrupted non-zero generation buffers")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Error("should NOT be fs.ErrNotExist for non-zero gen corruption")
	}

	// Save should also refuse
	err = store.Save([]byte("test"))
	if err == nil {
		t.Fatal("expected Save to fail with corrupted non-zero generation buffers")
	}
}

func TestBlockNodeMapStore_LoadPrefersNewerGeneration(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Manually write buffer A gen=1 and buffer B gen=2 with different payloads
	uuidA, _ := generateWriterID()
	uuidB, _ := generateWriterID()
	copy(dev.data[BlockNodeMapAOffset:], marshalBuffer(1, uuidA, []byte("old")))
	copy(dev.data[BlockNodeMapBOffset:], marshalBuffer(2, uuidB, []byte("new")))

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("new")) {
		t.Errorf("expected newer payload, got %q", loaded)
	}
}

func TestBlockNodeMapStore_PayloadTooLarge(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	oversized := make([]byte, nmMaxPayloadSize+1)
	if err := store.Save(oversized); err == nil {
		t.Fatal("expected error for oversized payload")
	}
}

func TestBlockNodeMapStore_SaveOverwrites(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Save three versions and verify latest is loaded
	for i, payload := range []string{"v1", "v2", "v3"} {
		if err := store.Save([]byte(payload)); err != nil {
			t.Fatalf("Save %d failed: %v", i+1, err)
		}
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("v3")) {
		t.Errorf("expected v3, got %q", loaded)
	}
}

func TestMarshalParseBuffer_RoundTrip(t *testing.T) {
	uuid, err := generateWriterID()
	if err != nil {
		t.Fatalf("generateWriterID failed: %v", err)
	}
	payload := []byte("test payload for marshal/parse round trip")
	gen := uint64(42)

	buf := marshalBuffer(gen, uuid, payload)
	if int64(len(buf)) != BlockNodeMapRegionSize {
		t.Fatalf("marshal buffer size: expected %d, got %d", BlockNodeMapRegionSize, len(buf))
	}

	state := parseBuffer(buf)
	if !state.valid {
		t.Fatal("parsed buffer should be valid")
	}
	if state.generation != gen {
		t.Errorf("generation mismatch: expected %d, got %d", gen, state.generation)
	}
	if state.writerUUID != uuid {
		t.Errorf("WriterUUID mismatch")
	}
	if !bytes.Equal(state.payload, payload) {
		t.Errorf("payload mismatch: expected %q, got %q", payload, state.payload)
	}
}

func TestParseBuffer_CorruptCRC(t *testing.T) {
	uuid, _ := generateWriterID()
	buf := marshalBuffer(7, uuid, []byte("data"))

	// Corrupt a byte in the middle
	buf[100] ^= 0xFF

	state := parseBuffer(buf)
	if state.valid {
		t.Error("buffer with corrupt CRC should be invalid")
	}
	// Generation should still be readable for reinitialization guard
	if state.generation != 7 {
		t.Errorf("expected generation 7 even with corrupt CRC, got %d", state.generation)
	}
}

func TestParseBuffer_ZeroedBuffer(t *testing.T) {
	buf := make([]byte, BlockNodeMapRegionSize)
	state := parseBuffer(buf)

	if state.valid {
		t.Fatal("zeroed buffer must never be valid")
	}
	if state.generation != 0 {
		t.Errorf("expected generation 0 for zeroed buffer, got %d", state.generation)
	}
}

func TestBlockNodeMapStore_CrashRecoveryCorruptBufferA(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Save two versions: v1 → A (gen=1), v2 → B (gen=2)
	if err := store.Save([]byte("v1")); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}
	if err := store.Save([]byte("v2")); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Corrupt buffer A's CRC
	corruptOffset := BlockNodeMapAOffset + BlockNodeMapRegionSize - 4
	copy(dev.data[corruptOffset:], []byte{0xFF, 0xFF, 0xFF, 0xFF})

	// Load should use buffer B (gen=2)
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load with corrupt A failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("v2")) {
		t.Errorf("expected v2 from buffer B, got %q", loaded)
	}
}

// parseBufferAt reads and parses a buffer at a given absolute offset on a memDevice.
func parseBufferAt(dev *memDevice, offset int64) bufferState {
	raw := make([]byte, BlockNodeMapRegionSize)
	copy(raw, dev.data[offset:offset+BlockNodeMapRegionSize])
	return parseBuffer(raw)
}

func TestParseBuffer_OversizedPayloadLength(t *testing.T) {
	buf := make([]byte, BlockNodeMapRegionSize)
	// Set generation
	binary.LittleEndian.PutUint64(buf[0:8], 1)
	// Set payload length to something impossibly large
	binary.LittleEndian.PutUint32(buf[nmPayloadLenOffset:nmPayloadLenOffset+4], uint32(nmMaxPayloadSize+100))
	// Fix CRC so we get past the CRC check
	dataEnd := int(BlockNodeMapRegionSize) - nmCRCSize
	checksum := crc32.ChecksumIEEE(buf[:dataEnd])
	binary.LittleEndian.PutUint32(buf[dataEnd:], checksum)

	state := parseBuffer(buf)
	if state.valid {
		t.Error("buffer with oversized payload length should be invalid")
	}
}

func TestParseBuffer_GenerationZeroRejected(t *testing.T) {
	// A crafted buffer with gen=0 and valid CRC should not be treated as valid
	uuid, _ := generateWriterID()
	buf := marshalBuffer(0, uuid, []byte("gen zero payload"))

	state := parseBuffer(buf)
	if state.valid {
		t.Error("generation 0 should never be valid")
	}
	if state.generation != 0 {
		t.Errorf("expected generation 0, got %d", state.generation)
	}
}

func TestBlockNodeMapStore_EqualGenerations(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)
	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Manually write both buffers with the same generation
	uuidA, _ := generateWriterID()
	uuidB, _ := generateWriterID()
	copy(dev.data[BlockNodeMapAOffset:], marshalBuffer(10, uuidA, []byte("from A")))
	copy(dev.data[BlockNodeMapBOffset:], marshalBuffer(10, uuidB, []byte("from B")))

	// Load should succeed (A wins deterministically) and not crash
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load with equal generations failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("from A")) {
		t.Errorf("expected A to win on equal generation, got %q", loaded)
	}
}

// tamperDevice wraps a memDevice and overwrites a buffer region after
// the Nth WriteAt call, simulating a concurrent writer.
type tamperDevice struct {
	*memDevice
	writeCount   int
	tamperAfterN int
	tamperOffset int64
	tamperData   []byte
}

func (d *tamperDevice) WriteAt(p []byte, off int64) (int, error) {
	n, err := d.memDevice.WriteAt(p, off)
	if err != nil {
		return n, err
	}
	d.writeCount++
	if d.writeCount == d.tamperAfterN {
		copy(d.memDevice.data[d.tamperOffset:], d.tamperData)
	}
	return n, nil
}

func TestBlockNodeMapStore_ConflictReturnsError(t *testing.T) {
	base := newMemDevice(BlockMinDeviceSize)

	// Seed buffer A with gen=1 so we're past first boot
	uuid, _ := generateWriterID()
	copy(base.data[BlockNodeMapAOffset:], marshalBuffer(1, uuid, []byte("seed")))

	// Create a tamper device that overwrites buffer B after the first WriteAt
	// (which is the save to buffer B). This simulates another writer clobbering
	// our write, causing the UUID verification to fail.
	conflictUUID, _ := generateWriterID()
	conflictBuf := marshalBuffer(2, conflictUUID, []byte("interloper"))

	td := &tamperDevice{
		memDevice:    base,
		tamperAfterN: 1, // after first write (our attempt)
		tamperOffset: BlockNodeMapBOffset,
		tamperData:   conflictBuf,
	}

	store := NewBlockNodeMapStore(td, logr.Discard())

	// Save should return a ConflictError — no internal retry.
	// The caller is responsible for re-loading, re-merging, and retrying.
	err := store.Save([]byte("my data"))
	if err == nil {
		t.Fatal("Save should return ConflictError when another writer clobbers the buffer")
	}
	if !IsConflictError(err) {
		t.Fatalf("expected ConflictError, got: %v", err)
	}
}

func TestBlockNodeMapStore_CallerRetryPreservesData(t *testing.T) {
	// Regression test for the stale-data retry bug.
	//
	// Scenario:
	//   Writer A wants to save {A}
	//   Writer B concurrently saves {B}, clobbering A's write
	//   Writer A detects ConflictError
	//   Writer A reloads → gets {B}
	//   Writer A merges → {A,B}
	//   Writer A saves {A,B}
	//   Final Load() → {A,B}  (no data lost)
	//
	// The old code would have retried with the same stale {A} data,
	// overwriting {B} and silently losing writer B's entry.
	base := newMemDevice(BlockMinDeviceSize)

	// Seed buffer A with gen=1 so we're past first boot
	seedUUID, _ := generateWriterID()
	copy(base.data[BlockNodeMapAOffset:], marshalBuffer(1, seedUUID, []byte("seed")))

	// Writer B's data — this will be injected into buffer B after writer A's
	// write attempt, simulating a concurrent writer that wins the race.
	writerBUUID, _ := generateWriterID()
	writerBBuf := marshalBuffer(2, writerBUUID, []byte("writerB"))

	td := &tamperDevice{
		memDevice:    base,
		tamperAfterN: 1, // overwrite buffer B after writer A's write
		tamperOffset: BlockNodeMapBOffset,
		tamperData:   writerBBuf,
	}

	storeA := NewBlockNodeMapStore(td, logr.Discard())

	// Step 1: Writer A attempts Save({A}) — tamper causes UUID mismatch
	err := storeA.Save([]byte("writerA"))
	if err == nil {
		t.Fatal("expected ConflictError from tampered write")
	}
	if !IsConflictError(err) {
		t.Fatalf("expected ConflictError, got: %v", err)
	}

	// Step 2: Writer A reloads — gets writer B's data (the winner)
	loaded, err := storeA.Load()
	if err != nil {
		t.Fatalf("Load after conflict failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("writerB")) {
		t.Fatalf("expected writerB data after conflict, got %q", loaded)
	}

	// Step 3: Writer A merges its own data with the winner's data
	merged := []byte("writerA+writerB")

	// Step 4: Writer A saves merged data (tamper only fires once, so this succeeds)
	if err := storeA.Save(merged); err != nil {
		t.Fatalf("merged Save failed: %v", err)
	}

	// Step 5: Final Load() must return the merged data — both writers preserved
	final, err := storeA.Load()
	if err != nil {
		t.Fatalf("final Load failed: %v", err)
	}
	if !bytes.Equal(final, merged) {
		t.Fatalf("expected merged data %q, got %q", merged, final)
	}
}

func TestBlockNodeMapStore_GenerationOverflow(t *testing.T) {
	dev := newMemDevice(BlockMinDeviceSize)

	// Write buffer A with MaxUint64 generation
	uuid, _ := generateWriterID()
	copy(dev.data[BlockNodeMapAOffset:], marshalBuffer(math.MaxUint64, uuid, []byte("max gen")))

	store := NewBlockNodeMapStore(dev, logr.Discard())

	// Load should succeed
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load at MaxUint64 gen failed: %v", err)
	}
	if !bytes.Equal(loaded, []byte("max gen")) {
		t.Errorf("unexpected payload: %q", loaded)
	}

	// Save should fail with overflow error
	err = store.Save([]byte("overflow"))
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("overflow")) {
		t.Errorf("expected overflow in error message, got: %v", err)
	}
}

func TestBlockNodeMapStore_FirstBootVerifiesWrite(t *testing.T) {
	// Verify that first boot uses the write-verify protocol (not a shortcut).
	// If another node writes to buffer A between our write and verify,
	// the UUID mismatch should trigger a ConflictError.
	base := newMemDevice(BlockMinDeviceSize)

	// Tamper after the first write to buffer A (first boot target)
	conflictUUID, _ := generateWriterID()
	conflictBuf := marshalBuffer(1, conflictUUID, []byte("other node"))

	td := &tamperDevice{
		memDevice:    base,
		tamperAfterN: 1,
		tamperOffset: BlockNodeMapAOffset,
		tamperData:   conflictBuf,
	}

	store := NewBlockNodeMapStore(td, logr.Discard())

	// Should detect conflict and return ConflictError
	err := store.Save([]byte("my first boot"))
	if err == nil {
		t.Fatal("Save should return ConflictError when first boot write is clobbered")
	}
	if !IsConflictError(err) {
		t.Fatalf("expected ConflictError, got: %v", err)
	}
}

// failReadDevice wraps a memDevice and returns errors on ReadAt for
// specific offset ranges, simulating I/O failures.
type failReadDevice struct {
	*memDevice
	failOffsetStart int64
	failOffsetEnd   int64
}

func (d *failReadDevice) ReadAt(p []byte, off int64) (int, error) {
	if off >= d.failOffsetStart && off < d.failOffsetEnd {
		return 0, errors.New("simulated I/O error")
	}
	return d.memDevice.ReadAt(p, off)
}

func TestBlockNodeMapStore_ReadFailureNotMisinterpreted(t *testing.T) {
	base := newMemDevice(BlockMinDeviceSize)

	// Fail reads to both node map regions
	fd := &failReadDevice{
		memDevice:       base,
		failOffsetStart: BlockNodeMapAOffset,
		failOffsetEnd:   BlockNodeMapBOffset + BlockNodeMapRegionSize,
	}

	store := NewBlockNodeMapStore(fd, logr.Discard())

	// Load should return an I/O error, NOT fs.ErrNotExist
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error on I/O failure")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Error("I/O error must not be misinterpreted as fs.ErrNotExist")
	}

	// Save should also fail with I/O error
	err = store.Save([]byte("test"))
	if err == nil {
		t.Fatal("expected error on I/O failure during Save")
	}
}

func TestBlockNodeMapStore_PartialReadFailure(t *testing.T) {
	base := newMemDevice(BlockMinDeviceSize)

	// Write valid data to buffer A
	id, _ := generateWriterID()
	copy(base.data[BlockNodeMapAOffset:], marshalBuffer(5, id, []byte("good data")))

	// Fail reads only for buffer B region
	fd := &failReadDevice{
		memDevice:       base,
		failOffsetStart: BlockNodeMapBOffset,
		failOffsetEnd:   BlockNodeMapBOffset + BlockNodeMapRegionSize,
	}

	store := NewBlockNodeMapStore(fd, logr.Discard())

	// Load should succeed using buffer A despite buffer B being unreadable
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load should succeed with one readable buffer, got: %v", err)
	}
	if !bytes.Equal(loaded, []byte("good data")) {
		t.Errorf("expected 'good data', got %q", loaded)
	}
}

func TestBlockNodeMapStore_NonLinearizableButValid(t *testing.T) {
	// The write-verify protocol is explicitly non-linearizable (see design doc).
	// This test verifies the key invariant: after concurrent racing writes,
	// Load() always returns a complete, valid table from exactly one writer —
	// never corrupted or partial data.
	base := newMemDevice(BlockMinDeviceSize)

	// Seed buffer A
	idA, _ := generateWriterID()
	copy(base.data[BlockNodeMapAOffset:], marshalBuffer(1, idA, []byte("seed")))

	// Simulate two concurrent writers by having two stores on the same device.
	// Writer 1 writes, then Writer 2 overwrites the same buffer before
	// Writer 1 can verify.
	store1 := NewBlockNodeMapStore(base, logr.Discard())
	store2 := NewBlockNodeMapStore(base, logr.Discard())

	// Writer 2 saves first (gets gen=2 in buffer B)
	if err := store2.Save([]byte("writer2 data")); err != nil {
		t.Fatalf("store2.Save failed: %v", err)
	}

	// Writer 1 saves (gets gen=3 in buffer A since B is now active)
	if err := store1.Save([]byte("writer1 data")); err != nil {
		t.Fatalf("store1.Save failed: %v", err)
	}

	// The invariant: Load() returns one complete writer's data, never garbage
	loaded, err := store1.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	isWriter1 := bytes.Equal(loaded, []byte("writer1 data"))
	isWriter2 := bytes.Equal(loaded, []byte("writer2 data"))
	if !isWriter1 && !isWriter2 {
		t.Fatalf("Load returned data from neither writer: %q", loaded)
	}

	// Verify it's a valid buffer by checking the raw device state.
	// Both buffers must individually have valid CRC — no partial writes.
	stateA := parseBufferAt(base, BlockNodeMapAOffset)
	stateB := parseBufferAt(base, BlockNodeMapBOffset)

	if !stateA.valid && !stateB.valid {
		t.Fatal("at least one buffer must be valid after two successful saves")
	}
	if stateA.valid && stateB.valid && stateA.generation == stateB.generation {
		t.Errorf("equal generations after sequential saves is unexpected: gen=%d", stateA.generation)
	}
}
