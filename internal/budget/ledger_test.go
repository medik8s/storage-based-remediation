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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }

func newTestLedger(t *testing.T, recovered *State) *Ledger {
	t.Helper()
	return NewLedger(recovered, func(State) error { return nil })
}

func autonomousAttempt(id, epoch string) Attempt {
	return Attempt{ID: id, EpochID: epoch, Kind: KindAutonomous}
}

func requestedAttempt(id, epoch string) Attempt {
	return Attempt{ID: id, EpochID: epoch, Kind: KindRequested}
}

// Design §5.6 lines 648-654: one autonomous attempt per outage.
func TestPermitsAutonomous_OneAttemptPerOutage(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()

	if !l.PermitsAutonomous() {
		t.Fatal("fresh ledger must permit an autonomous attempt")
	}
	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}
	if l.PermitsAutonomous() {
		t.Fatal("second autonomous attempt in the same outage must be refused")
	}
	if l.Exhausted() != ReasonBudgetExhausted {
		t.Fatalf("refusal token = %q, want %q", l.Exhausted(), ReasonBudgetExhausted)
	}
}

// Design §5.6 line 620: a requested reboot marks the outage without
// consuming the autonomous allowance.
func TestCharge_RequestedDoesNotConsumeAllowance(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()

	if err := l.Charge(requestedAttempt("cr-uid-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}
	if !l.PermitsAutonomous() {
		t.Fatal("a requested charge must not consume the autonomous allowance")
	}
	// But the requested charge opened the epoch: the next autonomous
	// attempt lands in the SAME epoch and consumes it.
	if err := l.Charge(autonomousAttempt("incident-1", "ignored"), now); err != nil {
		t.Fatal(err)
	}
	state := l.State()
	if state.CurrentEpoch == nil || state.CurrentEpoch.ID != "local-epoch-1" {
		t.Fatalf("epoch ID = %+v, want the epoch opened by the requested charge", state.CurrentEpoch)
	}
	if l.PermitsAutonomous() {
		t.Fatal("the autonomous attempt after a requested one must consume the allowance")
	}
}

// Design §5.6 line 630: a charge is irreversible; a replayed identity is
// the same charge, never two.
func TestCharge_IdempotentByIdentity(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()

	for i := 0; i < 3; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), at); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(l.State().Attempts); got != 1 {
		t.Fatalf("replayed charge recorded %d attempts, want 1", got)
	}
}

// Design §5.6 line 654: the epoch clears only after sustained healthy
// storage for the recovery period.
func TestObserveHealthyFor_EpochClearsOnlyAfterFullRecovery(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()
	period := 5 * time.Minute

	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}

	l.ObserveHealthyFor(period-time.Second, period)
	if l.PermitsAutonomous() {
		t.Fatal("recovery one second short must not clear the epoch")
	}

	l.ObserveHealthyFor(period, period)
	if !l.PermitsAutonomous() {
		t.Fatal("a full recovery period must clear the epoch")
	}

	// A later charge opens a new epoch: the recovery bought back exactly
	// one attempt, not an unlimited one.
	if err := l.Charge(autonomousAttempt("incident-2", "local-epoch-2"), now.Add(period)); err != nil {
		t.Fatal(err)
	}
	if l.PermitsAutonomous() {
		t.Fatal("the new epoch's attempt must be consumed again")
	}
}

// A health gap restarts the clock: the caller passes the sustained
// duration, and zero means the epoch survives.
func TestObserveHealthyFor_HealthGapDoesNotClear(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()
	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}

	l.ObserveHealthyFor(4*time.Minute, 5*time.Minute)
	l.ObserveHealthyFor(0, 5*time.Minute) // storage went unhealthy again
	l.ObserveHealthyFor(4*time.Minute, 5*time.Minute)

	if l.PermitsAutonomous() {
		t.Fatal("a health gap must restart the recovery clock")
	}
}

// Design §5.6 line 652: unknown history permits an attempt and is reported
// as HistoryUnknown — never silently treated as an unused allowance.
func TestUnknownHistory_PermitsAndReports(t *testing.T) {
	l := newTestLedger(t, nil) // nil recovered: unknown history

	if !l.PermitsAutonomous() {
		t.Fatal("unknown history must permit an attempt after the fresh checks")
	}
	if !l.HistoryUnknown() {
		t.Fatal("ledger without trusted history must report it")
	}
	// Exhausted is the refusal token and only ever describes a known
	// charge; an unknown-history ledger permits, so its condition is
	// reported through HistoryUnknown, never through Exhausted.
	if l.Exhausted() != ReasonBudgetExhausted {
		t.Fatalf("refusal token = %q, want %q", l.Exhausted(), ReasonBudgetExhausted)
	}

	// The §5.9 view must say the history is unknown.
	known, ids := l.Publishable()
	if known || len(ids) != 0 {
		t.Fatalf("publishable = (%v, %v), want (false, none)", known, ids)
	}
}

