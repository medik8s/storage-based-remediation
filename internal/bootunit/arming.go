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

import "fmt"

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

	// JournalUnusable reports whether the journal could not be read or
	// trusted at all: missing, unreadable, or corrupt.
	JournalUnusable() bool
}

// EvaluateArming applies the arming tiers from the design: the cost of the
// action is matched to the strength of the evidence. Over-cordon on weak
// evidence, never under-cordon; hold only on strong evidence.
//
// Tiers:
//   - The journal is unusable: cordon. The unit only runs when the agent's
//     arming flag exists, so a missing or corrupt journal next to a present
//     flag means the flag outlived or outpaced the journal — fail closed,
//     because a cordon is reversible and a missed one is not.
//   - A reboot decision is recorded: cordon (strongest evidence)
//   - Unclean shutdown and storage was unhealthy at the last write: cordon
//     (covers crashes nobody planned: the watchdog fires when feeding stops,
//     which also means an agent that died recording nothing)
//   - Unclean shutdown and storage was healthy: normal boot
//   - Clean shutdown, no arming state: normal boot
func EvaluateArming(journal ArmingJournal) ArmingDecision {
	if journal == nil {
		return ArmingDecision{Armed: false, Reason: "no journal provided"}
	}

	if journal.JournalUnusable() {
		return ArmingDecision{
			Armed:  true,
			Reason: "journal missing or unreadable while the arming flag exists; failing closed",
		}
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

// String returns a human-readable representation for logging.
func (d ArmingDecision) String() string {
	return fmt.Sprintf("Armed=%v Reason=%q", d.Armed, d.Reason)
}
