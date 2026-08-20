# Block Device Format Specification v1

This document specifies the binary on-disk format for SBR Block mode. It is
the implementation companion to
[rwx-block-volume-support.md](rwx-block-volume-support.md).

All integer fields use **little-endian** byte order, consistent with the
existing SBD message format (`message.go`). All CRC32 checksums use the
**IEEE polynomial** (`crc32.ChecksumIEEE` in Go, polynomial `0xEDB88320`),
matching the existing SBD protocol.

## Device Layout

```text
Sector(4K)   Offset (bytes)   Size          Region
────────────────────────────────────────────────────────────────────────────
0            0                4,096         Superblock
1-16         4,096            65,536        Node Map Buffer A
17-32        69,632           65,536        Node Map Buffer B
33-287       135,168          1,044,480     Heartbeat Slots (255 x 4096)
288-542      1,179,648        1,044,480     Fence Slots (255 x 4096)
────────────────────────────────────────────────────────────────────────────
Total                         2,224,128     (~2.12 MB)
```

Go constants:

```go
const (
    BlockSectorSize            int64 = 4096
    BlockSuperblockOffset      int64 = 0
    BlockSuperblockSize        int64 = 4096
    BlockNodeMapAOffset        int64 = 4096
    BlockNodeMapBOffset        int64 = 69632
    BlockNodeMapRegionSize     int64 = 65536   // 16 sectors per buffer
    BlockHeartbeatRegionOffset int64 = 135168
    BlockFenceRegionOffset     int64 = 1179648
    BlockSlotSize              int64 = 4096    // per-slot I/O granularity
)
```

## Superblock (Sector 0)

The superblock is formatted once by the Init Job and is **almost
immutable** thereafter — only the Flags field may be modified during
operator-orchestrated migration (see Flags and Migration Procedure).

Normal `--init` behavior is idempotent and non-destructive: if a valid V1
superblock already exists, the job logs "device already initialized" and
exits 0 without modifying the device.

If a new layout version is required, a distinct migration/reformat mode
is used. The migration job accepts a `--migrate --target-version=<N>`
flag and follows this sequence:

1. Read the existing superblock and confirm the device is quiesced
   (Quiesce flag set). Refuse to proceed if agents have not paused.
2. Rewrite the superblock with the target layout version, recomputing
   CRC32 over bytes 0–75 and calling `Sync()`.
3. If the target version requires a different layout (region offsets or
   sizes), zero and reinitialize the affected regions.

This separation ensures `--init` never destroys a valid superblock, while
`--migrate` requires an explicit target version and a quiesced device as
preconditions.

```text
Byte Offset   Size   Field
───────────────────────────────────────────
0             6      Magic ("SBRBLK")
6             2      Version (uint16 LE, value 1 → bytes 0x01 0x00)
8             8      Node Map A Offset
16            8      Node Map A Length
24            8      Node Map B Offset
32            8      Node Map B Length
40            8      Heartbeat Region Offset
48            8      Heartbeat Region Length
56            8      Fence Region Offset
64            8      Fence Region Length
72            4      Flags (uint32, LE; see Flags)
76            4      CRC32 (bytes 0–75)
───────────────────────────────────────────
Total: 80 bytes (padded to 4096 by zeroes)
```

### Version Validation

On startup, the agent reads the superblock and validates the magic and
version fields:
- If the magic does not match `SBRBLK`, the agent refuses to start.
- If the version is unrecognized, the agent refuses to start.
- The controller surfaces the resulting agent failure through status
  conditions and events.

### Flags

The Flags field is a 32-bit bitmask. V1 defines one flag; remaining bits
are reserved and must be zero.

| Bit | Name     | Description |
|-----|----------|-------------|
| 0   | Quiesce  | When set, agents must stop heartbeating and enter a safe paused state. Used during operator-orchestrated format migration (e.g. V1 → V2). The controller or a migration job sets this bit; agents detect it via periodic superblock re-reads. |
| 1–31 | Reserved | Must be zero. Agents must reject superblocks with unknown flags set. |

The quiesce flag makes the superblock **almost immutable**: it is written
once by the init job and only modified during controlled migration
operations (never during normal runtime). Only the controller or a
migration job writes the quiesce flag — agents never modify the
superblock.

