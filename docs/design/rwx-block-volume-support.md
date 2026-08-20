# RWX Block Volume Support for Heartbeat Backend

## Problem

SBR currently requires an RWX Filesystem StorageClass for its shared heartbeat
backend. The controller creates a PVC with `ReadWriteMany` access mode and
default `volumeMode: Filesystem`, mounts it as a directory at `/dev/sbr/`, and
the agent operates on three files inside that directory: `sbr-device`
(heartbeat slots), `sbr-device-fence` (fence slots), and `sbr-device-nodemap`
(JSON node map).

Kubernetes virtualization platforms (KubeVirt and distributions built on it)
typically use a StorageClass backed by Ceph RBD that provisions RWX Block
volumes for VM disks. Users want to reuse this StorageClass for SBR rather
than deploying a second StorageClass solely for SBR. Today this is not
possible: if a user points `sharedStorageClass` at their VM StorageClass, the
controller creates a Filesystem PVC, the CSI driver cannot provision it, and
the PVC stays Pending.

### Why This Matters

Requiring a separate RWX Filesystem StorageClass adds operational overhead.
Supporting RWX Block removes this deployment barrier and aligns SBR with the
storage topology that virtualization-enabled clusters already have.

## Design Principles

- Filesystem mode behavior remains unchanged.
- Block mode uses the same SBD protocol semantics.
- The shared device remains the sole source of truth; Kubernetes API is not
  used for heartbeat coordination.
- The block layout is versioned and self-describing.
- Safety is preferred over availability during ambiguous storage states.

## Current Limitations

Three areas of the codebase assume Filesystem semantics:

1. **PVC creation** (`storagebasedremediationconfig_controller.go:566-665`):
   The controller creates the PVC without setting `volumeMode` (defaults to
   `Filesystem`).

2. **DaemonSet volumes** (`storagebasedremediationconfig_controller.go:1801-1864`):
   The shared storage is mounted via `volumeMounts` (directory). Block PVCs
   require `volumeDevices` (raw block path).

3. **Node map persistence** (`sbdprotocol/nodemanager.go:438-674`): The
   `NodeManager` stores the node-to-slot mapping using `os.ReadFile()`, and
   `os.WriteFile()` + `os.Rename()`. `rename()` provides atomic, crash-safe
   updates on POSIX filesystems. Raw block devices do not support these
   operations and lack native atomicity for arbitrary-sized writes.

The heartbeat and fence I/O already use positioned `ReadAt`/`WriteAt` via the
`blockdevice.Device` type with `O_RDWR|O_SYNC|O_DIRECT`. While these work on
block devices, their current hardcoded 512-byte buffer sizes and alignments do
not safely guarantee O_DIRECT compatibility across all block device topologies
(especially 4K-native devices).

## Proposed Design

Add a `sharedStorageVolumeMode` field to `StorageBasedRemediationConfigSpec`.
When set to `Block`, the controller creates a Block PVC, exposes it as a raw
device via `volumeDevices`, and the agent partitions that single device into
logical regions.

To safely replace filesystem atomicity (`rename()`) and concurrency
(`flock()`), Block mode introduces device layout versioning and a
double-buffered node map with optimistic write-verify conflict detection.

The existing Filesystem path is unchanged. All behavioral differences are
gated on the volume mode.

### Architecture Overview

```text
              StorageBasedRemediationConfig
                           |
                sharedStorageVolumeMode
                      /            \
              Filesystem            Block
                  |                   |
           PVC (Filesystem)    PVC (Block Device)
                  |                   |
           volumeMounts         volumeDevices
                  |                   |
         /dev/sbr/ (directory)  /sbr/block-device (raw device)
           |     |     |          |    |    |    |
         hb   fence  nodemap    super nmap  hb  fence
        file   file   file      block bufs  slots slots
```

### CRD Changes

**File:** `api/v1alpha1/storagebasedremediationconfig_types.go`

```go
// SharedStorageVolumeMode defines the volume mode for the shared storage PVC.
// +kubebuilder:validation:Enum=Filesystem;Block
type SharedStorageVolumeMode string

const (
    SharedStorageVolumeModeFilesystem SharedStorageVolumeMode = "Filesystem"
    SharedStorageVolumeModeBlock      SharedStorageVolumeMode = "Block"
)
```

On the spec:

```go
// SharedStorageVolumeMode specifies the volume mode for the shared storage PVC.
// "Filesystem" (default): mounts the PVC as a directory.
// "Block": exposes the PVC as a raw block device.
// +optional
SharedStorageVolumeMode *SharedStorageVolumeMode `json:"sharedStorageVolumeMode,omitempty"`
```

