# Storage-Based Remediation (SBR) — Code Map

**Repository:** `github.com/medik8s/storage-based-remediation`

> **Key architectural note:** The main operator binary runs `StorageBasedRemediationConfig` reconciliation and admission webhooks. **Fencing for `StorageBasedRemediation` CRs runs in the sbr-agent** via `SBRRemediationReconciler` — not in the operator process.

---

## Directory structure

```
storage-based-remediation/
├── api/v1alpha1/           # CRD Go types, defaults/validation helpers, admission webhook for Config
├── cmd/
│   ├── main.go             # sbr-operator: manager, config controller, webhooks only
│   └── sbr-agent/          # sbr-agent: heartbeats, peer monitor, watchdog, remediation fencing
├── config/                 # Kustomize / deployment manifests
├── internal/
│   ├── agent/              # Shared CLI flag names + shared-storage path constants (DaemonSet ↔ agent)
│   ├── blockdevice/        # Raw block I/O with timeouts and retries (O_DIRECT primitives)
│   ├── blockformat/        # SBD superblock + slot layout for Block-mode shared storage (built on blockdevice)
│   ├── controller/         # StorageBasedRemediationConfig + StorageBasedRemediation reconcilers
│   ├── mocks/              # Interfaces (watchdog, block device) for tests
│   ├── retry/              # Generic exponential backoff retry
│   ├── sbdprotocol/        # SBD message format + node map + NodeManager (slots, locking)
│   ├── storage/            # Standalone NFS CSI setup helper (backs tools/setup-shared-storage; not on the operator/agent runtime path)
│   ├── storage/odf/        # Standalone ODF/AWS block storage setup helper (backs tools/setup-odf-storage; not on the operator/agent runtime path)
│   ├── version/            # Build/version info
│   └── watchdog/           # Linux watchdog open/pet (ioctl + softdog fallback)
├── test/
│   ├── e2e/                # Ginkgo e2e (cluster disruption, remediation, storage class checks)
│   └── utils/              # Test helpers
└── tools/                  # setup-shared-storage (NFS CSI) and setup-odf-storage (ODF/AWS block) standalone CLIs
```

---

## Key files and what they do

### `cmd/main.go` — Operator entry point

- Registers `StorageBasedRemediationConfigReconciler` only.
- Sets up controller-runtime manager, metrics, health probes, leader election (`LeaderElectionID: sbr-operator-leader-election`).
- Registers `StorageBasedRemediationConfigValidator` webhook when `--enable-webhooks=true`.
- **Does not** run `SBRRemediationReconciler`.

### `cmd/sbr-agent/preflight.go` — Startup device checks

- Validates the configured SBR device is accessible before the agent starts its loops.
- **Block mode:** reads and validates the SBD superblock (`blockformat.HasSuperblockMagic`/`UnmarshalSuperblock`) at the fixed offset, then runs a direct-I/O write/read test (`performSBRBlockWriteTest`) against the node's slot using `blockdevice.DirectIOAlloc` buffers.
- **Filesystem mode:** equivalent read/write probes against the heartbeat/fence files.

### `cmd/sbr-agent/main.go` — Agent entry point and core runtime

Owns all agent loops, the embedded controller-runtime manager, and the self-fence decision logic.

| Function | Description |
|---|---|
| `SBRAgent`, `NewSBRAgentWithWatchdog` | Construct agent: devices, node manager, metrics, embedded reconciler |
| `watchdogLoop` | Periodic pet or self-fence / SBR-unhealthy policy |
| `petWatchdogWhenHealthy` | Pet with `retry.Do` when SBR is healthy |
| `handleWatchdogTickSBRUnhealthy` | Detect-only vs skip pet if CR exists / API error |
| `heartbeatLoop`, `writeHeartbeatToSBR` | Write heartbeat to own slot |
| `peerMonitorLoop` | Read peers, check liveness, set `SBRStorageUnhealthy` on Node |
| `readOwnSlotForFenceMessage` | Detect fence message in own slot → self-fence |
| `executeSelfFencing` | panic / systemctl-reboot / none |
| `shouldTriggerSelfFence` | Threshold check + abort if no remediation CR |
| `remediationExistsForThisNode` | K8s Get with 5s timeout; used as CR existence gate |
| `initializeControllerManager`, `addSBRRemediationController` | Embed `SBRRemediationReconciler` in agent |

