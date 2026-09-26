# Storage-Based Remediation (SBR) — Failure Modes

This document describes failure scenarios, how they are detected, what the system does, and the resulting end state. Constants and behavior are taken from the implementation.

---

## Governing constants and timers

| Constant / setting | Value | Where |
|---|---|---|
| `MaxConsecutiveFailures` | `7` default, configurable **2–32** via `spec.maxConsecutiveFailures` | `api/v1alpha1/storagebasedremediationconfig_types.go`, `cmd/sbr-agent/main.go` |
| `FailureCountResetInterval` | `10 min` — resets all failure counters | `cmd/sbr-agent/main.go` |
| `DefaultMinMissedHeartbeatsForRemediation` | `6` (MaxConsecutiveFailures - 1) | `cmd/sbr-agent/main.go` |
| Heartbeat tick interval | `max(1s, SBR_TIMEOUT_SECONDS / 2)` | `cmd/sbr-agent/main.go` |
| Peer liveness timeout | `heartbeatInterval × MaxConsecutiveFailures` | `peerMonitor.checkPeerLiveness` |
| `sbrUnhealthyConditionStaleAge` | `(MaxConsecutiveFailures+1) × heartbeatInterval + 5s` ≈ **125s** at defaults | `cmd/sbr-agent/main.go` |
| `RemediationCheckTimeout` | `5s` — timeout for CR existence check | `cmd/sbr-agent/main.go` |
| `SBRAgentRemediationGracePeriod` | `3 min` — after `SBRStorageUnhealthy=Unknown`, wait before setting `True` again | `cmd/sbr-agent/main.go` |
| Block device I/O timeout | `--io-timeout` default **2s**; min 1s, max 10m | `cmd/sbr-agent/main.go`, `internal/blockdevice` |
| Block device retries | 3 | `internal/blockdevice/blockdevice.go` |
| Watchdog pet retries | 2 | `internal/watchdog/watchdog.go` |
| Fencing completion timeout | `Spec.TimeoutSeconds` default **60s** | `internal/controller/storagebasedremediation_controller.go` |
| `maxHeartbeatAge` (stale heartbeat = fenced) | **60s** fixed | `hasNodeStoppedHeartbeating` |
| Config controller API retries | 3, backoff 500ms–10s | `internal/controller/storagebasedremediationconfig_controller.go` |

---

## 1. Node-level failures

### 1.1 Heartbeat write fails after retries

- **Failure:** Marshal error, I/O failure, or timeout on heartbeat device write/sync.
- **Detection:** `heartbeatLoop` → `incrementFailureCount("heartbeat")`; `sbr_device_io_errors_total` metric increments.
- **Response:** Counter increases; on success counter resets. After **7 consecutive failures** → `setSBRHealthy(false)`, devices closed.
- **Outcome:** Agent marked unhealthy in metrics; next watchdog tick enters the SBR-unhealthy path (see 1.4).

### 1.2 Watchdog pet fails after retries

- **Failure:** `Watchdog.Pet()` still errors after retries.
- **Detection:** `incrementFailureCount("watchdog")`; event `EventReasonWatchdogPetFailed` at threshold.
- **Outcome:** After **7 consecutive failures** → same self-fence path as 1.4.

### 1.3 Fence-slot read errors in peer loop

- **Failure:** Read/write/sync errors while reading own fence slot.
- **Detection:** `incrementFailureCount("sbr")` from `readOwnSlotForFenceMessage`.
- **Note:** `resetFailureCount("sbr")` is never called in production code — count only resets via `FailureCountResetInterval` or restart.
- **Outcome:** After **7** → self-fence path.

### 1.4 Node SBR-unhealthy: watchdog pet decision

- **Trigger:** `sbrHealthy == false` on any watchdog tick.
- **Response (per case):**
  - `detectOnlyMode` → emit event, return (no pet, no reboot since watchdog is not armed in this mode).
  - API error on CR check → **stop petting** (reboot imminent via watchdog timeout). Event: `SBRUnhealthySkipPetAPIError`.
  - CR exists → **stop petting**. Event: `SBRUnhealthyWatchdogTimeout`.
  - No CR (confirmed) → **keep petting**, let NHC decide.
- **Outcome:** Either hardware watchdog fires (skip-pet paths), or node stays up if no remediation CR.

### 1.5 Self-fence aborted — no remediation CR