Helper methods:

```go
func (s *StorageBasedRemediationConfigSpec) GetSharedStorageVolumeMode() corev1.PersistentVolumeMode {
    if s.SharedStorageVolumeMode != nil && *s.SharedStorageVolumeMode == SharedStorageVolumeModeBlock {
        return corev1.PersistentVolumeBlock
    }
    return corev1.PersistentVolumeFilesystem
}

func (s *StorageBasedRemediationConfigSpec) IsBlockMode() bool {
    return s.GetSharedStorageVolumeMode() == corev1.PersistentVolumeBlock
}
```

### PVC and Pod Changes

**PVC creation:** Set `VolumeMode` on the PVC spec based on the new field.
The 10Mi PVC request guarantees enough space for the ~2.12 MB block layout
regardless of CSI driver rounding.

**DaemonSet volumes:**
- **Filesystem mode** (unchanged): `volumeMounts` with `mountPath: /dev/sbr`.
- **Block mode**: `volumeDevices` with `devicePath: /sbr/block-device`.

**Agent arguments in Block mode:**
```text
--sbr-device=/sbr/block-device
--sbr-file-locking=false
```

The agent auto-detects block mode by probing sector 0 of the device for a
valid V1 superblock. No explicit `--sbr-volume-mode` flag is needed — the
device is self-describing. If a valid superblock is found, the agent enters
block mode. If `sharedStorageVolumeMode` is `Block` and the agent cannot
read a valid superblock during startup, the agent must exit with an error.
It must not fall back to filesystem mode.

`--sbr-file-locking=false` because `flock()` is not meaningful on a raw
block device. In Filesystem mode, when the PVC is backed by a cluster
filesystem (e.g. CephFS, NFSv4), `flock()` provides cluster-wide
serialization brokered by the storage backend's metadata server or
distributed lock manager. Raw block devices bypass this infrastructure
entirely, so `flock()` reverts to host-local semantics only.

**Init Job:** In Block mode, the Init Job runs the same agent image with
the `--init` flag (`sbr-agent --init --sbr-device=/sbr/block-device`). This
reuses the superblock serialization code directly, avoiding the fragility
of producing CRC32 checksums and computed layout fields in shell. The
init job reads sector 0 and distinguishes three cases:

- **Valid V1 superblock** (magic + version + CRC match): exit 0 without
  modifying the device (idempotency).
- **Valid SBRBLK magic but unrecognized version**: exit with error.
  This prevents a V1 init job from overwriting a newer-version device.
  Reformatting requires explicit operator action (e.g. zeroing sector 0).
- **All other content** (zeroed, junk, residual data from previous PVC
  tenant): validate device capacity, zero the layout region, write V1
  superblock, Sync(). CSI drivers do not guarantee zeroed blocks on
  freshly provisioned volumes, so the init job must handle arbitrary
  pre-existing content.

DaemonSet agents must not start heartbeat loops until the Init Job has
completed successfully. The agent handles node map initialization on
first startup.

### Block Device Layout (High Level)

Block mode reserves regions for a superblock, two node map buffers,
heartbeat slots, and fence slots. The layout uses 4 KB sectors to support
O_DIRECT on both 512-byte and 4K-native block devices. The layout is
versioned via a self-describing superblock to allow future changes.

```text
Region             Size          Content
──────────────────────────────────────────────────────────────
Superblock         4 KB          Magic, Version, Layout Descriptors
Node Map Buffer A  64 KB         Double-buffered node map (primary)
Node Map Buffer B  64 KB         Double-buffered node map (secondary)
Heartbeat Slots    ~1 MB         255 slots x 4 KB
Fence Slots        ~1 MB         255 slots x 4 KB
──────────────────────────────────────────────────────────────
Total              ~2.12 MB
```

NodeIDs range from 1 to 255. Within each region, the slot offset is
`(nodeID - 1) * BlockSlotSize`. The heartbeat and fence protocol payloads
remain unchanged (33 bytes for heartbeat, 36 bytes for fence); the
additional bytes in each 4 KB slot are padding.

The detailed binary format (superblock byte layout, node map buffer wire
format, endianness, CRC coverage) is specified in
[rwx-block-volume-format.md](rwx-block-volume-format.md).

### Agent Changes

**OffsetDevice:** The single `blockdevice.Device` is wrapped in an
`OffsetDevice` that applies a region base offset to all `ReadAt`/`WriteAt`
calls. OffsetDevice translates offsets but does not handle alignment — the
caller is responsible for using correctly-sized buffers and aligned offsets.

