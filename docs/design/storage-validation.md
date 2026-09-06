# Improved Storage Validation

Status: Proposed
Date: 2026-09-02
Owners: SBR maintainers

## 1. Summary

SBR decides a node is dead by watching its heartbeat on a shared disk. When the
node stops writing, SBR treats it as dead.

This only works if every node can reliably write to that disk and read what its
peers wrote. Not every storage backend gives us that. We have hit real problems
in both modes:

- **Block mode.** On some backends (for example Portworx shared block), only one
  node can write at a time. That node is the first one to attach the disk. Other
  nodes hang when they try an O_DIRECT write. They do not fail. They just get
  stuck.
- **Filesystem mode.** On some backends (for example NFS), reads can be stale.
  One node writes, but another node reads an old value. SBR then acts on wrong
  data. _Proposal in this doc does NOT solve this problem_

So SBR needs a real healthcheck. It must confirm that the storage the admin
configured actually lets every node write, before SBR trusts it. Without 
that, a live node can look dead. SBR could then fence a healthy node by mistake. 

To solve this issue, this document proposes two primary changes:

1. **Add a write check.** Make each agent's startup check do a real write. Then
   "the DaemonSet became fully Ready" tells us every node could write to the real
   disk at the same time.
2. **Gate fencing on the write check.** The controller records the result on the
   config CR. The agent reads it before fencing. If the write check has not passed,
   the agent does not fence.

There is another bug with how the controller performs a storage class validation 
today. It reconciles the config CR. On every reconcile it calls `validateStorageClass`
(`internal/controller/storagebasedremediationconfig_controller.go`).

That function does one of two things:

- If the provisioner is in a known-good list, it passes right away.
- If the provisioner is unknown, it calls `testRWXSupport`. That function creates
  a temporary ReadWriteMany PVC, waits 5 seconds, checks the result, then deletes
  the PVC.

Since solution for this bug touches the same code surface, this document proposes 
a small change in this flow too:

1. **Cache the StorageClass check.** Today the controller re-runs this check on
   every reconcile. It creates and deletes a test PVC about every 36 seconds. We
   will save the result instead and stop the churn.

## 2. Background

The current basic StorageClass check only shows that one RWX PVC can bind. It does 
not show that several nodes can write to the disk at the same time.

At runtime, the agent's block-mode startup check only reads the superblock
(`checkSBRBlockDevice`). It never writes. So it doesn't actually check all the 
functionality it needs from the storage.

There is also no quorum check in the fencing path. `checkFencingCompletion`
decides a node is dead from one signal: its heartbeat slot is stale for 60
seconds. It cannot differentiate between a dead node and a node that has a 
genuine config problem preventing it from working.

## 3. Goals and non-goals

### Goals
- Fail loudly before any fencing decision is made when storage does not provide basic write functionality needed in block mode.
- Improve existing storage class check (`testRWXSupport()`) in unknown provisioner case.

### Non-goals
- Change the NHC flow or the heartbeat-stop rule for fencing.
- Detect stale cross-node reads (for example on NFS). The write check in this
  design is a same-node round-trip: a node writes its own slot and reads it back.
  It does not confirm that peers see a node's fresh timestamp. Catching stale
  reads needs a cross-node freshness handshake at startup, which we are leaving
  out of this design for now.

## 4. Proposed design

### 4.1 One shared status field

Add a status field to `StorageBasedRemediationConfigStatus`
(`api/v1alpha1/storagebasedremediationconfig_types.go`). It caches the result of
the storage checks. The key is the identity of the StorageClass being checked.

