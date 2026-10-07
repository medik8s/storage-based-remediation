# AGENTS.md — Storage Based Remediation Operator

> **IMPORTANT — read this first.** Before making any changes in this repository, you MUST
> read the medik8s **common agent guide**, the **OFFICIAL guidance** for all medik8s
> operators: **https://github.com/medik8s/.github/blob/main/AGENTS.md** . It is
> authoritative project guidance and must not be ignored.

## Medik8s context

SBR is one of several independent remediation providers in the [medik8s](https://medik8s.io) family.
The orchestrator is **Node Healthcheck Operator (NHC)**: it watches `NodeConditions`, and when a node is unhealthy, creates a `StorageBasedRemediation` CR using the `StorageBasedRemediationTemplate` configured by the admin. SBR then fences the node; NHC tracks the result.
Other medik8s remediators (SNR, MDR, FAR) implement the same NHC template contract and can be used alongside or instead of SBR depending on the cluster's fencing capabilities.

## What SBR does

Implements cloud-native SBD (Storage-Based Death / STONITH Block Device) for Kubernetes clusters that lack out-of-band power management (IPMI/BMC). It uses shared RWX storage for node heartbeats and fence messages. The operator reconciles StorageBasedRemediationConfig resources; each agent embeds the remediation reconciler. Peer agents process StorageBasedRemediation requests and write fence messages. The target agent reads its fence slot and initiates self-fencing; the watchdog is a separate reboot mechanism, not a reader of storage slots.

SBR also acts as a node health detector: agents monitor peer heartbeats on shared storage and set the `SBRStorageUnhealthy` Node condition when a peer's heartbeat times out. NHC can be configured to watch this condition and trigger remediation. Detection runs in both normal and detect-only modes; detect-only mode disables remediation and watchdog arming while keeping heartbeat monitoring and Node condition updates active.

Two storage backends are supported:
- **Filesystem mode** — RWX PVC mounted as a filesystem; requires compatible shared storage; cache-coherency mount options are set on provisioned NFS/CephFS StorageClasses
- **Block mode** — RWX raw block PVC; requires a storage class that supports true concurrent O_DIRECT writes from all nodes

## Build & test

Use the Go toolchain declared in `go.mod` (currently go 1.26.0, toolchain go1.26.5), a running
container engine, and network access for tool/envtest downloads. Native `vet`
and targets depending on it require Linux. `test-linux` runs vet and tests in
a Linux container but runs generation and formatting on the host first.

```bash
# Unit tests (Linux host; also regenerates the bundle and checks git cleanliness)
make test

# Unit tests (MacOS host)
make test-linux          # runs inside a Linux container; use this on macOS

# Build operator + agent binaries (Linux host)
make build build-agent

# Build both container images (Linux host; override tags for local builds)
make build-images IMG=localhost/sbr-operator:dev AGENT_IMG=localhost/sbr-agent:dev

# Regenerate CRDs + RBAC manifests after API changes
make manifests generate

# Lint
make lint
make lint-fix

# e2e tests (cluster must have SBR already deployed)
make test-e2e
```

## Local development & testing

Follow the shared workflow in the
[common agent guide](https://github.com/medik8s/.github/blob/main/AGENTS.md) — it documents
the standardized `dev-*` make targets (`dev-setup`, `dev-deploy`, `dev-redeploy`,
`dev-undeploy`, `dev-describe`, `dev-help`, …) provided by `medik8s/tools` (`dev/dev.mk`).

**To develop and test against a real OpenShift / Kubernetes cluster**:
`export SKIP_KIND=true` before the `dev-*` targets; images are pushed to `ttl.sh`.

Github CI workflow runs on a Kind cluster (filesystem-mode via the `SETUP_NFS_RWX` /
`SETUP_NULL_DEVICE_WATCHDOG` add-ons; see `medik8s/tools` `dev/README.md`). SBR's
storage-fencing flow needs real shared **RWX storage** — block mode (ODF / Ceph RBD) or an
RWX filesystem StorageClass — which the default cluster setup does not provision.

## Testing

- Unit tests live next to source throughout `api/`, `cmd/sbr-agent/`, and `internal/`; controller integration tests use envtest.
- E2e tests are in `test/e2e/` (Ginkgo v2). They require a running cluster with SBR installed and a valid RWX `StorageClass`.
- Set `KUBECONFIG` for e2e. The suite discovers storage classes and sets watchdog paths in its test configurations; E2e includes disruptive scenarios.

## Key design constraints

- Every participating node must be able to **durably and concurrently write** to its own heartbeat slot. In block mode, the storage **MUST** support direct I/O (O_DIRECT flag).
- The agent runs as a privileged DaemonSet. Filesystem mode mounts the whole host `/dev`; block mode mounts the watchdog parent directory at `/host-dev` so it does not hide the CSI-mapped block device. It also mounts host paths for reboot operations.

## Security

- The agent is configured as privileged with `CAP_SYS_ADMIN` and other capabilities; OpenShift requires a compatible SCC grant.
- The admission webhook validates `StorageBasedRemediationConfig` when installed and enabled. Unit/fake-client tests do not establish admission behavior; verify the live webhook separately when testing validation.
