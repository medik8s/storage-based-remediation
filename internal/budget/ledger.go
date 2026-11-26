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

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Ledger is the in-memory authority over the reset budget (design §5.6).
// It owns one State, mirrors it to the journal through the caller's
// persist function, and answers the only question the fence path asks:
// may this node make another autonomous reboot attempt right now?
//
// All decisions follow the design's asymmetry: a refused autonomous reboot
// is recoverable (the watchdog keeps being fed, storage may recover, the
// epoch clears), while an unbounded reboot loop is not. Every operation
// that could lose a charge is conservative — charges are irreversible,
// compaction never touches the current epoch, and an unreadable journal
// yields unknown history, which permits an attempt but is reported as
// such, never silently treated as an unused allowance.
//
// The ledger is safe for concurrent use: the fence path charges under the
// agent's fence-execution latch, while the heartbeat path observes storage
// recovery — two loops that share no lock of their own.
type Ledger struct {
	mu sync.Mutex

	state State

	// persist mirrors a mutated state to the journal. It is best-effort by
	// contract: a failed persist never reverses an in-memory decision
	// (design §5.6 line 650 — a failed journal write does not prevent an
	// otherwise permitted reboot), and the next successful persist catches
	// up. Nil disables persistence. It is called under the ledger lock, so
	// flushes are serialized and observe mutations in order.
	persist func(State) error

	// dirty records that memory has moved ahead of the journal: a flush
	// failed. Charge and ObserveHealthyFor are both best-effort, so the write
	// is retried on the next observation instead of only at the next charge —
	// otherwise a journal left holding a cleared epoch's charge would
	// re-impose it after a restart.
	dirty bool
}

// NewLedger builds the process-local ledger over a recovered state. A nil
// recovered state starts unknown history: in-memory only, attempts
// permitted, reported as HistoryUnknown (design §5.6 line 652).
func NewLedger(recovered *State, persist func(State) error) *Ledger {
	state := State{SchemaVersion: SchemaVersion}
	if recovered != nil && recovered.SchemaVersion == SchemaVersion {
		state = *recovered
	}
	return &Ledger{state: state, persist: persist}
}

// PermitsAutonomous reports whether the node may make another autonomous
// reboot attempt in the current outage (design §5.6 lines 648–654). The
// answer is no only when trusted history shows the current unresolved
// epoch's autonomous attempt is already consumed; unknown history permits
// the attempt and must be reported through HistoryUnknown().
func (l *Ledger) PermitsAutonomous() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.CurrentEpoch == nil {
		return true
	}
	return !l.state.CurrentEpoch.AutonomousCharged
}

// Exhausted returns the report token for a refused autonomous attempt
// (design §5.6 line 563). The gate only refuses on a charge the ledger
// itself knows about, so the refusal is always BudgetExhausted;
// HistoryUnknown is the separate, permit-time condition reported through
// HistoryUnknown().
func (l *Ledger) Exhausted() string {
	return ReasonBudgetExhausted
}

// HistoryUnknown reports whether the ledger runs without trusted history.
func (l *Ledger) HistoryUnknown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.state.HistoryKnown
}

// Charge records a reboot commitment against the current outage (design
// §5.7). The first charge of either kind opens the epoch; only autonomous
// attempts consume the allowance. Charging is idempotent by identity — the
// same attempt ID in the same epoch is the same charge, never two — and
// irreversible: nothing un-charges an attempt, because a late or repeated
// write must not reverse a decision or inflate a budget (design §5.6
// line 630).
//
// The epoch ID is assigned here when none is open, so every attempt of one
// outage shares it across restarts. Callers may supply the node health
// check's episode ID when it is known; otherwise a local ID is generated.
func (l *Ledger) Charge(a Attempt, now time.Time) error {
	if a.ID == "" {
		return fmt.Errorf("attempt ID must not be empty")
	}
	if a.EpochID == "" {
		return fmt.Errorf("attempt epoch ID must not be empty")
	}
	switch a.Kind {
	case KindAutonomous:
		a.Counted = true
	case KindRequested:
		a.Counted = false
	default:
		return fmt.Errorf("attempt kind must be %q or %q, got %q", KindAutonomous, KindRequested, a.Kind)
	}
	a.TS = now

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state.CurrentEpoch == nil {
		l.state.CurrentEpoch = &Epoch{ID: a.EpochID, OpenedAt: now, LastChargeAt: now}
	} else {
		// An open epoch keeps its identity even if the caller derived a
		// fresh one: the outage is defined by the ledger, not by the
		// caller's clock.
		a.EpochID = l.state.CurrentEpoch.ID
		l.state.CurrentEpoch.LastChargeAt = now
	}

	// Idempotent by identity: an attempt already charged to this epoch is
	// the same decision replayed (a restart reconcile, or a retry of the
	// journal write), not a new charge.
	for _, existing := range l.state.Attempts {
		if existing.ID == a.ID && existing.EpochID == a.EpochID {
			return nil
		}
	}

	l.state.Attempts = append(l.state.Attempts, a)
	if a.Counted {
		l.state.CurrentEpoch.AutonomousCharged = true
	}
	// A charging ledger is the authority over its own history from here
	// on: the pre-charge unknown-ness cannot un-consume this epoch, and
	// every future restart reads the state this charge founded.
	l.state.HistoryKnown = true
	l.compact()
	// The persist is best-effort by contract (design §5.6 line 650: a
	// failed journal write does not prevent an otherwise permitted
	// reboot): its error is the persist function's to report, never a
	// reason to fail the in-memory charge.
	_ = l.flush()
	return nil
}

