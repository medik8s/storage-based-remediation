# AGENTS.md — Storage Based Remediation Operator

## Medik8s context

SBR is one of several independent remediation providers in the [medik8s](https://medik8s.io) family.
The orchestrator is **Node Healthcheck Operator (NHC)**: it watches `NodeConditions`, and when a node is unhealthy, creates a `StorageBasedRemediation` CR using the `StorageBasedRemediationTemplate` configured by the admin. SBR then fences the node; NHC tracks the result. 
Other medik8s remediators (SNR, MDR, FAR) implement the same NHC template contract and can be used alongside or instead of SBR depending on the cluster's fencing capabilities.

## What SBR does

Implements cloud-native SBD (Storage-Based Death / STONITH Block Device) for Kubernetes clusters that lack out-of-band power management (IPMI/BMC). It uses shared RWX storage for node heartbeats and fence messages. The operator reconciles StorageBasedRemediationConfig resources; each agent embeds the remediation reconciler. Peer agents process StorageBasedRemediation requests and write fence messages. The target agent reads its fence slot and initiates self-fencing; the watchdog is a separate reboot mechanism, not a reader of storage slots. Detect-only mode disables remediation and watchdog arming.

Two storage backends are supported:
- **Filesystem mode** — RWX PVC mounted as a filesystem; requires compatible shared storage and cache-coherency settings; NFS mount options are validated
- **Block mode** — RWX raw block PVC; requires a storage class that supports true concurrent O_DIRECT writes from all nodes

## Repository layout

```
api/v1alpha1/           CRD types: StorageBasedRemediation, Config, Template + webhook
internal/
  controller/           Controller reconcilers (SBR + SBRConfig)
  agent/                Agent constants and shared configuration
  storage/              Storage backend abstraction (filesystem + block modes)
  blockdevice/          Block-mode raw device I/O (O_DIRECT, Linux-only)
  blockformat/          On-disk slot layout / SBD block formatting
  sbdprotocol/          SBD message protocol (heartbeat / poison messages)
  watchdog/             Watchdog device abstraction
  retry/                Retry helpers
  mocks/                Generated mocks for unit tests
  version/              Operator version package
cmd/                    operator main package
cmd/sbr-agent/          Agent runtime and embedded remediation controller (DaemonSet)
test/e2e/               Ginkgo v2 e2e suite
test/destructive/       Destructive fencing tests
test/utils/             Shared test helpers
hack/                   Dev/debug scripts (includes testing_block_mode.sh for block storage testing)
docs/                   Design docs (coordination, webhook validation, storage class, etc.)
examples/               Sample CRs and storage class manifests
config/                 Kustomize bases, RBAC, webhook and bundle inputs
bundle/                 Generated OLM bundle (manifests, metadata, tests)
catalog/                Generated OLM catalog (FBC) for the operator
```

## Build & test

Use the Go toolchain declared in `go.mod` (currently Go 1.26.5), a running
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

# Lint (installer failure recorded in docs/agents-command-audit.md)
make lint
make lint-fix

# e2e tests (cluster must have SBR already deployed)
make test-e2e
```

## Local development & deployment

Deploying to a dev cluster is standardized across all medik8s operators via the
shared dev environment in [`medik8s/tools`](https://github.com/medik8s/tools)
(`dev/dev.mk`). The Makefile pulls these targets in automatically: it uses a
sibling `../tools` checkout if present, otherwise shallow-clones the repo into
`.tools/` on first `make dev-*` use.

```bash
make dev-setup       # Create a Kind cluster (1 control-plane + 3 workers) with deps
make dev-deploy      # Build image, load it, install CRDs, deploy the operator
make dev-describe    # Summarize nodes, pods, CRs, leases, and events
make dev-redeploy    # Rebuild operator image and restart operator pods
make dev-undeploy    # Remove the operator
make dev-teardown    # Destroy the Kind cluster
make dev-help        # List all dev-* targets
```

Deploy to an existing cluster (OCP, etc.) with `SKIP_KIND=true`; images are
pushed to the ephemeral `ttl.sh` registry:

```bash
export KUBECONFIG=~/.kube/my-cluster
export SKIP_KIND=true
make dev-setup dev-deploy
```

The shared Kind setup does not provision RWX storage. It can exercise operator
deployment and reconciliation, but the storage-fencing flow needs suitable shared
block/filesystem storage and a working watchdog. See
[`dev/README.md`](https://github.com/medik8s/tools/blob/main/dev/README.md) in
`medik8s/tools` for prerequisites, all targets, and per-operator coverage.

## Code style

- Go, Kubebuilder v4, controller-runtime; follows standard medik8s patterns.
- Imports must be sorted (`make fix-imports` / `make test-imports`).
- Run `make fmt vet` before committing.
- No new direct commits to `main`; open a PR.

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
- Never loosen RBAC beyond the generated `config/rbac/` manifests without a review.

## Keeping the docs current

If your changes affect anything described here — build commands, repo layout, CRD semantics, storage constraints, security posture, test setup — or any other existing documentation (`README.md`, `CONTRIBUTING.md`, anything under `docs/`, inline command or usage references), update all of it so the docs never drift from the code.

## Commit conventions

- Reference the relevant issue or PR number when applicable.
- Use WIP in title when creating draft PRs to save CI resources.