- **Trigger:** All failure thresholds met, but `remediationExistsForThisNode()` returns `(false, nil)`.
- **Response:** Self-fence suppressed. Event: `SelfFenceAbortedNoRemediation`.
- **Outcome:** No immediate reboot; NHC may still create the CR later.

### 1.6 Self-fence executed

- **Trigger:** `shouldTriggerSelfFence()` returns true AND CR confirmed (or API error — fail-safe), OR fence message for own node is detected in own slot.
- **Response:** Event `SelfFenceInitiated`, `selfFencedCounter++`, watchdog petting stops. Then based on the reboot method (currently fixed to `systemctl-reboot` by the controller — see `runbook.md` §3.3): tries `systemctl reboot`/`reboot -f`/sysrq, falling back to `panic()` if all fail.
- **Outcome:** Intended host reboot.

### 1.7 Startup / preflight failures

- **Failure:** Empty SBR device path, preflight checks fail, invalid watchdog timing (ratio < 3:1), K8s client init fails.
- **Note:** Code requires **both** watchdog and SBR device to pass preflight (a comment says "either/or" — the comment is wrong).
- **Outcome:** Process exits; agent does not run.

---

## 2. Peer detection failures

### 2.1 Peer heartbeat times out

- **Failure:** No newer heartbeat from a peer within `heartbeatInterval × MaxConsecutiveFailures`.
- **Detection:** `peerMonitor.checkPeerLiveness`.
- **Response:** Peer marked `IsHealthy=false`; metrics updated.

### 2.2 Peer unhealthy but below remediation threshold

- **Failure:** Peer unhealthy, but estimated missed heartbeats < **6**.
- **Outcome:** No condition flip yet; logged at V(1).

### 2.3 Peer crosses remediation threshold

- **Failure:** Missed heartbeats ≥ 6.
- **Response:** `SBRStorageUnhealthy=True` set on the peer's `Node` object.
- **Outcome:** NHC observes condition and creates `StorageBasedRemediation` CR named after the peer.

### 2.4 Peer name cannot be resolved

- **Failure:** `nodeManager` has no mapping for the peer's slot.
- **Outcome:** Condition update skipped for that peer from this agent.

### 2.5 Stale `SBRStorageUnhealthy=True` condition

- **Failure:** Condition has been `True` longer than ~125s (default).
- **Response:** Set to `Unknown` (reason: `GivingAgentChance`) so NHC can remove remediation and give the node's own agent a chance to report healthy.
- **Outcome:** After `SBRAgentRemediationGracePeriod` (3 min), if still unhealthy: set back to `True`.

### 2.6 Peer recovers

- **Response:** If condition was `True` or `Unknown`, set to `False` (reason: `Recovered`).

### 2.7 Protocol-level slot noise (non-fatal)

- **Failure:** Empty slot, invalid type, unmarshal error on peer header.
- **Outcome:** Return `nil` without updating peer state or incrementing counters — effectively ignored until next tick.

---

## 3. Fencing failures (reconciler)

### 3.1 Remediation targets own node

- **Detection:** CR name equals `ownNodeName`.
- **Outcome:** Skipped; event emitted. No fencing.

### 3.2 Node manager missing or target node not in map

- **Failure:** `nodeManager == nil` or `LookupNodeIDForNode` returns no result.
- **Outcome:** `handleFencingFailure` → `FencingInProgress=False`, `Ready=False`, event `FencingFailed`.

### 3.3 Fence write fails

- **Failure:** `fenceDevice` nil/closed or `writeFenceMessage` returns error.
- **Outcome:** Same as 3.2.

### 3.4 Fencing completion timeout

- **Failure:** Target node hasn't shown signs of fencing within `Spec.TimeoutSeconds` (default 60s).
- **Response:** Fencing is **forced complete** after timeout regardless of node state. OOS taint is then applied.
- **Note:** Node may still show `Ready=True` if it didn't actually reboot — a known limitation.

### 3.5 Fence message still in target slot

- **Detection:** `hasNodeStoppedHeartbeating` sees `FENCE` type still in slot.
- **Outcome:** Returns `false` — not yet considered fenced (give target time to process).

### 3.6 CR deletion

- **Trigger:** User deletes `StorageBasedRemediation`.
- **Response:** Uncordon node, wait for unschedulable taint removal, remove OOS taint, remove finalizer.

---

## 4. Block-mode-specific failures

### 4.1 Superblock invalid or missing at startup

