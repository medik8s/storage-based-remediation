// Package nodestate stores node-local history for remediation agents.
//
// # Ownership and execution
//
// Each component writes its own JSON file under /var/lib/medik8s/state. A Store
// serializes operations for the same path within one process. The consuming
// agent must hold a stable host executor lock for its lifetime; atomically
// replacing JSON does not exclude another process. The read-only reporter uses
// LoadPeer or Snapshot and never performs mutable recovery.
//
// Keep probe I/O, journal I/O, watchdog feeds and peer reconciliation on separate
// execution paths. File I/O is not cancellable once it enters the kernel. Allow
// only one outstanding journal operation; a timeout does not end that operation.
// A previously established commitment must not be cancelled by a later I/O fault.
//
// # Startup and recovery
//
// Load returns a classification. StatusOK means readable supported contents whose
// file and directory synchronization succeeded. It is not evidence of API
// ownership, a reboot, continuing isolation, or workload-cleanup authorization.
// The consumer must validate its own versioned Agent.Extra values and live
// identities. ReadBootID supplies local kernel identity, not remote reboot proof.
//
// StatusAbsent cannot distinguish first installation from loss of the entire
// directory. Before deliberately initializing with Write, verify that no live
// unresolved execution exists and that all required reset history is known.
// Otherwise remain in HistoryReconciliationRequired. A missing file never proves
// that a request has not already executed.
//
// Corrupt and unsupported files stay at the live path. StatusCorrupt means
// reconciliation is required, not that recovery succeeded. Update and Write
// refuse to replace such history. Operational and access errors are returned,
// without quarantining the file. No event ring or cleanup policy can erase this
// distinction: the original evidence remains until explicitly reconciled.
// ReconcileHistory requires a consumer-authorized reconciliation callback and an
// audit reason. It must preserve known commitments, identities and reset budgets.
// Neither API-object absence nor elapsed time is sufficient authorization.
//
// # Durability
//
// Writes use temporary file, write, file fsync, close, rename and directory fsync.
// Only nil error confirms durability. ErrDurabilityUnconfirmed means the new
// record is visible but MUST NOT authorize a new reset. Do not repeat a
// non-idempotent mutation: pass the returned candidate to ConfirmDurability.
// It checks that the candidate is still current and synchronizes it without
// changing its revision or invoking the mutator. A mismatch is ErrStateChanged.
// Load synchronizes the surviving document after restart. The consumer must
// reconcile that surviving state with live execution records; a missing/older commitment
// is not proof that an earlier uncertain operation did not execute.
//
// Update also confirms the current history before invoking the callback. This
// permits a subsequent transition only after the predecessor is durable. A failed
// predecessor confirmation returns no document and never invokes the callback.
// Read-only peers see complete snapshots, but do not confirm the writer's action
// or grant authority. A host file must never be the sole authority for cleanup.
//
// # Identity, retention and schema evolution
//
// CRRef execution identity is the pair (CR UID, Node UID). Generation is evidence
// metadata, not permission for another reset. References missing either
// UID cannot be matched or merged. They remain requested diagnostic records;
// AppendRebootChecked rejects them for new writes.
//
// Reboots, IO, Events and Conditions are diagnostic collections with retention.
// A consumer keeps its own required state as opaque strings in Agent.Extra:
// its attempt identity, boot-bound commitment and reset accounting, versioned
// by a key of its own. Common stores and returns those strings. It never
// prunes that map, parses a consumer state machine, charges a budget or
// infers that an attempt was accepted. If required records do not fit, writes
// fail with ErrTooLarge rather than dropping them. Unknown JSON fields survive
// supported read-modify-write cycles; exceeding a configured preservation
// limit refuses the operation without deleting fields.
//
// Authority for whether a remediation was accepted, cancelled or completed
// belongs to the consumer's API records, never to this file. The journal
// retains reservations, commitments and accounting so that a restarted agent
// can recognise what it already did.
//
// Snapshot and ReadAll default to this Store's component only. A file left by
// an unrelated controller therefore cannot make this consumer's history
// incomplete. Any controller may own an independent component, and a caller
// that deliberately combines histories names every component it expects.
// KnownComponents is a catalog of conventional names; it does not describe
// what is installed and is not a required peer set.
//
// Snapshot retains failed or cancelled reads. HistoryComplete must be checked
// before treating CountReboots as more than a lower bound. Even a syntactically
// complete diagnostic history is not a replacement for the consumer's own
// authoritative budget records.
// Pruning records with tied timestamps excludes the discarded timestamp from
// the complete interval. A clock rollback cannot restore discarded history.
//
// # Paths and deployment
//
// Reads and writes reject disallowed ownership, permissions and symlinks,
// including ancestor symlinks. These path checks are defense in depth against
// deployment errors, not protection from privileged concurrent ancestor swaps.
// Protect the host directory and hold the executor lock. Compromised host root
// is outside the trust boundary. CheckDurableStorage rejects known unsuitable
// filesystems; it does not prove a correct persistent host mount. The deployment
// must validate its mount (a disk-backed emptyDir is still not host history).
//
// This package and nodestatetest depend only on the Go standard library.
package nodestate
