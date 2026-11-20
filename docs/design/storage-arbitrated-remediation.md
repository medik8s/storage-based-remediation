# Storage-Arbitrated Remediation for OpenShift Virtualization

| Item | Value |
|---|---|
| Status | Proposal for review |
| Target release | 5.9.0 |
| Tracking | `RHWA-1839`, `RHWA-1837`, `RFE-9581` |

Contributors: Daniel Froehlich ([@DanielFroehlich](https://github.com/DanielFroehlich)), Wesley Hayutin ([@weshayutin](https://github.com/weshayutin)), Pranav Gaikwad ([@pranavgaikwad](https://github.com/pranavgaikwad)), and Michal Pryc ([@mpryc](https://github.com/mpryc)).

## Index

- [1. Introduction](#1-introduction)
- [2. Motivation](#2-motivation)
- [3. Goals](#3-goals)
- [4. Non-Goals](#4-non-goals)
- [5. Design](#5-design)
  - [5.1. Use-case matrix](#51-use-case-matrix)
  - [5.2. Components and ownership](#52-components-and-ownership)
  - [5.3. Remediation flow overview](#53-remediation-flow-overview)
  - [5.4. Configuration API](#54-configuration-api)
  - [5.5. Per-node probe and watchers](#55-per-node-probe-and-watchers)
  - [5.6. Storage and API decision policy](#56-storage-and-api-decision-policy)
  - [5.7. Host journal and repeated-request checks](#57-host-journal-and-repeated-request-checks)
  - [5.8. Remediation CR status and reboot evidence](#58-remediation-cr-status-and-reboot-evidence)
  - [5.9. Requested remediation, escalation, and cleanup](#59-requested-remediation-escalation-and-cleanup)
  - [5.10. NHC policy and reboot limits](#510-nhc-policy-and-reboot-limits)
  - [5.11. Startup, shutdown, and observability](#511-startup-shutdown-and-observability)
  - [5.12. Optional pre-kubelet delay](#512-optional-pre-kubelet-delay)
- [6. Open Questions](#6-open-questions)
- [7. Upgrade / Downgrade Concerns](#7-upgrade--downgrade-concerns)
- [8. Test Plan](#8-test-plan)
- [Alternatives considered](#alternatives-considered)
- [Terminology](#terminology)

## 1. Introduction

Storage Based Remediation (SBR) uses shared storage for heartbeats and fence messages. Each SBR agent writes heartbeats to its assigned slot. SBR agents on other nodes read those heartbeats. If the heartbeats stop, a peer sets the target Node's `SBRStorageUnhealthy` condition to `True`.

Node Healthcheck Operator (NHC) creates a `StorageBasedRemediation` custom resource (remediation CR). The CR permits a peer to write a fence message into the target node's slot. The target agent reads that message and reboots its host. Peers apply the OutOfService (OOS) taint to the target Node.

The proposed `BlockPerNode` mode gives each agent a private volume for its storage probe. Watchers read each volume without writing to it. The local agent uses storage probe results and Kubernetes API requests to determine when to reboot.

## 2. Motivation

The current shared Block volume requires several nodes to write to it at the same time. Some storage drivers, such as Portworx Block, cannot provide this access. SBR must work with all supported CSI providers on OpenShift.

The proposed mode lets only the local agent write to its probe volume. Other nodes read the heartbeats. If the heartbeats stop, peers apply the OutOfService taint.

The target agent checks the API for a remediation CR for its node. If it cannot reach the API, it reboots its host. In both cases, peers apply the OutOfService taint so Kubernetes and workload controllers can remove workloads.

## 3. Goals

- Preserve existing `Filesystem` and shared `Block` behavior. Keep `Filesystem` as the effective default when the volume mode is absent.
- Introduce `BlockPerNode`. Give each selected node one private probe volume.
- Continue to feed the watchdog during API loss while storage remains healthy.
- Let the local agent reboot its host if storage and API access remain unavailable under Section 5.6.
- Have a peer apply the OutOfService taint after storage loss whether or not the target can reach the API.

## 4. Non-Goals

- Remove or redesign the existing `Filesystem` and shared `Block` modes.
- Exchange fence messages through per-node probes.
- Support multiple StorageClasses or multiple probes per node in this release.
- Force delete Terminating pods after applying OOS taint.

## 5. Design

### 5.1. Use-case matrix

These rows describe the proposed BlockPerNode behavior.

| # | Scenario | Proposed behavior | Limit or dependency |
|---|---|---|---|
| 1 | Healthy storage and API | The local agent checks its probe, feeds the watchdog, and reports healthy storage on the Node. NO REBOOT. | Each probe checks only the selected storage backend. |
| 2 | Cluster-wide API outage with healthy storage | The local agent continues feeding the watchdog. It does not reboot its host for API loss alone. NO REBOOT. | Other Kubernetes and workload controllers can still act on their own conditions. |
| 3 | Broad storage outage with reachable API | The agent sets `SBRStorageUnhealthy=True` after `maxConsecutiveFailures` consecutive failed evaluations. It waits for a valid NHC request. Running peers process remediation CRs and update their target Nodes through the API. REBOOT via NHC. | NHC thresholds can block new CRs. Late reports can arrive after an agent decides to reboot. These thresholds cannot prevent every reboot. CSI can prevent an agent Pod from starting its peer controller. |
| 4 | Storage and API unavailable | The local agent reboots after fresh checks show unhealthy storage and API unavailability through the total-outage grace period. It uses available journal history to avoid another autonomous reboot during the same outage. A watcher reports missing heartbeats by setting `SBRStorageUnhealthy=True`. NHC creates a CR, and a peer applies the OutOfService taint under NHC policy. REBOOT. | If valid history shows an earlier autonomous attempt, the agent waits for sustained healthy storage before another attempt. The policy sets no cluster-wide reboot limit. |
| 5 | One node loses API or cluster-network access | The local agent continues to feed the watchdog while storage stays healthy. NO REBOOT. | Configure NHC to check only `SBRStorageUnhealthy=True`. |
| 6 | One node loses storage with API available | The target agent or watcher reports unhealthy storage. NHC can create a CR. A peer writes `Offered` and its deadline to the target Node's remediation annotation. The target tries to accept the offer and reboots under Section 5.7. The peer applies OOS after it checks matching boot evidence or the `OnTimeout` deadline expires. REBOOT. | Workloads can remain Terminating while the Node is Ready. |
| 7 | Kernel panic, host freeze, or power loss | A tested, armed watchdog can reboot a powered host that stops feeding it. A watcher can report expiry if successful reads show the completed probe sequence did not advance during the expiry period. REBOOT. | A watchdog cannot restore power. A watcher must be available to report expiry, and NHC must be available to create a CR. The base policy does not guarantee workload relocation from a host that stays off. |
| 8 | Planned agent restart or upgrade | Before a planned stop, the agent disarms its watchdog if it has made no reboot decision. It keeps request entries in the host journal. Another eligible peer reads the request phase and deadline in the Node annotation. It checks CR status and continues the request. After startup, the local agent compares the host's current boot ID with the saved `beforeBootID`. NO REBOOT. | Keep an eligible peer running during the rollout. Check that the watchdog supports disarm. A replacement agent must acquire the host lock before it controls the watchdog. |
| 9 | Kubelet failure with healthy storage | The local agent continues feeding the watchdog while its monitoring loop continues to run. NO REBOOT. | This storage policy does not remediate every kubelet failure. |
| 10 | Slow or hung storage I/O | Use `ioTimeout` as the deadline for the complete probe. Count failed health evaluations while watchdog decisions and peer CR processing continue. NO REBOOT if the probe succeeds within `ioTimeout`. | A deadline cannot cancel I/O that blocks in the kernel. Limit each process to one unfinished probe operation per device. Do not use a lock that lets blocked probe I/O stop watchdog feeds or peer API work. |
| 11 | Network partition with healthy storage | Do not reboot solely for the partition. This NHC policy does not request relocation solely for it. NO REBOOT. | This is not a guarantee against concurrent workloads created by other controllers. |
| 12 | API or storage recovers after the reboot decision | The agent continues the reboot it chose. If possible, it keeps the original request identities and boot ID in the host journal. After restart, it reports the current boot ID. Peers remove the OutOfService taint after storage recovers. | Before deciding, the agent stops if it sees that the peer closed the request. If the agent cannot see that update, it can still reboot after acceptance remains unknown. |
| 13 | Different nodes require different StorageClasses | Deferred. Configure a StorageClass for each node group. | See Section 6. |
| 14 | One node uses several storage backends | Deferred. Probe each backend and use the results to determine the Node's storage condition. | See Section 6. |
| 15 | Stuck or terminating workload Pod | A peer reports policy success after it confirms the OutOfService taint. It can check remaining Pods, VMIs, and VolumeAttachments for up to five minutes. On CR deletion, it removes only SBR-added restrictions that no other CR still requires. | Workload finalizers do not block CR deletion. Workloads can remain on the target or fail to start elsewhere. |
| 16 | Host journal unavailable or recovered | The agent uses API reads with time limits to recover matching request identities and attempt history. Under the host lock, it tries to recreate a corrupt journal. If writing still fails, it keeps current state in memory and reports missing history as `Unknown`. | Journal failure alone does not trigger a reboot. An agent with unknown autonomous history can reboot if it meets Section 5.6. Some history and boot results can be unavailable in API status. |
| 17 | Repeated remediation during persistent failure | A peer's successful update to `Offered` for a newer CR marks the earlier request `Superseded`. The target can still issue an earlier reboot it already chose. Valid history limits the agent to one autonomous attempt per outage. Without that history, the agent can reboot again if it meets Section 5.6. | The agent can try another autonomous reboot after it observes sustained healthy storage, normally five minutes, without requiring API recovery. Each CR retains its own original boot ID. |

### 5.2. Components and ownership

- **Configuration Controller**. The controller reads the SBR configuration and creates or updates the agent DaemonSet. It groups selected nodes and creates watcher Deployments for those groups.
- **Local SBR agent Pod**. One Pod runs on each selected node. It uses a private Block volume to test storage health. It runs these two components.
  - **Agent**. The agent feeds the watchdog and uses a separate process to write the host journal. When the API is reachable, it sets the target Node's `SBRStorageUnhealthy` condition to `True`. When the API is unavailable, it reboots its host without a request.
  - **Peer reconciler**. The peer reads remediation CRs for other nodes. It checks boot IDs and updates CR status. It applies or removes the OutOfService taint on target Nodes under Section 5.9.
- **Watcher Deployments**. The operator deploys separate watcher Pods. Each Pod mounts the assigned probe volumes in read-only mode. It sets `SBRStorageUnhealthy=True` if successful reads show no completed probe progress for the expiry period.
- **Boot Reporter**. A separate DaemonSet runs the reporter. After kubelet starts, the reporter reads the host boot ID and available reboot entries in the host journal.
- **Optional Boot Service**. A systemd service runs before kubelet on each node. It delays kubelet startup after reboot to reduce the risk of concurrent workload instances. Enable it only for an autonomous reboot.

This example uses one node per watcher group (`watcherGroupSize: 1`). Each node can mount the other node's probe read-only.

<table>
  <tr><th colspan="2">Cluster controllers</th></tr>
  <tr>
    <td valign="top"><strong>SBR configuration controller</strong><br/>Creates or updates the agent and reporter DaemonSets.<br/>Groups selected nodes and creates watcher Deployments.</td>
    <td valign="top"><strong>Node Healthcheck Operator (NHC)</strong><br/>Checks Node conditions against its policy.<br/>Creates remediation CRs and requests their deletion.</td>
  </tr>
</table>

<table>
  <tr><th colspan="3">Node 1</th></tr>
  <tr>
    <td valign="top"><strong>Local agent (SBR agent Pod)</strong><br/>Writes, flushes, and reads Node 1's probe.<br/>Feeds the watchdog and writes reboot details to the journal.<br/>Reboots Node 1 under Section 5.6.</td>
    <td valign="top"><strong>Peer reconciler (same SBR agent Pod)</strong><br/>Writes offers for Node 2 and checks its boot IDs.<br/>Updates CR status.<br/>Sets and removes Node 2's cordon and OutOfService taint under Section 5.9.</td>
    <td valign="top"><strong>Watcher Pod</strong><br/>Reads Node 2's probe without writing.<br/>Updates Node 2's storage condition.</td>
  </tr>
  <tr>
    <td valign="top"><strong>Boot reporter Pod</strong><br/>Reads Node 1's current boot ID and available reboot entries.<br/>Writes boot information to the Node annotation described in Section 5.8.</td>
    <td valign="top"><strong>Optional boot service (systemd)</strong><br/>Can delay Node 1's kubelet after a selected reboot.<br/>Allows kubelet to start when the configured period ends.</td>
    <td></td>
  </tr>
</table>

<table>
  <tr><th colspan="3">Node 2</th></tr>
  <tr>
    <td valign="top"><strong>Local agent (SBR agent Pod)</strong><br/>Writes, flushes, and reads Node 2's probe.<br/>Feeds the watchdog and writes reboot details to the journal.<br/>Reboots Node 2 under Section 5.6.</td>
    <td valign="top"><strong>Peer reconciler (same SBR agent Pod)</strong><br/>Writes offers for Node 1 and checks its boot IDs.<br/>Updates CR status.<br/>Sets and removes Node 1's cordon and OutOfService taint under Section 5.9.</td>
    <td valign="top"><strong>Watcher Pod</strong><br/>Reads Node 1's probe without writing.<br/>Updates Node 1's storage condition.</td>
  </tr>
  <tr>
    <td valign="top"><strong>Boot reporter Pod</strong><br/>Reads Node 2's current boot ID and available reboot entries.<br/>Writes boot information to the Node annotation described in Section 5.8.</td>
    <td valign="top"><strong>Optional boot service (systemd)</strong><br/>Can delay Node 2's kubelet after a selected reboot.<br/>Allows kubelet to start when the configured period ends.</td>
    <td></td>
  </tr>
</table>

### 5.3. Remediation flow overview

The local agent tests its probe volume and feeds the watchdog until it decides to reboot. The watcher checks whether the target's completed probe sequence advances. NHC checks the Node condition and decides whether to create a remediation CR. A peer applies or removes the OutOfService taint. The agent or reporter submits the target's boot ID through the API. The optional host service can delay kubelet startup for a configured period after reboot.

#### Example: NHC requests remediation

Node 1 loses storage access but can still reach the API. Node 2 remains healthy and runs the peer controller for Node 1.

1. The agent on Node 1 sets `SBRStorageUnhealthy=True` on its Node after its failed evaluations reach `maxConsecutiveFailures`. A watcher can also set this condition when successful reads show no completed probe progress for the expiry period.
2. NHC checks its health policy. If the policy permits remediation, NHC creates an SBR CR for Node 1.
3. The peer on Node 2 writes the request details, original boot ID, and OutOfService deadline to Node 1's remediation annotation. In the same update, it sets the phase to `Offered` and cordons Node 1.
4. The agent on Node 1 checks whether the offer applies to its current boot. It tries to change the phase to `Accepted` in the Node annotation. It reboots if the update succeeds or if the result remains unknown after the permitted attempts. It does not choose a reboot if it sees that the request was canceled, a newer request replaced it, or its identities do not match.
5. After reboot, the agent or boot reporter on Node 1 writes the current boot ID through the API. The peer on Node 2 checks whether the original boot for the CR ended. With `OnTimeout`, it can also apply the OutOfService taint after the deadline without acceptance or proof of reboot.
6. After the peer confirms the taint, it sets the CR success conditions. Kubernetes and workload controllers process workload removal and volume detach. The peer checks for remaining Pods, VMIs, and VolumeAttachments for up to five minutes. Workloads can still run or wait for relocation after the peer reports success.
7. NHC requests CR deletion when its health policy permits it. The peer on Node 2 stops new taint operations and observation scans for that CR. It removes an SBR-added cordon or taint only if no other CR still needs it. It preserves any restriction that existed before the CR. It keeps the original boot ID and request details in Node 1's annotation for later boot checks. It removes the CR finalizer without waiting for boot confirmation or workload removal.

#### Example: The local agent reboots autonomously

Node 1 loses both storage and API access. Node 2 remains healthy and runs a watcher and the peer controller for Node 1.

1. The agent on Node 1 counts failed storage evaluations. When the count reaches the threshold and the API is unavailable, the agent starts `totalOutageGracePeriod`. It waits for that period while both failures continue.
2. The agent checks the host journal and any available API history for an earlier autonomous reboot attempt during this outage. If it confirms one, it waits for sustained healthy storage before another autonomous reboot. It may have no API result that shows whether NHC created a CR.
3. If possible, the agent writes the attempt ID and current boot ID to its host journal. It reboots Node 1 under the rules in Section 5.7. A failed journal write does not prevent an otherwise permitted reboot.
4. A watcher on Node 2 can set `SBRStorageUnhealthy=True` on Node 1 if successful reads show no completed probe progress for the expiry period. NHC can then create a CR if its policy permits remediation. A failed watcher read alone cannot set the condition to `True`.
5. After reboot, the optional boot service on Node 1 can delay kubelet for the configured period. The service must be enabled and must recognize the reboot. If it cannot read the reboot details from the journal, it may allow kubelet to start without delay. Kubelet starts when the period ends, even if the API remains unavailable.
6. When API access returns, the agent or boot reporter on Node 1 submits the current boot ID and available attempt history. The peer on Node 2 checks whether that information establishes that the original boot of an active, eligible CR ended. It can then apply the OutOfService taint to Node 1. The CR retains its original deadline. Under `OnTimeout`, the peer can apply the taint at that deadline without a boot report from Node 1.
7. An active, eligible CR is required before the peer on Node 2 can apply the OutOfService taint. A later CR can cause another reboot if the peer writes a new offer for Node 1's new boot ID.

See Section 5.6 for reboot decisions, Section 5.8 for boot ID checks, and Section 5.9 for OutOfService and CR deletion. See Section 5.12 for the optional kubelet delay.

### 5.4. Configuration API

| Field | Effective default | Validation | Meaning |
|---|---|---|---|
| `sharedStorageVolumeMode` | `Filesystem` | `Filesystem`, `Block`, or `BlockPerNode` | Existing shared Filesystem, existing shared Block, or one per-node Block probe with read-only watchers. |
| `ioTimeout` | `15s` in `BlockPerNode`, `2s` in existing modes | Duration from `1s` through `10m`. Valid in all modes. | Complete probe deadline in BlockPerNode. Existing behavior in shared modes. |
| `sbrTimeoutSeconds` | Existing `30` | Existing integer range `10–300` | Supplies the BlockPerNode evaluation and probe interval through `GetSBRUpdateInterval()`. |
| `maxConsecutiveFailures` | Existing `7` | Existing integer range `2–32` | Consecutive failed local evaluations before the agent reports unhealthy storage. Watcher expiry uses Section 5.5. |
| `totalOutageGracePeriod` | `60s` in `BlockPerNode` | Duration from `0s` through `1h`. Valid only in `BlockPerNode`. | The agent can decide to reboot autonomously only after storage remains unhealthy and the API remains unavailable for this period. |
| `autonomousRebootRecoveryPeriod` | `5m` in `BlockPerNode` | Positive duration. Valid only in `BlockPerNode`. | After successful probes show healthy storage for this period, an earlier autonomous attempt no longer prevents another attempt. API recovery is optional for this check. |
| `oosTaintPolicy` | `OnTimeout` | `OnTimeout` or `OnReboot`. Valid only in `BlockPerNode`. | Under both policies, a peer can apply the OutOfService taint after it verifies that the request's original boot ended. `OnTimeout` also permits the peer to apply the taint when the reboot grace period ends. |
| `rebootGracePeriod` | `60s` | Positive duration. Valid only in `BlockPerNode`. | For `OnTimeout`, measure from the `Offered` timestamp saved in the target Node's annotation. Acceptance does not change the deadline. An autonomous reboot starts no OOS timer. |
| `bootUnitEnabled` | `false` | Boolean. Valid only in `BlockPerNode`. | Enable the optional host service that delays kubelet after a recognized autonomous reboot. |
| `bootUnitTimeout` | No default | Positive duration. Required when `bootUnitEnabled` is `true`. Valid only in `BlockPerNode`. | Maximum period that the boot unit blocks kubelet. At expiry, allow kubelet to start even if the API is unavailable. |
| `watcherGroupSize` | `16` | Positive integer. Valid only in `BlockPerNode`. | Maximum selected nodes per watcher group. Each group receives one watcher. |
| `watcherGroupLabels` | Empty list | Distinct valid Node label keys. Valid only in `BlockPerNode`. | Put selected nodes in separate watcher groups if their region, zone, or configured label values differ. |

The BlockPerNode defaults do not replace existing shared-storage thresholds. Omitted `ioTimeout` preserves the existing two-second timeout in Filesystem and Block. An omitted volume mode remains equivalent to explicit `Filesystem`. Explicit `Block` continues to select shared storage.

Get each effective default from the configuration getters. In the CRD, leave `ioTimeout` without a default so the getter can select it by mode. Reject `ioTimeout: 0s`. With `totalOutageGracePeriod: 0s`, the agent still checks for unhealthy storage and API unavailability before it can reboot. It does not wait for an additional grace period. Require a positive `autonomousRebootRecoveryPeriod`.

An omitted `bootUnitEnabled` means `false`. Require an explicit `bootUnitTimeout` when enabling the unit. A timeout can be set while the unit is disabled, but it has no effect. Reject an invalid, zero, or negative timeout whenever the field is present. The timeout is independent of `rebootGracePeriod` and does not change either OutOfService policy.

Proposed additions in [configuration types](../../api/v1alpha1/storagebasedremediationconfig_types.go):

```go
package v1alpha1

// Existing imports, type, constants, and other fields are omitted.
const (
	SharedStorageVolumeModeBlockPerNode SharedStorageVolumeModeType = "BlockPerNode"
	DefaultIOTimeout                                                = 2 * time.Second
	DefaultBlockPerNodeIOTimeout                                    = 15 * time.Second
	OOSTaintPolicyOnTimeout                                         = "OnTimeout"
	OOSTaintPolicyOnReboot                                          = "OnReboot"
	DefaultRebootGracePeriod                                        = 60 * time.Second
	DefaultWatcherGroupSize                                        = 16
)

type StorageBasedRemediationConfigSpec struct {
	// +kubebuilder:validation:Enum=Filesystem;Block;BlockPerNode
	// +optional
	SharedStorageVolumeMode *SharedStorageVolumeModeType `json:"sharedStorageVolumeMode,omitempty"`

	// +optional
	IOTimeout *metav1.Duration `json:"ioTimeout,omitempty"`

	// +optional
	TotalOutageGracePeriod *metav1.Duration `json:"totalOutageGracePeriod,omitempty"`

	// +optional
	AutonomousRebootRecoveryPeriod *metav1.Duration `json:"autonomousRebootRecoveryPeriod,omitempty"`

	// +kubebuilder:validation:Enum=OnTimeout;OnReboot
	// +optional
	OOSTaintPolicy *string `json:"oosTaintPolicy,omitempty"`

	// +optional
	RebootGracePeriod *metav1.Duration `json:"rebootGracePeriod,omitempty"`

	// +optional
	BootUnitEnabled *bool `json:"bootUnitEnabled,omitempty"`

	// +optional
	BootUnitTimeout *metav1.Duration `json:"bootUnitTimeout,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +optional
	WatcherGroupSize *int32 `json:"watcherGroupSize,omitempty"`

	// +optional
	WatcherGroupLabels []string `json:"watcherGroupLabels,omitempty"`
}

func (s *StorageBasedRemediationConfigSpec) IsBlockPerNodeMode() bool {
	return s.GetSharedStorageVolumeMode() == SharedStorageVolumeModeBlockPerNode
}

func (s *StorageBasedRemediationConfigSpec) GetIOTimeout() time.Duration {
	if s.IOTimeout != nil {
		return s.IOTimeout.Duration
	}
	if s.IsBlockPerNodeMode() {
		return DefaultBlockPerNodeIOTimeout
	}
	return DefaultIOTimeout
}
```

Keep `GetSharedStorageVolumeMode()` returning `Filesystem` when the mode is omitted. Keep `IsBlockMode()` true only for shared `Block`. Its callers select shared devices and RWX claims and remove shared-volume resources. Check `IsBlockPerNodeMode()` before calling those shared-mode paths.

`BlockPerNode` is an SBR configuration value. It is not a Kubernetes PV or PVC volume mode. The generated per-node claim uses `volumeMode: Block` and `ReadWriteMany`.

Update the `sharedStorageClass` description and validation for both Block values. Preserve the existing StorageClass requirement for shared `Block`. `BlockPerNode` requires a class that supports the per-node RWX Block claims described in Section 5.5.

Update the custom resource definition (CRD), generated code, webhook, samples, and agent arguments together. The controller currently sets `--io-timeout` to the two-second [agent constant](../../internal/agent/constants.go). Pass `GetIOTimeout()` through `buildSBRAgentArgs()` and the existing `buildBlockInitJob()` in [the configuration controller](../../internal/controller/storagebasedremediationconfig_controller.go).

The [preflight checks](../../cmd/sbr-agent/preflight.go) also use a separate fixed two-second timeout. Pass the effective timeout to applicable initialization and preflight operations. In BlockPerNode, start the peer manager without waiting for these storage checks.

Existing modes retain per-operation timeout and retry behavior. Their complete operations can therefore exceed `ioTimeout`. The Filesystem `ReopenDevice()` fallback does not currently enforce a timeout. Exposing the field does not add a deadline guarantee to that fallback. See [device operations](../../internal/blockdevice/blockdevice.go) and [device selection](../../cmd/sbr-agent/main.go).

For BlockPerNode, apply one deadline to the write, flush, read, verification, and retries in a complete probe. Check that nested timeouts do not shorten this deadline unintentionally. An I/O error can end the probe early. Finish all retries within the original deadline. Section 5.6 specifies how the agent uses the result.

Use separate time limits for API calls, journal writes, workload observations, and the hardware watchdog. Keep `rebootMethod` internal. Keep `10Mi` as the internal probe size and test it with supported storage.

#### Field interactions

| Configuration | Existing `Filesystem` or `Block` outcome | `BlockPerNode` outcome |
|---|---|---|
| `sharedStorageVolumeMode` omitted or `Filesystem` | Existing shared Filesystem behavior. | Not selected. |
| `sharedStorageVolumeMode: Block` | Existing shared ReadWriteMany (RWX) Block behavior. | Not selected. |
| `sharedStorageVolumeMode: BlockPerNode` | Not selected. | One ReadWriteMany (RWX) Block claim per agent Pod. Only that agent writes it. |
| `sharedStorageClass` | Existing shared-storage validation and provisioning. | Required. Selects one StorageClass for per-node probes. It must support dynamic RWX Block provisioning and concurrent read-only watchers. |
| `watchdogPath` | Existing behavior. | The local agent opens the watchdog at the same path. Only its monitoring loop controls the watchdog. |
| `nodeSelector` omitted or empty | Existing worker-node selection. | Same worker-node selection. The effective selector includes Linux nodes only. |
| `sbrTimeoutSeconds` | Existing timeout and derived intervals. | Reuse `GetSBRUpdateInterval()` for probe scheduling and failed-state evaluation. It divides the duration by six, with a one-second minimum. |
| `maxConsecutiveFailures` | Existing counter behavior. | Sets how many failed evaluations the local agent counts before it declares storage unhealthy. The watcher uses the expiry period in Section 5.5. |
| `ioTimeout` | Existing per-operation behavior with a configurable value. Omission retains `2s`. | Deadline for a complete probe. Omission selects `15s`. Reaching the deadline starts the count of failed evaluations. The agent waits for the counter to reach the threshold before reporting unhealthy storage. |
| `detectOnlyMode: Enabled` | Existing detection-only behavior. | Stop peers from making new offers or applying OutOfService. Prevent new autonomous reboot decisions. Continue any confirmed acceptance and any reboot already chosen. Continue to report boot and storage observations, check for remaining workloads and attachments after confirmed OOS, and release CR restrictions. |
| `detectOnlyMode` omitted or `Disabled` | Existing behavior. | A local agent can reboot for a request or after combined storage and API loss under the rules below. |
| `totalOutageGracePeriod` or `autonomousRebootRecoveryPeriod` is present | Admission rejects the inapplicable field. | Use the configured duration or default for the combined-outage timer or storage recovery timer. |
| `oosTaintPolicy` or `rebootGracePeriod` is present | Admission rejects the inapplicable field. | When offering a request, the peer writes the effective OutOfService policy and fixed deadline to the Node annotation. |
| `bootUnitEnabled` or `bootUnitTimeout` is present | Admission rejects the inapplicable field. | Configure the standalone host service. Only an enabled unit with a valid timeout delays kubelet. |
| `watcherGroupSize` or `watcherGroupLabels` is present | Admission rejects the inapplicable field. | Partition by region, zone, and custom label values. Split each partition at the configured size. |

The existing defaults remain `30` seconds and seven consecutive failures. The schema ranges stay the same. BlockPerNode uses consecutive failed evaluations at the interval in Section 5.6. The shared modes use shared-heartbeat expiry and mixed operation counters. Use the evaluation schedule in Section 5.6 to calculate detection time.

Test the fifteen-second BlockPerNode default and supported overrides on CSI storage that represents the storage used by supported workloads. The `1s–10m` range matches current device validation. A backend can require a different value within that range. Allow `ioTimeout` values longer than the probe interval or hardware watchdog timeout. Continue API checks and watchdog feeds while a probe runs.

An example BlockPerNode configuration is:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: sbr-block-per-node
  namespace: sbr-system
spec:
  sharedStorageVolumeMode: BlockPerNode
  sharedStorageClass: example-block
  ioTimeout: 15s
  sbrTimeoutSeconds: 30
  maxConsecutiveFailures: 7
  totalOutageGracePeriod: 60s
  autonomousRebootRecoveryPeriod: 5m
  oosTaintPolicy: OnTimeout
  rebootGracePeriod: 60s
  bootUnitEnabled: false
  watcherGroupSize: 16
  watcherGroupLabels:
    - storage.example.com/failure-domain
```

To enable the optional boot unit, set both fields in the same configuration:

```yaml
spec:
  bootUnitEnabled: true
  bootUnitTimeout: 2m
```

The two-minute timeout is an example, not a default. It does not change the CR's reboot deadline.

#### Validation and immutability (VERY TECHNICAL, CAN BE SKIPPED)

Extend `ValidateCreate()` and `ValidateUpdate()` in the [configuration webhook](../../api/v1alpha1/storagebasedremediationconfig_webhook.go).

1. Accept `Filesystem`, `Block`, and `BlockPerNode` in the existing volume-mode validation.
2. Check the syntax and allowed ranges of the duration fields. Require positive values for the autonomous recovery and reboot grace periods. Require a StorageClass for `BlockPerNode`.
3. Accept `ioTimeout` in all modes. Reject the new remediation policy fields outside BlockPerNode. Validate OOS policy values.
4. Reject changes to the effective volume mode, total-outage grace, and autonomous recovery period.
5. Reject a configuration if it selects any node that another BlockPerNode configuration selects.
6. Reject different StorageClasses across BlockPerNode configurations in this release.
7. Validate positive watcherGroupSize and distinct valid watcherGroupLabels keys. Reject the fields outside BlockPerNode.
8. Validate `bootUnitEnabled` and `bootUnitTimeout`. Require a positive timeout when enabled. Reject either field outside BlockPerNode, including explicit `false`.

Compare parsed duration values. Treat an omitted volume mode and explicit `Filesystem` as equal. Treat an omitted duration and its explicit effective default as equal. Reject removal of a nondefault immutable duration. Apply valid `ioTimeout` changes through the replacement procedure below.

To switch to or from `BlockPerNode`, replace the configuration. Changes to mutable fields can require replacement agent Pods. Keep each request's stored policy, Node UID, configuration UID, original boot ID, and deadline.

Before retiring the configuration, prevent new offers and new CR-driven taint operations. Complete the required Node updates to release its CRs' restrictions. A peer can complete timeout processing or remove the CR without `BootEnded`.

A replacement configuration cannot resume an unfinished request. The target can still issue a reboot it already chose. A later boot report may be unavailable.

Apply this replacement rule to selector, StorageClass, watchdog path, detect-only, and probe timing changes. An OutOfService policy or grace change applies only when peers make later offers. For group-size or group-label changes, replace watcher assignments under Section 5.5. Check both nodes that leave and nodes that enter the selection before changing their assignments. Keep the configuration until peers close the requests to new operations and release the CRs' restrictions. A replacement configuration cannot resume the old requests.

Both boot-unit fields are mutable. Apply changes to the host settings for later eligible boots. A delay already in progress keeps its original deadline. Disabling the unit prevents future delays once the host receives the setting. An API update alone does not confirm that an unreachable host has received it.

Keep the original deadline for every probe that started. Use the new timing values when the replacement monitor starts. Restart the failure count and elapsed-time checks as Section 5.11 specifies.

The webhook and controller each rely on the other to check for configurations that select the same node. Check configurations across namespaces with their effective worker selectors and the Linux restriction. Concurrent creates and later node-label changes require controller checks after admission.

After admission, the configuration controller checks which nodes each configuration selects. Repeat the checks after concurrent creates and node-label changes. If a configuration is invalid or conflicts with another, prevent new actions under it. A running agent can continue to use settings it already verified when the API becomes unavailable. Starting a new process after simultaneous API and journal loss is deferred. This proposal adds no separate file with a copy of the configuration for that case.

The local agent acquires a non-blocking exclusive `flock` on `/var/lib/medik8s/state/sbr-executor.lock`. Keep the lock file separate from the journal file that the agent replaces atomically. Do not delete the lock file during operation. The agent acquires the lock before it opens or arms the watchdog, loads mutable journal fields, or attempts to accept an offer. It holds the lock until it stops.

If the agent cannot acquire the lock, or the host does not support it, the agent reports `LocalAgentConflict`. That process cannot write to the watchdog or journal. Its peer controller can continue to process other Nodes' remediation CRs if it meets Section 5.2. A new Pod UID, expired Lease, replacement peer, or elapsed time does not release the lock. Do not disarm a watchdog that an unknown process opened.

Set DaemonSet `maxSurge: 0` and `maxUnavailable: 1` explicitly. The local agent still acquires the host lock. Check the Node assignment to select its configuration. Check the Node's remediation annotation to select the current request. Check the host lock to determine which process can control the watchdog.

#### Configuration replacement (VERY TECHNICAL, CAN BE SKIPPED)

To change an immutable value, the administrator deletes the old SBR config CR. After the components stop and the resources are removed as described below, the administrator creates the replacement. For mutable values, follow the rollout procedure above.

When the administrator deletes a BlockPerNode SBR config CR, the components complete these steps:

1. The configuration controller prevents peers from writing new offers for the old configuration.
2. Peers prevent new OutOfService updates and stop observation scans. They release the CRs' scheduling restrictions under Section 5.9. They release the restrictions even if `BootEnded` is missing or the last scan showed remaining resources. They keep available request identities and boot results. A replacement configuration cannot resume the old requests.
3. Each local agent that has made no reboot decision checks that it disarmed its watchdog before stopping its monitoring loop.
4. The configuration controller removes the old watcher assignments and their PVC references. It waits for the old watcher, agent, and reporter Pods to stop. Then it releases the selected nodes from the old configuration.
5. The configuration controller removes resources owned by the old configuration and permits a replacement configuration.

An unreachable node can delay SBR configuration replacement. Only the local agent that holds the host lock can control its watchdog. Keep the host journal and its completed request identities when the administrator deletes or replaces the configuration.

Keep the agent DaemonSet, service account, and permissions until peers stop new actions and confirm the Node updates that release each CR's restrictions. Check saved CR UID entries even after a CR disappears. Removal can proceed without `BootEnded`. Keep an eligible peer until the required Node updates finish.

If the configuration is terminating, a peer can stop OutOfService updates and workload checks for its CRs. It can remove their UIDs from the restrictions list. If no CR still needs the restrictions, it removes the cordon and taint that SBR added. It cannot write new offers. A replacement configuration cannot resume an old request.

If no eligible peer remains, the configuration controller reports that it cannot complete the required Node updates and waits for a peer.

The current deletion handler removes role-based access control (RBAC) and uses garbage collection to delete the DaemonSet. Add a separate BlockPerNode path to [the configuration controller](../../internal/controller/storagebasedremediationconfig_controller.go) to release restrictions before removing the RBAC and DaemonSet. Let Kubernetes delete the generic ephemeral PVCs with their owning Pods. Skip the shared-PV cleanup path for these claims.

### 5.5. Per-node probe and watchers

#### Summary

Each selected node tests storage using its own small volume. Only that node writes to the volume. Watchers on other nodes read the results and report whether the storage checks continue to succeed. The operator assigns watchers to groups of nodes and reports when a watcher cannot monitor its group. A failed watcher read does not, by itself, show that the target node has failed. New successful checks are required before the agent or watcher reports recovery.

#### Technical details

Each selected node runs one local SBR agent with one generic ephemeral Block volume. The Pod owns its PersistentVolumeClaim (PVC). Set the PVC request from a named `10Mi` value in the controller. The driver can allocate a larger volume. A supported backend must accept the request or specify a minimum request size that passes its tests.

The StorageClass must support raw Block, dynamic provisioning, direct I/O, and concurrent read-only access from other nodes. The claim requests `ReadWriteMany`. Only the target agent writes. The driver must make completed writes visible to the watcher. If the watcher cannot establish that it is reading current data, it cannot use the result to report expiry or recovery.

`SBRStorageUnhealthy` describes the health of the configured probe storage. The administrator selects a StorageClass and topology that represent the storage used by the affected workloads. Use distinct status and event results for PVC provisioning, volume attachment, probe I/O, and storage health.

Add a BlockPerNode branch before `validateStorageClass()`, `ensurePVC()`, and `ensureSBRDevice()` in [the configuration controller](../../internal/controller/storagebasedremediationconfig_controller.go). Those calls currently create one shared RWX PVC when the administrator selects `Block`. Prefer `WaitForFirstConsumer` for topology-constrained storage. Report a warning if the StorageClass sets `volumeBindingMode: Immediate` with topology constraints. Accept that combination.

The proposed DaemonSet fragment is:

```yaml
spec:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 0
  template:
    spec:
      containers:
        - name: sbr-agent
          # Existing image, arguments, and host mounts are omitted.
          volumeDevices:
            - name: storage-probe
              devicePath: /dev/sbr-probe
      volumes:
        - name: storage-probe
          ephemeral:
            volumeClaimTemplate:
              spec:
                accessModes: [ReadWriteMany]
                volumeMode: Block
                storageClassName: example-block
                resources:
                  requests:
                    storage: 10Mi
```

Keep the watchdog device mount separate from `/dev/sbr-probe`. Mounting the entire host `/dev` over that path can hide the CSI device.

A container restart reuses the Pod's PVC. A node reboot can also retain the same Pod and PVC. A replacement Pod receives its own PVC. Kubernetes deletes the old PVC with its owning Pod. The PersistentVolume (PV) reclaim policy controls what happens to the underlying volume. See [generic ephemeral volume lifecycle](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#lifecycle-and-persistentvolumeclaim).

The local agent writes heartbeat data to its probe. The probe stores no reboot evidence, shared NodeMap, or fence messages. Assigned watchers mount the same PVC read-only. Replacing the probe does not delete the host journal.

The local agent must select the per-node prober before shared-storage initialization in `NewSBRAgentWithWatchdog()`. Disable shared heartbeat writes, peer checks, and fence-slot reads only in BlockPerNode. Keep the existing shared-storage initialization and runtime paths intact. See [the agent constructor and loops](../../cmd/sbr-agent/main.go).

In BlockPerNode, select the new startup path before the current storage preflight and constructor. Initialize API clients and start the peer manager without opening the probe. Then initialize the local agent independently. The existing constructor initializes storage and NodeMap before its manager. Section 5.11 specifies which tasks must continue if another task stops.

Run the boot reporter in a separate DaemonSet without the probe volume. Mount the host journal read-only and omit the watchdog device. When CSI cannot attach the probe, the agent Pod can remain Pending, including any sidecar. If a selected node's agent or reporter Pod is Pending, the configuration controller reports the unavailable component in status and events.

Use the same StorageClass in all BlockPerNode configurations in the initial implementation. Admission rejects a different StorageClass. The controller checks for conflicts from concurrent creations before it starts agent Pods. Section 6 describes the deferred extension.

##### Watcher assignment and observations

The operator puts each selected target Node in a group and sets its `sbr.medik8s.io/heartbeat-group` label. Group targets by `topology.kubernetes.io/region`, `topology.kubernetes.io/zone`, and the keys in `watcherGroupLabels`. Put targets in separate groups if any value differs. Treat an absent standard topology label as a separate value. Volumes still require a watcher on a node that can attach them. If a required custom label is missing, report that the operator cannot assign a watcher and the watcher cannot check the target's probe.

Split targets with matching values into groups of at most `watcherGroupSize` Nodes. The default limit is 16. Run one watcher Pod per group. A watcher references the existing PVCs in its namespace with `persistentVolumeClaim.readOnly: true` and raw block devices. It creates no probe PVCs.

Use required node affinity to select a watcher node outside its group. That node must satisfy the driver and topology constraints of every assigned volume. If no node meets these requirements, the watcher remains Pending. The operator reports that the watcher cannot read probes for that group.

A watcher rejects an assignment that includes its own node. The operator replaces affected watcher Pods when it changes group membership. Label changes alone do not move a running Pod.

Each target has one agent Pod, one reporter Pod, one probe PVC, and its PV. The peer controller runs in the agent Pod. With N targets and at most S targets per group, `ceil(N / S)` is the minimum number of groups before grouping by topology. One failed mount can prevent the watcher from reading all probes in its group. Report for each target whether a watcher can read its probe.

The operator stores the watcher assignment in the target Node's `sbr.medik8s.io/heartbeat-assignment` annotation. Include the Node UID, configuration UID, agent Pod UID, PVC UID, watcher Pod UID, and assignment generation. Keep read results in a separate annotation. Only the watcher Pod with the UID in the current assignment can publish results for it.

The watcher writes each observation to the target Node's `sbr.medik8s.io/heartbeat-observation` annotation. Include the assignment, target and probe identities, writer boot ID, producer incarnation, last completed probe sequence, observation revision, and read result. The heartbeat data identifies the latest submitted probe and the latest probe that completed successfully. Section 5.6 describes when the agent writes the completion sequence to the probe. The disk format and integrity checks are deferred.

| Observation | Meaning | Effect on `SBRStorageUnhealthy` |
|---|---|---|
| `Progressing` | Valid reads show that the completed probe sequence increased. | The watcher can set `False` if the reads satisfy the recovery checks for the current failure. |
| `Expired` | Successful reads of valid heartbeats show no increase in the completed probe sequence during the expiry period. | Set `True` after the expiry check. NHC waits for its configured condition duration. |
| `Unknown` | A read failed, the heartbeat or assignment identities did not match, or there were too few observations to check progress. | Preserve the current condition and report that the watcher cannot determine expiry or recovery. |

The watcher applies the same expiry rule for all Node `Ready` values. It reads only heartbeat data from the probe. It reports expiry or recovery in the Node condition. NHC checks that condition against its policy before it creates a remediation CR. Peers can then apply the OutOfService taint under Section 5.9.

Use the first valid heartbeat to start the expiry period. Measure the period with the watcher's monotonic clock. Start a new period after a read gap, watcher restart, assignment change, or change of writer process. After API recovery, take new probe readings before updating the Node condition. To report recovery, require an increase in the completed probe sequence. A blank or invalid heartbeat, read error, or failed attachment cannot establish expiry or recovery.

When retrying a Node update, keep the time and age of the original observation. A conflict on an unrelated `resourceVersion` change does not change when the watcher took the reading. Repeat the read after the observation expires. If the watcher cannot determine whether the observation comes before or after the current condition update, keep the condition and its saved probe sequence.

Set a time limit for each read. Continue reading other targets when one read blocks. Limit unfinished reads when a kernel operation blocks. Write changed observation results to the Node annotation and refresh them at limited intervals. If observations are stale or absent, report that the watcher cannot determine probe progress.

When a replacement agent Pod gets a new PVC, remove the old watcher assignment. Wait for the first valid heartbeat from the new PVC. Remove watcher references to the old claims so Kubernetes can delete them. Peers can still process remediation CRs when the watcher cannot read a probe.

##### Order agent and watcher condition updates

The local agent and watcher share one Node annotation to order updates to `SBRStorageUnhealthy`. Save the Node UID, configuration UID, agent Pod UID, PVC UID, writer boot ID, and producer incarnation with each observation. A producer incarnation identifies one writer process. Check the new writer's identity before using its observations. The watcher also checks the assignment generation and observation revision. Recovery also requires a new successful probe result.

Update the condition, failure revision, and probe sequence in one conditional `nodes/status` update. Keep the revision and sequence in the ordering annotation. Check the live Node UID and `resourceVersion`. Preserve unrelated fields.

On conflict, read the live Node and check the observation again. If the relevant revision changed, repeat the probe or watcher read as applicable. See [Kubernetes Node status update strategy](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/registry/core/node/strategy.go#L149-L174).

1. When the agent or watcher reports a new failure, increment the failure revision. Store the latest submitted probe sequence at that failure as the failure sequence. For watcher expiry, use the latest submission in the valid heartbeats. Keep the greater failure sequence for the same writer process. Keep the current revision and sequence for an older or repeated failure report.
2. Before reporting local recovery, read the current failure revision and start a new probe. Before reporting watcher recovery, read that revision and read a new baseline heartbeat. Then require a later read that shows an increase in the completed probe sequence.
3. Before setting the condition to `False`, check the current writer identity and failure revision. For the same writer process, require a completed sequence greater than the failure sequence. For a verified replacement writer, require a new success result and a matching failure revision. Compare sequences only from the same writer process.
4. Before reporting expiry, check the condition and probe sequence from the start of the expiry period. If a newer observation shows progress, read a new baseline heartbeat and restart the expiry period.
5. Preserve `LastTransitionTime` when the condition status stays the same, including a change in report source or reason.

For example, the writer submitted probe 42 and the condition update saved 42 as the failure sequence. A later heartbeat shows that probe 41 completed successfully. It cannot set the condition to `False`. An expiry report for probe 41 also cannot replace 42 as the failure sequence. A new, verified completion for a sequence above 42 can set the condition to `False` if the failure revision still matches. Compare sequences and revisions even when the host clocks agree.

### 5.6. Storage and API decision policy

#### Summary

The local agent checks storage and API access separately.

- If storage stays healthy, the agent keeps feeding the watchdog even when the API is unavailable.
- If storage reaches the failure threshold and the API remains available, the agent reports the failure and waits for a remediation request.
- If storage stays unhealthy and the API remains unavailable, the agent waits through the configured grace period. It checks earlier autonomous attempts before deciding to reboot.

A valid remediation request can also cause a reboot while storage is healthy. Slow storage operations must not stop watchdog feeds or the other checks.

#### Technical details

The local agent checks storage with probes and checks API reachability with live requests. Run probes, API calls, journal writes, and watchdog feeds independently. If one of these operations blocks, the others can continue.

The local agent writes, flushes, reads, and verifies each probe. Increase the completed probe sequence only when all four steps succeed. The next probe write includes that sequence. The watcher can see a completion one cycle after it occurred. Do not use a separate heartbeat loop that can increase the completed sequence while the probe fails. Include the publication delay when setting the expiry period.

Use one deadline for the complete probe, including flushes and retries. The heartbeat format is deferred. Use these values in the failure count below.

- `I` is the effective `ioTimeout`, defaulting to fifteen seconds.
- `P` is `GetSBRUpdateInterval()`, defaulting to five seconds.
- `M` is `maxConsecutiveFailures`, defaulting to seven.

The local agent applies this failure-count policy:

1. At each scheduled probe time, start a new probe only if the process has no unfinished probe or probe I/O.
2. When the first probe returns an error or reaches deadline `I`, start the failed interval with a count of one.
3. Wait at least `P` after the first failure before the next failed-state evaluation.
4. While storage remains in the failed state, increase the count once per evaluation. Wait at least `P` between increments.
5. If another probe fails between evaluations, keep the count and the next evaluation time.
6. When the count reaches `M`, treat storage as unhealthy and publish `SBRStorageUnhealthy=True` when API access permits.
7. Before the agent decides to reboot, a new probe that completes successfully clears the failed state and sets the count to zero.

Count failed health evaluations. A probe that reaches its deadline starts the count, and the agent waits for the threshold before it declares storage unhealthy. Pending I/O before its deadline neither adds a new failure nor clears an existing one.

Keep the count if only time elapsed. Keep the BlockPerNode count separate from the existing mixed operation counters and their ten-minute reset. Measure intervals with a monotonic clock. If scheduling skips an evaluation, continue the schedule without adding an increment for the skipped evaluation.

If a probe reaches its deadline while a kernel operation is blocked, count the timeout as a failure. A Go timeout cannot cancel the blocked operation. Allow at most one unfinished probe operation per device in the process. Continue the failed-state evaluations while the operation blocks. Start another operation only after the blocked one ends.

If an operation finishes after its deadline, ignore its result for recovery. Require a later probe that completes successfully. Process that success before a failed-state evaluation due at the same time. Once the agent chooses a reboot, keep the decision in memory for the current boot under Section 5.7.

This example starts with healthy storage and a zero failure count. With default values, the agent needs six more failed evaluations after the first failure. These evaluations take at least thirty seconds. If a probe blocks, it takes fifteen seconds to reach its deadline and start the failure count. The agent declares storage unhealthy at least forty-five seconds after it starts that probe.

Scheduling the agent and updating the Node condition can add delay. If the probe returns an error immediately, the agent starts the failure count sooner.

If the API remains unavailable, the agent then waits sixty seconds under the default total-outage grace period. In the blocked-probe example, the agent can decide to reboot no earlier than 105 seconds after it starts the probe. The reboot can occur later. The peer calculates the separate OutOfService deadline from the `Offered` timestamp it saves on the target Node.

The agent and watcher update the same `SBRStorageUnhealthy` condition under Section 5.5. Watcher results leave the target's local failure count and autonomous reboot checks unchanged. NHC can create a remediation CR if the condition satisfies its policy.

Use the ordering annotation and conditional `nodes/status` update from Section 5.5. Preserve unrelated Node fields. Keep `LastTransitionTime` if the condition status stays the same. Combine duplicate writes. Limit the delay before writing a change to `SBRStorageUnhealthy=True`. NHC then applies its separate condition duration.

When the target observes detect-only mode, it stops trying to accept new offers. It cannot choose to reboot because the acceptance result is unknown or because both storage and API are unavailable. Peers stop publishing new offers. The target can continue a confirmed `Accepted` request for this boot until it sees a newer offer replace it. The target continues any reboot it already chose.

| Storage state | Request or API state | Local action before a reboot decision |
|---|---|---|
| Any state | A valid offer for this Node and boot | Try to change `Offered` to `Accepted` under Section 5.7. Reboot if the agent confirms acceptance or if the permitted attempts leave the result unknown. |
| Any state | The API confirms `Accepted` for this Node and current boot | Continue the same attempt until the local agent sees that a newer offer replaced it. |
| Healthy or below the failure threshold | No valid offer | Continue checking the probe and feeding the watchdog. |
| Unhealthy | API reachable, no valid offer | Update `SBRStorageUnhealthy=True` and feed the watchdog while NHC evaluates its policy. |
| Unhealthy | API unavailable before `totalOutageGracePeriod` ends | Feed the watchdog until the grace period ends or storage or API access recovers. |
| Unhealthy | API unavailable through `totalOutageGracePeriod`, with no known earlier autonomous attempt in this outage | Try to write the autonomous reboot decision to the host journal. Reboot even if the write fails. |
| Unhealthy | API unavailable through `totalOutageGracePeriod`, with valid history of an earlier autonomous attempt in this outage | Feed the watchdog and report `BudgetExhausted` when possible. |
| Any state | Detect-only enabled | Continue to report storage and boot observations. Do not try to accept a new offer or choose a reboot for unknown acceptance or combined storage and API loss. Continue confirmed acceptance unless the agent sees a newer offer replace it. Continue any reboot the agent already chose. |

Storage recovery does not cancel a valid requested reboot. Before deciding, reject mismatched request identities, an unaccepted offer that the agent sees the peer withdrew, and any request that the agent sees is `Superseded`.

The peer sets the newer request to `Offered` and the earlier request to `Superseded` in one Node update that checks `resourceVersion`. This also applies to an earlier `Accepted` request.

The local agent can still issue a reboot it already chose. If it cannot see the update, it can still reboot for the earlier request if Section 5.7 permits it. Section 5.9 lists when peers stop trying to apply OutOfService and stop checking workloads and attachments for the CR.

Send an authenticated API request with a time limit to check reachability. Use a live result. The routine check reads the small SBR remediation CR, or requests `/readyz` when RBAC permits. Live Node reads also check request identities, condition failure revisions, the CR UID that controls the request, and reported boot IDs. Follow the check order in Sections 5.5 and 5.8.

A successful empty response or `NotFound` shows that the API endpoint is reachable. If the Node is absent or its name has a different UID, reject the request. If authorization, certificate, proxy, or client configuration fails, do not accept a request on that basis. Do not count these errors as API unavailability for an autonomous reboot.

Count connection failures and network deadlines toward API unavailability. A received `429` or `5xx` shows that the configured endpoint is reachable. Do not use such a response to authorize a request. On either response, continue to feed the watchdog and reset the API-unavailability timer.

Report credential, certificate-validation, and client-configuration errors as degraded API access. Do not use those errors to start an autonomous reboot. A failed Node status update or a missing remediation CR alone does not show API unavailability.

The local agent starts `totalOutageGracePeriod` when the failed-evaluation count reaches its threshold and the API is unavailable. If the API is still reachable at the threshold, the agent starts the timer when the API becomes unavailable while storage remains unhealthy. Reset the timer if storage recovers or the API becomes reachable.

Repeat the API checks throughout the grace period. Require fresh results that show API unavailability. If the checks stop or stall, restart the grace period when fresh checks resume.

To permit another autonomous reboot after a known earlier attempt, require healthy storage for `autonomousRebootRecoveryPeriod`. API failures do not restart this storage recovery timer. While storage remains healthy, the agent continues to feed the watchdog without API access. A later autonomous reboot still requires the agent to reach the failed-evaluation threshold again and verify API unavailability through the grace period.

NHC thresholds can limit new CRs during a broad storage outage. Local agents use their own storage and API checks to decide when to reboot autonomously. Several hosts can reboot during the same outage. In a later boot, the agent may have no usable history of an earlier autonomous reboot during this outage. It can reboot again after it satisfies the checks and grace period.

### 5.7. Host journal and repeated-request checks

#### Summary

The agent tries to save reboot decisions and earlier attempts in a file on the host. After restart, it uses that file and available API information to decide which work can continue. It never executes the same request after that request's original boot has ended. If it knows that it already attempted an autonomous reboot during the outage, it waits for sustained storage recovery before another attempt.

A journal failure does not, by itself, trigger or prevent a permitted reboot. If earlier attempt information is lost, the agent can reboot again after fresh failure checks and the required wait. Once the agent chooses a reboot, it continues even if storage or API access returns.

#### Technical details

The local agent writes request IDs, the corresponding Node, configuration, and boot IDs, reboot decisions, and autonomous attempts to the host journal. Keep this file at `/var/lib/medik8s/state/sbr.json`, separate from the probe volume and the Pod. The local agent uses the host lock in Section 5.4.

The reporter reads complete copies of the file when available. The journal format, library API, and implementation are separate work.

Use the CR UID as the ID of a requested attempt. Save the Node UID, configuration UID, and original boot ID with it.

For an autonomous attempt, the agent uses a locally assigned ID. That ID cannot let the agent execute a request or let the peer apply OutOfService.

The agent checks the Node annotation for the request phase and whether a newer offer replaced it. A journal entry cannot set the phase to `Offered` or `Accepted`, or let the agent start a new requested reboot.

##### Decide a requested reboot

One decision loop in the local agent chooses the action while the agent holds the host lock. The agent keeps the reboot decision in memory for the rest of the current boot.

1. Read the request in the live Node annotation before trying to accept it. Check the CR UID, Node UID, configuration UID, and original boot ID against the current host. Reject a request if it is known to be `Superseded`, even if its earlier phase was `Accepted`.
2. Continue the same attempt if the annotation shows a matching `Accepted` request for the current boot. Otherwise, require a matching `Offered` request that the agent can still accept. Try to change its phase to `Accepted`. Require the Node version to match the version read.
3. Try to set `Accepted` at most twice. This limit is hardcoded. Allow up to two seconds for each acceptance API call and each read to check its result. Wait at most one second between retries. Limit the entire sequence to 30 seconds, including reads and retry delays. If a result is unknown, read the live Node annotation before the next attempt. After a conflict, check the IDs again and check whether the agent can still accept the offer.
4. End the sequence when the agent knows the result or completes the allowed attempts and reads. The agent can choose reboot when it confirms acceptance. It can also choose reboot if it validated the earlier offer and still cannot determine the result when the sequence ends. Before choosing reboot, reject a known refusal, a request known to be `Superseded`, an unaccepted offer that the peer withdrew, or an ID mismatch.
5. Keep the reboot decision in memory and try to write it to the host journal. Wait at most two seconds for the journal operation. Continue to reboot if the write fails, the wait ends, or another journal operation prevents the write.

The acceptance attempts and journal write have their own time limits. The OutOfService grace period defaults to 60 seconds and does not affect those limits.

The agent can execute a request while storage is healthy. A requested reboot neither counts toward the autonomous limit nor permits another autonomous reboot in the same outage. If acceptance remains unknown, the agent cannot report `Accepted` or `BootEnded` on that basis.

The agent can still use an otherwise permitted earlier decision if it cannot see that a newer offer replaced the request. Once the agent chooses reboot, a newer offer, health recovery, and later API or journal failures cannot reverse the decision.

If the agent sees that a newer validated offer replaced the earlier request before it chooses reboot, it stops processing that request. See Section 5.9. The newer CR retains its own original boot ID. The agent cannot execute either request after the boot identified in that request ends.

##### Keep journal writes independent of reboot

The local agent can continue without a successful journal write. Allow only one unfinished journal operation on the host. When the wait ends, the operation can still update the file. Keep the host lock until that operation finishes.

A replacement process cannot start another journal operation while the earlier operation can still update the file. A late write can save attempt information. It cannot let the agent choose reboot or reverse a decision.

If the agent cannot read or write the journal, it keeps current decisions and observations in memory. Keep any trusted information that shows the agent already attempted an autonomous reboot during the outage. Report missing history as Unknown. Keep confirmed matching information from the API. An absent Node entry does not rule out a reboot attempt while the API was unavailable.

If the file is corrupt, try to recreate it atomically while holding the host lock. If recreation fails, use the information in memory. Preserve a journal that uses an unsupported format. Report that a replacement journal survived a host reboot only after journal recovery verifies that copy after the reboot. Separate journal work defines how writes survive reboot and how processes transfer the file and lock.

After the local agent chooses reboot, it keeps that decision and stops feeding the watchdog for the current boot. Keep the watchdog armed during shutdown of that boot. Use the software reboot and watchdog fallback in [the existing agent](../../cmd/sbr-agent/main.go). The watchdog can reset the host even if the software reboot command stalls. The command result and elapsed time cannot establish that a boot ended.

##### Recover after restart

The local agent reads the kernel boot ID and any available journal entries. It tries to read matching information through the API within a time limit. If the journal is unavailable, an entry that remains in the Node annotation can supply the original request IDs. The agent cannot infer a request or acceptance from a CR name, an absent Node entry, or a missing journal.

- If the request's original boot ended, the agent must never execute it on the new boot. Ignore the saved instruction to stop feeding the watchdog on the previous boot. The agent can feed the watchdog on the current boot even if the old instruction remains in the journal.
- If saved information shows that the agent chose reboot for this boot under the same valid configuration, keep that decision. Continue to withhold watchdog feeds. If saved information does not show whether the agent sent the software reboot command, it can send it again in the same boot. An agent with a replacement configuration cannot resume the old request or stop feeding the watchdog based on the old decision. Report a kept decision from this boot, because the reboot did not take place: publish an event that identifies the incident and increase a counter.
- If the target accepted the request and has not chosen reboot, follow the current request phase. The agent must stop processing it if it sees that a newer offer replaced it. If it cannot see that replacement, it can continue the otherwise permitted work. After configuration retirement, a compatible agent might no longer be available to continue an accepted request.
- If the current boot ID is invalid, the agent cannot choose a new reboot. Keep any reboot decision that the agent can establish it already made.
- If a replacement process cannot read the API or the journal, it cannot choose a requested reboot based on missing request IDs. It can continue checking the conditions for an autonomous reboot only with a trusted effective configuration and fresh observations. Starting the process without a trusted configuration is deferred.

##### Check earlier autonomous attempts before reboot

If trusted information is available, limit the agent to one autonomous reboot attempt per outage. Assign a local ID to the attempt and include the outage ID when available. Before reboot, try to write the attempt to the journal so the agent can detect it after restart. The agent can continue with the permitted reboot if the write fails. If trusted information shows an attempt during the current outage, the agent cannot make another autonomous attempt during that outage.

If the agent cannot determine whether it attempted an autonomous reboot, it can reboot after it completes the fresh storage and API checks in Section 5.6. In a continuing outage, the host can reboot again if no attempt information survives. A journal failure alone never starts a reboot.

The agent can make another autonomous attempt after it observes healthy local storage for `autonomousRebootRecoveryPeriod`. The effective default is five minutes. Start the recovery timer again after a failed or expired probe or a gap in fresh storage observations. Keep the timer running during API failures. A requested reboot, process restart, or configuration change alone does not permit a new autonomous attempt.

When API access returns, compare attempt IDs and outage IDs in the journal and the Node annotation. Count a report with the same IDs only once. Keep a recovered attempt for an earlier outage separate from attempts for the current outage. Detailed journal recovery and storage are separate work.

### 5.8. Remediation CR status and reboot evidence

#### Summary

Each host boot has a different boot ID. After a reboot, the agent or a separate reporter publishes the host's boot information. A peer checks that information against the request to determine whether the earlier boot ended. The separate reporter can do this without access to the storage probe.

The request status shows acceptance, boot observations, taint application, and remaining workload checks separately. A changed boot ID does not explain why the host rebooted or prove that workloads remain stopped. Success of the remediation policy does not necessarily mean that a reboot occurred.

#### Technical details

Add optional BlockPerNode fields to [remediation status](../../api/v1alpha1/storagebasedremediation_types.go). Preserve the existing fields and conditions for Filesystem and shared Block.

| Status field | Meaning |
|---|---|
| nodeUID, configUID, volumeMode, beforeBootID | IDs and mode that the peer writes to CR status before it adds the finalizer and writes the offer to the Node annotation. These values cannot change for this CR. |
| executionPhase | Copy of the request phase in the Node annotation. Values include Offered, Accepted, Cancelled, Superseded, and BootEnded. |
| observedBootID, bootObservation | The boot ID read on the host and the result of checking for a boot change. Results include AcceptedBootEnded, BootObserved, and EvidenceUnavailable. |
| policy, offeredAt, rebootDeadline | The OutOfService policy, time of the offer, and deadline that the peer saves in the Node annotation. |
| cleanup.phase, cleanup.startedAt, cleanup.deadline | The phase, start, and deadline for the peer's first window to check for remaining workloads and attachments. |
| cleanup.outOfServiceApplied, cleanup.reason | Whether the peer confirmed the OutOfService taint and why it reported the observation result. |
| restrictionsReleased | The restrictions list no longer shows that this CR UID requires a cordon or taint. Other CRs in the list can still keep the Node cordoned or tainted. |

Cleanup phases are `Pending`, `Observing`, `Completed`, `Incomplete`, `Unknown`, and `Skipped`. Use Pending if a taint write fails or its result is unknown. Use Observing after the peer confirms OutOfService. Use Skipped if the peer stops the operation before it begins checking workloads and attachments.

With `outOfServiceApplied=false`, the peer has yet to confirm the taint for this operation. The taint may already be present.

The proposed fields extend the existing status type. Other existing fields are omitted:

```go
package v1alpha1

type StorageBasedRemediationStatus struct {
	NodeUID              types.UID        `json:"nodeUID,omitempty"`
	ConfigUID            types.UID        `json:"configUID,omitempty"`
	VolumeMode           string           `json:"volumeMode,omitempty"`
	BeforeBootID         string           `json:"beforeBootID,omitempty"`
	ExecutionPhase       string           `json:"executionPhase,omitempty"`
	ObservedBootID       string           `json:"observedBootID,omitempty"`
	BootObservation      string           `json:"bootObservation,omitempty"`
	Policy               string           `json:"policy,omitempty"`
	OfferedAt            *metav1.Time     `json:"offeredAt,omitempty"`
	RebootDeadline       *metav1.Time     `json:"rebootDeadline,omitempty"`
	Cleanup              *CleanupStatus   `json:"cleanup,omitempty"`
	RestrictionsReleased bool             `json:"restrictionsReleased,omitempty"`
}

type CleanupStatus struct {
	Phase               string       `json:"phase,omitempty"`
	StartedAt           *metav1.Time `json:"startedAt,omitempty"`
	Deadline            *metav1.Time `json:"deadline,omitempty"`
	OutOfServiceApplied bool         `json:"outOfServiceApplied,omitempty"`
	Reason              string       `json:"reason,omitempty"`
}
```

The agent checks the request phase in the Node annotation before it executes a request. CR status reports the result. The agent follows the Node entry and the unknown-result rules in Section 5.7. Use BootEnded only for a confirmed accepted request. Report the result of the OutOfService policy and a boot change for an unaccepted request separately.

If an API response is lost, read the live object before retrying the same request UID. Do not apply the change twice.

##### Confirm the end of a boot for an accepted request

The local agent can accept an offer only if its actual boot ID and its Node and configuration UIDs match the offer. If the boot ID differs, report the mismatch. The peer sets Cancelled for the unaccepted offer for the earlier boot. For an active OnTimeout request, the peer can still use the original deadline.

After a reboot, the agent or separate reporter performs the following steps:

1. Read the matching Accepted entry in the Node annotation through the live API.
2. Check the Node UID, original boot ID, and request UID.
3. Read the current local host boot ID.
4. Check that the current boot ID is valid, is nonempty, and differs from beforeBootID.
5. Update the boot observation in the same entry only if the Node version still matches. Repeat the identity checks after a conflict.

The peer compares the reported observation with that Accepted entry. It writes BootEnded and the observed boot ID together. The reporter can use an Accepted entry that remains in the Node annotation unless a newer offer replaced it. It can do so without the CR, journal, or probe.

If the entry is Superseded, save the boot observation and keep the Superseded phase. A lost response to a boot report cannot let the agent execute a request. If the current boot ID differs from the request's original ID, the agent cannot execute that request again.

A replacement configuration need not process requests that the old configuration left in the Node annotation.

##### Observe other boot changes

Use a separate BootObserved result for an autonomous attempt or an unaccepted request. Before reading the current host boot ID, read the saved entry that identifies the earlier boot. Check the Node UID and either the autonomous attempt ID or the CR UID. Check that the saved boot ID is trustworthy for this attempt.

For an autonomous attempt, the reporter can read the entry from a complete, valid journal. For an unaccepted request, use the original boot ID in the offer. The original boot ID for a request cannot change.

A confirmed change shows that the boot with the earlier ID ended. For an unaccepted offer, the initial Node boot ID can be stale. The boot can end before the peer sets Offered. OnReboot can use the observation that the original boot ended. The observation leaves the reboot cause and whether the reboot followed the offer unknown.

The peer can use BootObserved for an active eligible CR and leave its execution phase unchanged. Check that the report matches the current CR UID, Node UID, and original boot ID. A watcher report or a change in Node.status.nodeInfo.bootID alone cannot replace the required ordered read on the host.

If the earlier boot ID is unavailable, report EvidenceUnavailable. Elapsed time, a replacement Node UID, and the presence of the CR cannot establish that the boot changed. For an active eligible OnTimeout CR, the peer can still use the original deadline. The peer can process CR deletion without evidence of a boot change.

### 5.9. Requested remediation, escalation, and cleanup

#### Summary

For an eligible NHC request, a peer prepares the target's reboot request and prevents new workloads from scheduling there. The target checks the request before deciding whether to reboot. The peer applies the OutOfService taint after it confirms that the earlier boot ended or the timeout policy permits it. Kubernetes and workload controllers then process workload removal and volume detach.

The peer reports policy success after it confirms the taint and can check remaining workloads for a limited period. This success does not prove that all workloads stopped or moved. When the CR is deleted, the peer removes only the scheduling restrictions that SBR added and no other CR still requires. It can finish CR deletion while workloads or attachments remain.

#### Technical details

To let Kubernetes process Pod deletion and volume detach, SBR adds node.kubernetes.io/out-of-service=nodeshutdown:NoExecute to the Node. Kubernetes, KubeVirt, and CSI controllers act on their own resources. SBR does not delete Pods, VMIs, VolumeAttachments, PVCs, or PVs directly.

##### Select the target and save an offer

Before changing API objects for shared storage or BlockPerNode, the peer checks which configuration applies to the target. Use the target's effective assignment and any mode and configuration UID already saved for that request. The peer cannot create an offer if the configuration is missing or conflicts. Missing CR status alone does not let the peer select shared-storage fencing.

An existing request uses the configuration and policy saved for it. After configuration retirement, peers stop creating offers under that configuration and keep its existing request information.

Peers do not continue an unfinished request under a replacement configuration. The target can still carry out a reboot it already chose.

The CR UID identifies one request. Before adding the finalizer, the peer writes the Node UID, configuration UID, volume mode, and initial boot ID to CR status. These values stay the same for the life of the CR. An updated CR generation, a configuration change, or a later boot cannot give that CR another reboot attempt.

Before adding the first finalizer, the peer creates or checks the Node remediation annotation. Confirm that any required API write succeeded before continuing. If the annotation existed and is now missing, require repair before continuing. Before offering the request, save the same request IDs and mode on the Node.

Reuse SBRRemediationReconciler. Add the sbr.medik8s.io/remediation-owner annotation to the Node. The annotation contains a schema version, a Node UID, and the following fields.

| Field | Contents |
|---|---|
| Execution | The CR namespace, name, and UID, configuration, initial boot ID, phase, and any observed boot ID. Keep earlier phases and observations separate from the CR UID that the peer currently uses for OutOfService. |
| Policy | The UID of the NHC-created request that the peer uses for OutOfService, its policy, original start and deadline, whether the peer confirmed the taint, and whether it can start or retry OutOfService work. |
| Budget | Known autonomous attempts and the ID of each attempt's outage period, or Unknown if the attempt history is unknown. |
| Restrictions | The cordon and OutOfService values before the first SBR restriction, and each CR UID that still requires those restrictions. |
| Revision | The Node's coordinationRevision value. The peer increments it when it updates the Node before removing a CR finalizer. |

Each writer preserves unrelated fields and requires resourceVersion to match on update. After a conflict, read the live API objects again. Check their UIDs and whether the requested change is still allowed. BlockPerNode condition writers also follow the ordering checks in Section 5.5. A merge without those checks is insufficient.

The peer performs the following steps to offer a request:

1. Read the live Node. Check its UID and configuration assignment. Check the request phases, CRs that still require restrictions, and CR UID that currently controls OutOfService in the annotation.
2. Read the live CR. Check that NHC created it as required by Section 5.9. Check its UID, saved Node UID, configuration UID, mode, and initial boot ID. Check the finalizer, deletion timestamp, stop state, and terminal states. Refuse a request in Superseded.
3. Update the Node only if its version still matches. Set the phase to Offered in the annotation. In the same update, save the policy and deadline, add the CR UID to the restrictions list, and cordon the Node. Set any earlier Offered or Accepted request to Superseded in that same update.
4. If that update conflicts, repeat the entire read and update sequence.

The CR deadline starts at the saved Offered timestamp. Save the effective rebootGracePeriod, oosTaintPolicy, Offered timestamp, and calculated deadline in the Node annotation. Follow the clock assumptions in Section 6.9. Acceptance, peer restart, API recovery, journal repair, and configuration changes cannot restart the timer. A CR has no timer until the peer first sets Offered for it.

Only a newer valid offer can replace a request. An offer is newer when the API saves the first Offered update for a different eligible CR UID after the earlier offer. The order in which those Node updates succeed determines which offer is current. A later CR creation time, a candidate CR read, or a failed Node update cannot replace the earlier offer.

When the Node update succeeds, the peer stops starting or retrying OutOfService for the earlier CR. It also stops checking workloads and attachments for that CR. Keep its policy result, any confirmation that the taint was present, and its UID in the restrictions list. Keep this information even if the peer has yet to update CR status.

Keep Superseded on the Node while that CR exists unless its terminal phase has been saved in CR status. Deleting the newer CR cannot make the earlier offer active again.

For example:

1. Request A is Accepted for boot X. The target has not chosen reboot.
2. A peer checks request B and saves its first offer on the Node. In the same update, it sets A to Superseded and saves B's own deadline.
3. The target reads the update. It stops processing A and compares B's original boot ID with the current boot ID.
4. B disappears before the target accepts it. The target cannot resume A. Deletion removes only B's UID from the restrictions list.
5. If the target had already chosen to reboot for A, it could still reboot. A newer offer cannot reverse that decision.

##### Execute and observe a request

| Phase | How it changes |
|---|---|
| Offered | A peer writes the request IDs, initial boot ID, policy deadline, and CR UID in the restrictions list to the Node annotation. The target compares its current local boot ID with the original boot ID in the offer before trying to accept it. |
| Accepted | The target sets Accepted in the Node annotation only if the Node version still matches. The target can reboot for that request in the identified boot. |
| Cancelled | A peer changes an unaccepted offer to Cancelled. A target that sees this phase does not execute the request. |
| Superseded | A peer saves a newer valid offer on the same Node UID and changes the earlier phase to Superseded. A target that sees this before it chooses reboot cannot continue the earlier request. |
| BootEnded | A peer checks the ordered observation from Section 5.8 against the accepted request and its original boot ID. It confirms that the original boot ended. |

The peer and target use the same Node version to set Cancelled and Accepted for an unaccepted offer. The API can accept only one update for that version. At an OnTimeout deadline, the peer sets Cancelled if the offer is still unaccepted. It does this before starting timeout cleanup. If the target has already set Accepted, preserve it. NHC stop or CR deletion can also cause the peer to set Cancelled for an unaccepted offer.

The target makes at most two attempts to set Accepted. This limit is hardcoded. Allow two seconds per API call, at most one second between attempts, and 30 seconds for the full sequence. End earlier if the result is known or both attempts and the reads that check their results finish.

If the target validated the offer and still cannot determine acceptance after the sequence, it can reboot without a new storage failure result. If the target sees cancellation, Superseded, or an ID mismatch before choosing reboot, it cannot choose that reboot. A target can still reboot if it cannot observe that the peer withdrew the offer or if it already chose reboot.

The reboot alone cannot establish Accepted. A journal failure does not block a permitted reboot.

Report the OutOfService policy result separately from the reboot phase. In the original boot, the target can resume a confirmed Accepted attempt after the peer stops OutOfService work or the CR disappears. The target must stop processing that attempt if it sees that a newer offer replaced it before it chooses reboot. A newer offer cannot reverse a chosen reboot or cause the target to feed the watchdog again. A target that does not see the replacement can still reboot for the earlier request if it meets the reboot checks in Section 5.7.

Keep the earlier request IDs and original boot ID to check later boot reports. These saved values cannot let the agent execute the old request or let the peer resume OutOfService for it. They cannot change the restrictions required by the newer CR.

The newer CR keeps its own original boot ID and deadline. After the boot changes, the agent cannot execute requests that name the earlier boot ID. A replacement configuration does not take over unfinished execution.

##### Select when to apply OutOfService

A peer can apply OutOfService only for an active, eligible NHC-created BlockPerNode CR. Before offering the request or applying OOS, check that NHC created the CR. Check the live CR and Node UIDs. Restrict request creation to the supported NHC path so a manually copied owner reference cannot bypass NHC policy.

The peer uses the policy and deadline saved for the current CR in the Node annotation. Both policies first check for a valid report that the original boot ended.

| Policy | When it permits OutOfService |
|---|---|
| OnReboot | The peer checks that a valid BootEnded or BootObserved result shows the current CR's original boot ended. |
| OnTimeout, default | The peer makes the same boot check or waits until rebootGracePeriod expires. The default grace is 60 seconds. After expiry, the peer can apply OutOfService even if it cannot confirm acceptance or the end of the original boot. |

Start rebootGracePeriod from the Offered timestamp that the peer saves in the Node annotation. Use synchronized clocks. Keep the same deadline after peer replacement, acceptance, API loss, or configuration changes.

Storage recovery does not end the policy of an active eligible CR. NHC can request deletion separately. NHC can use a watcher report when deciding whether to create a request. A watcher or autonomous report alone cannot let the peer apply OutOfService or start the timer.

At the timeout, use a Node version check to set any still-unaccepted offer to Cancelled. The peer can still apply OutOfService for the CR under OnTimeout. If the target set Accepted first, preserve that phase.

If the target reports a different current boot ID before acceptance, set Cancelled for the unaccepted offer for the earlier boot. The peer can still use the active CR's original OnTimeout deadline. Expiry alone does not establish the execution phase or show that a boot ended.

Before starting or retrying OOS, read the live Node and CR. Check their UIDs and configuration. Check that the CR UID matches the current policy entry and that the CR meets the NHC and OutOfService policy requirements. Check that the peer can still apply OutOfService for this CR.

Check detect-only, stop, and deletion state. If detect-only starts, do not start or retry OutOfService. Keep boot observations and taint confirmations. Still remove the CR's restrictions entry when required. Repeat the checks after conflicts.

In one Node update, change the taint and save its confirmation in the annotation. Require the Node version to match. The peer can confirm a matching taint that already exists without claiming that SBR added it. Preserve any CR UIDs that already require the taint. Preserve unrelated taints.

If the API response is lost, read the outcome and the current policy CR UID in the live Node annotation before retrying. Keep taint application pending if it failed or if the peer cannot confirm the result. Elapsed time alone does not establish success.

If a boot report and grace expiry both qualify, keep the first valid policy result saved on the Node. A late boot report can update the saved request phase or observation. It cannot start another OutOfService operation.

##### Apply one cleanup observation window

When the peer confirms the taint, save the start and deadline for one window to check workloads and attachments. Save those times with the taint confirmation. The deadline is five minutes after the start. Keep the start time and deadline across retries, peer restarts, and API interruptions.

While the peer can still apply OutOfService, keep failed or unconfirmed taint writes pending. The five-minute window limits only checks after confirmation. It does not change the reboot grace deadline. Limit the time for each API call and the delay before retrying.

Use live API reads during the window:

- Check Pods assigned to the node name and check that the live Node UID matches the UID saved for the CR.
- Check VMIs assigned to the Node or linked to relevant virt-launcher Pods.
- Check VolumeAttachments for the Node independently of Pods.
- Count relevant terminating objects as present.
- Treat permission errors, failed lists, incomplete pages, and failed discovery as unknown.
- Treat the optional KubeVirt API as not applicable only if discovery confirms its absence.

Exclude only SBR agent and reporter Pods whose identities the peer can check. Check their namespace, Node, Pod UID, DaemonSet owner UID, and configuration. Exclude a probe attachment only if the links among its PV, PVC, and Pod identify the probe. A label alone is insufficient.

Give the SBR agent and reporter Pods a permanent OutOfService NoExecute toleration. Do not set tolerationSeconds. Other deletion or detach paths can still affect them.

Report Completed only if fresh complete lists show no relevant resources after the peer confirms the taint. At the deadline, report Incomplete if complete final lists show remaining objects. Report Unknown if results are partial or unavailable.

The lists can represent different points in time. Before saving the result, recheck the Node UID and the CR UID that controls the operation. Check the operation state in the Node annotation to determine whether the peer can still publish the result for that CR.

Before publishing the terminal result, update the Node annotation to prevent more OutOfService updates and workload and attachment checks. Stop checks also when NHC stops the request, someone deletes the CR, or a newer offer replaces the request. If the peer stops checks and loses the result before reporting it, report Unknown with a reason when it resumes. Do not start a new window.

An earlier API write can succeed before the Node update that stops further taint updates and scans. In that case, read its result and keep any CR UID it added to the restrictions list. If the Node update that stops further work succeeds first, the earlier write uses an outdated version and must fail.

##### Report policy success and process deletion

For an active CR, confirming the taint completes the selected remediation policy. Use the existing success conditions `FencingSucceeded=True`, `Succeeded=True`, `Ready=True`, `Processing=False`, and `FencingInProgress=False`. These conditions report the CR's policy result. The target may still be running and its health is a separate check.

Use a reason that distinguishes a verified boot change from `RebootGraceExpired`. These conditions do not confirm acceptance or show that the original boot ended. Report success without waiting for confirmation that the boot ended or workloads moved. Continue optional checks in the original window unless NHC stops the request, someone deletes the CR, or a newer offer replaces it.

If the peer cannot confirm the taint, report `outOfServiceApplied=false` and the API failure reason. Keep the taint operation pending and retry while the peer can apply OutOfService for the CR. Reboot grace expiry alone does not establish success or terminal failure. If NHC stops the request or someone deletes the CR, stop trying to apply OutOfService for it. The five-minute window cannot expire before the peer confirms the taint and starts that window.

The current [shared-storage success handler](../../internal/controller/storagebasedremediation_controller.go) sets success conditions, and its reconciler can return early. Keep that behavior for existing modes. In BlockPerNode, keep watching the Node after success. This lets the peer process late boot reports and remove CR entries from the restrictions list when required.

When NHC stops a request or someone deletes its CR, stop starting or retrying OutOfService work for it. Stop checking workloads and attachments. Try to set Cancelled for any unaccepted offer with the Node version check in Section 5.9. Keep accepted execution and the earlier request IDs and boot ID for later reports.

If the Node update that stops taint work succeeds first, do not apply OutOfService for that CR. Do not replace a stopped result with normal success. If the taint update succeeds first, keep that CR UID in the restrictions list until the peer removes it during release.

When the CR is deleted, remove only its UID from the Node's restrictions list. If the original Node UID still exists, confirm the required Node update in Section 5.9 before removing the finalizer. If a live read shows that UID is gone, remove the finalizer without changing a replacement Node. If the original Node still exists and its remediation annotation is corrupt, require repair.

The peer can remove the finalizer without acceptance, BootEnded, workload removal, or a successful cleanup result. An accepted attempt can remain unfinished after CR deletion. If the final UID leaves the restrictions list, the target can resume service before a delayed reboot. Late boot reports cannot let the peer apply OutOfService for a CR after its policy stops or a newer offer replaces it.

NHC requests deletion after health recovery, subject to its healthyDelay policy. It does not use FencingSucceeded to trigger deletion. It uses Succeeded during escalation. It can advance on its own timeout while the Node remains unhealthy. When no shared coordination exists, do not configure another provider to act concurrently. See [NHC escalation](https://github.com/medik8s/node-healthcheck-operator/blob/dc02045b23a8a0d091116a4f4d679d26e9ef3004/internal/controller/nodehealthcheck_controller.go) and [NHC deletion](https://github.com/medik8s/node-healthcheck-operator/blob/dc02045b23a8a0d091116a4f4d679d26e9ef3004/internal/controller/resources/manager.go).

##### Release a CR's restrictions

Add the CR UID to the Node annotation's restrictions list when that CR first requires the restrictions. When adding the first UID, save the cordon and OutOfService values that existed before SBR changed them. Preserve earlier restrictions and taints with unrelated keys or effects. Check the current value before deleting a taint that SBR added. The saved value cannot show whether an administrator subsequently set the same cordon. Administrators must coordinate changes to that field.

Keep a CR UID in the restrictions list after timeout success, ordinary success, or NHC stop if the CR still requires the restrictions. Finishing the checks for workloads and attachments does not remove OutOfService. On CR deletion, the peer removes only that CR UID.

If the list becomes empty, remove only restrictions that SBR added. Clear the saved original values in the same update. Keep the restrictions if the list still contains a different CR UID.

Before removing the finalizer, perform these steps:

1. Read the live CR whose deletion has started. Check its UID.
2. Read the live Node. Compare its UID with the UID saved for the CR. If the original Node still exists, prevent new OutOfService attempts for this CR. Cancel its unaccepted offer. Preserve its execution state and original boot ID.
3. Require the Node version to match. Remove this CR UID from the restrictions list and remove SBR-added restrictions only if no other CR UIDs remain. Increment coordinationRevision in the same update, even when the list already lacks this CR UID.
4. Confirm that the Node update succeeded. Remove the finalizer only if the CR UID and resourceVersion still match.

The peer can remove the finalizer when workload checks are incomplete or boot evidence is missing. If a live read confirms that the original Node is absent or its name now has a different UID, remove the finalizer using the CR UID and version checks. Do not change a replacement Node. Node disappearance does not prove fencing. An API error does not establish absence.

If the original Node still exists and the expected annotation is missing or corrupt, report the need for repair. Do not infer which CRs still need restrictions or remove the finalizer before repair. Do not wait for the Node to become schedulable or for the node controller to remove its unschedulable taint.

Incrementing coordinationRevision changes the Node version. An offer or taint update that uses the earlier version cannot then succeed. If the earlier update succeeds first, read its result and check again which restrictions the peer must release.

Repeat the full sequence after a conflict or unknown response. A missing CR UID in the restrictions list alone does not confirm the Node update. Never use an old CR to change restrictions on a Node with a different UID.

Peers watch Nodes and CRs. After a CR disappears, peers check its saved entries in the Node annotation. Forced finalizer removal skips the Node update required before removal. In that case, stop starting or retrying OutOfService for that CR and remove only its UID from the restrictions list. If no other CR UIDs remain, remove the restrictions that SBR added.

A replacement CR with the same name has a different UID. Deleting a Node also deletes its annotation. Use only information that survives in a trusted source. A missing or invalid annotation cannot let a peer change a replacement Node.

Limit the size of the Node annotation. Keep CR UIDs that still require restrictions, the autonomous attempts needed to check the limit, and terminal phases that prevent another offer. A matching Accepted entry that remains on the Node can still let the agent continue that request if a newer offer has not replaced it. Entries kept only to check later reports cannot do that.

If the earlier request or boot IDs were removed and no trusted copy remains, report EvidenceUnavailable. A late report cannot let the agent execute a request or the peer start more OutOfService work. It cannot delay CR deletion. Report and refuse new offers if the annotation cannot keep the current request entry and the CR UIDs that still require restrictions.

##### Publish autonomous history and evidence

A host can reboot without knowing whether NHC created a CR. When API access returns, the agent or reporter can publish an autonomous attempt that it can still read. Include the attempt ID, Node UID, configuration UID, original boot ID, and any known information about the outage and whether the attempt counted toward its limit.

The report can help establish the earlier boot ended and whether the agent attempted a reboot during that outage. It cannot add a CR UID to the restrictions list or let the peer apply OOS.

For a current eligible CR, the peer can use an autonomous boot report if it shows that the boot named in the CR ended on the same Node UID. Check the Node UID and original boot ID. Save the observation for that CR and keep its original Offered deadline. Keep BootObserved separate from the acceptance phase.

Use the accepted-request checks in Section 5.8 if the CR is already Accepted. After an autonomous reboot, a valid request can cause another reboot in the new boot.

If no eligible CR remains, keep only the saved attempt, its outage information, and the boot observation. A delayed report cannot create or reactivate a CR, publish an old unhealthy result, start or restart a timer, or change restrictions. A watcher or local agent can later publish fresh storage results using Section 5.5. NHC decides whether those results qualify for a new request.

##### Accepted limitations

A peer can apply OutOfService while Ready=True and while workloads still run. A verified earlier boot change or grace expiry leaves it unknown whether the Node remains shut down. Kubernetes expects an administrator to check that the Node is shut down before using this taint.

Workloads can restart before the peer adds the taint. Force deletion and detach can overlap workload writes. SBR accepts these risks to allow workload removal. [Kubernetes shutdown guidance](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#non-graceful-node-shutdown-handling)

Pods can remain after the window if they tolerate the taint, have finalizers, or depend on an unavailable controller. Terminating Pods and pending detach can also remain. Removing OutOfService cannot cancel operations that other controllers already accepted. The peer can remove the restrictions before those operations finish. Whether replacement workloads started can still be unknown.

The optional boot gate uses its own timer to let kubelet start. It can let kubelet start before API access returns, workloads disappear, or volumes detach. CR status, workload checks, node health, and API access do not control when the gate lets kubelet start.

### 5.10. NHC policy and reboot limits

#### Summary

NHC watches the storage health condition and decides whether to create a remediation request. Its health thresholds and pause settings can prevent new requests during a broad failure. These controls do not stop autonomous reboots or reverse a reboot that an agent already chose. Different valid requests can cause separate reboots.

After health recovery, NHC can request deletion of the CR according to its configured delay. The peer then removes that CR's scheduling restrictions when no other CR needs them. For planned maintenance, use detect-only mode to stop agents from accepting new requests or choosing new autonomous reboots. Pausing NHC alone does not prevent autonomous reboots.

#### Technical details

Configure NHC to watch only `SBRStorageUnhealthy=True`. Use one SBR remediation template. The local agent reports unhealthy storage after its failure count reaches the threshold in Section 5.6. A watcher can report an expired heartbeat when it meets the checks in Section 5.5. It can report expiry even if the agent cannot publish the condition. A failed probe read alone cannot establish expiry.

NHC can create a request if any configured unhealthy condition qualifies. Adding a `Ready` condition could let NHC create a request during API or network loss while storage stays healthy.

NHC uses the condition duration, minHealthy, pause setting, and optional stormCooldownDuration when it decides whether to create a CR. A broad outage can prevent NHC from creating a CR. A peer can apply OOS only for a current eligible NHC-created CR. It does not recalculate the NHC thresholds before each taint update.

If the watcher cannot determine expiry or recovery, leave a valid True condition unchanged. NHC can use that condition to create a CR until a new report that meets Section 5.5 clears it. See [the reviewed NHC API](https://github.com/medik8s/node-healthcheck-operator/blob/dc02045b23a8a0d091116a4f4d679d26e9ef3004/api/v1alpha1/nodehealthcheck_types.go).

NHC uses one Lease per node to control request creation. Two NHC configurations that select the same node use that same Lease. A pause or a block on new requests cannot reverse a reboot that the agent already chose. NHC does not limit autonomous reboots across the cluster. The local agent checks the conditions in Sections 5.6 and 5.7. If trusted information shows an autonomous attempt in the current outage, the agent cannot make another.

The following example uses worker nodes. Select thresholds and durations for the deployment.

```yaml
apiVersion: remediation.medik8s.io/v1alpha1
kind: NodeHealthCheck
metadata:
  name: sbr-storage
spec:
  selector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
      kubernetes.io/os: linux
  minHealthy: "51%"          # size against the storage failure domain
  stormCooldownDuration: 10m # inactive when unset
  healthyDelay: 2m           # deletion delay
  unhealthyConditions:
    - type: SBRStorageUnhealthy
      status: "True"
      duration: 30s
  remediationTemplate:
    apiVersion: storage-based-remediation.medik8s.io/v1alpha1
    kind: StorageBasedRemediationTemplate
    name: sbr-block-per-node
    namespace: sbr-system
```

The local agent or watcher can report healthy storage after a fresh observation meets the checks in Section 5.5. The agent continues to measure how long fresh probes show healthy storage during API loss. When the recovery period completes, the agent can make another autonomous attempt if the required failure checks later qualify.

API loss can prevent the agent from reporting recovery. Use cleanup status to show whether checks found remaining workloads or attachments.

NHC requests CR deletion after the node no longer meets the unhealthy condition for the configured `healthyDelay`. The delay can end before workloads disappear or volumes detach. When NHC stops a request or deletion starts, peers stop starting or retrying OutOfService for that CR.

During deletion, peers remove its UID from the restrictions list under Section 5.9. SBR can remove its taint while workloads or attachments remain. The boot gate in Section 5.12 lets kubelet start on its own deadline.

A zero healthyDelay lets NHC request immediate deletion. A negative value disables NHC's automatic deletion under the reviewed contract. In that configuration, success alone does not cause NHC to delete the CR or cause the peer to remove its restrictions.

The peer saves the `Offered` time in the Node annotation. That time starts the CR's fixed `rebootGracePeriod`, which defaults to 60 seconds. With either policy, first check for a valid report that the original boot ended. With OnTimeout, the peer can also apply OOS at the deadline whether or not the target accepted the request. Later acceptance or peer checks cannot restart the period.

The peer reports policy success when it confirms the taint. It can do so before workloads move. With OnReboot, time alone cannot let the peer apply the taint.

Use the deadline for the CR that currently controls OutOfService. See Section 5.9 for the rules to select the CR and remove restrictions.

The local agent can reboot autonomously before NHC creates a CR or when NHC creates none. If a current eligible CR exists, the peer can use the report of that attempt if the Node UID and original boot ID match. The peer cannot apply OOS without that CR. A later valid CR can request another reboot. Requested reboots do not count toward the autonomous limit. See Sections 5.6 and 5.7 for when the agent can make or count an autonomous attempt.

Use one remediation template for this policy. Other providers and administrators can act independently of SBR. The SBR owner annotation does not track or control actions by Self Node Remediation, Fence Agents Remediation, Machine Deletion Remediation, MachineHealthCheck, or administrators. Report overlapping actions that SBR can detect. The administrator must prevent concurrent remediation of the same node or failure domain in BlockPerNode mode.

For planned maintenance, enable detect-only mode before disrupting storage or API access. Check that the local agent uses that mode. Pausing NHC, cordoning the Node, or applying a maintenance taint does not stop the agent from choosing an autonomous reboot. Detect-only mode cannot reverse a reboot that the agent already chose.

### 5.11. Startup, shutdown, and observability

#### Summary

The agent starts its peer controller before it initializes local storage checks. Once running, that controller can process other nodes' requests even when local storage or journal operations fail. However, an agent Pod that cannot start because its probe volume cannot attach provides no peer controller.

Only one local agent can control the host watchdog and write the journal at a time. During a planned stop, it disarms the watchdog unless it has already chosen a reboot. A separate reporter publishes boot information without using the probe volume. Status, events, and metrics show which components are available and which operations need attention.

#### Technical details

The agent process initializes the API clients and schemes, then starts the BlockPerNode peer manager. Start it before storage preflight or local agent initialization. The current `main()` performs storage preflight before constructing the agent. `NewSBRAgentWithWatchdog()` initializes devices and NodeMap before its controller manager. Add the earlier BlockPerNode startup in [the agent entry point](../../cmd/sbr-agent/main.go). Keep the existing shared-storage startup order.

The agent process stops the peer manager during process shutdown or configuration removal as Section 5.4 specifies. The current `StartWithContext()` stops the manager when the agent context ends. A BlockPerNode probe or journal worker must report its error without cancelling the peer manager through a shared context or error group. The peer manager continues processing requests through the API.

The peer processes requests for other nodes even if the local probe, journal, or watchdog is unavailable.

Before the local agent starts monitoring, acquire the host lock and check the configuration and local boot ID. Start watchdog monitoring after its startup checks pass. Start probes after their startup checks pass. Neither task waits for journal operations.

In detect-only mode, skip opening and arming the watchdog for ordinary monitoring. Keep a confirmed accepted attempt for this boot unless the agent observes that a newer offer replaced it. The agent can still carry out a reboot it already chose.

Use the time limits in Section 5.7 to read saved information. If the journal fails, the agent can still perform otherwise permitted actions. It may be unable to tell whether it made an autonomous attempt during the outage.

Provisioning delay alone does not establish a storage failure. The agent cannot choose a new reboot if it cannot determine the effective configuration from trusted data. Starting the agent when it has neither API access nor usable local state remains deferred, regardless of storage health.

When the process restarts or is replaced, clear temporary failure counts and timers. Start a new probe and new API reachability checks. A process or host restart cannot establish that storage is healthy.

Keep the current storage condition until fresh observations meet the checks in Section 5.5. Restart the storage recovery timer if the agent cannot establish that fresh storage checks continued. API failures do not restart that timer.

Allow one unfinished probe operation per device in each process. A new process starts a fresh probe after its startup checks pass. Keep only one unfinished journal operation per host, even across process replacement, as Section 5.7 specifies.

Use ordinary shutdown only if the agent has not chosen reboot for the current boot. Disarm the watchdog before stopping its monitoring loop. Use the existing shutdown path where it meets this requirement. If possible, write a clean shutdown marker for diagnostics. The agent can still carry out a reboot it already chose after configuration retirement.

Count selected nodes, ready peers, ready local agents, and ready reporters in metrics. Count targets that have a watcher able to read their probe, ready probes, and armed watchdogs. Use configuration status conditions to report when required components cannot serve their target nodes. Report controller readiness separately from probe and watchdog readiness. The peer can process another node's requests before its Pod reports Ready.

The peer-ready count includes the target itself. Check Section 5.2 separately to determine whether another eligible peer exists for that target.

Use conditions and events to report when an eligible peer is unavailable. Keep per-node details out of any configuration list that has no size limit. Use `ConcurrentWriteable` only for shared-storage readiness.

The existing `LeadershipAcquired` condition and `HasLeadership()` helper are unused outside tests in the reviewed source and are not part of BlockPerNode.

Use status or events to report mode, probe identity, and reason values from a fixed list. Include `WaitingForExecution`, `Accepted`, `Superseded`, `WaitingForBootEvidence`, `EvidenceUnavailable`, `CleanupIncomplete`, `CleanupUnknown`, `LocalAgentConflict`, `HistoryUnknown`, and `BudgetExhausted`. Use metrics to report time in each phase, reboot attempts, coverage, conflicting writes, removal of CR UIDs from the restrictions list, and observation results. Never use tokens or UIDs as metric labels.

Report reboot history and attempts by multiple writers to update the same API object, journal, or probe. Keep diagnostic timestamps while the API is unavailable. When access returns, publish current conditions. Raise an alert while a finalizer keeps a CR present and that CR reduces NHC's healthy count.

Keep separate service accounts for the operator, the agent that runs both the peer controller and local monitoring, the reporter, and the watchers. The peer controller and local agent share one process identity and therefore share its API permissions. Kubernetes RBAC cannot separate those roles inside that process.

Update [agent RBAC markers](../../cmd/sbr-agent/main.go) and the generated [agent role](../../config/rbac/sbr_agent_generated_role.yaml) to give peers access to the following resources.

| Resource | Required peer access | Purpose |
|---|---|---|
| SBR remediation CRs, status, and finalizers | Read and conditional update or patch | Save request IDs, update status, and manage finalizers across namespaces. NHC creates and deletes requests. |
| SBR config CRs | Read | Check the configuration assigned to the target and the configuration UID saved for the request. |
| Nodes and Node status | Read and conditional update or patch | Update request entries in the Node annotation, cordon the Node, and update its taints. Report storage health on the local Node. |
| Pods | Read | List Pods across namespaces and check which SBR Pods the peer must exclude. |
| PVCs, PVs, and VolumeAttachments | Read | Find attachments for the target Node, check which attachments belong to the probe, and check whether relevant attachments remain. |
| DaemonSets | Read | Check the Pod and DaemonSet owner UIDs before excluding SBR Pods. |
| VMIs | Read | Check whether relevant KubeVirt workloads remain. |

Keep permission in the agent role to update the main Node resource. Add the attachment and workload read permissions. This path needs no Pod-delete permission. Other controllers delete workload resources. The operator needs no workload deletion permissions.

Register `storage/v1` and `apps/v1` in the schemes for the agent API clients. Register KubeVirt types if using a typed VMI client. The current `getScheme()` registers only core and SBR types. Check `initializeKubernetesClients()` and manager setup. Read the live API to check which request controls OutOfService, which CR UIDs still require restrictions, and which workloads or attachments remain.

Keep the request watch in `SetupWithManager()`. Watch request phases and the restrictions list in the Node annotation. Check both on initial synchronization even when the CR is absent. Use the stored namespace, name, and UID to find the request that needs a follow-up check. Limit delays when retrying pending Offered requests, boot checks, workload and attachment checks, or removal of a CR UID from the restrictions list. Start processing an NHC stop or CR deletion without waiting for an unfinished scan.

Cached clients require list and watch permissions for their informers. Do not restrict workload scans or remediation CR watches to the configuration namespace.

Only the local agent accesses the watchdog and writes the journal. The reporter can read host state and update observations in the Node annotation. A shared DaemonSet service account cannot limit each Pod to its current Node or restrict individual status fields. Use a separate resource for observations if RBAC must limit which fields the reporter can change.

The reporter needs Node read and conditional update access so it can publish observations after forced CR deletion. The reporter changes only matching boot observations and observations for saved autonomous attempts. These writes alone cannot let a peer apply OOS. The peer can apply OOS only for an active eligible NHC-created CR.

RBAC cannot restrict a Node update to one annotation field. The design trusts the agent, reporter, and watcher code to use only their permitted fields. Preserve accepted request phases and known autonomous attempts during unrelated Node patches.

Watchers need `get` on Nodes and conditional `patch` or `update` on `nodes/status`. In the status update, write the condition and the annotation that orders condition writes. To update observations separately, give watchers the matching Node permission.

RBAC cannot restrict these permissions to one condition or annotation. Watchers need no access to write remediation CRs, delete workloads, or control the watchdog. The operator assigns watchers.

Update manifests and OpenShift Security Context Constraints (SCC) bindings for the `/var/lib/medik8s/state` host mount. Require directory mode `0700`, file mode `0600`, and the expected file and directory owner. Open files without following symbolic links. Prevent symbolic link and path replacement. Keep credentials out of the journal.

Host root can forge the local state if compromised. The design trusts host root.

##### Deferred watchdog details

The local agent must hold the host lock before accessing the watchdog. When the agent feeds the watchdog, probe or journal I/O must not delay the feeds. Closing the device does not establish that the watchdog is disarmed.

Separate watchdog work covers driver checks, shutdown code, and tests on real hosts. Preserve the shutdown rules and any reboot decision the agent already made. This proposal does not define a new watchdog API.

Document what to do when no peer is available, the reporter or probe is missing, or the agent cannot determine whether it attempted an autonomous reboot. Cover inconsistent CR UIDs in the restrictions list, unfinished workload checks, and pending CSI detach. Cover host lock or watchdog failure, replacement Node UIDs, CR UIDs left in the restrictions list, configuration deletion, and downgrade. Forced finalizer removal or a missing API object, including a deleted Node, cannot establish fencing.

### 5.12. Optional pre-kubelet delay

#### Summary

An optional host service can delay kubelet startup after a recognized autonomous reboot. Set `spec.bootUnitEnabled: true` and a positive `spec.bootUnitTimeout` in the SBR config CR to enable it. The default is disabled. When the configured period ends, it lets kubelet start even if the API is unavailable or workload removal is incomplete.

The delay is independent of when a peer applies or removes the OutOfService taint. It does not guarantee that old workloads have stopped or their volumes have detached. Separate work will define installation and how the operator applies these settings to each selected host.

#### Technical details

A standalone host service reads the effective `bootUnitEnabled` and `bootUnitTimeout` values saved on its host. Apply validated settings before a reboot. The service must not fetch the CR or wait for the API before letting kubelet start. These local settings configure only the boot unit. They do not provide offline configuration or reboot permission for the SBR agent.

When enabled for a recognized autonomous reboot, the service measures the timeout from the start of its delay in that boot. Include local checks in that time limit. Use a local elapsed-time clock and retain the original deadline across service restarts within the same boot. Settings received during a delay apply to later eligible boots and cannot extend the active deadline.

At its deadline, the service lets kubelet start even if the API is unavailable. It does not check the OutOfService taint, Node health, or any message from a reporter or peer. A disabled unit adds no delay. Missing or invalid local settings must not cause an indefinite wait or an invented timeout. In that case, report the local error and allow kubelet to start.

The recognition rules are deferred work, but this rule is not. If the service finds a stored reboot decision and cannot establish whether this boot follows that decision, it applies the configured delay and reports the local ambiguity. The service skips the delay only when no decision is present or the decision cannot be read at all. A missing or invalid configuration still allows kubelet to start, as above.

The peer uses the CR's saved reboot deadline independently of the host service's deadline. Under `OnTimeout`, the peer can add the OutOfService taint before the reset, during the delay, or after the delay ends. Under `OnReboot`, the target agent or reporter normally publishes boot evidence after kubelet starts.

A workload already assigned to the node can restart before the peer adds the taint or before its volumes detach. Local state that cannot be read at all can cause the service to miss a delay. An expired delay does not show that workloads stopped or that volumes detached. The policy accepts missed delays.

Separate work will define installation, host setting delivery, reboot recognition, and platform-specific tests. Disabling or removing the configuration must remove future SBR boot delays when host reconciliation succeeds. Preserve normal kubelet startup when removing the service. API validation alone does not establish that the host installed or enabled it. BlockPerNode can run with the service disabled.

## 6. Open Questions

### 6.1. Residual limitation: every evidence path unavailable

The separate reporter reads the host boot ID without attaching the per-node probe. If no reporting path supplies valid evidence, the peer reports `EvidenceUnavailable`. The peer reports Accepted only after it confirms that phase in the Node annotation. It requires the ordered boot observation in Section 5.8 to confirm that the accepted boot ended.

Under OnTimeout, the peer can add the OutOfService taint after the saved reboot deadline. Confirmed taint application completes that policy. Under OnReboot, the peer must verify boot evidence before it can add the taint. NHC stop or CR deletion requires the peer to close further taint updates and scans for either policy. A peer can remove the CR finalizer without BootEnded after it confirms the required Node update or verifies that the original Node UID is absent. API failure can delay both checks.

### 6.2. Accepted risks: OutOfService and scheduling restoration

Under OnTimeout, the peer can add the OutOfService taint while the target runs in its original boot. Under OnReboot, the peer verifies that the identified boot ended. A rebooted host can restart workloads before the peer adds the taint. Force deletion or detach can overlap workload writes and cause duplicate writers or data corruption.

The optional boot gate delays kubelet until its local deadline. It is disabled by default. It lets kubelet start when the deadline expires, even if it cannot reach the API or workload removal remains unconfirmed. The peer uses its own deadline to decide when to add the OutOfService taint. If journal data is lost, the service can miss a delay it would otherwise apply. The policy accepts these limits.

A changed boot ID can show that a specific boot ended. The cleanup deadline ends the peer's workload and attachment checks for the CR. `FencingSucceeded=True` means the peer confirmed the OutOfService taint for the selected policy. Workloads can still run or restart after any of these events.

Pods can remain Terminating after the peer adds the OutOfService taint to a Ready Node. Workloads with matching tolerations, static workloads, finalizers, and storage controllers can prevent removal or detach. SBR leaves workload deletion to the Kubernetes and KubeVirt controllers, and volume detach to the storage controllers.

During CR deletion, the peer removes that CR UID from the Node annotation's restrictions list. When the list becomes empty, the peer removes only the cordon and OutOfService taint that SBR added. Pods, VMIs, or attachments can remain, or the peer can lack a complete list. With a positive NHC healthyDelay, NHC can still delete the CR before controllers finish removal and detach. Controller actions can continue after the peer removes the taint. The policy accepts this risk.

Document these risks for users. Check these limits in the tests for each release.

### 6.3. Accepted limitation: cleanup during API loss

An agent can reboot without API access. A peer must reach the API and find a current eligible NHC-created CR before it can add the OutOfService taint. A failed watcher read does not set the target's storage condition to True. NHC might not create a CR for that reboot. Without an eligible CR, the agent or reporter can publish surviving attempt data, boot observations, and outage counts only. Kubernetes can leave workloads assigned to the original node after the reboot.

When the API returns, peers use the reboot deadline saved with `Offered`. Before adding the taint, the peer reads the live CR and Node and repeats the checks in Section 5.9. A late report can update boot evidence after stop, deletion, or supersession. It cannot permit another taint update for that CR. Peers can still remove a deleting CR's restrictions entry and remove SBR-added restrictions when the list becomes empty.

### 6.4. Limits on autonomous and requested reboots

The agent can choose one autonomous reboot per unresolved outage when the journal or matching API data shows a known attempt count. If those data show that it already chose an attempt, the agent waits for sustained successful storage probes before it can choose another. The default recovery period is five minutes. The agent counts storage recovery even if the API remains unavailable. If the count is Unknown, the agent can reboot again after fresh failure checks and a new total-outage grace period. Saved data for an attempt in the same outage still prevents another reboot for that outage.

Distinct valid CR UIDs can each request a reboot. A requested attempt does not change the count of autonomous attempts for the outage. A later CR can request another reboot after an autonomous reboot. The design accepts that result. If the agent loses the data that show whether it sent the software reboot command, it can send that command again in the same boot. The agent cannot execute a CR after the CR's saved initial boot ends.

This design sets no absolute limit on autonomous resets. A host can repeat them across boots only when no attempt information survives each boot, and the design accepts that possibility because no durable local record can be trusted after repeated loss. In that case, the agent reports `HistoryUnknown` and reports every autonomous reboot decision. Operators must alert on repeated autonomous reboots of the same Node and use platform-level boot limits where the platform provides them. NHC policies limit requested reboots, not these autonomous reboots.

Each local agent decides whether its host can reboot autonomously. Multiple nodes can reboot together or reboot again when their history is unavailable. NHC applies its limits when it decides whether to create a CR. Peers add OutOfService only for current eligible NHC-created CRs. If NHC creates no CRs, peers cannot add OutOfService for the outage. The design accepts these availability risks.

### 6.5. Deferred multiple StorageClasses

Scenario 13 needs a configuration that specifies which StorageClass each node group uses. The configuration controller must generate the corresponding probe claim templates and report their identities. Validation must reject overlapping node assignments. It must also reject configurations that give two agents control of the same watchdog. NHC may need separate thresholds for groups that depend on different storage backends.

Scenario 14 needs several probes per node and a rule for setting `SBRStorageUnhealthy`. One possible rule sets it to True when any required backend fails. Another requires all required backends to fail. The policy must identify the workload storage that each probe tests.

Both extensions need status for each probe and rules for attaching each probe volume. They also need checks that identify which probe attachments the peer excludes from workload observations. Add tests when the API fields and controller behavior for either extension are defined.

### 6.6. Unknown history and missing Node annotations

Recover only request IDs, attempt information, and boot observations that match the request or outage. Use the host journal or matching entries in the Node annotation. Report unavailable information as Unknown.

If the journal is corrupt, attempt to recreate it under the host lock. If recreation fails, keep decisions and observations in memory without reporting a durable write. A failed update can leave an older valid journal available. Keep an unsupported journal format unchanged.

Deleting a remediation CR leaves the `sbr.medik8s.io/remediation-owner` annotation on the Node. Deleting the Node removes the annotation. A different UID for the same Node name does not show that the old host stopped. Never run the old request on the replacement Node or change its cordon or taints using the old UIDs.

If a live API read confirms that the original Node UID is gone, the peer can remove the CR finalizer under Section 5.9. The peer leaves a replacement Node unchanged. If the original Node still exists but its coordination annotation is missing or corrupt, repair it before removing the CR's restrictions entry or finalizer.

If the journal and the relevant entries in the API are lost, SBR cannot recover the old request UIDs, their saved Node UIDs and initial boot IDs, or boot observations. An agent with a trusted effective configuration can run fresh storage and API checks for autonomous reboot. Journal loss alone never triggers reboot. A replacement process requires a validated API offer or valid retained acceptance to choose a requested reboot. Offline startup without API access or usable local state remains deferred. This design adds no separate saved copy of the configuration or administrative approval API.

### 6.7. Limits on local processes and scheduling restrictions

The host `flock` permits only one cooperating local agent to hold the lock. A process that ignores the lock can still act. The design assumes that host root is trusted because it can change the journal and other host state.

When the first CR enters the restrictions list, the peer saves the current cordon and OutOfService taint state. These values let the peer preserve restrictions that existed before SBR acted. If another actor sets the same cordon after SBR set it, the peer cannot distinguish the two changes. Document how administrators must coordinate updates to the cordon.

Keep each CR UID in the restrictions list while the CR still requires the cordon or OutOfService taint. Keep a terminal phase such as `Superseded` so peers cannot offer the same CR again. Separate work will define how long to keep history and how to limit the annotation size. On CR deletion, confirm the required Node update before removing the finalizer even if boot evidence is missing. The peer can omit that update when a live API read proves that the original Node UID is gone, as Section 5.9 specifies.

### 6.8. Accepted limitation: peer availability

Each target needs at least one controller in another running agent Pod that can reach the API and meets Section 5.2 requirements. The controller can process that target's requests while its own probe is unhealthy. Run peer API operations independently of the local storage calls.

If CSI cannot attach a probe volume, the replacement agent Pod and its peer controller can remain Pending. A shared outage or rollout can therefore leave a target without a peer that meets Section 5.2 requirements. A cluster with one selected node has no other agent Pod to process that node's requests. A later release could run that controller in a Pod without a probe volume. This proposal keeps it in the agent Pod.

Without an available peer, no component writes `Offered`, `Cancelled`, or `BootEnded` to the Node annotation or removes the CR finalizer. The target agent still handles local execution. The agent or reporter can still publish boot observations under Section 5.8. When a peer becomes available, it reads the saved request phase and uses the existing cleanup deadline.

### 6.9. Deferred work and operating assumptions

Assume synchronized clocks on the target, peers, watchers, and NHC. Use the saved timestamps to calculate deadlines that these components share. Do not adjust for clock differences or for the delay between generating a timestamp and committing its API update. For each request, calculate the reboot deadline from its saved `Offered` time. Use a monotonic clock to measure local elapsed intervals.

Assume that users select a probe StorageClass that represents the storage their VMs use. The operator groups nodes using topology labels. It places each watcher on a different node that can mount all assigned volumes. If no suitable node outside the group exists, report that the watcher cannot read those probes. A watcher cannot monitor its own node.

The following work remains deferred:

- Define the bytes and fields in a heartbeat. Section 5.5 specifies the identities and probe sequences that readers need to order condition updates.
- Define which request and attempt entries to keep, when to remove them, and how to limit the Node annotation size.
- Define how an agent starts when it cannot reach the API and has no usable local state.
- Define the journal file format, library interface, and watchdog implementation in the separate journal work.
- Define installation, host setting delivery, and reboot recognition for the optional service. Section 5.4 defines its configuration fields.
- Test the supported storage drivers, platforms, performance, and cluster sizes separately.

When peers check Pods, VMIs, and attachments, they report which old resources remain. A replacement VM or Pod can still fail to start after those resources disappear.

### 6.10. Accepted limitations of the optional boot gate

A cordon stops new scheduling on the Node. Workloads already assigned to it can start. The optional host service lets kubelet start at its local deadline even if the CR or taint remains, the Node is unhealthy, or no peer sends a message.

API loss can delay the taint update. A missing or unreadable journal, or a short delay, can let workloads restart before or after the peer adds the taint. A long delay postpones kubelet startup. At expiry, the service can let kubelet start while old workloads or attachments remain. Users accept these limits when they enable the service.

### 6.11. Unsupported configuration replacement during unfinished execution

A replacement configuration cannot use an old request to choose a reboot or apply OutOfService. Preserve the available request phase, boot observations, and attempt history. Use the original request's deletion procedure to remove its restrictions entry. A reboot already chosen by the old agent can still occur. Replacing the configuration during this interval can lose execution data or delay restriction removal. Administrators must resolve existing requests before replacing the configuration.

## 7. Upgrade / Downgrade Concerns

An upgrade still treats omitted volume mode as `Filesystem`. With `ioTimeout` omitted, use two seconds for existing modes and fifteen seconds for BlockPerNode. Existing modes keep their failure counter and retry rules.

Only BlockPerNode accepts the new OutOfService, total-outage, autonomous recovery, boot-unit, and watcher grouping fields. If omitted, use OnTimeout and a 60-second reboot grace. The target agent makes at most two updates to accept an offer. Group targets by region and zone with at most 16 nodes per group by default.

Omitting `bootUnitEnabled` preserves normal kubelet startup. Enabling it requires an explicit positive `bootUnitTimeout`. Before removing support for these fields, disable the service on affected hosts and preserve normal kubelet startup. An unreachable host can retain its last applied settings until host reconciliation succeeds.

Install compatible CRDs and webhooks. Deploy compatible operator, agent, reporter, and watcher images before enabling BlockPerNode. The separate journal work will specify the shared library interface and compatible versions.

Each component must use the Node annotation schema and the fields required for its role. These fields include the `Superseded` phase, separate boot observations, whether further OOS updates are permitted, and saved request and boot IDs. They also include the restrictions list and attempt counts for each outage. A peer can add OutOfService only for an eligible NHC-created CR.

The agent counts successful storage recovery even when the API is unavailable. Never send BlockPerNode arguments to an older agent. The target must recognize the mode and Node annotation version before choosing a requested reboot. Keep unsupported journal formats unchanged. When the agent cannot recover autonomous history, apply the fresh failure checks and total-outage grace in Section 5.6.

Install the required API permissions and OutOfService tolerations before enabling cleanup. Compatible peers can operate at the same time. Each peer changes only the fields for its operation and checks the live UIDs and `resourceVersion` before it commits a Node update.

Use `maxSurge: 0`, `maxUnavailable: 1`, and the host lock during replacement. For each target with an unfinished request, keep another agent Pod running with API access and a controller that meets Section 5.2 requirements.

Document when workloads can restart before the OOS taint or detach, and when CR deletion can remove scheduling restrictions while cleanup is incomplete. Test compatible versions, storage drivers, platforms, and supported cluster sizes separately. Update the source links when implementation starts.

The effective mode is immutable. To change modes, first resolve existing requests and confirm the Node updates that remove their restrictions. Use the disarm and shutdown procedure that the watchdog driver supports when no reboot was chosen. The administrator can finish CR deletion without BootEnded.

A reboot that the old agent already chose can occur after the peer closes taint updates for its CR. Keep the original Node UID, configuration UID, CR UID, and boot ID for later evidence. Replace the SBR config CR only after resolving existing requests. The new configuration cannot continue an old request.

Preserve the host journal and the autonomous attempt count and outage IDs in the Node annotation across configuration replacement. Do not delete the lock file while a process can hold it. A restart cannot change Unknown attempt history without matching evidence.

Before downgrade, the administrator confirms that peers resolve BlockPerNode requests and remove their CR UIDs from the restrictions lists. The peers remove SBR-added cordons and taints when a list becomes empty, under Section 5.9. The administrator deletes the BlockPerNode SBR config CRs through their normal deletion procedure. Confirm that the agents stopped before installing incompatible binaries or CRDs. Preserve the durable journal and Node entries that show prior autonomous attempts and their outage IDs.

Keep peer Pods running with API access until the required Node updates succeed. If an old CR UID remains in a restrictions list, configuration deletion can wait for its removal. Report that CR as the cause of the delay. Remaining workloads or attachments alone must not delay configuration deletion.

Existing shared-storage configurations can coexist only if their node sets do not overlap. The webhook and configuration controller both check whether configurations select the same nodes. The replacement process cannot use the watchdog while the old process holds the host lock.

## 8. Test Plan

Extend the existing API, controller, agent, and end-to-end test suites. These are proposed tests. No test in this plan has run. Use suitable API servers, hosts, drivers, and controllers to test webhook, watchdog, CSI, and KubeVirt behavior.

### 8.1. API and compatibility

- Omit the volume mode, then test explicit `Filesystem` and explicit `Block`. Check that each case uses its existing defaults and shared probe.
- Check that admission accepts `BlockPerNode`. Check that it rejects an unknown mode, an omitted required StorageClass, and invalid durations.
- Check the default `ioTimeout` in each mode. Set explicit values, including the `1s–10m` limits, in every mode. Check that admission rejects zero.
- Set total-outage grace to its permitted limits. Check that admission accepts zero grace and requires a positive autonomous recovery period. Check that it rejects both fields in existing modes.
- Omit the OOS policy and check that the effective policy is OnTimeout. Test explicit OnTimeout and OnReboot. Omit reboot grace and check for a 60-second deadline interval.
- Submit an invalid OOS policy or a reboot grace at or below zero. Check that admission rejects them. Check that it rejects both fields in existing shared modes.
- Omit `bootUnitEnabled` and check that it defaults to false. Enable it with a positive timeout and check that admission accepts both fields.
- Enable the unit without a timeout. Supply invalid, zero, or negative timeouts with either enable value. Check that admission rejects these cases.
- Disable the unit with a valid timeout present. Check that the timeout has no effect. Reject both boot-unit fields in existing modes.
- Omit `watcherGroupSize` and check that the limit is 16. Check that admission rejects sizes at or below zero. Submit invalid and duplicate `watcherGroupLabels` keys. Check that admission rejects each case. Check that it rejects both fields in existing modes.
- Change the configuration after a peer writes Offered. Check that the saved policy and reboot deadline for the CR stay the same. Check that kubelet starts normally when the standalone service is disabled by default.
- Check the timeout in the agent arguments and the value that initialization and preflight use. Leave the timeout unset in existing modes and check that their previous behavior continues.
- Submit equivalent durations and an explicit value equal to the default. Check that admission treats these as the same effective values. Change an immutable effective value or change the effective volume mode. Check that admission rejects the update.
- Update a permitted timing value during a probe. Check that the active probe uses its original deadline. Check that only the replacement monitor uses the new value.
- Check that each per-node PVC uses Kubernetes `volumeMode: Block` and RWX access. Check that only its target agent writes and assigned watchers mount it read-only. Check that no Kubernetes volume-mode field contains `BlockPerNode`.
- Create overlapping configurations in different namespaces and create them concurrently. Change node labels after creation. Select conflicting StorageClasses. Check that admission rejects conflicting configurations. Check that the configuration controller detects conflicts after admission. Check that configurations with compatible StorageClasses can select disjoint node sets.
- Enable `detectOnlyMode` and check that the agent still detects storage failures. Vary `maxConsecutiveFailures` and `sbrTimeoutSeconds`. Check the failure count and time between BlockPerNode evaluations.
- Check the Node execution phases and transitions. Check that the CR, Node, and configuration UIDs and the initial and observed boot IDs meet the identity rules for each observation. Reject unknown phases and invalid transitions.

### 8.2. Probe, local decisions, and watchdog

- Inject short, persistent, and intermittent probe failures, then restore successful probes. Check when the failed evaluation count reaches the unhealthy threshold and when success resets the count.
- Inject the first I/O error or let the first probe reach its deadline. Check that the failure count becomes one. Trigger additional errors, retries, or callbacks before the next evaluation and check that the count stays one.
- Check that at least one evaluation interval separates counter increments. Delay the scheduler and check that the agent adds no increments for missed evaluations. Check that elapsed time alone does not reset the count.
- Use one configured deadline for write, flush, read, and retries in a complete probe. Check that a nested timeout does not shorten this deadline unintentionally.
- Test the fifteen-second default and explicitly configured timeouts. Check that the agent counts an expired probe as a failure and still uses the configured threshold.
- Hold a probe between write, flush, and read operations or during a retry delay. Check that the process starts no second probe until the first operation resolves.
- Block a kernel operation past the probe deadline. Check that subsequent failed evaluations reach the threshold without starting a second probe in that process.
- Let the timed-out probe report success late. Check that it does not reset the failed count or clear the unhealthy condition. Require a fresh probe to complete successfully.
- Restart the process during failure detection. Check that it clears transient counts and preserves the published unhealthy condition until fresh recovery evidence.
- Restart the process while a probe is outstanding. Check that the new process starts a fresh probe after its startup checks without waiting for operations from the old process.
- Interrupt API access alone, storage access alone, then both. Check the separate API and probe results. Check that the agent starts total-outage grace only when storage is unhealthy and the API is unavailable.
- Return live `NotFound`, authentication and authorization failures, 429, 5xx, certificate errors, transport errors, DNS errors, and deadlines. Check the API reachability result for each case.
- Restore storage before and after the target writes Accepted. Check that storage success neither prevents a valid requested reboot nor lets the target choose a second reboot for that CR.
- Enable `detectOnlyMode` and check that the agent still reports storage observations. Check that it makes no new acceptance update and cannot reboot from an unknown acceptance result. Check that it makes no new autonomous reboot decision. In separate cases, enable the mode after confirmed acceptance for the same boot or after the agent chooses reboot. Check that the mode preserves the accepted attempt unless the target sees a newer offer replace it. Check that the mode does not reverse the chosen reboot.
- Start the peer manager before local probe initialization. Block probe or journal I/O and check that the peer still updates another target's request through the API.
- Inject a local agent failure. Check that it reports the failure while the peer manager keeps running and passes its liveness check.
- Start a replacement process or Pod while the old process holds the host lock. Check that the replacement cannot access the watchdog or write the journal.
- Fail or block the software reboot command and check that the watchdog fallback continues independently. With no chosen reboot, check that the agent disarms the watchdog before it stops monitoring during planned shutdown.
- In the separate watchdog work, define tests for driver behavior, nowayout, and process termination. Use the null device only in test support.
- Preserve existing shared-storage heartbeat and fencing tests.

### 8.3. Saved requests, boot evidence, and autonomous reboot history

- Have two peers submit offers for different CR UIDs and the same Node UID at the same time. Check that only the current offer can enter Accepted and that unrelated Node fields remain unchanged.
- Have a peer change `Offered` to `Cancelled` while the target changes it to `Accepted` using the same Node version. Check that only one update succeeds.
- Change the CR UID, configuration UID, Node UID, or local boot ID before acceptance. Check that the target rejects offers that do not match those values.
- Restart peers and the agent at every execution transition. Check that the target never executes the same CR in a different boot. Lose the journal data that show whether the agent sent the software reboot command. Check that it can resend the command in the same boot.
- Drop the response to every acceptance update and make live reads inconclusive. Check that the agent attempts exactly two acceptance updates. Keep storage healthy and check that the agent can still reboot.
- Check that each acceptance update or follow-up read waits no more than two seconds. Check that the retry delay is at most one second and that the entire sequence is at most 30 seconds.
- Check that the target ends acceptance as soon as it knows the result or finishes the attempts and follow-up reads. Check that it waits no more than two seconds for the separate journal write.
- Show the target that acceptance failed or that the peer changed its unaccepted offer to Cancelled. Check that the target does not choose a reboot. Repeat without showing the target the Cancelled phase. Leave its acceptance result unknown through the required attempts and follow-up reads. Check that the target can still reboot under the fallback policy.
- Fail the journal write of the chosen requested reboot. Check that the target can reboot and that the failure does not create an Accepted entry. Check that blocked journal I/O does not stop watchdog fallback.
- Require the local agent or reporter to read the exact `Accepted` Node entry through the live API before it reads the current host boot ID.
- Publish the initial and observed boot IDs with the matching CR UID in a conditional Node update. Check that the write fails if the execution entry changes before it commits.
- Check that a peer that meets Section 5.2 requirements compares the observation with the accepted CR UID, Node UID, and initial boot ID before it sets `BootEnded`.
- Submit an observation with the same initial and observed boot ID, an empty boot ID, a mismatched identity, or a different request. Check that the peer rejects each observation.
- Change `Node.status.nodeInfo.bootID` without publishing the result of the ordered reads in Section 5.8. Check that the peer does not set `BootEnded`.
- Reboot before acceptance, reboot during the ordered reads, and delay publication of an observation. Check that the peer sets `BootEnded` only if the observation establishes that the exact accepted boot ended in the required order.
- Use a stale Node boot ID when a peer writes Offered. Report that this initial boot ended. Check that the report does not claim the reboot occurred after Offered.
- Delete a CR after the target confirms Accepted. Lose the target journal before reboot. Check that the target can resume the same attempt only while its original boot remains current.
- Keep the probe unavailable after reboot. Check that the separate reporter can read the accepted Node entry and host boot ID, then publish a matching observation through the API.
- Delete the CR in every execution phase. Check that the peer preserves the saved UIDs and initial boot ID for late evidence. Check that it stops further taint updates and workload and attachment checks. Check that it removes the CR UID from the restrictions list without waiting for BootEnded.
- Replace the Node with a different UID and the same name. Submit old request updates and boot observations. Check that they change no field on the replacement Node.
- Update the generation of one CR and then create distinct valid CR UIDs. Check that a generation update cannot start a second attempt for the same CR. Check that each valid new CR can request one reboot even after an autonomous reboot. Check that retries and new CRs use the same ordered updates and do not change autonomous attempt counts.
- Restart the process, replace the Pod, and reboot the host. Check which autonomous attempt IDs and outage counts survive in the journal. Test full disks, read-only filesystems, updates that finish late, and writes whose durability is unknown.
- Test missing, corrupt, unreadable, and unsupported journals separately. Check that the agent attempts to recreate corrupt journals and leaves unsupported formats unchanged. Check that it recovers only matching attempt or request data from the API.
- Fail journal recreation. Check that the agent continues monitoring with in-memory state and reports Unknown attempt history. Check that journal failure alone does not trigger reboot.
- Keep storage and the API unavailable across boots. Retain valid history that shows a used autonomous attempt. Check that the agent cannot choose another autonomous reboot until storage stays healthy for the recovery period.
- Repeat without usable history. Check that the agent can choose another autonomous reboot only after fresh storage and API failure checks and a new total-outage grace.
- Reboot with Unknown history. Restore valid history showing that the agent already used its autonomous attempt for the outage. Check that the agent prevents another autonomous reboot for that outage.
- Let a journal operation remain blocked after the caller's deadline. Replace the process and check that the old operation keeps the host lock until it resolves. Check that a late write does not permit or reverse a reboot.
- Omit the storage recovery period and check for five minutes of continuous successful storage observations. Inject a failed probe, an expired probe, or a gap in observations. Check that the agent restarts the recovery interval.
- Keep the API unavailable while fresh probes succeed. Check that the agent continues to feed the watchdog. After five minutes, check that the agent can permit an autonomous reboot in a new outage.
- Fail or interrupt API checks while probes keep succeeding. Check that the agent continues to count the elapsed storage recovery time.
- Restart the agent during storage recovery. Check that the recovery interval starts again if the agent cannot show continuous storage observations. Assume synchronized clocks for API timestamps.
- After reboot, lose the latest journal update while the CR still exists. Check that the agent or reporter obtains the original request and boot IDs from the API and publishes a boot observation without requesting another reboot.
- With no saved Accepted phase, publish a separate BootObserved result. Check that the peer does not set Accepted or attribute an autonomous reboot to a request that the agent did not observe.
- Drop the response to the boot observation update after reboot. Check that the agent or reporter reads the live entry and retries the observation if needed. Check that the target does not reboot again.
- Fail the journal update that clears the previous boot's saved instruction to stop feeding the watchdog. Check that the agent feeds the watchdog in the current boot without executing the old request.
- Trigger an autonomous reboot and check that existing requested execution entries remain unchanged. Create a new CR later and check that its execution entry does not claim the earlier reboot.
- Submit offer B while A is Offered or Accepted. Check that one conditional Node update changes A to Superseded and sets B to Offered.
- Submit two eligible CRs concurrently with no prior offer. Check that the order of successful first-offer commits selects the current request. Check that the earlier request's terminal phase prevents another offer for it.
- Let the target see A's Superseded phase before it chooses A's reboot. Check that it abandons A even if A entered Accepted first.
- Let the target choose A's reboot before A becomes Superseded. Repeat with API loss that prevents the target from seeing the change. Check that the target can still reboot under the fallback rules while the peer cannot resume taint updates for A.
- Delete B while A's CR remains. Check that A stays Superseded and that its saved deadline stays the same. Check that no peer offers A again.
- Publish late boot evidence for A. Check that A retains its Superseded phase and saved policy result. Check that B keeps its initial boot ID, reboot deadline, and restrictions entry.

### 8.4. Add and remove scheduling restrictions and check workloads

Use envtest to race a peer's conditional Offered update against CR deletion. Check real API version conflicts and finalizer removal.

- Have two peers try to add different CR UIDs to the restrictions list at the same time. Check that both preserve the cordon and OutOfService taint values saved when the first CR entered the list.
- Remove one CR UID while another remains in the restrictions list. Check that the peer leaves the cordon and taint that the remaining CR still requires.
- Remove the last CR UID from the restrictions list. Check that the peer removes only the cordon and OutOfService taint SBR added. Check that it preserves pre-existing restrictions and unrelated taints.
- Remove one CR UID, add another, and submit a delayed update for an old request concurrently. Check that each write checks the Node UID and `resourceVersion`.
- Pause the peer after it reads the Node and checks the CR. Pause it before it writes Offered and adds the CR UID to the restrictions list. Delete the CR normally. Check that the peer increments `coordinationRevision` before it removes the finalizer. Let storage fail again and delay the condition update. Resume the paused peer. Check that its stale Node update fails.
- Reverse the order so the Offered update commits first. Check that the peer closes further taint updates and scans before it removes the CR UID. Check that it preserves acceptance and evidence history and can remove the finalizer without BootEnded.
- Delete a CR without a restrictions entry, including after an earlier peer removed it. Check that the peer increments `coordinationRevision` even when it removes no entry. An unchanged Node update must not permit finalizer removal.
- Delete a CR before any peer offers it. Check that the Node coordination annotation was confirmed before finalizer installation. Check that the peer validates the original Node UID and increments `coordinationRevision` before finalizer removal.
- Remove CR A's UID as the last entry and uncordon the Node. Drop the response or restart the peer before it removes A's finalizer. Add CR B's UID and cordon the Node again. Retry A's Node update and increment `coordinationRevision`. Remove A's finalizer while the unschedulable taint remains. Check that B keeps its execution phase, restrictions entry, cordon, taints, and saved original restrictions. Check that autonomous attempt data remain unchanged.
- Delay removal of the unschedulable taint after the last CR UID leaves the restrictions list. Check that the peer can remove the finalizer once the required Node update succeeds. Repeat after an administrator cordons the Node before the retry. Check that the peer preserves the administrator's cordon and does not wait for taint removal.
- Make the Offered update conflict. Set the CR deletion timestamp, stop it through NHC, set a terminal state, or replace its UID. Check that the peer reads the live Node first, then reads the live CR. Check that it repeats every eligibility check before it writes Offered using the new Node version.
- Delete or replace the original Node before the peer removes the CR UID from the restrictions list. Check that it confirms the old UID is gone through a live read and removes the CR finalizer without changing a replacement Node.
- Remove or corrupt the coordination annotation while the original Node UID remains. Check that the peer reports the need for repair and keeps the finalizer until repair. Return API errors and check that the peer does not treat them as proof of Node absence.
- Run OnReboot without valid boot evidence and check that no peer adds the taint. Repeat with valid BootEnded or BootObserved for the CR's initial boot. Check that the peer can add the OutOfService taint in either case. BootObserved must not change the request phase to Accepted.
- Create a CR outside the supported NHC creation path. Check that the peer does not write Offered, supersede another request, or add the OutOfService taint for it.
- Under OnTimeout, add the OutOfService taint after the first valid boot evidence or when the reboot deadline saved with Offered expires. Vary CR creation and acceptance times. Check that neither controls the deadline and that acceptance is not required.
- Race the peer's change from Offered to Cancelled at the deadline against the target's change to Accepted. Check that only one conditional update succeeds. Check that Cancelled cannot later become Accepted.
- Keep the target offline through the reboot deadline. Check that the peer can set the OutOfService taint and report policy success after confirmation without BootEnded. Check that this does not confirm acceptance.
- Delete the CR or have NHC stop it before grace expires. Include NHC deletion with healthyDelay set to zero. Check that a delayed OutOfService taint update fails after the peer closes further work.
- Add the OutOfService taint and check that the agent and reporter Pods tolerate it. Observe the Kubernetes controllers as they remove workloads.
- Assert that SBR never directly deletes Pods, VMIs, VolumeAttachments, PVCs, or PVs. The BlockPerNode cleanup path must not require Pod-delete permission.
- Check that the peer saves the start and five-minute deadline in the same Node update that confirms OOS application. Check that conflicts, peer replacement, and restart do not change those times.
- Fail taint updates and drop API responses. Check that the peer reads the live Node to resolve an uncertain taint update. Check that deadline expiry alone does not confirm taint application.
- Skip an optional scan. Return complete empty lists, complete lists with remaining resources, partial lists, API errors, and discovery results that confirm an optional resource API is absent. Check the cleanup result for each case.
- Return an incomplete list or a read error. Check that the peer reports Unknown when observations end. Check that an error or elapsed deadline does not cause Completed.
- Confirm the OutOfService taint and check that the peer immediately reports policy success. Leave relevant resources present when the five-minute window expires. Check that complete observations lead to Incomplete.
- Fail repeated taint updates so the peer cannot confirm application. After reboot grace expires, check that the CR still reports Pending and `outOfServiceApplied=false`. Check that it reports neither policy success nor terminal failure based on elapsed grace alone.
- Finish the five-minute observation period and check that the peer ends scans for this CR in the Node annotation. Check that the peer neither extends the observation deadline nor adds the taint again for that operation. Check separately that expiring reboot grace can permit the initial taint update.
- Keep a CR UID in the restrictions list and check that the peer retains its taint. Delete the CR while observations remain incomplete. Check that the peer removes the entry and removes SBR-added restrictions if the list then becomes empty.
- Have NHC stop an earlier request and retain its CR for escalation history. Check that the peer can write Offered for a later eligible CR as specified in Section 5.9 while old workloads remain. Check that it preserves the earlier CR's saved failure result.
- Create a later eligible CR while the peer still observes resources for the earlier CR. Check that the peer can commit the new Offered update and stop checking workloads and attachments for the earlier CR without waiting for those resources to disappear.
- Close further scans and taint updates for the CR in the Node annotation before publishing the terminal cleanup phase in CR status. Check that delayed taint writes fail after the Node annotation stops further taint updates or the peer writes a newer offer. Stop the peer after the Node write but before the status update. Check that it reports Unknown on restart without restarting the scans.
- Have the target report a local boot ID different from the offered initial boot ID. Check that the peer changes Offered to Cancelled and preserves the initial boot ID.
- Let a newer valid offer replace a Cancelled request, then delete the old CR. Check that the peer can recover the old request and boot IDs from retained data for evidence. Check that the old request cannot execute or apply the taint.
- Delete a CR after another request becomes current. Check that the peer removes only the deleted CR UID from the restrictions list. Check that it does not copy the current request's execution phase to the deleted CR.
- Restore storage and have NHC request CR deletion while a peer checks workload cleanup. Check that workload finalizers and remaining attachments do not prevent removal of the remediation CR's finalizer after the required Node update.
- Interrupt API access separately from leaving workloads or attachments present. Check that failed Node updates for a deleting CR can be retried. Check that the peer does not remove the finalizer before the required Node update succeeds.
- Stop every peer that meets Section 5.2 requirements and start one again. Check that it reads the same execution phase, CR UIDs in the restrictions list, and original deadline.
- Retire a configuration. Check that peers keep running with the required API permissions until they confirm the Node updates that remove restrictions. Check that the replacement configuration does not continue old execution.

#### Watcher reads and storage condition updates

- Fail the target's probe reads while writes succeed. Check that the heartbeat can publish the last earlier successful completion. Check that its completed sequence does not advance for failed reads.
- Check that watchers mount probes read-only. Check that they update only their observations and the ordered `SBRStorageUnhealthy` condition. Check that they cannot offer remediation requests, reboot a host, or add OutOfService.
- Show no increase in completed sequence across the expiry period using successful reads of valid heartbeat data. Check that this gives Expired. Check that read errors, partial heartbeat data, blank devices, cached data, and insufficient observations do not establish expiry.
- Replace the target agent Pod, its PVC, and watcher assignments. Check that the current assignment rejects old UIDs and generation values, including delayed annotation writes. Remove watcher references and check that PVC protection can release retired claims.
- Stop the watcher after it publishes Expired. Check that missing observations do not clear the unhealthy condition. Check that a retry cannot refresh the old observation's timestamp.
- Block a probe read and check that the watcher continues reading other targets. Block an assigned volume mount and check that the operator reports which targets lack a watcher able to read their probes.
- Group nodes with matching region, zone, and configured custom label values. Check that the operator splits each set into groups no larger than `watcherGroupSize`, which defaults to 16.
- Omit a standard topology label and check that its absence has its own grouping value. Omit a configured custom label and check that the operator reports that it cannot assign a watcher or monitor the affected probe.
- Check that the operator creates one watcher for each group and places it on a node outside the group that can mount all its claims. If no such host exists, check that the operator reports that the assigned probes lack an available watcher.
- Change grouping labels or `watcherGroupSize`. Check that the operator replaces the affected watcher Pods. Submit old assignment UIDs or generation values and check that the target Node rejects the reports.
- Restore API access and publish an autonomous report without an eligible CR. Check that the peer saves only attempt details, the outage ID, the known attempt count, and available boot information. Check that it adds no restrictions entry or OutOfService taint.
- Submit an autonomous boot observation with the exact Node UID and initial boot ID of an eligible CR. Check that the peer stores the observation for that CR and preserves its reboot deadline. Check that the observation does not confirm acceptance.
- Delete a CR while an autonomous report is pending. Check that the peer completes the required Node update and removes the CR UID from the restrictions list. Check that delayed reporting cannot preserve or restore the entry or its restrictions.
- Submit valid Expired observations with Node Ready True, False, and Unknown. Check that each sets `SBRStorageUnhealthy=True`. Check that NHC applies its request policy before it creates a CR.
- Inject read errors, invalid heartbeat data, stale assignments, or heartbeats without an increase in the completed probe sequence. Check that they cannot clear the unhealthy condition. Check that an Unknown observation does not set the condition to True.
- Race an old watcher recovery update against a newer target failure. Reverse the reporting roles. Check that the newer failure remains and that `LastTransitionTime` stays the same when the condition status stays the same.
- Publish successful completion of probe 41 after probe 42 fails. Check that sequence 41 cannot clear the condition. For the same producer, require a fresh completed sequence greater than 42 with the current failure revision.
- Retry an older Expired report after probe 42 fails. Check that the failure revision and sequence boundary stay the same. Check that retrying an expired sample does not update its timestamp.
- Start or restart the watcher after the target reports a failure. Check that the watcher reads the current failure revision and collects a fresh baseline before reporting recovery.
- Publish a fresh completion that follows the current failure. Check that the agent or watcher can set `SBRStorageUnhealthy=False` after it checks the producer identity, failure revision, and sequence under Section 5.5.
- Use a live API server to check that one conditional `nodes/status` update sets the condition and its ordering annotation. Race the update with kubelet status and unrelated annotation updates. Check that unrelated values survive retries.
- Check that a watcher cannot add OutOfService. Set NHC thresholds that prevent it from creating a CR. Check that an autonomous reboot alone does not create a CR.
- Publish a delayed autonomous report after CR deletion, NHC stop, or supersession. Check that it does not create a CR, set the storage condition from stale data, start a reboot grace period, or permit a taint update.
- Make fresh local storage and API checks permit autonomous reboots on several nodes. Check that the agents apply no cluster-wide limit. When a node's history shows it already used its attempt for that outage, check that the local agent does not reboot it again.
- Run the reporter and boot service separately. Check that the service lets kubelet start at its local deadline without a probe heartbeat or CR deletion.

### 8.5. End-to-end tests

Exercise the use-case matrix in Section 5.1. Keep the tests for Filesystem and shared Block. Test BlockPerNode with representative CSI drivers that allow one target to write an RWX Block volume while its watchers mount it read-only.

Run at least two peers for one target. Stop a peer while the target attempts acceptance, while the peer lists workloads, or while the peer removes restrictions. Check that another peer uses the same request UID and deadlines without granting another reboot. Separately lose the saved information that shows whether the agent sent the software reboot command. Check that the agent can resend it in the same boot.

Block one peer Pod's probe I/O. Check that its controller still processes another target's request. Separately block probe attachment for all peer Pods that meet Section 5.2 requirements. Check that offers, peer-validated phase updates, and finalizer removal wait for a peer. When the boot reporter can reach the API, check that it can still publish boot observations.

Replace Pods and PVCs while preserving the host journal. Test requested reboot, NHC timeout, CR deletion, and operator restart. Replace a configuration only after peers resolve its existing requests. Check that incomplete Pod, VMI, or attachment observations do not delay policy success after confirmed OOS or CR deletion after the required Node update.

Start workloads after reboot and before the peer applies the OutOfService taint. Capture which workloads restart and which attachments remain. Check the resulting behavior against the accepted risk in Section 6.2.

Scenarios 13 and 14 remain deferred. For scenario 17, check the order of request updates and reboot decisions. Test the autonomous count when it is known and when it is Unknown. Check that the host lock excludes a second local agent. In the power-loss test, do not expect the watchdog to restore power.

### 8.6. Kind cluster testing in CI

Add a job for `sharedStorageVolumeMode: BlockPerNode` to [the Block Kind workflow](../../.github/workflows/kind-e2e-block.yaml). Keep the shared Block job and Filesystem tests.

Extend [the block setup script](https://github.com/medik8s/tools/blob/main/dev/kind-block.sh) and its storage manifest to create independent per-node volumes for BlockPerNode tests. The existing fixture shares one loop device across two workers and exposes one static RWX Block PV.

1. Read the Kind node count before creating any devices.
2. For N Kind nodes, create N distinct backing images, loop devices, and RWX Block volumes.
3. Expose each device to its target writer and assigned watchers. Set the PV topology so those nodes can mount that device.
4. Check that each target has a distinct device, PV, and Pod-owned PVC. Check for Kubernetes `volumeMode: Block` and RWX access. Check that the watchers mount the devices read-only.
5. Reclaim or provision PVs so replacement Pods can use new claims. Check that a retained PV still bound to a deleted claim cannot match a new claim automatically.
6. Select at least three workers. Run two peer controllers concurrently for a target on the third worker.
7. Fail one node's storage or block its I/O. Check that another node's probe can still complete.
8. Replace the Pod and claim. Check that replacement does not remove the host journal or its surviving attempt history.

Extend [the setup tests](https://github.com/medik8s/tools/blob/main/dev/tests/test_kind_block.py) for different node counts and separate devices. Test partial setup failure, replacement claims, and repeated teardown. Fail device detach and test a retry. Collect device mappings, PV and PVC UIDs, agent and reporter logs, Node execution phases, restriction entries, and cleanup results.

Run teardown even if setup or a test fails. Remove loop devices and backing images only after the Kind nodes are gone. If detach fails, keep the loop device, backing file, and data needed to retry detach.

The current Kind reboot watcher waits for `FencingSucceeded` before restarting a container. OnReboot requires matching boot evidence before it can report that condition. Use test-only adapters that simulate the reboot and supply the corresponding boot observation explicitly. Test OnTimeout separately and check that it can report success without that observation. A fixture timeout supplies no boot observation.

Kind shares the host kernel. Its null watchdog exercises test code paths. Keep production checks enabled. Test hardware watchdogs on real hosts. Test CSI and KubeVirt behavior separately from these simulations.

### 8.7. Tests for the optional boot service

Use real hosts to check that systemd starts the service before kubelet and to test watchdog behavior.

- Leave the service disabled and check that kubelet starts normally.
- Configure and enable a finite local delay. Check that kubelet can start at expiry when API access, certificates, the reporter, or the probe are unavailable.
- Apply the CR settings to the host. Reboot autonomously with the API unavailable and check that the service uses the saved timeout.
- Reboot without a recognized autonomous attempt. Check that enabling the unit does not add a delay to that boot.
- Restart the service in the same boot. Check that it uses the original deadline and does not start a new delay.
- Change the timeout or disable the unit during a delay. Check that the active deadline stays unchanged and later boots use applied settings.
- Leave a host unreachable during a settings update. Check that the update does not claim the host received the new values.
- Remove or corrupt local settings. Check that kubelet can start without an indefinite wait. Remove the service and check normal kubelet startup.
- Fail local reads or supply stale journal data. Check that the service ends the failed read attempt within its allowed time. Document when the service misses a delay.
- Apply OutOfService before reboot, during the delay, and after expiry. Check that the peer uses the reboot deadline and the service uses its local deadline.
- Leave the journal corrupt after an autonomous reboot. Check that the service does not prevent the agent from attempting recreation or continuing with in-memory state when recreation fails.
- In the separate boot-service work, test installation, host setting delivery, reboot recognition, and watchdog behavior on each supported platform.

## Alternatives considered

- Run the BlockPerNode controller in the elected operator manager or in a separate Pod without a probe volume. That controller could process requests before the target's storage attaches. The selected design keeps it in the agent Pod, which can remain Pending if its own probe cannot attach as Section 6.8 describes.
- Elect one peer or acquire a Lease for each remediation CR. Either option can reduce duplicate processing. Peers would still need one conditional Node update to select the current CR UID when requests compete. This proposal uses that update and lets peers run concurrently.
- Keep the current shared-storage SBR design. It requires multiple agents to write to one shared volume. The CSI drivers targeted by this proposal cannot provide the required shared Block volume for those concurrent writes.
- Use NHC to chain Self Node Remediation and SBR. This reuses SNR's reboot and cleanup behavior. Both providers would need to agree which process controls the watchdog, how they exchange boot IDs, and which provider handles workload removal.
- Use Node `Ready` to trigger remediation or use out-of-band fencing as the primary method. A `Ready` trigger would permit a request during API or network failure with healthy storage. Out-of-band fencing can stop a host independently where power controls exist. Some target environments lack them. Test it separately on supported platforms.
- Require the agent to save its reboot decision to the journal before every reboot. This could prevent some attempts from going uncounted. If the journal write failed, the agent could not reboot. The selected policy lets the agent reboot when the write fails or its wait expires.
- Before an outage, save that the current boot may use one autonomous reboot. This could limit repeated resets if the final journal write fails. The design instead checks whether surviving journal or API data shows a used attempt. When that history is Unknown, the agent can reboot after fresh failure checks and total-outage grace.
- Block kubelet until a peer sends a release message or an administrator removes a saved systemd mask. This would require rules for who sends the message or removes the mask and when. The selected host service lets kubelet start when its local delay ends.
- Require admission approval before a node can rejoin after remediation. This would require components to check and enforce the approval, and tests on each supported platform. The selected design allows the peer to remove SBR restrictions while old workloads or attachments remain.

The selected design uses one remediation provider. Only the local agent controls its host watchdog and chooses a local reboot. It gives each selected node a Block probe and assigns watchers to mount it read-only. Existing modes keep their shared probes. BlockPerNode measures storage health separately from API reachability.

## Terminology

A **remediation CR** is a `StorageBasedRemediation` resource. An **SBR config CR** selects nodes and supplies agent settings. An **NHC config CR** tells NHC which conditions, thresholds, and template to use when it creates a remediation CR.

| Term | Meaning in this design |
|---|---|
| Local agent | The process that tests its node's storage, feeds or disarms its watchdog, chooses and starts reboot, and writes its journal. |
| Peer controller | The API controller in an agent Pod. It writes offers, checks boot evidence, adds or removes scheduling restrictions, and updates CR status for other nodes. |
| Reporter | The separate Pod that reads the saved request or attempt before reading the current host boot ID. It publishes the observation through the API without attaching the probe. |
| Probe | A per-node RWX Block volume that the target agent writes, flushes, and reads to test storage. Its data include the current probe sequence and the last completed sequence. |
| Watcher | A Pod that reads assigned probes. It publishes the read result and can update `SBRStorageUnhealthy` with the shared ordering checks. |
| Autonomous report | The agent or reporter publishes the surviving attempt ID, Node UID, configuration UID, original boot ID, known outage count, and boot evidence. The report cannot add a restrictions entry, start reboot grace, or permit a taint update by itself. |
| Journal | The host file at `/var/lib/medik8s/state/sbr.json`. The agent tries to save attempt IDs, reboot decisions, boot IDs, and diagnostics. It continues with available state if a read or write fails. |
| Request identity | The CR UID identifies one request. For that UID, the peer saves a fixed Node UID, configuration UID, volume mode, and initial boot ID. |
| Offered | The peer saves the CR identity, Node and configuration UIDs, initial boot ID, policy, and reboot deadline in the Node annotation. The target checks those IDs before it attempts at most two updates to Accepted. |
| Accepted | The target changes Offered to Accepted in a conditional Node update for the original boot. If the target sees a newer valid offer replace this one before it chooses a reboot, it abandons the earlier request. |
| Cancelled | The peer changes Offered to Cancelled in a conditional Node update. The target cannot change that entry to Accepted. If the target cannot see the change and its acceptance result remains unknown after the required checks and attempts, it can still reboot. |
| Superseded | In one conditional Node update, a peer sets a newer valid request to Offered and marks the previous one Superseded. Once the target sees that change, it cannot choose a reboot for the earlier request. |
| BootEnded | The peer compares an ordered boot observation with the accepted CR UID, Node UID, and initial boot ID. It writes BootEnded and the observed boot ID after confirming that the initial boot ended. |
| BootObserved | The peer verifies that the identified boot ended for an autonomous attempt or an unaccepted request. The observation does not set Accepted or identify the cause of the boot change. |
| Chosen reboot | The agent decides to reboot its host during the current boot. It keeps that decision in memory, tries to write it to the journal, and stops feeding the watchdog. |
| Attempt | One requested or autonomous reboot operation. The target uses the CR UID for a requested attempt and a locally generated ID for an autonomous attempt. |
| Current reboot request | The Node annotation identifies the current CR UID. The target can resume the Accepted attempt on its original boot unless it sees that a newer offer replaced it. A boot observation alone cannot let the target choose a reboot. |
| Listed CR | A CR whose UID is in the Node annotation's holders field because it still requires the cordon or OutOfService taint. A CR can stay listed after a different request becomes current. |
| Saved scheduling restrictions | The peer saves the Node's existing cordon and OutOfService taint state when the first CR enters the restrictions list. |
| Cleanup observation | After confirming OOS, a peer checks Pods, VMIs, and VolumeAttachments for the target. The check stops at most five minutes after the saved start time. |
| Rejoin | During CR deletion, the peer removes the CR UID from the restrictions list. If the list becomes empty, it removes only the cordon and taint that SBR added. The optional boot service uses its own deadline. |
| Autonomous reboot count and limit | If valid history shows that the agent already chose an autonomous reboot for the outage, it cannot choose another during that outage. With Unknown history, fresh failure checks and total-outage grace can permit another reboot. A requested reboot does not change this count. |
| Outage period | An interval without sustained storage recovery. If history shows that the agent already chose an autonomous reboot, it cannot choose another until storage recovers for the configured period. API recovery does not end the interval. |
| Permit another autonomous reboot | After fresh storage probes stay healthy for the configured recovery period, the agent can choose one autonomous reboot in a later outage. The default is five minutes. It measures that time even when the API is unavailable. |
| Watcher group | Nodes with matching region, zone, and configured custom label values. The operator assigns no more than `watcherGroupSize` targets to one group and runs one watcher for it. |
| Boot unit or gate | Optional host service controlled by `bootUnitEnabled` and `bootUnitTimeout`. It delays kubelet after a recognized autonomous reboot and defaults to disabled. |
| Held | The host service prevents kubelet from starting. It lets kubelet start when the local deadline expires, even if the API is unavailable or workload removal is unconfirmed. |
| Clean and unclean shutdown | The journal may show that the agent stopped normally. A missing marker provides no evidence of reboot cause or Node health. |

The peer writes Offered. The target conditionally writes Accepted and separately chooses whether to reboot. The peer checks the ordered boot observations required by Section 5.8 before writing BootEnded to the Node annotation. After confirming the OutOfService taint, the peer sets the CR success conditions and can continue checking workloads and attachments. Workloads can still run or restart after policy success or the observation deadline.
