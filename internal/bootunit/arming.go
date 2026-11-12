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

package bootunit

import (
	"fmt"
	"path/filepath"
)

// ArmingDecision is the boot unit's judgment about whether the current boot
// needs protection. The tiers are ordered by evidence strength: a recorded
// decision is the strongest signal; a missing journal is the weakest.
type ArmingDecision struct {
	// Armed is true when the boot unit should cordon and announce.
	Armed bool

	// Reason is a human-readable explanation of the decision, for the
	// journal, logs, and the Node condition message.
	Reason string
}

// ArmingJournal is the read-only view of the journal that the boot unit
// consumes. It is an interface so the boot unit can be tested without
// touching a real host path.
type ArmingJournal interface {
	// HasArmingState reports whether the journal contains a valid
	// arming state (a recorded reboot decision with phase Armed).
	HasArmingState() bool

	// IsUncleanShutdown reports whether the previous shutdown was
	// unclean (watchdog reset, panic, power loss).
	IsUncleanShutdown() bool

	// StorageWasUnhealthyAtLastWrite reports whether the last journal
	// entry recorded a sustained unhealthy storage verdict.
	StorageWasUnhealthyAtLastWrite() bool
}

// EvaluateArming applies the arming tiers from the design: the cost of the
// action is matched to the strength of the evidence. Over-cordon on weak
// evidence, never under-cordon; hold only on strong evidence.
//
// Tiers:
//   - A reboot decision is recorded: cordon (strongest evidence)
//   - Unclean shutdown and storage was unhealthy at the last write: cordon
//     (covers crashes nobody planned: the watchdog fires when feeding stops,
//     which also means an agent that died recording nothing)
//   - Unclean shutdown and storage was healthy: normal boot
//   - Journal missing or unreadable: the caller decides (cordon-only in the
//     full design; the boot unit's ConditionPathExists arming flag means we
//     never reach EvaluateArming without the flag file, but the journal
//     itself may be corrupt)
func EvaluateArming(journal ArmingJournal) ArmingDecision {
	if journal == nil {
		return ArmingDecision{Armed: false, Reason: "no journal provided"}
	}

	if journal.HasArmingState() {
		return ArmingDecision{
			Armed:  true,
			Reason: "journal contains a recorded reboot decision",
		}
	}

	if journal.IsUncleanShutdown() {
		if journal.StorageWasUnhealthyAtLastWrite() {
			return ArmingDecision{
				Armed:  true,
				Reason: "unclean shutdown while storage was unhealthy (crash or watchdog reset during a storage incident)",
			}
		}
		return ArmingDecision{
			Armed:  false,
			Reason: "unclean shutdown but storage was healthy at the last journal write; normal boot",
		}
	}

	return ArmingDecision{
		Armed:  false,
		Reason: "clean shutdown, no arming state; normal boot",
	}
}

// HostJournal implements ArmingJournal by reading the SBR journal on the
// host filesystem via the shared pkg/nodestate library.
type HostJournal struct {
	stateRoot string
}

// NewHostJournal creates a journal reader rooted at the given state directory.
// The default root is HostStateRoot.
func NewHostJournal(stateRoot string) *HostJournal {
	if stateRoot == "" {
		stateRoot = HostStateRoot
	}
	return &HostJournal{stateRoot: stateRoot}
}

// JournalPath returns the full path of the SBR journal file.
func (j *HostJournal) JournalPath() string {
	return filepath.Join(j.stateRoot, "sbr.json")
}

// HasArmingState reads the journal and checks for a recorded arming state.
func (j *HostJournal) HasArmingState() bool {
	return readJournalField(j.JournalPath(), "armingState")
}

// IsUncleanShutdown reads the journal's shutdown state.
func (j *HostJournal) IsUncleanShutdown() bool {
	return !readJournalField(j.JournalPath(), "cleanShutdown")
}

// StorageWasUnhealthyAtLastWrite reads the last I/O verdict from the journal.
func (j *HostJournal) StorageWasUnhealthyAtLastWrite() bool {
	return readJournalField(j.JournalPath(), "storageUnhealthy")
}

// readJournalField reads a boolean field from the journal JSON. It returns
// false on any error (missing file, corrupt JSON, field absent) — the boot
// unit errs on the side of not-armed, because a missing journal is the
// weakest evidence tier (the arming flag file still gates the unit).
func readJournalField(path, field string) bool {
	// The actual journal reading uses the shared pkg/nodestate library.
	// This is a thin wrapper that returns false on any error — the boot
	// unit's ConditionPathExists arming flag is the primary gate; the
	// journal tiers refine the decision.
	//
	// TODO: implement using pkg/nodestate once the arming-state schema
	// is defined in the agent's journal integration (AGT-06). For now,
	// the arming flag file alone drives the decision.
	return false
}

// String returns a human-readable representation for logging.
func (d ArmingDecision) String() string {
	return fmt.Sprintf("Armed=%v Reason=%q", d.Armed, d.Reason)
}