// A trusted recovery carries the epoch across the restart.
func TestReconcile_PersistedEpochSurvivesRestart(t *testing.T) {
	l1 := newTestLedger(t, nil)
	now := fixedTime()
	if err := l1.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}

	recovered := l1.State()
	l2 := newTestLedger(t, &recovered)
	if l2.PermitsAutonomous() {
		t.Fatal("a restart must not hand the node a fresh budget for the same outage")
	}
	if l2.Exhausted() != ReasonBudgetExhausted {
		t.Fatalf("token = %q, want %q", l2.Exhausted(), ReasonBudgetExhausted)
	}
}

// A restart reconcile cannot double charge the same decision.
func TestReconcile_NoDoubleChargeAcrossRestart(t *testing.T) {
	l1 := newTestLedger(t, nil)
	now := fixedTime()
	if err := l1.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}

	recovered := l1.State()
	l2 := newTestLedger(t, &recovered)
	// The same decision replayed (a late journal write caught up):
	if err := l2.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(l2.State().Attempts); got != 1 {
		t.Fatalf("restart reconcile recorded %d attempts, want 1", got)
	}
}

// A ledger persisted by a different format version is unknown history, and
// is never silently reinterpreted.
func TestNewLedger_UnsupportedSchemaIsUnknown(t *testing.T) {
	l := NewLedger(&State{SchemaVersion: 99, CurrentEpoch: &Epoch{ID: "x", AutonomousCharged: true}}, nil)
	if !l.HistoryUnknown() {
		t.Fatal("an unsupported schema version must yield unknown history")
	}
	if !l.PermitsAutonomous() {
		t.Fatal("unknown history must permit an attempt")
	}
}

// The §5.9 view: history-known plus the outage IDs of autonomous attempts.
func TestPublishable_ReportsAutonomousOutageIDs(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()
	if err := l.Charge(requestedAttempt("cr-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}
	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), now); err != nil {
		t.Fatal(err)
	}

	known, ids := l.Publishable()
	if !known {
		t.Fatal("trusted ledger must report known history")
	}
	if len(ids) != 1 || ids[0] != "local-epoch-1" {
		t.Fatalf("outage IDs = %v, want [local-epoch-1]", ids)
	}
}

// The persisted state carries the HistoryKnown flag so the recovery is
// truthful after a round-trip.
func TestState_RoundTripKeepsHistoryKnown(t *testing.T) {
	l := newTestLedger(t, nil)
	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), fixedTime()); err != nil {
		t.Fatal(err)
	}

	encoded, err := Encode(l.State())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HistoryKnown {
		t.Fatal("a decoded persisted state must report known history")
	}
	if decoded.CurrentEpoch == nil || decoded.CurrentEpoch.AutonomousCharged != true {
		t.Fatalf("decoded epoch = %+v, want a charged epoch", decoded.CurrentEpoch)
	}
}

// Compaction bounds the history but never the current epoch's attempts.
func TestCompact_NeverDropsCurrentEpoch(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()

	// Fill with resolved-epoch attempts beyond the bound.
	for i := 0; i < MaxTrackedAttempts+10; i++ {
		epoch := "local-old-"
		if err := l.Charge(autonomousAttempt(oldAttemptID(i), epoch), now); err != nil {
			t.Fatal(err)
		}
		l.ObserveHealthyFor(time.Hour, time.Minute) // resolve the epoch
	}
	// Charge the current epoch with more attempts than the bound.
	if err := l.Charge(autonomousAttempt("current-1", "local-current"), now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxTrackedAttempts; i++ {
		if err := l.Charge(requestedAttempt(currentAttemptID(i), "local-current"), now); err != nil {
			t.Fatal(err)
		}
	}

	state := l.State()
	if len(state.Attempts) > MaxTrackedAttempts+len([]string{"current-1"})+MaxTrackedAttempts {
		t.Fatalf("compaction left %d attempts, bound exceeded", len(state.Attempts))
	}
	current := 0
	for _, a := range state.Attempts {
		if a.EpochID == "local-current" {
			current++
		}
	}
	if current != MaxTrackedAttempts+1 {
		t.Fatalf("current epoch attempts = %d, want %d (never compacted)", current, MaxTrackedAttempts+1)
	}
}

func oldAttemptID(i int) string     { return "old-" + string(rune('a'+i%26)) + "-" + itoa(i/26) }
func currentAttemptID(i int) string { return "cur-" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

// A failed persist never reverses an in-memory charge.
func TestCharge_PersistFailureDoesNotReverse(t *testing.T) {
	l := NewLedger(nil, func(State) error { return errors.New("disk full") })
	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), fixedTime()); err != nil {
		t.Fatalf("charge must succeed despite a failed persist, got %v", err)
	}
	if l.PermitsAutonomous() {
		t.Fatal("the in-memory charge must stand when the persist fails")
	}
}

