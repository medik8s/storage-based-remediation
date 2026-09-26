# Storage-Based Remediation (SBR) — Architecture

## What SBR does (high level)

SBR provides **cluster node coordination and fencing over shared storage**: each node runs an **sbr-agent** that heartbeats into fixed slots on shared device files, watches peers, and can **write fence messages** so a failed node self-reboots when it reads its fence slot. A separate **sbr-operator** deploys and configures agents from a `StorageBasedRemediationConfig` CR.

---

## Repository layout

| Area | Role |
|------|------|
| `cmd/main.go` | **SBR operator** — controller-runtime manager for `StorageBasedRemediationConfig` + validating webhook |
| `cmd/sbr-agent/main.go` | **SBR agent** — watchdog + shared-device loops + embedded `StorageBasedRemediation` reconciler |
| `api/v1alpha1/` | CRD Go types (`StorageBasedRemediation`, `StorageBasedRemediationConfig`, `StorageBasedRemediationTemplate`), webhooks, deepcopy |
| `internal/controller/` | `StorageBasedRemediationConfigReconciler` (operator); `SBRRemediationReconciler` (used by agent) |
| `internal/sbdprotocol/` | Slot layout, heartbeat/fence messages, `NodeManager` (node name ↔ slot ID, shared nodemap) |
| `internal/blockdevice/` | Raw block I/O primitives (O_DIRECT, timeouts, retries) |
| `internal/blockformat/` | On-disk SBD superblock + slot layout for **Block mode** shared storage, built on `internal/blockdevice` |
| `internal/watchdog/` | Linux watchdog (with softdog fallback in agent) |
| `internal/agent/` | Shared flags/constants (device paths, mount dir `/dev/sbr`, block device path `/sbr-block`, file names) |
| `internal/retry/` | Shared exponential backoff for API and I/O |
| `internal/storage/`, `internal/storage/odf/` | Standalone provisioning helpers for `tools/setup-shared-storage` (NFS CSI) and `tools/setup-odf-storage` (ODF/AWS block) CLIs — **not** on the operator/agent runtime path |

---

## Key components and responsibilities

### 1. SBR operator (`cmd/main.go`)

- Runs **controller-runtime** with **leader election** (ID: `sbr-operator-leader-election`) — serializes config reconciliation, not per-node fencing.
- Registers only **`StorageBasedRemediationConfigReconciler`** (explicit code comment: remediation reconciler runs in the agent, not here).
- Optional **validating admission webhook** for `StorageBasedRemediationConfig`.

### 2. `StorageBasedRemediationConfigReconciler`

- Reconciles **`StorageBasedRemediationConfig`**.
- **Finalizer** `sbr-operator.medik8s.io/cleanup` for delete path.
- Ensures shared **sbr-agent ServiceAccount** and **ClusterRoleBindings** (including OpenShift **privileged SCC** binding).
- If `spec.sharedStorageClass` is set: validates StorageClass supports **ReadWriteMany** (with mode-specific provisioner checks — see below) → creates **RWX PVC** → runs an init **Job** (`buildFSInitJob` or `buildBlockInitJob`, chosen by `spec.sharedStorageVolumeMode`) that initializes the shared device.
- **Two shared-storage volume modes** (`spec.sharedStorageVolumeMode`, immutable after creation):
  - **`Filesystem`** (default): RWX PVC mounted as a filesystem; init Job creates heartbeat file, fence file, and shared nodemap under the mount; agent uses file locking (`--sbr-file-locking=true`).
  - **`Block`**: raw block RWX PVC exposed to the agent via `volumeDevices` at `/sbr-block`; init Job writes an SBD superblock (`internal/blockformat`) instead of files; no filesystem-level nodemap file (nodemap lives in the block layout); agent uses direct I/O with no file locking (`--sbr-file-locking=false`); requires a block-capable RWX provisioner (e.g. Ceph RBD) — checked separately from filesystem RWX support.
