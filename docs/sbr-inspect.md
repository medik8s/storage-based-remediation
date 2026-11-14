# Inspecting SBR shared storage

Use the agent image containing `sbr-agent inspect` to collect a read-only JSON
report of the node map, heartbeat slots and fence slots in either storage mode:

```bash
kubectl exec -n sbr-operator-system <agent-pod> -- sbr-agent inspect --sbr-device /dev/sbr/sbr-device -o json > sbr-state.json
```

Use the configured `--sbr-device` path from the pod's `sbr-agent` container
arguments. The example above uses the default filesystem path; use `/sbr-block`
for block mode, or the configured heartbeat file path for a custom mount.

Automatic discovery from `/proc/1/cmdline` works only when PID 1 is `sbr-agent`.
The operator-created agent pod uses the host PID namespace and mounts host
`/proc`, so PID 1 is the host init process and discovery fails there. Pass
`--sbr-device` explicitly in these pods. The command does not contact Kubernetes.
Also specify the path explicitly with a wrapper process, a shared process
namespace, or an offline copy:

```bash
# Filesystem heartbeat file (or its containing directory)
sbr-agent inspect --sbr-device /dev/sbr/sbr-device -o json
# Raw block device, or a block-format image copied for diagnosis
sbr-agent inspect --sbr-device /sbr-block -o json
# Increase the overall deadline on slow storage
kubectl exec -n sbr-operator-system <agent-pod> -- \
  sbr-agent inspect --sbr-device /dev/sbr/sbr-device --timeout 60s --output json
```

Use `-c <agent-container>` with `kubectl exec` if the pod has multiple containers.
The `inspect` subcommand must precede its flags. Only JSON output is supported;
it is indented for human reading. `inspect --help` lists the flags.

## Safety and format handling

Inspection dispatches before normal agent startup. It does not initialize or
repair storage, run preflight write checks, create/reset readiness markers,
start metrics, contact Kubernetes, assign node IDs, acquire write locks, or
open/pet/disarm a watchdog. All device descriptors are opened read-only and
without create/truncate flags. It may be used while agents are running or while
a block device is quiesced. Ordinary filesystem access-time metadata may change
as a consequence of reading, according to mount options.

- **Filesystem mode:** reads the heartbeat file and its `-fence` and `-nodemap`
  companions. Slots use `nodeID * 512`; reserved slot zero is excluded. All
  IDs 1–255 are considered. Legacy initialization allocates only 255 slots
  including slot zero, so a wholly absent slot 255 is treated as unused until
  its first write. Partial slots and shorter files are reported as errors.
- **Block mode:** validates superblock magic, version, CRC and the V1 layout
  before using its offsets. Reads use O_DIRECT and aligned buffers, with no
  buffered fallback for block-format data. Slots use region offset plus
  `(nodeID - 1) * 4096`. The existing double-buffered node-map reader selects
  the highest valid generation, including fallback to the other valid buffer.
  Unknown or corrupt recognized superblocks are rejected, never repaired.
- Protocol and node-map decoding use the same internal packages as the agent.
  Slot header CRCs, version, message type and slot identity are checked.
  The existing fence wire format does **not** checksum its target/reason
  payload; inspection cannot add integrity guarantees absent from that format.

Filesystem reads prefer direct I/O where supported and otherwise open files
anew for buffered reads, preserving close-to-open behavior on NFS. Visibility
still depends on the shared filesystem and its mount options.

The default 30-second deadline bounds the whole storage inspection, including
path discovery, opens and reads. On timeout the command exits unsuccessfully.
A kernel I/O request stuck in uninterruptible sleep can still delay process
reaping; the command cannot repair or cancel a failed storage backend.

## JSON and exit status

| Field | Meaning |
|---|---|
| `schema_version` | Output schema version, currently `1` |
| `mode`, `device`, `observed_at` | Detected mode, resolved heartbeat/device path and observation start time (UTC) |
| `superblock` | Block header, including layout and quiesce flags; omitted in filesystem mode |
| `node_map_status` | `valid`, `absent` (not yet registered), or `invalid` |
| `node_map` | Decoded node-map table, or `null` when absent/invalid |
| `heartbeats`, `fences` | Nonzero usable slots in ascending node ID order; empty arrays when unused |
| `errors` | Read/decode failures; an empty array means no such failures were detected |

Each slot includes `node_id` (the physical slot owner), absolute byte `offset`,
`sender_node_id`, `type`, nanosecond-precision UTC `timestamp` and `sequence`.
Fence slots also contain `target_node_id`, numeric `reason` and `reason_name`.
A fence's sender may differ from its slot owner; its target must match that
owner. A cleared fence is shown as reason `0` / `NONE`, not an active request.
A nonzero corrupt slot remains in the report with an `error` instead of being
silently omitted. Data decoded from a slot with an error must not be trusted.

Exit status is zero for successful inspection, including initialized empty
storage or an absent node map. Missing heartbeat/fence files, corrupt records,
invalid superblocks, I/O failures, invalid flags, deadlines and output failures
produce a nonzero exit. Where possible, the command writes the diagnostic JSON
with an `errors` array **before** exiting nonzero. Fatal setup/superblock errors
or timeouts may produce no JSON. Error messages go to stderr, not stdout.

This is a live observation, **not an atomic snapshot or a cluster health
verdict**. Agents may update the map and slots between reads; compare repeated
reports when investigating inconsistent observations. A valid older node-map
buffer can mask a torn newer buffer, just as it can for the running agent.
No freshness, fencing completion, or cross-node storage-coherency guarantee is
implied by exit status zero. The report exposes node names and cluster identity;
handle it as cluster diagnostic data.

## Tests

Focused tests require Linux and the Go toolchain declared in `go.mod`, but no
Kubernetes or OpenShift cluster:

```bash
go test -mod=mod ./internal/inspection ./cmd/sbr-agent ./test/utils -run '^TestInspect' -count=1
go test -mod=mod -race ./internal/inspection ./cmd/sbr-agent ./test/utils -run '^TestInspect' -count=1
```

Block tests use regular image files with real O_DIRECT reads. They verify both
slot boundaries, CRC/layout rejection, node-map generation selection/fallback,
quiesce visibility, missing/truncated storage, corrupt records, and unchanged
storage bytes. CLI tests cover JSON, errors, PID 1 argument discovery, main
subcommand dispatch and a blocked-read timeout. Test utility tests verify the
`kubectl exec` command, explicit device selection from pod arguments for host-PID
pods (including custom filesystem paths and block mode), missing configuration,
and preservation of failure diagnostics.

`testSBRInspection()` now uses this CLI in both `fs` and `block` E2E runs; its
block-mode skip and raw `dd` parsing are removed. Deploy the rebuilt agent image
before running the test. Live cluster E2E remains necessary to validate CSI
block devices, shared-filesystem visibility and the deployed container path.