### `api/v1alpha1/`

| File | Role |
|---|---|
| `groupversion_info.go` | Group/version registration, `AddToScheme` |
| `storagebasedremediation_types.go` | `StorageBasedRemediation` types, status conditions, `NodeConditionSBRStorageUnhealthy` constant |
| `storagebasedremediationtemplate_types.go` | Template CRD for NHC-style consumers |
| `storagebasedremediationconfig_types.go` | Full Config spec + `ValidateAll` + field validators + defaults + `deriveAgentImageFromOperator`; `SharedStorageVolumeModeType` (`Filesystem`/`Block`), `IsBlockMode()`; `StorageValidationStatus` (`ConcurrentWriteable`, `ProbedNodeCount`, `LastProbeTime`) |
| `storagebasedremediationconfig_webhook.go` | Admission: `ValidateCreate` / `ValidateUpdate` call `Spec.ValidateAll()` |

### `internal/controller/`

| File | Role |
|---|---|
| `storagebasedremediationconfig_controller.go` | DaemonSet, PVC, init Job, SA/RBAC, StorageClass validation, `buildDaemonSet`, `buildSBRAgentArgs`, `updateStatus`. Block-mode specific: `buildBlockInitJob` vs `buildFSInitJob`, `buildVolumeDevices`, `isRWXBlockCompatibleProvisioner`, `updateStorageValidation` (concurrent-write probe) |
| `storagebasedremediation_controller.go` | `SBRRemediationReconciler`: cordon, `writeFenceMessage`, `checkFencingCompletion`, `ensureOutOfServiceTaint`, deletion cleanup. Uses `internal/blockformat` for fence writes when the SBRConfig is in Block mode |

### `internal/blockformat/`

| File | Role |
|---|---|
| `superblock.go` | On-disk `Superblock` layout, magic bytes, marshal/unmarshal, region offsets |
| `blockdeviceadapter.go` | Adapts `internal/blockdevice.Device` to the slot/region model used by block-mode heartbeat and fence I/O |
| `offsetdevice.go` | Read/write a sub-region of a block device at a fixed byte offset (heartbeat/fence regions) |
| `blocknodemap.go` | Block-mode equivalent of `sbdprotocol`'s nodemap — node↔slot assignment stored in the superblock region instead of a filesystem file |
| `init.go` | Writes the initial superblock + zeroes slots during the block-mode init Job |

### `internal/sbdprotocol/`

| File | Role |
|---|---|
| `message.go` | Slot size, message types (`HEARTBEAT`, `FENCE`), marshal/unmarshal, fence reason constants |
| `nodemap.go` | JSON node-name↔slot mapping on disk: `NodeMapTable`, hash-based `AssignSlot`, checksum |
| `nodemanager.go` | `NodeManager`: load/sync map file, `GetNodeIDForNode`, `LookupNodeIDForNode`, `WriteWithLock` / `ReadWithLock`, stale cleanup, periodic sync |

### `internal/blockdevice/blockdevice.go`

`Device`: `Open` / `OpenWithTimeout`, `ReadAt` / `WriteAt` / `Sync` with I/O timeouts (goroutine + `time.After`) and `retry.Do`.

### `internal/watchdog/watchdog.go`

`Watchdog`: `Pet()` (ioctl keepalive + write fallback), `NewWithSoftdogFallback` (loads `softdog` via nsenter), `Close`.