**O_DIRECT alignment contract for Block mode:** All `ReadAt`/`WriteAt`
operations must use 4 KB (4096-byte) aligned offsets and lengths, with
page-aligned memory buffers. The existing SBD protocol payloads (33-byte
heartbeat header, 36-byte fence header) are smaller than one 4 KB sector.
In Block mode, each slot read/write operates on a full 4 KB sector
(`BlockSlotSize = 4096`) rather than the protocol's 512-byte
`SBD_SLOT_SIZE`. The protocol payload occupies the first bytes of the
sector; the remaining bytes are padding. This means Block mode does
**not** reuse the existing 512-byte I/O paths unmodified — the slot size,
buffer allocation, and offset calculations change.

**Storage coherence prerequisite:** Block mode correctness relies on the CSI
backend providing coherent shared access semantics for concurrent reads and
writes to the same volume. Ceph RBD provides this guarantee.

### Runtime Code Path

No new goroutines are introduced. The heartbeat loop, peer check loop, and
watchdog loop are identical in both modes. The volume mode is resolved once
at agent startup in `main.go`: Filesystem mode opens three files, Block
mode opens one device and wraps it in three `OffsetDevice` views. The
resulting device handles are passed to the same loops — they call
`ReadAt`/`WriteAt` without knowing which mode they are in.

### Agent Device Opening

Currently the agent opens three separate files by deriving paths from the
base `--sbr-device` path (appending `-fence` and `.nodemap` suffixes).
In Block mode, the agent opens a single device and creates `OffsetDevice`
views. Block mode is auto-detected by probing sector 0 for a valid V1
superblock:

- **Filesystem (no superblock):** Unchanged — separate
  `blockdevice.OpenWithTimeout()` calls for heartbeat and fence devices.
- **Block (valid superblock):** One `blockdevice.OpenWithTimeout()` call on
  the raw device, then `OffsetDevice` wrappers for heartbeat and fence
  regions, each wrapped in a `BlockDeviceAdapter` that satisfies
  `BlockDeviceInterface`.

The existing `blockdevice.Device` retry config (3 attempts, exponential
backoff) and I/O timeout (from `--io-timeout`) apply to the underlying
device in both modes. `OffsetDevice` delegates all I/O to the wrapped
`Device`.

### Node Map Persistence

The `NodeMapStore` interface extracts the **storage layer only** from
`NodeManager`. The existing CAS logic (version checking, retry with
backoff), corruption recovery, and stale node cleanup remain in
`NodeManager` unchanged. `NodeManager` calls `store.Load()` / `store.Save()`
where it currently calls `os.ReadFile()` / write-and-rename directly.

```go
type NodeMapStore interface {
    Load() ([]byte, error)
    Save(data []byte) error
}
```

**`FileNodeMapStore`** wraps the current file I/O: `os.ReadFile` for Load,
`os.WriteFile`+`os.Rename` for Save. `flock()` coordination remains in
`NodeManager` (called before `store.Save()`), not in the store.

**`BlockNodeMapStore`** uses double-buffering with optimistic write-verify
conflict detection, replacing `rename()` crash-safety and `flock()`
concurrency:

- **Crash consistency:** CRC32 covers each 64 KB buffer. CRC32 verification
  detects torn writes with high probability (~1 in 2³² chance of a false
  match). On restart, the agent falls back to whichever buffer has a valid
  CRC and the highest generation.

- **Concurrency:** Because raw block devices bypass the cluster filesystem's
  distributed lock manager, Block mode uses a write-verify protocol with
  generation counters, WriterUUIDs, and a bounded verification delay. The
  full algorithm is specified in
  [rwx-block-volume-format.md](rwx-block-volume-format.md).

- **Safety and limitations:** The node map is **non-authoritative advisory
  metadata**. It is not consulted during fencing decisions. Fencing is
  based on heartbeat slot content: CRC validity, embedded NodeID,
  timestamps, and sequence numbers. The peer monitor rejects heartbeats
  where the embedded NodeID does not match the expected slot owner
  (`main.go:1079`). However, this check validates the *logical* NodeID
  only — it cannot distinguish between two physical nodes that were both
  assigned the same slot due to a duplicate node map entry. In a
  duplicate assignment scenario, both nodes write valid heartbeats with
  the same NodeID to the same slot. If one node fails, the other
  continues writing, masking the failure from peers. The result is a
  **liveness failure** (dead node not fenced), not a safety violation
  (wrong node fenced). Recovery depends on the mechanisms below detecting
  the duplicate and triggering re-assignment.

