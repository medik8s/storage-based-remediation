Here's a summary of the detailed specification we've developed, ready for a developer to begin implementation:

---

## Cloud-Native SBD for Node Healthcheck (Medik8s) - Detailed Specification

**1. Executive Summary**
This document outlines the specification for a cloud-native implementation of Storage-Based Death (SBD) for Kubernetes. The primary goal is to provide a robust node fencing mechanism in environments where traditional out-of-band management (IPMI, iDRAC) is unavailable, but shared block or replicated storage (e.g., SAN, Ceph) is present. This solution will integrate seamlessly with Medik8s' Node Healthcheck Operator to enable automated, reliable node remediation.

**2. Problem Statement**
Current Kubernetes node remediation solutions often rely on IPMI/iDRAC for fencing, which is not feasible in many cloud environments or certain on-premise setups. For stateful workloads depending on shared storage (like Ceph, traditional SANs via CSI), a mechanism is needed to reliably fence unhealthy nodes by leveraging their shared storage access, ensuring data consistency and high availability by preventing split-brain scenarios.

**3. Core Mechanism: SBD via CSI Block PV**
* **Principle:** Adapt the traditional SBD protocol for Kubernetes. SBD relies on a shared, low-latency block device for inter-node liveness signaling and "death" messages.
* **Shared "Watchdog" Device:** A single, dedicated `Block PersistentVolume (PV)` will serve as the SBD signalling mechanism.
    * **Provisioning:** This PV must be provisioned via a CSI driver that supports `volumeMode: Block` and, critically, **concurrent multi-node block access** (conceptually `ReadWriteMany` at the block level). Examples include specific cloud provider shared block storage or `ceph-csi` for shared RBD.
    * **Access:** Each participating node will mount this *exact same* PV as a raw block device (e.g., `/dev/sbd-watchdog`) within the SBD Agent pod.
* **SBD Protocol:** The SBD Agent will implement the SBD protocol's specific byte offsets and message structures for heartbeats, peer status, and fencing instructions directly on this raw block device.

**4. SBD Agent Architecture**
* **Deployment:** Implemented as a **Kubernetes DaemonSet**, ensuring an instance runs on every node targeted for SBD management.
* **Dual Watchdog Role:**
    * **External Watchdog (Shared SBD Block Device):** The agent continuously writes its own heartbeat to its designated slot on the shared SBD PV. It also continuously reads heartbeats from other cluster nodes to detect peer failures.
    * **Internal Watchdog (Local Kernel Watchdog):** The agent will interface with the node's local kernel watchdog device (e.g., `/dev/watchdog`). It will "pet" this watchdog regularly. If the agent itself becomes unresponsive, hangs, or loses access to the shared SBD device (and thus cannot determine its liveness/peer status), it will *stop petting* the local watchdog, causing the node to panic and reboot.