### `internal/agent/flags.go`

Flag name constants (`FlagWatchdogPath`, `FlagSBRDevice`, etc.), defaults, and **shared storage layout constants** — the bridge between DaemonSet args and agent runtime (`SharedStorageSBRDeviceFile`, `SharedStorageFenceDeviceSuffix`, `SharedStorageNodeMappingSuffix`, mount path `/dev/sbr`).

### `internal/retry/retry.go`

`retry.Config`, `retry.Do`, `IsTransientError`, `NewRetryableError` — used by agent, blockdevice, watchdog, and controllers throughout.

---

## Key functions quick reference

| Function | File | Description |
|---|---|---|
| `(*SBRRemediationReconciler).Reconcile` | `internal/controller/storagebasedremediation_controller.go` | Fence peer: cordon → write fence → wait → OOS taint → success |
| `(*SBRRemediationReconciler).writeFenceMessage` | `internal/controller/storagebasedremediation_controller.go` | Marshal fence to target slot on fence device |
| `(*SBRRemediationReconciler).ensureOutOfServiceTaint` | `internal/controller/storagebasedremediation_controller.go` | Apply `node.kubernetes.io/out-of-service` NoExecute taint |
| `(*SBRRemediationReconciler).checkFencingCompletion` | `internal/controller/storagebasedremediation_controller.go` | Poll node Ready status + heartbeat staleness; force-complete on timeout |
| `(*StorageBasedRemediationConfigReconciler).Reconcile` | `internal/controller/storagebasedremediationconfig_controller.go` | Validate SC, PVC, init Job, DaemonSet, status |
| `(*StorageBasedRemediationConfigReconciler).validateStorageClass` | `internal/controller/storagebasedremediationconfig_controller.go` | RWX provisioner checks + optional test PVC |
| `(*StorageBasedRemediationConfigReconciler).buildDaemonSet` | `internal/controller/storagebasedremediationconfig_controller.go` | Build full privileged pod spec with host mounts and agent args |
| `shouldTriggerSelfFence` | `cmd/sbr-agent/main.go` | Failure threshold check; aborts if no remediation CR confirmed |
| `handleWatchdogTickSBRUnhealthy` | `cmd/sbr-agent/main.go` | Per-tick: detect-only / stop petting (CR exists or API error) / keep petting (no CR) |
| `executeSelfFencing` | `cmd/sbr-agent/main.go` | Runs reboot method; stops watchdog petting |
| `peerMonitorLoop` | `cmd/sbr-agent/main.go` | Read peers, assess liveness, set `SBRStorageUnhealthy` node condition |
| `setNodeConditionSBRStorageUnhealthyStatus` | `cmd/sbr-agent/main.go` | Patch node status with `SBRStorageUnhealthy` condition |
| `(*NodeManager).WriteWithLock` | `internal/sbdprotocol/nodemanager.go` | Serialize writes to SBR device + map file with optional file lock |
| `(*NodeManager).GetNodeIDForNode` | `internal/sbdprotocol/nodemanager.go` | Assign or retrieve slot ID for this node |
| `(*Watchdog).Pet` | `internal/watchdog/watchdog.go` | ioctl keepalive (+ write fallback) |
| `retry.Do` | `internal/retry/retry.go` | Backoff retry for transient errors |

---

## Where is X? Quick lookup