// ObserveHealthySince applies the recovery rule (design §5.6 line 654): the
// epoch clears only once storage has been healthy for the whole recovery
// period, and that period is measured from the later of the healthy-since
// instant and the epoch's own last charge. A clock that started before the
// charge says nothing about recovery after it, so measuring from it would let
// one charge be cleared by evidence that predates it.
//
// The clamp and the clear run under a single lock, in one call: a caller that
// read the charge time separately and then observed could have a concurrent
// Charge land in between and see its brand-new epoch cleared by the older
// timestamp.
func (l *Ledger) ObserveHealthySince(healthySince, now time.Time, recoveryPeriod time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.CurrentEpoch == nil {
		// The epoch may already have been cleared by an earlier call whose
		// journal write failed. Retry here rather than returning: a journal
		// still holding that epoch's charge would re-impose it on restart.
		if l.dirty {
			_ = l.flush()
		}
		return
	}
	start := healthySince
	if last := l.state.CurrentEpoch.LastChargeAt; last.After(start) {
		start = last
	}
	if recoveryPeriod > 0 && now.Sub(start) >= recoveryPeriod {
		l.state.CurrentEpoch = nil
		_ = l.flush()
	}
}

// ObserveHealthyFor is ObserveHealthySince for callers that only know how long
// storage has been healthy. It derives the instant and delegates, so the clamp
// applies identically and the two cannot diverge.
func (l *Ledger) ObserveHealthyFor(healthyFor time.Duration, recoveryPeriod time.Duration) {
	now := time.Now()
	l.ObserveHealthySince(now.Add(-healthyFor), now, recoveryPeriod)
}

// DropForeignEpoch clears the current epoch when every charge that produced it
// is explicitly bound to a different Node object, so a replacement Node does
// not inherit its predecessor's spent budget. The cluster deletes a Node object
// as part of evacuating it, so the new incarnation's history starts clean.
//
// Charges with no recorded Node UID cannot be validated and are kept, and an
// epoch no recorded attempt refers to is kept too: both leave the budget in the
// conservative direction. The dropped charges stay in the history as
// diagnostics; nothing is deleted.
func (l *Ledger) DropForeignEpoch(nodeUID string) bool {
	if nodeUID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.CurrentEpoch == nil {
		return false
	}
	matched := false
	for _, a := range l.state.Attempts {
		if a.EpochID != l.state.CurrentEpoch.ID {
			continue
		}
		matched = true
		if a.NodeUID == "" || a.NodeUID == nodeUID {
			return false
		}
	}
	if !matched {
		return false
	}
	l.state.CurrentEpoch = nil
	_ = l.flush()
	return true
}

// Publishable is the §5.9 Budget view a peer or reporter publishes into the
// Node annotation record: whether the history is known, and the outage IDs
// of the recorded autonomous attempts.
func (l *Ledger) Publishable() (historyKnown bool, outageIDs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.state.Attempts {
		if a.Kind == KindAutonomous {
			outageIDs = append(outageIDs, a.EpochID)
		}
	}
	return l.state.HistoryKnown, outageIDs
}

// State returns a copy of the durable state.
func (l *Ledger) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stateCopy()
}

// stateCopy snapshots the state without locking. Callers must hold l.mu —
// including flush, which runs under the lock and must not re-enter it.
func (l *Ledger) stateCopy() State {
	out := State{
		SchemaVersion: l.state.SchemaVersion,
		HistoryKnown:  l.state.HistoryKnown,
	}
	if l.state.CurrentEpoch != nil {
		epoch := *l.state.CurrentEpoch
		out.CurrentEpoch = &epoch
	}
	out.Attempts = append([]Attempt(nil), l.state.Attempts...)
	return out
}

// compact bounds the persisted history. The current epoch's attempts are
// never dropped — dropping them would hand a looping node a fresh budget.
func (l *Ledger) compact() {
	if len(l.state.Attempts) <= MaxTrackedAttempts {
		return
	}
	current := ""
	if l.state.CurrentEpoch != nil {
		current = l.state.CurrentEpoch.ID
	}
	kept := make([]Attempt, 0, MaxTrackedAttempts)
	for _, a := range l.state.Attempts {
		if a.EpochID == current {
			kept = append(kept, a)
			continue
		}
		if len(kept) < MaxTrackedAttempts {
			kept = append(kept, a)
		}
	}
	l.state.Attempts = kept
}

func (l *Ledger) flush() error {
	if l.persist == nil {
		return nil
	}
	// stateCopy, not State: flush runs under l.mu and must not re-enter it.
	if err := l.persist(l.stateCopy()); err != nil {
		l.dirty = true
		return err
	}
	l.dirty = false
	return nil
}

// NewLocalEpochID assigns an outage ID for an autonomous charge. It is
// prefixed so it can never collide with a node health check's EpisodeID.
func NewLocalEpochID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate epoch ID: %w", err)
	}
	return LocalEpochID + hex.EncodeToString(b[:]), nil
}