```go
const (
    FlagQuiesce uint32 = 1 << 0
)

func (s *Superblock) IsQuiesced() bool {
    return s.Flags&FlagQuiesce != 0
}
```

### Modifying Flags

Any procedure that modifies the Flags field (including setting or
clearing Quiesce) must:

1. Read the full superblock sector (4096 bytes) from the device.
2. Unmarshal and validate the superblock (magic, version, CRC).
3. Update the Flags field in the parsed `Superblock` struct.
4. Re-marshal the superblock, which recomputes CRC32 over bytes 0–75
   and writes the updated checksum at bytes 76–79.
5. Write the re-marshalled superblock to offset 0.
6. Call `Sync()` to ensure durability before agents consume the change.

This applies to all flag modifications: the controller setting Quiesce
before migration, and the migration job clearing it after completion.

## Heartbeat and Fence Slots

Each slot is 4096 bytes. The protocol payload is unchanged:
- Heartbeat: 33 bytes (8 magic + 2 version + 1 type + 2 nodeID +
  8 timestamp + 8 sequence + 4 CRC)
- Fence: 36 bytes (33 header + 2 targetNodeID + 1 reason)

The remaining bytes in each 4 KB slot are zero padding. This satisfies
O_DIRECT alignment requirements on both 512e and 4Kn block devices.

### Slot Offset Formula

NodeIDs range from 1 to 255. Within each region:

```text
slot_offset = (nodeID - 1) * BlockSlotSize
```

Slot 0 does not exist (NodeID 0 is invalid per `IsValidNodeID()`). The
`OffsetDevice` base offset translates region-relative offsets to absolute
device offsets.

The existing `SBD_SLOT_SIZE` (512) remains the protocol constant. In Block
mode, `BlockSlotSize` (4096) is used for offset calculations.

## Node Map Buffer Format (64 KB each)

```text
Byte Offset   Size       Field
───────────────────────────────────────────────────
0             8          Generation (uint64, LE)
8             16         WriterUUID (raw bytes)
24            4          PayloadLength (uint32, LE)
28            variable   Payload (JSON data)
28+N          variable   Padding (zeroes)
65532         4          CRC32 (uint32, LE, bytes 0–65531)
───────────────────────────────────────────────────
Total: 65,536 bytes (64 KB)
```

The Payload field contains the output of `NodeMapTable.Marshal()`: a 4-byte
CRC32 prefix (the existing node map checksum) followed by the JSON-encoded
`NodeMapTable`. This is the same byte sequence that Filesystem mode writes
to the `sbr-device-nodemap` file. The outer envelope (Generation,
WriterUUID, PayloadLength, outer CRC32) is Block-mode specific; the inner
payload is mode-independent.

Maximum payload length: 65,504 bytes (65,536 - 28 byte header - 4 byte
CRC32). Writers must reject payloads exceeding this limit before writing;
readers must reject buffers where `PayloadLength > 65504`. In practice,
255 nodes x ~150 bytes ≈ 38 KB, well within the limit.

## O_DIRECT Requirements

1. **I/O alignment and length:** All reads and writes use 4096-byte aligned
   offsets and lengths. The minimum I/O unit is one 4 KB sector.

2. **Memory alignment:** I/O buffers must be page-aligned. Use
   `golang.org/x/sys/unix.Mmap` or `github.com/ncw/directio` to allocate
   page-aligned byte slices, replacing `make([]byte, size)` for raw block
   writes.

3. **Payload-to-sector mapping:** The SBD protocol payloads (33-byte
   heartbeat, 36-byte fence) are smaller than one 4 KB sector. In Block
   mode, each slot read/write operates on a full `BlockSlotSize` (4096)
   sector. To read a payload: read the full 4 KB sector, extract the first
   N bytes (payload size). To write a payload: zero a 4 KB buffer, copy the
   payload into the first N bytes, write the full 4 KB sector. The
   remaining bytes in each sector are zero-padding. This replaces the
   Filesystem mode's 512-byte `SBD_SLOT_SIZE` and `PadToSlotSize()`.