* **Self-Termination Logic:** If the SBD Agent detects a "fence" message directed at itself on the shared SBD device, it will immediately cease petting its local kernel watchdog, leading to self-panic and reboot.
* **Kubernetes API Watcher:** The agent (or a leader-elected component within the agent's project) will watch for `StorageBasedRemediation` Custom Resources (CRs). If an `StorageBasedRemediation` CR targets a *peer* node, a healthy agent instance will write the appropriate "fence" message for the target node onto the shared SBD device.

**5. Split-Brain Prevention & Consistency**
* **Shared Storage Arbitration:** The shared SBD Block PV serves as the single source of truth for node liveness. Any node losing connectivity to this PV will self-fence via its local kernel watchdog.
* **SBD Protocol Semantics:** Utilizes unique node IDs, timestamps, and sequence numbers on the shared device for robust liveness detection and preventing stale data issues.
* **Kubernetes API Server Quorum:** The creation of `StorageBasedRemediation` CRs by Medik8s' Node Healthcheck Operator relies on a healthy, quorum-based Kubernetes API server. Loss of API server quorum will prevent new external fencing actions from being initiated.
* **Leader Election for Fencing Orchestration:** The component of the SBD Agent responsible for writing external fence messages (to fence *other* nodes) will be leader-elected. This ensures only a single, coordinated fencing action occurs, preventing race conditions and ensuring consistency even during control plane partitions.

**6. Deployment & Management (Kubernetes Operator Pattern)**
* **SBD Operator:** A dedicated Kubernetes Operator will manage the lifecycle of all SBD components (CRDs, DaemonSets, RBAC).
* **Installation:** Via Helm Chart (recommended) or Kustomize.
* **Custom Resources:**
    * `SBDConfig` (Cluster-scoped): Defines cluster-wide SBD parameters (e.g., `sharedStorageClass`, `watchdogTimeout`, `sbdWatchdogPath`, `staleNodeTimeout`).
    * `StorageBasedRemediation` (Namespaced): A new CRD created by Node Healthcheck Operator to request SBD-based fencing for a specific node. Contains `nodeName`, `remediationRequestTime`, `status` (Phase, Message).
* **Monitoring:**
    * **Prometheus Metrics:** SBD Agent will expose comprehensive metrics (e.g., `sbd_agent_status_healthy`, `sbd_device_io_errors_total`, `sbd_watchdog_pets_total`, `sbd_peer_status`, `sbd_self_fenced_total`). `ServiceMonitor` provided for Prometheus Operator.
    * **Logging:** All components log to `stdout`/`stderr` with configurable levels.
    * **Events:** Operator emits Kubernetes Events for key lifecycle actions.
    * **Status Fields:** CR status fields provide real-time updates.
* **Troubleshooting:** `kubectl get/describe` on CRs, `kubectl logs` for agents/operator, Prometheus/Grafana dashboards.

**7. Security Considerations**
* **Principle of Least Privilege (PoLP):** Granular RBAC for Operator and Agent ServiceAccounts. Agent ServiceAccount will *not* have direct `delete` or `update` permissions on `Nodes`.
* **Pod Security:** SBD Agent pods will likely require `privileged` Pod Security Standard due to `/dev/watchdog` and raw block device access. This should be managed in a dedicated, isolated namespace.
* **Communication Security:** All K8s API communication via TLS/RBAC. Internal agent communication (if any beyond SBD device) via mTLS. Metrics endpoint secured via network policies or mTLS.
* **Shared SBD Block Device Security:** Relies on CSI driver's strict volume attachment, SBD protocol's checksums, and the fact that no sensitive data is stored on the device.
* **Prevention of Unauthorized Fencing:**
    * Only Node Healthcheck Operator can `create` `StorageBasedRemediation` CRs (via RBAC).
    * Leader election prevents multiple agents from initiating external fences.
    * SBD protocol requires explicit, targeted fence messages.
    * Comprehensive alerting on unexpected fencing events.
* **Supply Chain Security:** Signed container images, regular CVE scanning, hardened base images.

**8. Key Edge Cases, Failure Modes, and Recovery Mechanisms**
* **SBD Agent Failures:** DaemonSet restarts, local kernel watchdog acts as self-fence, peer fencing as fallback.
* **Shared SBD Block Device Issues:** Immediate self-fencing of affected nodes by local kernel watchdog due to inability to pet. Monitoring and alerts.
* **Local Kernel Watchdog Failure:** Pre-flight checks on agent startup, refusal to participate if unavailable, configurable fallback to `systemctl reboot`.
* **Kubernetes API Server Issues/Quorum Loss:** Core SBD signalling on shared device remains operational. No *new* external fencing requests are processed (safe mode). Leader election fails, preventing uncoordinated fencing.
* **Node Healthcheck Operator Failure:** Operator resilience (HA deployment). Core SBD self-fencing still functions as a last line of defense.
* **Concurrent Fencing Attempts/Race Conditions:** SBD protocol idempotency, leader election, and targeted messaging mitigate.
* **"Zombie" SBD Devices:** Potential future cleanup mechanism for stale entries.
* **Human Error/Misconfiguration:** Robust admission webhooks for CR validation, clear status reporting, logging, and monitoring alerts.

---

This comprehensive specification provides the developer with all the necessary information to understand the problem, design choices, functional requirements, non-functional requirements (security, reliability), and operational considerations for building the cloud-native SBD solution.