- **Failure:** `preflight.go` reads the SBD superblock region and finds no magic bytes, or `UnmarshalSuperblock` fails.
- **Outcome:** Agent preflight fails; process exits without running (same class as 1.7).

### 4.2 Block write test fails

- **Failure:** `performSBRBlockWriteTest` (direct-I/O write/read against the node's slot) errors during preflight.
- **Outcome:** Agent preflight fails; process exits without running.

### 4.3 StorageClass provisioner incompatible with Block mode

- **Failure:** `sharedStorageVolumeMode: Block` set, but the StorageClass provisioner supports RWX filesystem only (not RWX block), or is on the known-incompatible list.
- **Detection:** `isRWXBlockCompatibleProvisioner` / `isRWXIncompatibleProvisioner` in `storagebasedremediationconfig_controller.go`.
- **Outcome:** Config validation error; reconciliation does not proceed (event `PVCError`). Message suggests a block-capable provisioner (e.g. Ceph RBD) or switching to `Filesystem` mode.

### 4.4 Concurrent-write (RWX) check not yet confirmed

- **Trigger:** Fewer than `min(2, desired)` agent replicas are Ready.
- **Response:** `status.storageValidation.concurrentWriteable` stays unset; `Ready` condition stays `False` with reason including "Storage write check not yet confirmed".
- **Outcome:** Config not marked `Ready` until enough agents come up to prove concurrent RWX access works.

### 4.5 Node count grows after write check was confirmed

- **Trigger:** DaemonSet's desired replica count exceeds `status.storageValidation.probedNodeCount` after a prior successful confirmation.
- **Response:** `concurrentWriteable` is cleared and the check re-runs at the new node count; a transient readiness dip (e.g. a node being fenced) does **not** trigger this — only growth past the previously probed count does.

---

## 5. Operator / config failures

### 5.1 Spec validation fails

- **Detection:** `sbrConfig.Spec.ValidateAll()`.
- **Outcome:** Event `ValidationError`; **no requeue** — user must fix spec.

### 5.2 StorageClass validation fails

- **Failure:** No `sharedStorageClass`; StorageClass not found; provisioner not RWX-compatible (mode-specific: filesystem RWX check for `Filesystem` mode, block RWX check for `Block` mode — see 4.3); temp PVC test fails.
- **Outcome:** Event `PVCError`; non-transient → no requeue; transient API error → requeue.

### 5.3 SBR device init Job fails

- **Failure:** Job still running → requeue; Job failed → delete and requeue; Job create fails → event `SBRDeviceInitError`. Uses `buildFSInitJob` (Filesystem) or `buildBlockInitJob` (Block).
- **Outcome:** DaemonSet creation blocked until init succeeds.

### 5.4 DaemonSet / RBAC / SA create or update fails

- **Outcome:** Respective error event emitted; requeue with backoff.

### 5.5 NFS mount options validation

- **Note:** `validateNFSMountOptions` returns `nil` early (a `TODO` in the code) — required `cache=none`/`sync` checks are **not enforced** despite the comments. Applies to Filesystem mode only.

---

## 6. API / control plane failures

### 6.1 API error during self-fence CR check (fail-safe)

- **Failure:** `remediationExistsForThisNode()` returns an error (timeout, API unavailable).
- **Response:** The abort condition `err == nil && !remediationExist` is **not met** → self-fence **proceeds**.
- **Outcome:** Node reboots even without confirmation a CR exists. Fail-safe: ambiguity → isolation.

### 6.2 API error during watchdog-unhealthy CR check

- **Failure:** Same API error on the watchdog tick path.
- **Response:** **Stop petting** watchdog. Event: `SBRUnhealthySkipPetAPIError`.
- **Outcome:** Watchdog fires → node reboots. Same fail-safe behavior.

### 6.3 Node condition patch fails (agent)

- **Failure:** `setNodeConditionSBRStorageUnhealthyStatus` patch fails.
- **Outcome:** Error logged; no built-in retry beyond the next peer check tick.

---

## Known code/doc mismatches

1. **Preflight comment** says "either watchdog or SBR" is sufficient — code requires **both**.
2. **`EventReasonSBRWriteFailed`** message says "write failures" but `sbrFailureCount` is only incremented on **fence-slot read** errors, not writes.
3. **`resetFailureCount("sbr")`** is never called in production — only in tests.
4. **`validateNFSMountOptions`** does not enforce anything at runtime (early return `TODO`).