4. **Implementation note:** Every `ReadAt()` and `WriteAt()` path in Block
   mode — including node map I/O, heartbeat slots, fence slots, and init —
   must use page-aligned buffers of 4 KB-aligned length. It is easy to
   accidentally allocate with `make()` in one path and lose O_DIRECT
   compatibility.

In Filesystem mode, the current 512-byte padding via `PadToSlotSize()`
continues to work unchanged.

## OffsetDevice

```go
type OffsetDevice struct {
    device     DeviceReadWriterAt
    baseOffset int64
    regionSize int64  // bounds limit; 0 = unbounded
}

func NewOffsetDevice(dev DeviceReadWriterAt, baseOffset, regionSize int64) *OffsetDevice
// Panics if baseOffset or regionSize is negative (programming error).

func (d *OffsetDevice) ReadAt(p []byte, off int64) (int, error) {
    if off < 0 {
        return 0, fmt.Errorf("negative offset not allowed")
    }
    if d.regionSize > 0 && off+int64(len(p)) > d.regionSize {
        return 0, fmt.Errorf("read exceeds region size")
    }
    return d.device.ReadAt(p, d.baseOffset+off)
}

func (d *OffsetDevice) WriteAt(p []byte, off int64) (int, error) {
    // Same negative-offset and bounds checks as ReadAt
    return d.device.WriteAt(p, d.baseOffset+off)
}

func (d *OffsetDevice) Sync() error { return d.device.Sync() }
```

The agent opens the device once and creates three bounded logical views.
Each view's `regionSize` is set to its region boundary to prevent
accesses from crossing into adjacent regions:
- Heartbeat: `NewOffsetDevice(dev, BlockHeartbeatRegionOffset, BlockSlotSize * BlockMaxNodes)`
- Fence: `NewOffsetDevice(dev, BlockFenceRegionOffset, BlockSlotSize * BlockMaxNodes)`
- Node map: `NewOffsetDevice(dev, BlockNodeMapAOffset, BlockNodeMapRegionSize * 2)`

### Sync() Semantics

`blockdevice.Device.Sync()` issues an `fsync()` system call, requesting
that buffered data for the device be committed to stable storage according
to the storage stack's durability guarantees. Note that `fsync()` guarantees
local durability; visibility to other nodes depends on the distributed
storage backend's consistency model (Ceph RBD provides strong consistency
for overlapping I/O regions).

## NodeMapStore Interface

```go
type NodeMapStore interface {
    Load() ([]byte, error)
    Save(data []byte) error
}
```

### FileNodeMapStore

Wraps the current file-based logic unchanged:

```go
type FileNodeMapStore struct {
    filePath string
}

func (s *FileNodeMapStore) Load() ([]byte, error) {
    return os.ReadFile(s.filePath)
}

func (s *FileNodeMapStore) Save(data []byte) error {
    tmpPath := s.filePath + ".tmp"
    if err := os.WriteFile(tmpPath, data, 0644); err != nil {
        return err
    }
    return os.Rename(tmpPath, s.filePath)
}
```

### BlockNodeMapStore

Uses double-buffering with optimistic write-verify conflict detection.

## Write-Verify Protocol

In Filesystem mode, `flock()` on a cluster filesystem (CephFS, NFSv4)
provides cross-node serialization via the storage backend's metadata server
or distributed lock manager. Raw block devices bypass this infrastructure
entirely — there is no MDS or DLM to broker locks. The write-verify
protocol replaces distributed locking with optimistic conflict detection.
It does not provide linearizable updates; correctness relies on the node
map being non-authoritative (see Safety Analysis).

### Algorithm

1. **Prepare:** Node reads both buffers, verifies CRCs, selects the one
   with the highest Generation.
2. **Write:** Node increments the generation (`Gen = Gen + 1`), generates a
   random WriterUUID, constructs the 64 KB block, and writes it to the
   *inactive* buffer via `WriteAt()`.
3. **Flush:** Node issues `Sync()` to request that the write be committed
   to stable storage before the verification read. Without this, the
   readback in step 5 could return stale cached data on some storage
   backends.