| Question | Answer |
|---|---|
| **Where is fencing logic?** | **Peer → self-fence** (own slot): `cmd/sbr-agent/main.go` — `readOwnSlotForFenceMessage`, `executeSelfFencing`. **NHC/operator → peer** (fence write): `internal/controller/storagebasedremediation_controller.go` — `executeFencing`, `writeFenceMessage`. |
| **Where is heartbeat written?** | `cmd/sbr-agent/main.go`: `heartbeatLoop` → `writeHeartbeatToSBR` → `writeHeartbeatToSBRInternal` (uses **`internal/sbdprotocol`** `MarshalHeartbeat` + `NodeManager.WriteWithLock`). |
| **Where is peer health checked?** | `cmd/sbr-agent/main.go`: `peerMonitorLoop` → `readPeerHeartbeat` → `peerMonitor.updatePeer` / `checkPeerLiveness`. |
| **Where is node condition set?** | `cmd/sbr-agent/main.go`: `setNodeConditionSBRStorageUnhealthy`, `setNodeConditionSBRStorageUnhealthyStatus`. Type constant: `api/v1alpha1/storagebasedremediation_types.go` — `NodeConditionSBRStorageUnhealthy`. |
| **Where is watchdog petted?** | `internal/watchdog/watchdog.go`: `Pet()`. Called from `cmd/sbr-agent/main.go`: `petWatchdogWhenHealthy` and (conditionally) `handleWatchdogTickSBRUnhealthy`. |
| **Where is self-fence decision made?** | `cmd/sbr-agent/main.go`: `shouldTriggerSelfFence` (failure counts + CR gate), called from `watchdogLoop`. |
| **Where is OOS taint applied?** | `internal/controller/storagebasedremediation_controller.go`: `ensureOutOfServiceTaint`. Delayed for fresh agent remediations: `isRemediationFresh`, `SBRAgentRemediationFreshAge`. |
| **Where is the DaemonSet built?** | `internal/controller/storagebasedremediationconfig_controller.go`: `buildDaemonSet`, `buildSBRAgentArgs`, `buildVolumeMounts`, `buildVolumes`, `buildNodeSelector`. |
| **Where is StorageClass validated?** | API name format: `api/v1alpha1/storagebasedremediationconfig_types.go` — `ValidateSharedStorageClass`. RWX/provisioner check: `internal/controller/storagebasedremediationconfig_controller.go` — `validateStorageClass`, `isRWXCompatibleProvisioner`, `testRWXSupport`. |
| **Where are agent CLI flags defined?** | Names + path constants: `internal/agent/flags.go`. `flag` declarations: `cmd/sbr-agent/main.go`. DaemonSet args: `buildSBRAgentArgs` in `internal/controller/storagebasedremediationconfig_controller.go`. |
| **Where is node slot assignment?** | `internal/sbdprotocol/nodemanager.go`: `NodeManager.GetNodeIDForNode` (assigns hash-based slot, persists to shared nodemap). |
| **Where is the message protocol?** | `internal/sbdprotocol/message.go`: slot layout, `NewHeartbeat`, `NewFence`, marshal/unmarshal. |
| **Where is Block-mode storage handled?** | Selection: `spec.sharedStorageVolumeMode` + `IsBlockMode()` in `api/v1alpha1/storagebasedremediationconfig_types.go`. Superblock/slot layout: `internal/blockformat/`. PVC/DaemonSet wiring (`volumeDevices`, `buildBlockInitJob`, host-dev mount): `internal/controller/storagebasedremediationconfig_controller.go`. Startup validation: `cmd/sbr-agent/preflight.go`. |
| **Where is the concurrent-write / RWX confirmation check?** | `internal/controller/storagebasedremediationconfig_controller.go`: `updateStorageValidation`, gates `status.storageValidation.concurrentWriteable` and the `Ready` condition. |

---

## E2E test structure

- **Suite setup:** `test/e2e/e2e_suite_test.go` — namespace `sbr-test-e2e`, cluster connection, optional AWS init, cleanup.
- **Specs:** `test/e2e/e2e_test.go` — `Describe("SBR Operator")` Ordered: kubelet disruption, basic config, fake remediation, incompatible StorageClass, node remediation (cordon → fencing → OOS taint), agent crash, storage disruption.
- **Key helpers:** RWX provisioner lists (mirrors controller), disruptor pods, `checkNodeHasSBRStorageUnhealthyCondition`, boot ID / reboot checks.