// Validation.
func TestCharge_RejectsMalformedAttempts(t *testing.T) {
	l := newTestLedger(t, nil)
	now := fixedTime()

	if err := l.Charge(Attempt{EpochID: "e", Kind: KindAutonomous}, now); err == nil {
		t.Fatal("empty attempt ID must be rejected")
	}
	if err := l.Charge(Attempt{ID: "a", Kind: KindAutonomous}, now); err == nil {
		t.Fatal("empty epoch ID must be rejected")
	}
	if err := l.Charge(Attempt{ID: "a", EpochID: "e", Kind: "Bogus"}, now); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

// Local epoch IDs carry the prefix that keeps them from colliding with
// node-health-check EpisodeIDs.
func TestNewLocalEpochID_Prefixed(t *testing.T) {
	id, err := NewLocalEpochID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) <= len(LocalEpochID) || id[:len(LocalEpochID)] != LocalEpochID {
		t.Fatalf("epoch ID %q must carry the %q prefix", id, LocalEpochID)
	}
}

// A failed persist is retried on a later observation even after the epoch has
// been cleared: otherwise the journal keeps a charge the ledger has released,
// and a restart would re-impose it.
func TestObserveHealthyFor_RetriesAFailedPersistAfterClear(t *testing.T) {
	var written []State
	fails := true
	l := NewLedger(nil, func(s State) error {
		if fails {
			return errors.New("journal unavailable")
		}
		written = append(written, s)
		return nil
	})

	if err := l.Charge(autonomousAttempt("incident-1", "local-epoch-1"), fixedTime()); err != nil {
		t.Fatal(err)
	}
	// Clearing while the journal is unwritable leaves memory ahead of disk.
	l.ObserveHealthyFor(time.Hour, time.Minute)
	if l.PermitsAutonomous() != true {
		t.Fatal("the epoch must be cleared in memory even when the write fails")
	}
	if len(written) != 0 {
		t.Fatalf("persist succeeded while it was set to fail: %+v", written)
	}

	// The journal recovers; a later observation (epoch already nil) retries.
	fails = false
	l.ObserveHealthyFor(time.Hour, time.Minute)
	if len(written) != 1 {
		t.Fatalf("persisted write count = %d, want 1 retry after the journal recovered", len(written))
	}
	if written[0].CurrentEpoch != nil {
		t.Fatalf("retried state carries epoch %+v, want the cleared state", written[0].CurrentEpoch)
	}
}

// The recovery clamp is applied inside the ledger, under one lock: a charge
// that lands after a stale healthy clock must still cost a full period, and
// there is no window between reading the charge time and clearing the epoch.
func TestObserveHealthySince_ClampsToTheLatestCharge(t *testing.T) {
	l := NewLedger(nil, nil)
	now := fixedTime()
	healthySince := now.Add(-time.Hour) // storage has been healthy for ages

	if err := l.Charge(autonomousAttempt("incident-1", "local-e1"), now); err != nil {
		t.Fatal(err)
	}

	// The old healthy clock must not clear the brand-new charge.
	l.ObserveHealthySince(healthySince, now, 5*time.Minute)
	if l.PermitsAutonomous() {
		t.Fatal("the epoch was cleared by a clock that predates the charge")
	}

	// A full period measured from the charge finally clears it.
	l.ObserveHealthySince(healthySince, now.Add(5*time.Minute), 5*time.Minute)
	if !l.PermitsAutonomous() {
		t.Fatal("a full recovery period after the charge must clear the epoch")
	}
}

