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

// Package budget implements the reset-budget ledger from design §5.6: at
// most one autonomous reboot attempt per outage period, durably recorded so
// a restart cannot hand the node a fresh budget, with requested reboots
// marking the outage without consuming the autonomous allowance.
//
// The ledger is the authoritative consumer state the nodestate library
// prescribes: it serializes into Document.Agent.Extra, which the library's
// retention never prunes. The diagnostic Reboots history and its
// CountReboots helper remain advisory; this package's State decides.
package budget

import "time"

// AttemptKind distinguishes who authorized a reboot (design §5.7): a
// requested attempt serves a remediation CR and is identified by that CR's
// UID; an autonomous attempt was decided by the agent's own evidence and
// carries a locally assigned ID that can never execute a request or gate
// OutOfService.
type AttemptKind string

const (
	KindRequested  AttemptKind = "Requested"
	KindAutonomous AttemptKind = "Autonomous"
)

// Epoch is one outage period (design §5.6 glossary): an interval without
// sustained storage recovery. It opens when an attempt is charged — the
// first charge of either kind marks the outage — and clears only after the
// agent observes healthy storage for the configured recovery period. A
// requested reboot marks the epoch without consuming the autonomous
// allowance (design line: "a requested reboot neither counts toward the
// autonomous limit nor permits another autonomous reboot in the same
// outage").
type Epoch struct {
	// ID identifies the outage. Locally assigned IDs carry the LocalEpochID
	// prefix so they can never collide with an EpisodeID stamped by a node
	// health check; the field is opaque to this package.
	ID string `json:"id"`
	// OpenedAt is when the epoch was first charged. Diagnostic.
	OpenedAt time.Time `json:"openedAt"`
	// LastChargeAt is when the most recent attempt was charged. Diagnostic.
	LastChargeAt time.Time `json:"lastChargeAt"`
	// AutonomousCharged reports whether the epoch's autonomous attempt has
	// been consumed.
	AutonomousCharged bool `json:"autonomousCharged"`
}

// Attempt is one charged reboot commitment (design §5.7): the identity of
// what authorized it, the outage it belongs to, and the boot it was decided
// in. Attempts are write-only history — a charge is irreversible within its
// epoch and idempotent by identity, so a restart reconcile cannot double
// charge the same decision.
type Attempt struct {
	// ID is the attempt identity: the CR UID for a requested attempt, or the
	// fence decision's incident ID for an autonomous one.
	ID string `json:"id"`
	// EpochID is the outage the attempt was charged to.
	EpochID string `json:"epochID"`
	// Kind reports who authorized the reboot.
	Kind AttemptKind `json:"kind"`
	// NodeUID binds the attempt to the Node object it was decided for, so a
	// replacement Node cannot inherit this host's budget.
	NodeUID string `json:"nodeUID,omitempty"`
	// ConfigUID binds the attempt to the configuration it ran under.
	ConfigUID string `json:"configUID,omitempty"`
	// BootID is the kernel boot ID current when the attempt was charged.
	BootID string `json:"bootID,omitempty"`
	// TS is when the attempt was charged.
	TS time.Time `json:"ts"`
	// Counted reports whether the attempt consumes the autonomous allowance.
	// Only autonomous attempts count (design §5.6 line 620).
	Counted bool `json:"counted"`
}

// State is the durable ledger serialized into the journal's Agent.Extra
// under StateKey. It holds the current unresolved epoch and the bounded
// attempt history. A missing or unreadable State means the history is
// unknown — never an unused allowance (design §5.6 line 652: unknown
// history permits a reboot after the fresh checks, and is reported as
// HistoryUnknown).
type State struct {
	// SchemaVersion is the ledger format version.
	SchemaVersion int `json:"schemaVersion"`
	// CurrentEpoch is the unresolved outage, or nil when storage has
	// recovered since the last charge. It is never dropped by compaction.
	CurrentEpoch *Epoch `json:"currentEpoch,omitempty"`
	// Attempts is the charged history, oldest first, bounded by
	// MaxTrackedAttempts. The current epoch's attempts are never compacted.
	Attempts []Attempt `json:"attempts,omitempty"`
	// HistoryKnown reports whether this State was read from a trusted
	// journal. False means the ledger runs on in-memory fallback and the
	// peer-visible history must be reported as Unknown.
	HistoryKnown bool `json:"historyKnown"`
}