- **Heartbeat slot ownership validation:** In Block mode, each agent
  periodically reads back its own heartbeat slot and verifies the embedded
  NodeID matches its assigned ID. If a mismatch is detected (another node
  is writing to the same slot), the agent triggers a node map reload and
  re-assignment. This detects duplicates when the competing writer's
  heartbeat overwrites the reader's own data, but has a detection gap
  when both writers use the same NodeID (as in duplicate assignment).

- **First boot:** If both buffers are invalid (freshly formatted device),
  the first agent writes an empty node map to Buffer A with Generation=1
  using the full write-verify protocol. This ensures concurrent agents
  starting simultaneously on a fresh device detect each other via
  WriterUUID mismatch and only one initialization succeeds.

The full write-verify protocol analysis, including the race window
characterization and watchdog bounding proof, is in
[rwx-block-volume-format.md](rwx-block-volume-format.md).

### StorageClass Validation

Known provisioners (`rbd.csi.ceph.com`) are validated via a fast path
without runtime probing. For all other provisioners, the controller creates
a temporary PVC with `volumeMode: Block` and `ReadWriteMany` to verify
capability dynamically.

On validation failure (PVC stays Pending past timeout, or provisioner
rejects the request), the controller sets the existing
`SharedStorageReady` condition to `False` with reason
`StorageClassIncompatible` and emits a Warning event. This uses the
existing condition/event infrastructure — no new conditions are needed.

### Webhook Validation

- `SharedStorageVolumeMode` must be nil, `"Filesystem"`, or `"Block"`.
- If `SharedStorageVolumeMode` is `"Block"`, `SharedStorageClass` must be
  set.
- On update: reject mutations to `SharedStorageVolumeMode` on an existing
  CR. For comparison, `nil` resolves to `Filesystem` — a change from `nil`
  to `"Filesystem"` is allowed (no effective change), but `nil` to
  `"Block"` is rejected.

## Compatibility and Upgrade

### What Does Not Change

- The SBD protocol message format (33-byte header, CRC32 checksum)
- `SBD_SLOT_SIZE` (512) as the protocol constant, `SBD_MAX_NODES` (255),
  `SBD_HEADER_SIZE` (33)
- The node map JSON payload format (the storage envelope differs in Block
  mode but the JSON content is identical)
- The heartbeat loop, peer monitor loop, and watchdog loop
- The fence message flow (write fence -> target reads own slot -> self-fence)
- Existing Filesystem-mode deployments: on-disk format, atomic rename
  persistence, and flock coordination are completely unchanged
- `SBRTimeoutSeconds`, `MaxConsecutiveFailures`, timing calculations
- The `blockdevice.Device` type, Prometheus metrics, security context, RBAC

### Upgrade Path

- The new field defaults to `nil` (resolves to `Filesystem`). Existing
  resources are unaffected — no migration required.
- Changing modes requires deleting the old config and creating a new one.
  The webhook rejects mutations to `SharedStorageVolumeMode` on existing
  resources.

## Alternatives Considered

### 1. Auto-detect volume mode from StorageClass

CSI capability discovery is not standardized in Kubernetes. An explicit
field is simpler, predictable, and matches how users already know their
storage topology.

### 2. Two separate PVCs (one for heartbeat, one for fence)

Doubles storage resource consumption and controller complexity. The
single-device-with-regions approach is how traditional SBD works.

### 3. Layer a filesystem on the block device

Single-node filesystems (ext4, xfs) on a shared RWX block device cause data
corruption. Cluster filesystems (GFS2, OCFS2) add a dependency that defeats
the purpose of reusing the existing StorageClass.

### 4. Store node map in a ConfigMap/Secret

Introduces API server dependency for heartbeat coordination, violating
SBD's design principle that the shared device is the sole coordination
mechanism.

### 5. Use 512-byte slot alignment instead of 4 KB

On 4K-native (4Kn) devices, 512-byte-aligned offsets fail with `EINVAL`.
4 KB alignment works on both 512e and 4Kn devices. The space overhead
(~2 MB vs ~319 KB) is negligible within the 10Mi PVC.

## Testing Strategy

1. **Unit Tests:** OffsetDevice offset arithmetic, BlockNodeMapStore
   double-buffer fallback and conflict detection, O_DIRECT buffer alignment.
2. **E2E Tests:** Deploy RWX Block (Ceph RBD) and verify fencing succeeds.
   Deploy RWX Filesystem and verify no regression.
3. **Failover Time E2E:** Measure time from heartbeat stop to fence
   completion on both Block and Filesystem backends. Assert Block failover
   is within 20% of Filesystem (same timing constants apply).
4. **Webhook Tests:** Reject `SharedStorageVolumeMode` mutations on existing
   resources. Reject Block mode without `SharedStorageClass`.
