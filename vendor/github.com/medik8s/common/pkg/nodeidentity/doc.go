// Package nodeidentity lets a process running on a Kubernetes node authenticate
// as that node and manage its OWN Node object, using the kubelet client
// certificate material that already exists on the host.
//
// # Scope and ownership
//
// The package is deliberately narrow: GET Nodes (its own, and reads of others),
// PATCH its own Node's status subresource, and PATCH exactly one field of its
// own Node's spec — the cordon flag, spec.unschedulable. It grants no taint,
// label, or other-node mutation. The Node authorizer and NodeRestriction define
// what node identity may do at the API server; empirically (OCP 4.22 /
// Kubernetes 1.35), custom status conditions from node identity are accepted,
// the cordon flag is accepted in both directions, and taint writes are refused
// (403) — including the node-restriction.kubernetes.io key prefix, which reads
// like a sanctioned self-taint path in the documentation but is not one.
// Consumers must not assume permissions beyond what they have verified on
// their target platforms; RunPermissionMatrix is the executable form of that
// verification.
//
// The cordon flag is the one scheduling write the node identity holds, and it
// is scheduling-only: a cordon blocks NEW pods from being placed on the node;
// it never stops kubelet from restarting pods already bound to it, and it
// never evicts anything. Callers using it as rejoin protection (for example a
// pre-kubelet self-cordon) must not treat it as isolation.
//
// A successful call proves delivery of a request, nothing else: not workload
// state, not workload isolation, not authorization for any remediation action.
//
// # Conditional writes and errors
//
// PatchOwnNodeCondition is a convenience for independent status observations;
// its cached resourceVersion is shared by all users of a Client. Decisions
// derived from a particular GET must use PatchOwnNodeConditionAtVersion with
// that snapshot's version, and reconsider the decision after a conflict.
// PatchOwnNodeStatus is a raw escape hatch: its caller must supply an atomic
// expected-UID guard. Post-response validation cannot undo an unguarded write.
//
// ErrAPIServer is not a connectivity verdict: it also covers received HTTP
// failures and TLS validation errors. Transport and body-read causes remain
// available through errors.Is/errors.As. Do not use this sentinel alone as
// an autonomous-remediation trigger.
//
// # Configuration and credentials
//
// Config is fully explicit. A consumer running while the node is healthy (for
// example an agent's enrollment routine) resolves the node name, Node UID, the
// direct host-routable API server endpoint, and the certificate, key, and CA
// paths, and records them; a consumer running while kubelet is stopped (for
// example a boot-hold reporter) reads that record and performs no discovery.
// The recorded endpoint must be the node kubeconfig's direct endpoint, never
// kubelet's local proxy endpoint: on some platforms kubelet talks through a
// local proxy whose static pod does not start when kubelet does not.
//
// # Certificate lifecycle
//
// The kubelet binary is the only rotator of the node client certificate. While
// a node is held (kubelet stopped), the certificate only ages; expiry during a
// hold is an expected state for the consumer to detect and report, not a
// condition this package remedies. This package never disables TLS
// verification: there is no InsecureSkipVerify escape hatch.
//
// # Transport: HTTP/1.1 only, no keep-alives
//
// The transport is HTTP/1.1 only (a custom tls.Config suppresses Go's HTTP/2
// upgrade) and disables keep-alives, so every request performs a fresh TLS
// handshake. The client certificate is supplied through a per-handshake
// callback that re-reads it from disk, so every request presents the
// certificate currently on disk, with no connection pool that could survive a
// credential change and no eviction logic. At reporting cadences (a GET+PATCH
// pair per heartbeat) the cost of a handshake per request is negligible. Do
// not "fix" this into pooled connections.
//
// Conditions written through this package may be merged or re-published by
// their owners; consumers that need a condition to persist must re-publish it
// on a heartbeat rather than assume the API server preserves it.
//
// # Trust boundary
//
// Host root can read the credentials this package uses. The package adds no
// protection against a compromised host and must not be treated as an
// attestation. Bearer-token kubelet authentication (managed clouds) is out of
// scope; the explicit Config does not preclude adding it.
package nodeidentity