4. **Wait:** Node sleeps for a randomized delay (1.5–2.5 seconds). This
   provides a bounded conflict detection window for in-flight concurrent
   writes to land on disk before verification. Correctness does not depend
   on storage latency assumptions; a failed verification causes retry. The
   delay is chosen to remain comfortably below the hardware watchdog timeout
   (typically 10–60 seconds).
5. **Verify:** Node reads back the inactive buffer from disk.
   - If CRC is valid AND Generation matches AND WriterUUID matches: success.
   - If mismatch: another node wrote concurrently. Back off with randomized
     jitter (50–200 ms) and retry from step 1.

### Crash Consistency (Torn Write Protection)

The CRC32 covers the entire 64 KB payload including the Generation and
WriterUUID. CRC32 verification detects torn writes with high probability
(~1 in 2³² chance of a false match). If a write mixes newly written sectors
with old sectors from a previous generation, the CRC check fails. On
restart, the agent detects the invalid CRC and falls back to the other
buffer.

### First Boot and Reinitialization Safety

If both buffers are invalid (freshly formatted device), the first agent to
start writes an empty node map to Buffer A with Generation=1 using the
**full write-verify protocol** (write, Sync, wait, readback). This ensures
that if multiple agents start simultaneously on a freshly formatted device,
only one writer's initialization succeeds — the others detect the conflict
via WriterUUID mismatch and retry, loading the winner's data.

**Reinitialization guard:** Before initializing, the agent verifies that
both buffers are truly empty (all zeroes or invalid CRCs with Generation=0).
If either buffer has a non-zero Generation with an invalid CRC, this
indicates a torn write from a previous operation rather than a fresh device.
In this case, the agent logs a warning and uses the other buffer if valid,
or refuses to start if both are corrupted with non-zero generations
(requiring operator intervention via the Init Job).

### Equal-Generation Conflict Resolution

When two nodes both read the same current generation G and both write G+1,
each with a different WriterUUID, the result depends on timing:

- If both writes target the same buffer, one overwrites the other. The
  verification step detects this for the overwritten writer (UUID mismatch).
- If both writes target different buffers (should not happen — both select
  the same inactive buffer based on generation), Load() resolves by
  selecting the buffer with the highest valid generation. If both are G+1,
  Buffer A wins (it is always read first, making the tie-break
  deterministic and consistent across all nodes).

In all cases, the losing writer retries. The generation counter is
monotonically increasing; after resolution, the winner's generation G+1 is
the new baseline.

### Safety Analysis

The write-verify protocol provides **eventual convergence with bounded stale
state**, not linearizable updates. The following analysis explains why this
is sufficient for SBR.

**The node map is non-authoritative advisory metadata.** It is used for
slot assignment and nodeID-to-name resolution. It is **not** consulted
during fencing decisions.

**Fencing decisions use heartbeat slot content directly:** The peer monitor
reads each slot, validates the CRC, checks the embedded NodeID against the
expected slot owner (`main.go:1079`), and evaluates timestamps/sequences.
The NodeID check validates the *logical* ID only — it confirms the slot
contains data written by an agent that *believes* it owns that slot. It
does not establish physical-writer uniqueness: if two nodes are assigned
the same slot (duplicate node map entry), both write valid heartbeats
with the same NodeID, and both pass the check.

**Duplicate assignment risk:** In a duplicate assignment scenario, if one
of the two nodes fails, the surviving node continues writing valid
heartbeats to the shared slot. Peers observe a healthy slot and do not
fence the failed node. This is a **liveness failure** (dead node not
fenced), not a safety violation (wrong node fenced). The protocol does
not have a physical-writer provenance mechanism (e.g. per-node UUID
embedded in each heartbeat) that would distinguish this case.

**Defense in depth — verification delay and watchdog:** The verification
delay (1.5–2.5 seconds) provides a bounded conflict detection window for
the node map write-verify protocol. The hardware watchdog bounds process
pauses. These reduce the probability of duplicate assignments reaching
the heartbeat layer but do not eliminate it.

**Heartbeat slot ownership self-check:** In Block mode, each agent
periodically reads back its own heartbeat slot and verifies the embedded
NodeID matches its assigned ID. If a mismatch is detected, the agent
triggers a node map reload and re-assignment. This detects conflicts
where a competing writer uses a *different* NodeID, but does not detect
duplicates where both writers use the same NodeID.

