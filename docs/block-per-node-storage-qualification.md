# BlockPerNode storage qualification

Status: no storage deployment has qualified through this implementation assignment.

The configuration controller reads `spec.sharedStorageClass` from each `StorageBasedRemediationConfig`.
The controller requests one 10Mi RWX Block claim for each agent Pod.
Kubelet exposes that claim to the agent at `/dev/sbr-probe`.
The agent writes only bytes 0 through 4095.

The controller places each watcher outside its complete target group.
It checks each included PV's CSI driver and required topology.
It checks the current host Node UID against the CSINode owner UID.
These checks do not establish successful attachment or fresh remote reads.

The watcher receives each verified claim through a read-only PVC reference.
Kubelet exposes each claim at `/dev/sbr-probe-<index>` in the watcher container.
The watcher opens each device with read-only direct I/O on Linux.
Direct I/O bypasses the local page cache.
It does not establish remote storage visibility.

The runtime uses a fixed one-second visibility allowance, V.
A successful remote read must return the latest acknowledged durable write, or a later write, after this allowance.
The requirement also applies during a partition when the backend returns a successful read.
Valid-looking stale records cannot reveal a failed visibility requirement.

The agent writes a submitted sequence and the previous successfully completed sequence into each slot.
It changes its process-memory completed sequence only after write, flush, read, and byte verification succeed before the probe deadline.
The next probe write publishes that completed value.
The publication bound is B=2×(I+P).
P is the probe interval from the exact agent Pod.
I is that Pod's whole-probe deadline.
M is that Pod's failed-evaluation threshold.

The watcher uses Q=P and D=min(I,P).
Its maximum valid-read gap is G=Q+D.
Its expiry threshold is E=B+V+(M−1)×P+G.
Default P=5s, I=15s, and M=7 give E=81s.
Continuous five-second samples first reach that threshold at 85s.
An advancing completed sequence takes precedence at the boundary.

Each retained qualification report must record these values.

- Report path, date, source commit, and source fingerprint.
- OpenShift or Kubernetes version and host kernel version.
- CSI driver version, backend version, storage settings, and topology.
- Agent Pod settings P, I, and M.
- Actual claim capacity and device alignment requirements.
- Successful and failed visibility measurements under load, partition, and reconnection.
- Near-deadline probe results and next-write publication measurements.
- Watcher admission, direct reads, and denied write attempts on a disposable device.
- Mount, topology, replacement, and controller restart results.

The current source does not establish these measurements.
It also does not complete host locking, watchdog execution, host journal integration, boot reporting, or peer remediation.
RHWA-2085 must establish complete process inventory and qualified shutdown evidence before ownership can transfer.
Unknown release, termination, or disarm checks retain Node ownership and the configuration cleanup finalizer.

The controller reports Kubernetes resource readiness in `status.conditions[type="WatcherResourcesReady"]`.
This condition does not report successful device reads.
The watcher keeps runtime evidence in its process memory and writes diagnostics to its container standard output.

An old watcher can still write a condition when its assignment fields and Node resource version remain current.
The protocol does not authenticate the heartbeat writer or order recovery against every unpublished local failure.
Three distinct advances govern watcher recovery.
The local agent retains its independent fresh-probe recovery rule.
