# Storage-Based Remediation (SBR) — Overview

## What problem SBR solves

Many high-availability patterns assume you can **fence** a failed machine through out-of-band management (e.g. IPMI/BMC). On Kubernetes in cloud or virtualized environments that is often **not available**, while you still need a safe way to stop a **zombie node** (still running but not participating correctly) from corrupting shared state.

SBR addresses that gap by using **shared storage** as a coordination and STONITH-style channel: nodes exchange **heartbeats** and **fence requests** on a common medium so the cluster can **isolate** an unhealthy node — typically by forcing it to **reboot via a local watchdog** — reducing **split-brain** risk for workloads that depend on exclusive access to shared resources.

---

## How it works (plain language)

An **SBR operator** installs and configures cluster-wide settings (`StorageBasedRemediationConfig`), validates ReadWriteMany shared storage, provisions the shared volume, and runs an **SBR agent** DaemonSet on nodes. Each agent keeps the **kernel watchdog** fed when healthy, writes periodic **heartbeats** into its own **slot** on the shared device, and reads peers' slots to detect failures. When a peer looks unhealthy, the agent publishes a **`SBRStorageUnhealthy`** node condition; a separate workflow (commonly **Node Health Check**) then creates a **`StorageBasedRemediation`** object named after the target node.

From that point, fencing takes one of two paths depending on whether the target node can still reach shared storage:

- **Storage still reachable:** A healthy peer agent writes a **fence message** to the target's slot on the fence device. The target's own agent reads it and immediately **self-fences** (reboot/panic).

- **Storage completely inaccessible:** The target can't read any fence message, but for the same reason it also **can't write its own heartbeats**. After enough consecutive write failures, the agent checks whether a `StorageBasedRemediation` CR exists for itself. If the CR exists — or if the API server is also unreachable (fail-safe) — it **stops petting its local watchdog**. The watchdog timer then expires and the OS reboots the node independently of shared storage. The only case where the node does *not* self-fence is a confirmed negative: API returns that no CR exists, in which case the node keeps petting and waits for NHC to decide.

After fencing, the remediation controller applies the **`node.kubernetes.io/out-of-service`** taint so Kubernetes evicts workloads from the fenced node cleanly.

> **Key design principle:** The watchdog is the universal enforcement mechanism. Reading a fence message is the fast path; the watchdog timeout is the safety net that works even when storage and API are both gone. When in doubt, the node defaults to isolating itself over risking split-brain.

---

## Key concepts

- **Fencing** — Making a failed or unsafe node unable to harm the cluster by triggering a self-reboot on that node.
- **Watchdog** — A hardware or software timer the agent must **pet** regularly; if the node stalls, petting stops and the node resets automatically.
- **Heartbeat** — A small, repeated write from each node into its **slot** on the shared SBR device so peers can confirm it is alive.
- **Fence message** — A write to the **fence device** telling a target node ID to self-fence; the target's agent detects it in its slot and acts.
- **Shared storage** — A `StorageClass`-backed **ReadWriteMany** PVC, in one of two volume modes (`spec.sharedStorageVolumeMode`, immutable after creation):
  - **Filesystem** (default) — a filesystem PVC; the operator initializes device files on it and mounts them into agents under `/dev/sbr`. File locking coordinates concurrent writers.
  - **Block** — a raw block PVC exposed to agents via `volumeDevices`; the operator writes an SBD superblock/slot layout instead of files, and agents use direct I/O (no file locking). Requires a block-capable RWX provisioner (e.g. Ceph RBD).
- **Slot** — A fixed region on the shared device allocated to a given node ID. The mapping of Kubernetes node name → slot ID is maintained in shared metadata on the device.
- **NHC (Node Health Check)** — The external operator expected to observe `SBRStorageUnhealthy` and create/delete `StorageBasedRemediation` CRs in response. Integrated by contract (no Go import).
- **OOS taint** — `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute`; applied by the remediation controller after successful fencing and removed during cleanup.
- **`detectOnlyMode`** — When enabled, the agent disarms the watchdog and never executes self-fence or peer fencing. Its purpose is to let NHC leverage SBR's storage-based health detection (the `SBRStorageUnhealthy` node condition) while preserving the **modularity to use any other medik8s remediator** (e.g. FAR, MDR) for the actual remediation action.

---

## When to use SBR

- You need **machine-level** remediation (node reboot/isolation), not just pod restart.
- You have (or can provision) **RWX-capable** shared filesystem storage.
- Nodes have an accessible **watchdog** device (softdog fallback is available).
- You are integrating with an **external remediation** workflow (e.g. NHC) that creates `StorageBasedRemediation` objects per node.

---

## Relationship to the medik8s ecosystem

| Component | Relationship |
|-----------|-------------|
| **NHC (Node Health Check)** | Observes `SBRStorageUnhealthy` condition → creates `StorageBasedRemediation` CR → agent fences the node. RBAC includes an `ext-remediation` ClusterRole aggregation for this. |
| **`StorageBasedRemediationTemplate`** | Template CRD for NHC-style external remediation consumers to instantiate remediations from a defined spec. |
| **SNR (Self Node Remediation)** | SNR also arms and pets the local watchdog. Running both SBR and SNR in full remediation mode on the same node causes a **conflict** — both compete for watchdog ownership. More broadly, any remediator that also arms the watchdog cannot run alongside SBR in full mode. The supported coexistence model is SBR in **`detectOnlyMode`** so NHC can use SBR's detection signal while delegating remediation to a separate remediator. |
| **FAR / MDR / other medik8s remediators** | Can be paired with SBR running in `detectOnlyMode`: SBR provides the storage-based detection signal, the other remediator owns the actual fencing action. |

---

## Current status and limitations

- **API version:** All CRDs are `v1alpha1`; OLM bundle lists `maturity: alpha`.
- **Fencing runs in agents, not the operator** — the operator binary handles config, RBAC, and DaemonSet management only.
- **`--sbr-device` is required** — watchdog-only mode without a shared SBR device was explicitly removed.
- **File locking compatibility** — shared FS coordination (Filesystem mode) depends on POSIX locking, which behaves differently across storage types (see `docs/sbr-coordination-strategies.md`). Block mode sidesteps this by using direct I/O instead of file locks.
- **Two storage volume modes** — `Filesystem` (default, file-backed) and `Block` (raw block PVC, superblock/slot layout via `internal/blockformat`) are both real, implemented paths as of the current codebase; the choice is immutable per `StorageBasedRemediationConfig`. Trust `internal/controller/storagebasedremediationconfig_controller.go` and the CRD spec over the README for which is default.