- Builds **DaemonSet** `sbr-agent-{configName}`: privileged, mounts `/sys` + `/proc`; mounts host `/dev` at `/dev` in Filesystem mode, or at a side path (`/host-dev`) in Block mode (a bind mount at `/dev` would make the container runtime skip creating the CSI block-device node, so the watchdog is resolved under `/host-dev` instead).
- **Concurrent-write validation** (`status.storageValidation`): after the DaemonSet reaches `min(2, desired)` Ready agents, the controller records `concurrentWriteable=true` (RWX confirmed); this gates the `Ready` condition and does **not** get cleared by later transient readiness dips (e.g. a node being fenced), only by the node count growing past what was probed.
- Updates **status**: `DaemonSetReady`, `SharedStorageReady`, `Ready`, `readyNodes`, `totalNodes`, `storageValidation` (`concurrentWriteable`, `probedNodeCount`, `lastProbeTime`).

### 3. SBR agent (`cmd/sbr-agent/main.go`)

- Requires both a watchdog and SBR device to be accessible at startup.
- Runs two loops: **(a)** kernel watchdog pet loop; **(b)** SBR heartbeat writes + peer heartbeat reads on the heartbeat device; fence device is separate.
- **Node slot assignment**: hash-based via `sbdprotocol.NodeManager`; initial `--node-id` flag is overridden at runtime.
- Sets **`SBRStorageUnhealthy`** condition on peer `Node` objects → intended signal for **NHC** to create a `StorageBasedRemediation` CR.
- On local failure: may stop petting watchdog or panic/reboot unless no remediation CR exists for that node.
- Hosts an embedded **controller-runtime manager** (no leader election) running `SBRRemediationReconciler`.

### 4. `SBRRemediationReconciler` (runs inside each agent)

- **Target node = CR `.metadata.name`**. Short-circuits if name matches own node (no self-fence via this path).
- Flow: add finalizer → **cordon** target → set **FencingInProgress** → **write fence message** to target's slot on fence device → poll until fenced (Node `Ready=False`, stale heartbeat, or timeout) → apply **`node.kubernetes.io/out-of-service`** taint → set success conditions.
- Delete path: uncordon → wait for taint removal → remove OOS taint → remove finalizer.

---

## Custom Resources (`api/v1alpha1/`)

**Group:** `storage-based-remediation.medik8s.io`, **Version:** `v1alpha1`

### `StorageBasedRemediationConfig` (namespaced)

Operator-managed desired state for agent DaemonSet, image, timing, and optional RWX shared storage.

The spec was trimmed down to a small set of fields; several settings that used to be per-CR (`watchdogTimeout`, `petIntervalMultiple`, `image`, `imagePullPolicy`, `logLevel`, `rebootMethod`, `iotimeout`) are now **fixed constants** in `internal/agent` (see `runbook.md` §3.1) — not currently exposed on the CR. Key spec fields:
- `sharedStorageClass` — drives PVC creation (RWX, `10Mi`)
- `sharedStorageVolumeMode` — `Filesystem` (default) | `Block`; immutable after creation; `Block` requires `sharedStorageClass`
- `watchdogPath`
- `sbrTimeoutSeconds` (10–300s, default 30) — derives heartbeat interval (`/2`) and update/peer-check intervals (`/6`)
- `maxConsecutiveFailures` (2–32, default 7) — threshold for both local self-fence and peer-unhealthy detection
- `detectOnlyMode` — `Disabled` | `Enabled` (arms/disarms local agent fencing paths; **note:** peer fencing reconciler does not read this flag)
- `nodeSelector`
- **Not on the spec:** agent image comes from the `RELATED_IMAGE_AGENT` env var on the operator (`deriveAgentImageFromOperator`), not a per-CR field.

### `StorageBasedRemediation` (namespaced)

Request to fence a node; **CR name must match the Kubernetes node name**.