```go
type StorageValidationStatus struct {
    // StorageClass and Provisioner say what was checked. If either changes,
    // the cached result is stale and the checks run again.
    StorageClass string `json:"storageClass,omitempty"`
    Provisioner  string `json:"provisioner,omitempty"`

    // RWXVerified is the result of the cheap PVC bind check (testRWXSupport).
    RWXVerified *bool `json:"rwxVerified,omitempty"`

    // ConcurrentWriteable records whether every node was confirmed able to write
    // to the real disk at the same time. nil means not checked yet.
    ConcurrentWriteable *bool `json:"concurrentWriteable,omitempty"`

    // ProbedNodeCount is the node count when ConcurrentWriteable was last
    // confirmed. If the node count grows past this, the check must run again.
    ProbedNodeCount int32 `json:"probedNodeCount,omitempty"`

    // LastProbeTime is when the checks last ran.
    LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

    // Message holds the latest detail, for operators to read.
    Message string `json:"message,omitempty"`
}
```

Add `StorageValidation *StorageValidationStatus` to the status struct. Then run
`make generate manifests`.

The cache key for the StorageClass check is `(StorageClass, Provisioner)`. The
reconciler already reads the StorageClass each pass, so it always has the current
provisioner to compare.

### 4.2 Change 1: cache the StorageClass check

Change `validateStorageClass` in the unknown-provisioner branch:

1. If the saved result matches the current StorageClass and provisioner, and
   `RWXVerified` is set, return the saved result. Do not create a PVC. Do not
   sleep.
2. Otherwise, run `testRWXSupport` once. Then save the result. Save both success
   and failure, so a bad StorageClass is not retried forever. Save the
   StorageClass, provisioner, time, and any message.
3. Only run the check again when the key changes. That happens if the admin edits
   `spec.storageClass`, or the StorageClass is recreated with a different
   provisioner.

The test PVC is now created at most once per StorageClass, not every 36 seconds.
That also removes the extra reconciles from the test PVC and the 5-second sleep.

Known-good provisioners are not affected. They still pass right away.

### 4.3 Change 2: Enhance agent's pre flight check

The agent's preflight check does a real write, and readiness is gated on that
check passing. This is one mechanism with three parts: the write, a sentinel the
agent creates only after the write passes, and a readiness probe that waits for
the sentinel.

**The write.** Today the block-mode startup check only reads the superblock
(`checkSBRBlockDevice`). Add a write step: the agent writes its own slot and reads
it back. Use the existing timeout helper in `internal/blockdevice`, so a hang
returns an error instead of blocking forever. Filesystem mode already does this
(`performSBRReadWriteTest`); block mode needs the same.

**The sentinel.** After `runPreflightChecks` succeeds, the agent creates a marker
file, for example `/var/run/sbr/preflight-ok`. It creates the marker only on
success. A failing or hanging preflight never creates it.

**The readiness probe waits for the sentinel.** The readiness probe checks for the
marker in addition to its current checks, for example:
`test -f /var/run/sbr/preflight-ok && test -c <watchdog> && <process alive>`.

On bad storage, it is important for us to ensure that the pod doesnt become ready
until and unless the preflight check is completed. Having the readiness probe tied
into the preflight check through the sentinel file does that.

**What this gives us.** After the change:

- On good storage, every agent writes, passes preflight, creates the sentinel, and
  becomes Ready.
- On bad storage, any agent that cannot write fails preflight, never creates the
  sentinel, and never becomes Ready.

So "the DaemonSet became fully Ready" now means "every node could write to the
real disk at the same time." That is the write check. We get it from the real disk
and the real set of nodes, with no extra pods.

### 4.4 Record the write-check result, keyed to node count

The controller already computes DaemonSet readiness in `updateStatus`, and
already reconciles when the DaemonSet changes. So it can record the result with no
new watch.

Both filesystem and block mode follow the same path: each agent passes
its preflight write check, the pod becomes Ready, and the controller reads
DaemonSet full-Ready. Filesystem mode already does a real write+readback at
preflight (`performSBRReadWriteTest`); block mode gets the equivalent (4.3). So
the flag is recorded identically in both modes.

Rules:

1. The write check passes when the DaemonSet reaches full Ready
   (`NumberReady == DesiredNumberScheduled`).