**Reconciliation:** Node map state is periodically reconciled. Duplicate
or stale assignments created by a missed conflict are detected during
the next reconciliation cycle and corrected (one node observes its slot
is occupied by another and re-assigns). The reconciliation interval
bounds the window of vulnerability to duplicate assignments.

### WriterUUID Logging

Agents log the WriterUUID on every write attempt and include both the local
and observed UUIDs in error messages when verification fails. If a collision
occurs in production, the UUIDs in pod logs allow direct correlation between
storage state and node behavior.

### Implementation Note: Heartbeat Loop Independence

The write-verify Wait step (1.5–2.5 seconds) must not block the heartbeat
loop. The `NodeManager` registration/update should run in a separate
goroutine or be invoked outside the heartbeat critical path to avoid falsely
triggering peer timeouts during delayed map updates.

## Init Job

The Init Job validates device capacity, zeroes the layout region, and
writes the V1 Superblock. The init runs via the agent image with the
`--init` flag (`sbr-agent --init --sbr-device=/sbr/block-device`), reusing
the superblock serialization code directly.

Steps:
1. Open device via `blockdevice.OpenWithTimeout()`
2. Read sector 0 and check for existing content:
   - If a valid V1 superblock exists (magic + version + CRC), log
     "device already initialized" and exit 0 (idempotency)
   - If magic matches `SBRBLK` but version is unrecognized, exit with
     error (prevents accidental downgrade of a newer-version device)
   - Otherwise (zeroed, junk, or corrupt): proceed with initialization
3. Verify device size ≥ `BlockMinDeviceSize` (2,224,128 bytes)
4. Zero exactly `BlockMinDeviceSize` bytes (the layout region) using
   1 MB chunks with page-aligned buffers
5. Serialize and write V1 Superblock to offset 0
6. `Sync()` to ensure durability on distributed storage

The zeroing range matches the layout size exactly — it does not extend
beyond `BlockMinDeviceSize`. This ensures the minimum device size
validation and the write range are consistent.

### Init Job Lifecycle

The controller gates DaemonSet creation on Init Job completion:

1. **Job succeeded** (`status.succeeded > 0`): DaemonSet proceeds.
2. **Job running**: Controller returns an error, blocking DaemonSet
   creation until the next reconciliation.
3. **Job failed**: Controller deletes the Job and recreates it on the
   next reconciliation cycle.
4. **Job not found**: Controller creates it.

This ensures agents never start heartbeat loops against an uninitialized
device. The init's read-before-write idempotency (step 2 above) is the
safety net for retries: if the Job is re-created (due to failure, TTL
garbage collection, or manual intervention), it detects the existing
superblock and exits without modifying the device.

The init job uses `volumeDevices` with the same device path as the agent.

## StorageClass Capability Testing

Known provisioners (`rbd.csi.ceph.com`) are validated via a fast path. For
all other provisioners, the controller creates a temporary PVC:

- Generated name (e.g. `sbr-capability-test-<hash>`) with `ownerReference`
  pointing to the `StorageBasedRemediationConfig` for automatic garbage
  collection.
- 60-second timeout for PVC binding, cleanup regardless of outcome.
- Result cached per StorageClass name for the lifetime of the controller
  process to avoid repeated probing.

## Storage Topologies

The structural equivalence between the two modes:

**Filesystem Mode:**
```text
/dev/sbr/ (directory)
  ├── sbr-device           (Heartbeat slots, implicit locking via OS)
  ├── sbr-device-fence     (Fence slots)
  └── sbr-device-nodemap   (JSON, crash-safe via rename(), concurrent via flock())
```

**Block Mode:**
```text
/sbr/block-device (raw device)
  ├── [Superblock]         (Immutable layout definitions)
  ├── [Node Map A & B]     (Double-buffered write-verify, replaces rename/flock)
  ├── [Heartbeat Slots]    (I/O adapted via OffsetDevice)
  └── [Fence Slots]        (I/O adapted via OffsetDevice)
```
