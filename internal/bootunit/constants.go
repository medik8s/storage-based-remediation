/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package bootunit implements the pre-kubelet self-cordon service. It runs as
// a systemd unit Before=kubelet.service, reads the SBR journal to decide
// whether the current boot needs protection, and if armed, cordons the node
// and publishes an SBRRejoinPending condition using the node identity.
package bootunit

import "time"

// Paths on the host filesystem. The agent installs the binary and the unit
// file under /usr/local and /etc/systemd/system; the journal, arming flag and
// identity descriptor live under the state root.
const (
	// UnitName is the systemd service name. It must sort Before=kubelet.service.
	UnitName = "sbr-self-cordon.service"

	// HostStateRoot is the single root for medik8s host state, shared with
	// the agent and the journal: the journal, the arming flag and the
	// identity descriptor. It is 0700 and never holds executables.
	HostStateRoot = "/var/lib/medik8s/state"

	// HostBinaryPath is the boot unit binary's path on the HOST, as the
	// systemd unit's ExecStart references it.
	//
	// It must be a path whose SELinux label systemd (init_t) may execute.
	// /var/lib/medik8s/state is NOT one: on enforcing RHCOS, exec of a
	// var_lib_t file by init_t is denied (verified empirically: AVC
	// "denied { execute } ... scontext=init_t tcontext=var_lib_t",
	// unit fails 203/EXEC). /usr/local/bin is the sanctioned writable
	// executable path on OSTree systems — a bind mount of /var/usrlocal/bin
	// labeled bin_t — and the unit starts cleanly from there (verified the
	// same way).
	HostBinaryPath = "/usr/local/bin/sbr-self-cordon"

	// ContainerHostBinPath is where the agent CONTAINER sees HostBinaryPath.
	// The DaemonSet mounts the host's /usr/local at /host/usr/local rather
	// than at /usr/local, because a same-path mount would shadow the
	// image's own /usr/local/bin that holds the source binary — the copy
	// would become a no-op onto itself. This follows the existing
	// convention of prefixed host mounts (as with /host-dev for the
	// watchdog directory). On OSTree, a hostPath mount of /usr/local
	// resolves through the /var/usrlocal bind mount, so it works on both
	// OSTree and conventional hosts.
	ContainerHostBinPath = "/host/usr/local/bin/sbr-self-cordon"

	// HostUnitPath is where the agent installs the systemd unit file.
	HostUnitPath = "/etc/systemd/system/" + UnitName

	// ArmingFlagPath is the ConditionPathExists gate for the unit. The agent
	// creates it when it ARMS THE WATCHDOG — not only when it commits a
	// reboot decision — because the weaker journal tiers exist precisely for
	// resets nobody planned: an agent that dies mid-storage-outage records
	// no reboot decision, and a reboot-commit-only flag would gate the unit
	// off in exactly that scenario, leaving Tier 2 unreachable. The flag is
	// created after the first durable journal write, so its presence implies
	// a journal existed (keeping the fail-closed tier sound), and the agent
	// removes it at startup once it has taken over protection duties for
	// the boot. While the flag exists the unit runs on every boot; the
	// journal tiers carry the decision, and an ordinary reboot costs one
	// process that exits immediately.
	ArmingFlagPath = HostStateRoot + "/sbr-armed"

	// HostIdentityEnvPath is the systemd EnvironmentFile the unit loads its
	// node identity from. The agent persists the descriptor with
	// WriteIdentityEnv at enrollment; the unit declares the file required,
	// so a node without a provisioned identity refuses to run the armed
	// path instead of running it with empty fields.
	HostIdentityEnvPath = HostStateRoot + "/sbr-identity.env"

	// ContainerBinaryPath is the location of the binary inside the agent
	// container image; the agent copies it to HostBinaryPath.
	ContainerBinaryPath = "/usr/local/bin/sbr-self-cordon"

	// ContainerUnitPath is the location of the unit template inside the
	// agent container image.
	ContainerUnitPath = "/usr/local/share/sbr-agent/" + UnitName

	// ContainerSystemdDir is where the agent container sees the host's
	// /etc/systemd/system. The DaemonSet mounts the host directory here so the
	// installer can write the unit file; activation separately runs systemctl
	// inside the host mount namespace, where the same file is at its real path.
	ContainerSystemdDir = "/host/etc/systemd/system"

	// ContainerHostUsrLocal is where the agent container sees the host's
	// /usr/local. It must not be mounted at /usr/local itself: that would
	// shadow the image's own /usr/local/bin, which holds the source binary.
	ContainerHostUsrLocal = "/host/usr/local"

	// ContainerKubeletConf is where the agent reads the node's kubelet
	// kubeconfig. Its API server is the host-routable endpoint the boot unit
	// needs — the in-cluster service address is not routable from the host
	// before kubelet (and therefore kube-proxy) is running.
	ContainerKubeletConf = "/host/etc/kubernetes/kubelet.conf"
)

// hostMountNamespace is the host mount namespace the agent enters to run host
// binaries. The agent mounts the host's /proc at /proc, so PID 1 here is the
// host's init and its mount namespace is the host's — the same mechanism the
// reboot path relies on.
const hostMountNamespace = "/proc/1/ns/mnt"

// RejoinConditionType is the Node condition the boot unit publishes when it
// cordons a returning node. It announces to the cluster (and to
// administrators via oc describe node) that this node returned from an
// SBR-committed reboot and has not yet been evacuated.
const RejoinConditionType = "SBRRejoinPending"

// Timeouts for the boot unit's API calls. The unit is Type=oneshot Before
// kubelet.service, so these must be generous enough for a recovering API but
// short enough that the node eventually boots even on a broken control plane.
const (
	// APITimeout bounds each individual API call the boot unit makes.
	APITimeout = 30 * time.Second

	// CordonRetryDelay is the delay between retry attempts when the API is
	// unreachable at boot (which is the expected state during recovery).
	CordonRetryDelay = 5 * time.Second

	// CordonRetryAttempts bounds how long the unit retries before giving up
	// on cordoning (the base-mode unit then exits and kubelet starts).
	CordonRetryAttempts = 12 // 60 seconds of delays

	// CordonTotalBudget bounds the whole cordon-and-announce operation: every
	// retry delay plus one in-flight call at APITimeout. It must stay below
	// the unit's TimeoutStartSec (120s in deploy/sbr-self-cordon.service):
	// a budget above it means systemd kills the unit mid-retry, and a cordon
	// that was still being attempted reads as a cordon failure. Keep the two
	// values in step when either changes.
	CordonTotalBudget = CordonRetryDelay*CordonRetryAttempts + APITimeout
)

// BootState describes what the boot unit decided to do.
type BootState string

const (
	// BootStateNotArmed means the journal shows no arming state and the
	// boot was clean: the unit exits immediately and kubelet starts normally.
	BootStateNotArmed BootState = "not-armed"

	// BootStateCordoned means the unit successfully cordoned the node and
	// published the rejoin condition. In base mode the unit then exits and
	// kubelet starts (new workloads are blocked by the cordon).
	BootStateCordoned BootState = "cordoned"

	// BootStateCordonFailed means the unit could not cordon or announce.
	// The node boots normally — this is the base-mode behavior (the cordon
	// is a scheduler signal, not a fence).
	BootStateCordonFailed BootState = "cordon-failed"
)