2. When that happens, set `ConcurrentWriteable = true` and record
   `ProbedNodeCount = DesiredNumberScheduled`.
3. Record the pass **once**. Do not clear it just because readiness later dips. A
   dip is normal: a node may be rebooting or being fenced. See the note below.
4. If `DesiredNumberScheduled` grows past `ProbedNodeCount`, there are new nodes
   we never checked. Require full Ready again at the new count before extending
   the result.

The DaemonSet drops below full Ready for many
reasons. Bad storage is only one. The others are normal: a node rebooting, a node
being fenced, an image pull, a transient restart. If we gated fe ncing on "all
agents Ready right now", we would deadlock. Fencing a node makes its agent not
Ready. So we could never fence. The write check is a statement about the storage,
taken at a calm moment. Storage does not change its write behavior at runtime, so
checking it once is enough.

### 4.5 Gate fencing on the write check (controller-to-agent signal)

The write-check result lives on the controller. Fencing happens in the agent. So
we need a signal from the controller to the agent.

1. The controller sets a condition on the config CR, for example
   `SBRConfigConditionStorageWriteable`, from `ConcurrentWriteable`. It also folds
   it into the overall `Ready` condition, next to `DaemonSetReady` and
   `SharedStorageReady`.
2. The agent's remediation reconciler reads that condition before it fences. If it
   is not `True`, the reconciler does not mark a node as fenced. It requeues and
   records why.

**This gate applies in both volume modes.** Filesystem-mode agents check
`ConcurrentWriteable == true` before fencing, exactly like block-mode agents. The
gate is not a block-only concern. Setting the flag but only enforcing it in block
mode would be inconsistent and would leave a real hole: filesystem mode has its
own write-capability failures (for example an unaligned O_DIRECT preflight write
returning EINVAL on a sharedv4/NFS server node, which CrashLoops the agent). The
gate should withhold fencing in that case too.

**What the gate guarantees per mode.** The gate means the same thing in both
modes — every node can write — but that guarantee covers different failures:

- **Block mode:** catches coordinator-exclusivity (only the first attacher can
  write), which is the false self-fence problem this design targets.
- **Filesystem mode:** catches total write breakage. It does **not** catch stale
  cross-node reads (see the non-goal in section 3). So "filesystem is gated" must
  not be read as "filesystem is safe from all substrate problems."

**Two rules that must hold.**

- **Fail safe.** If the agent cannot read the condition (API down, condition
  missing), it must not fence. A missing result means "not safe", never "safe".
- **Do not change how we confirm a node is dead.** This gate only *withholds*
  fencing. It is a startup and config signal. It is not the dead-node signal. The
  dead-node signal stays on storage: the victim's heartbeat stopped.
  `checkFencingCompletion` still must not treat the Kubernetes API as the
  dead-node signal.

The victim's self-fence path is unchanged. It stays storage-only: the victim
reads the fence message from its slot and hits its watchdog. It does not use the
API.

## 5. Telling "cannot write" apart from "fenced or rebooting"

This is the most important safety point. A fenced or rebooting node stops writing.
A live node that cannot write also stops writing. From storage reads alone, they
look the same.

The design avoids the trap by running the write check at a calm moment, then
recording the result:

- The write check (4.3, 4.4) runs when all nodes are healthy, before any fencing.
  So it measures what the storage can do, not whether a node is alive. A rebooting
  node cannot be mistaken for a storage fault, because the check is not re-run
  during a fence. A readiness dip during a fence does not clear the result.
- Where we look at Kubernetes `NodeReady`, we only use it to hold back fencing. We
  never use it as the dead-node signal. This matches the current rule in
  `checkFencingCompletion`, where `NodeReady=Unknown` does not mean the node is
  dead.

## 6. Risks and open questions

- **Storage that goes bad with no node change.** The write check does not run
  again unless the node count grows. A long safety timer can re-run it if we need
  that later. I think this falls under fencing domain instead of storage validation.
- **API change.** The new status field needs CRD regeneration and bundle updates.
