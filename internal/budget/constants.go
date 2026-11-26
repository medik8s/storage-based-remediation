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

package budget

// StateKey is the Agent.Extra key the ledger serializes under. The
// nodestate library's retention never prunes the Extra map, which is what
// makes this the authoritative home the library's doc.go prescribes for
// "authoritative SBR identity and reset-budget records".
const StateKey = "sbr/resetBudget"

// LocalEpochID prefixes outage IDs assigned by this agent, so they can
// never collide with an EpisodeID stamped by a node health check (the
// nodestate RebootEntry contract treats those as opaque).
const LocalEpochID = "local-"

// MaxTrackedAttempts bounds the persisted attempt list. It is a provisional
// value: the current epoch and its attempts are never compacted, so the
// bound only trims resolved-epoch history, and exceeding it can never hand
// the node a fresh budget for the current outage. Full cleanup at CR
// deletion is Story D7 (design line 947: "keep only the saved attempt, its
// outage information, and the boot observation").
const MaxTrackedAttempts = 64

// SchemaVersion is the current ledger format version. A persisted state
// with a different version is treated as unknown history rather than
// decoded partially.
const SchemaVersion = 1

// Report tokens from design §5.10 (the fixed reason list published through
// status and events).
const (
	// ReasonBudgetExhausted is reported when trusted history shows the
	// epoch's autonomous attempt is already consumed: the agent keeps
	// feeding the watchdog and does not reboot (design §5.6 line 563).
	ReasonBudgetExhausted = "BudgetExhausted"

	// ReasonHistoryUnknown is reported when the budget history could not be
	// read: an attempt remains permitted after the fresh §5.6 checks, and
	// the peer-visible budget must say the history is unknown.
	ReasonHistoryUnknown = "HistoryUnknown"
)