5. **Superblock Tests:** Reject unrecognized version, invalid magic/CRC,
   fully zeroed device.
6. **First-boot Tests:** Initialize fresh node map when both buffers are
   invalid.

## Edge Cases

| Scenario | Outcome |
|----------|---------|
| Block mode without `sharedStorageClass` | Webhook rejects |
| Node map write interrupted (power loss) | Torn write yields invalid CRC; agent loads alternate buffer |
| Concurrent node map writers | Verify delay + WriterUUID detects most conflicts; writers back off and retry. Undetected duplicates cause liveness failures (dead node not fenced) until next reconciliation cycle |
| StorageClass does not support RWX Block | PVC stays Pending; controller sets error condition |
| Agent reads unrecognized superblock version | Agent refuses to start; controller surfaces failure via status/events |
| Both node map buffers invalid (first boot) | Agent initializes empty node map in Buffer A |
| Volume mode mutation on existing config | Webhook rejects — immutable once set |
| Quiesce flag set on superblock | Agents stop heartbeating, enter safe paused state, report quiesced status |
| Agent starts on quiesced device | Agent reads superblock, detects quiesce flag, refuses to start heartbeat loop |

## Documentation

The `StorageBasedRemediationConfig` CRD description and the user guide are
updated to document `sharedStorageVolumeMode`. For OCP Virt deployments,
add a one-line recommendation: *"Use the same StorageClass as your virtual
machine disks."*

## Scaling and Format Migration

### V1 Scaling Boundary

The V1 block layout supports up to 255 nodes. This limit is driven by:

- **Node map buffer size (64 KB):** At ~150 bytes per node, 255 nodes
  produce ~38 KB of payload. Larger clusters would exceed the 64 KB
  buffer capacity (e.g. 2000 nodes ≈ 300 KB).
- **Write-verify contention:** The double-buffered write-verify protocol
  has a conflict rate that grows with concurrent writers. At 255 nodes the
  conflict probability per write cycle is low; at 2000+ nodes, most writes
  would fail verification and retry, creating cascading conflicts.

These are V1 constraints, not architectural limitations. The superblock
version field, self-describing region offsets, and `NodeMapStore` interface
are designed to support a V2 format with larger regions and a different
node map coordination strategy (e.g. per-node registration slots) without
breaking existing V1 deployments.

### Live Migration via Quiesce Flag

The superblock Flags field (bit 0) defines a **quiesce flag** that enables
operator-orchestrated format migration without requiring the DaemonSet to
be deleted and recreated. The migration flow:

1. Operator triggers migration (e.g. via CRD field or new CR version).
2. Controller runs a migration job that sets the quiesce flag (Flags
   bit 0) on the superblock via a single 4 KB write.
3. Agents detect the quiesce flag during periodic superblock re-reads,
   stop heartbeating, and report a "quiesced" status condition.
4. Controller waits for all agents to report quiesced.
5. Init job reformats the device with the new layout version (e.g. V2).
6. Agents detect the new superblock version, reinitialize with the
   appropriate `NodeMapStore` implementation, and resume fencing.

This migration is safe because:

- **Node map is non-authoritative:** Losing it and rebuilding is a normal
  recovery path (the `clearCorruptedSlot` flow). Nodes re-register on
  startup.
- **Device layout forces a clean break:** A V2 layout with more nodes
  requires larger heartbeat/fence regions, which means a physically
  different device layout. In-place upgrade of a V1 device to support
  more than 255 slots is not possible regardless of the node map protocol.
- **Single writer for quiesce flag:** Only the controller or migration
  job writes the flag — no concurrency concern on the superblock.
- **Agent binary supports multiple versions:** The agent reads the
  superblock version at startup and selects the appropriate store
  implementation. Old devices get V1 behavior; new devices get V2.

The quiesce flag is a dormant capability in V1 — it is never set during
normal operation and costs nothing until used. The actual migration
orchestration logic (controller side) is future work.

## Out of Scope

- **CSI driver certification matrix.** This feature targets Ceph RBD as the
  primary driver.
- **Multiple StorageClasses per node.** The current CRD is a singleton per
  namespace. Supporting multiple configs is a separate RFE; this design
  does not preclude it.
- **Changes to RWX Filesystem behavior.** The only cross-cutting change is
  the `NodeMapStore` interface extraction.
- **Performance benchmarking.** The I/O sizes (4 KB) are trivial for any
  modern storage backend.
- **V2 format design and implementation.** The quiesce flag and versioned
  superblock enable future format migration; the actual V2 layout, node map
  coordination protocol, and migration orchestration are separate work.