// The duration wrapper must apply the same clamp, so a caller cannot bypass it.
func TestObserveHealthyFor_AppliesTheSameClamp(t *testing.T) {
	l := NewLedger(nil, nil)
	if err := l.Charge(autonomousAttempt("incident-1", "local-e1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	l.ObserveHealthyFor(time.Hour, 5*time.Minute)
	if l.PermitsAutonomous() {
		t.Fatal("ObserveHealthyFor must clamp to the charge like ObserveHealthySince does")
	}
}

// A replacement Node does not inherit its predecessor's spent budget.
func TestDropForeignEpoch(t *testing.T) {
	charged := func(nodeUID string) *Ledger {
		l := NewLedger(nil, nil)
		a := autonomousAttempt("i", "local-e")
		a.NodeUID = nodeUID
		if err := l.Charge(a, fixedTime()); err != nil {
			t.Fatal(err)
		}
		return l
	}

	t.Run("foreign node drops the epoch", func(t *testing.T) {
		l := charged("old-node-uid")
		if !l.DropForeignEpoch("new-node-uid") {
			t.Fatal("an epoch charged only by another Node object must be dropped")
		}
		if !l.PermitsAutonomous() {
			t.Fatal("the replacement Node must be allowed one autonomous attempt")
		}
	})

	t.Run("same node keeps the epoch", func(t *testing.T) {
		l := charged("node-uid")
		if l.DropForeignEpoch("node-uid") {
			t.Fatal("an epoch charged by this Node must be kept")
		}
		if l.PermitsAutonomous() {
			t.Fatal("the spent budget must still apply to the same Node")
		}
	})

	t.Run("unverifiable charge is kept", func(t *testing.T) {
		l := charged("")
		if l.DropForeignEpoch("node-uid") {
			t.Fatal("a charge with no Node UID cannot be validated and must be kept")
		}
	})

	t.Run("no UID or no epoch is a no-op", func(t *testing.T) {
		l := charged("old-node-uid")
		if l.DropForeignEpoch("") {
			t.Fatal("without a Node UID nothing can be judged foreign")
		}
		if NewLedger(nil, nil).DropForeignEpoch("node-uid") {
			t.Fatal("an empty ledger has no epoch to drop")
		}
	})
}

// A recovery check must never write a cleared state while a charge younger
// than the recovery period exists. The assertion runs inside the persist hook,
// which is called under the ledger lock at the moment of the write, so a later
// charge cannot mask a premature clear.
func TestObservationCannotClearAChargeWithAStaleClock(t *testing.T) {
	const period = time.Hour
	now := fixedTime()
	stale := now.Add(-2 * period) // alone, this clock would clear immediately

	var clearedWrites int
	l := NewLedger(nil, func(s State) error {
		if s.CurrentEpoch == nil && len(s.Attempts) > 0 {
			clearedWrites++
		}
		return nil
	})

	if err := l.Charge(autonomousAttempt("incident-1", "local-e"), now); err != nil {
		t.Fatal(err)
	}
	l.ObserveHealthySince(stale, now, period)

	if clearedWrites != 0 {
		t.Fatalf("a cleared state was written %d time(s) while a charge younger than the period existed", clearedWrites)
	}
	if l.PermitsAutonomous() {
		t.Fatal("the epoch must still be spent")
	}
}

// The same invariant under concurrent charge/observe pairs: whatever the
// interleaving, no persist call may write a cleared state while a charge
// younger than the recovery period is present. The clamp and the clear share
// one lock, so this holds for every ordering; the race detector checks the
// locking at the same time.
func TestChargeDuringRecoveryObservationIsNeverCleared(t *testing.T) {
	const period = time.Hour
	now := fixedTime()
	stale := now.Add(-2 * period)

	var violations atomic.Int64
	l := NewLedger(nil, func(s State) error {
		if s.CurrentEpoch != nil {
			return nil
		}
		for _, a := range s.Attempts {
			if now.Sub(a.TS) < period {
				violations.Add(1)
				break
			}
		}
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = l.Charge(autonomousAttempt(itoa(i), "local-e"), now)
		}(i)
		go func() {
			defer wg.Done()
			l.ObserveHealthySince(stale, now, period)
		}()
	}
	wg.Wait()

	if n := violations.Load(); n != 0 {
		t.Fatalf("%d cleared states were written while a charge younger than the recovery period existed", n)
	}
	if l.PermitsAutonomous() {
		t.Fatal("a charge ended up cleared")
	}
	if got := len(l.State().Attempts); got != 200 {
		t.Fatalf("recorded %d charges, want 200", got)
	}
}