Key spec fields:
- `reason` — `HeartbeatTimeout` | `NodeUnresponsive` | `ManualFencing` (default: `NodeUnresponsive`)
- `timeoutSeconds` — 30–300 (default 60)

Status: `FencingInProgress`, `FencingSucceeded`, `Ready` conditions. Note: `LeadershipAcquired`, `status.nodeID`, `fenceMessageWritten`, and `operatorInstance` exist in the schema but are **not set** by the reconciler today — schema placeholders.

### `StorageBasedRemediationTemplate` (namespaced)

Wraps a `StorageBasedRemediation`-shaped template for external remediation integrations (e.g. NHC). No reconciler in this repo — external controllers instantiate remediations from it.

---

## Reconciliation flows

### A. `StorageBasedRemediationConfig` create/update

1. Validate spec; webhook may enforce additional rules (e.g. node selector overlap).
2. Ensure SA + RBAC (+ OpenShift SCC binding).
3. If shared storage: validate StorageClass → create PVC → run init Job for device files + nodemap.
4. Create/update DaemonSet; refresh status conditions.

### B. `StorageBasedRemediation` create/update (inside agent)

1. Only agents **not on the target node** fence it (own-node short-circuit).
2. Cordon → FencingInProgress → write fence to target slot on fence device → wait for confirmation or timeout → apply OOS taint → set success.
3. Delete: uncordon → remove OOS taint → drop finalizer.

### C. Failure / observability paths (agent)

- Local watchdog and SBR I/O failures → possible **self-fence**.
- Peer failure → **`SBRStorageUnhealthy`** on peer's `Node` → intended signal for **NHC** to create `StorageBasedRemediation`.

---

## Interaction with other systems

| System | How SBR connects |
|--------|-----------------|
| **Kubernetes** | Nodes, Pods, PVCs, StorageClasses, Jobs, DaemonSets, RBAC, optional SCC bindings |
| **NHC (Node Health Check)** | No Go import. Contract: `SBRStorageUnhealthy` condition on `Node` + `StorageBasedRemediation` CR named after the node. E2E tests simulate NHC by manually creating/deleting the CR |
| **FAR / external remediation** | `StorageBasedRemediationTemplate` + `external_remediation_clusterrole` support the external remediation RBAC pattern |
| **Storage** | RWX PVC in `Filesystem` mode (default): regular files under `/dev/sbr` (`sbr-device`, `sbr-device-fence`, nodemap). RWX PVC in `Block` mode: raw block device at `/sbr-block` with an SBD superblock/slot layout (`internal/blockformat`) — no filesystem files |
| **Prometheus** | Agent exposes metrics (`sbr_agent_status_healthy`, `sbr_device_io_errors_total`, etc.); operator uses controller-runtime metrics |

---

## Notable patterns and design decisions

- **Fencing runs in agents, not the operator**: Leader election in the operator serializes config/DaemonSet management only; actual fence writes happen in `sbr-agent` instances across the cluster.
- **Two logical devices on shared storage**: heartbeat device (liveness) and fence device (trigger reboot) are separate files.
- **Hash-based slot assignment** with persisted nodemap supersedes the old static `--node-id` flag.
- **File locking** optional (`--sbr-file-locking`) for coordination on shared FS.
- **API/implementation drift**: `LeadershipAcquired` status field and RBAC for leases are generated but unused in the reconciler.

---

> **Doc caveat:** Earlier revisions of this doc (and some in-repo docs) treated the "block volume" language as a README inaccuracy, describing only a filesystem-backed path. `Block` mode is now a real, implemented `sharedStorageVolumeMode` option (raw block RWX PVC via `internal/blockformat`) — but it's still opt-in; `Filesystem` (regular files on an RWX PVC) remains the default. Prefer `StorageBasedRemediationConfig` spec + `internal/agent` constants as ground truth for which mode is active.
