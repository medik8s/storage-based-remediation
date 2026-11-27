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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/medik8s/common/pkg/nodestate"
	"github.com/medik8s/common/pkg/nodestate/nodestatetest"

	"github.com/medik8s/storage-based-remediation/v5/internal/budget"
)

// newTestJournal returns a Journal over a fresh temporary state directory.
// nodestatetest.TempStateDir supplies what the security policy requires of
// a state directory and t.TempDir does not: a 0700 mode and a resolved
// path.
func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	j, err := NewJournal(nodestatetest.TempStateDir(t))
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	return j
}

// armedState is a representative arming decision for round-trip tests.
func armedState() *ArmingState {
	return &ArmingState{
		Version:      1,
		Phase:        PhaseArmed,
		NodeUID:      "11111111-2222-3333-4444-555555555555",
		ConfigUID:    "cfg-uid-1",
		IncidentID:   "incident-7f3a",
		BeforeBootID: "boot-before",
		Source:       "Requested",
	}
}

func TestParseArmingState(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantNil bool
		wantErr bool
	}{
		{name: "empty means absent", raw: "", wantNil: true},
		{name: "valid armed state", raw: `{"version":1,"phase":"Armed","incidentID":"x"}`},
		{name: "valid cleared state", raw: `{"version":1,"phase":"Cleared"}`},
		{name: "invalid JSON", raw: `{not json`, wantErr: true},
		{name: "unknown version", raw: `{"version":99,"phase":"Armed"}`, wantErr: true},
		{name: "missing version is unknown", raw: `{"phase":"Armed"}`, wantErr: true},
		{name: "missing phase is unknown", raw: `{"version":1}`, wantErr: true},
		{name: "unknown phase", raw: `{"version":1,"phase":"Bogus"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseArmingState(tt.raw)
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && got != nil {
				t.Fatalf("want nil state, got %+v", got)
			}
		})
	}
}

func TestArmingStateStringRoundTrip(t *testing.T) {
	want := armedState()
	got, err := ParseArmingState(want.String())
	if err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if *got != *want {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestJournalWriteAndReadArmingState(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	if _, err := j.ReadArmingState(ctx); err != nil {
		t.Fatalf("read on absent journal: %v", err)
	}

	if err := j.WriteArmingState(ctx, armedState()); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	want := armedState()
	if got == nil || *got != *want {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	snap := j.ReadForArming(ctx, "")
	if snap.JournalUnusable() {
		t.Fatalf("snapshot unusable: %s", snap.Problem())
	}
	if !snap.HasArmingState() {
		t.Fatal("snapshot must report the armed state")
	}
	// With the matching expected UID the binding validates and the decision
	// is usable for this node.
	snap = j.ReadForArming(ctx, want.NodeUID)
	if !snap.HasArmingState() || snap.ForeignDecision() {
		t.Fatalf("matching NodeUID must keep the decision: has=%v foreign=%v", snap.HasArmingState(), snap.ForeignDecision())
	}
	if snap.Arming().IncidentID != want.IncidentID {
		t.Errorf("IncidentID = %q, want %q", snap.Arming().IncidentID, want.IncidentID)
	}
	if d := EvaluateArming(snap); !d.Armed {
		t.Errorf("decision = %s, want armed", d)
	}
}

func TestJournalWriteArmingStateSeedsAbsentJournal(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	// No journal exists yet; the write must seed one rather than fail.
	if _, err := os.Stat(j.Path()); !os.IsNotExist(err) {
		t.Fatalf("journal unexpectedly exists: %v", err)
	}
	if err := j.WriteArmingState(ctx, armedState()); err != nil {
		t.Fatalf("WriteArmingState on absent journal: %v", err)
	}
	if _, err := os.Stat(j.Path()); err != nil {
		t.Fatalf("journal not created: %v", err)
	}
}

func TestJournalWriteArmingStateValidation(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	if err := j.WriteArmingState(ctx, nil); err == nil {
		t.Fatal("nil arming state must be rejected")
	}
	cleared := armedState()
	cleared.Phase = PhaseCleared
	if err := j.WriteArmingState(ctx, cleared); err == nil {
		t.Fatal("cleared phase must be rejected on write")
	}
}

func TestJournalWriteArmingStateRefusesCorruptJournal(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := j.WriteArmingState(ctx, armedState()); err == nil {
		t.Fatal("arming onto a corrupt journal must be refused")
	}
}

func TestJournalClearArmingState(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	if err := j.ClearArmingState(ctx); err != nil {
		t.Fatalf("clear on absent journal: %v", err)
	}

	if err := j.WriteArmingState(ctx, armedState()); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}
	if err := j.ClearArmingState(ctx); err != nil {
		t.Fatalf("ClearArmingState: %v", err)
	}

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState after clear: %v", err)
	}
	if got != nil {
		t.Errorf("arming state survived the clear: %+v", got)
	}
}

// writeDocument writes a hand-built document through the journal's store so
// tests can arrange shutdown states and I/O ring entries.
func writeDocument(t *testing.T, j *Journal, mutate func(*nodestate.Document)) {
	t.Helper()
	ctx := context.Background()
	_, err := j.store.Update(ctx, func(doc *nodestate.Document) error {
		mutate(doc)
		return nil
	})
	// Update refuses an absent journal; seed one first and retry.
	if err != nil {
		if seedErr := j.store.Write(ctx, nodestate.NewDocument(nodestate.ComponentSBR)); seedErr != nil {
			t.Fatalf("seed document: %v", seedErr)
		}
		if _, err := j.store.Update(ctx, func(doc *nodestate.Document) error {
			mutate(doc)
			return nil
		}); err != nil {
			t.Fatalf("update document: %v", err)
		}
	}
}

func TestJournalAbsentFailsClosed(t *testing.T) {
	j := newTestJournal(t)

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatal("absent journal must be unusable")
	}
	if d := EvaluateArming(snap); !d.Armed {
		t.Errorf("decision = %s, want armed (fail closed)", d)
	}
}

func TestJournalCorruptFailsClosed(t *testing.T) {
	j := newTestJournal(t)

	if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path(), []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatal("corrupt journal must be unusable")
	}
	if d := EvaluateArming(snap); !d.Armed {
		t.Errorf("decision = %s, want armed (fail closed)", d)
	}
	if _, err := j.ReadArmingState(context.Background()); err == nil {
		t.Error("ReadArmingState on corrupt journal must error")
	}
}

func TestJournalInsecurePermissionsFailClosed(t *testing.T) {
	j := newTestJournal(t)

	if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	// World-readable files are refused by the nodestate security policy;
	// the boot unit must treat that as unusable, not as "no evidence".
	if err := os.WriteFile(j.Path(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatal("journal with insecure permissions must be unusable")
	}
}

func TestJournalUndecodableArmingStateFailsClosed(t *testing.T) {
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{KeyArmingState: "{broken json"}
	})

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatalf("undecodable arming state must make the journal unusable, problem=%q", snap.Problem())
	}
	if d := EvaluateArming(snap); !d.Armed {
		t.Errorf("decision = %s, want armed (fail closed)", d)
	}
}

func TestJournalUnknownArmingStateVersionFailsClosed(t *testing.T) {
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{KeyArmingState: `{"version":99,"phase":"Armed"}`}
	})

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatal("unknown arming state version must make the journal unusable")
	}
}

func TestJournalMissingArmingPhaseFailsClosed(t *testing.T) {
	// A recorded decision without a phase must not read as "not armed":
	// it is corruption, and the boot fails closed on it.
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{KeyArmingState: `{"version":1}`}
	})

	snap := j.ReadForArming(context.Background(), "")
	if !snap.JournalUnusable() {
		t.Fatalf("missing arming phase must make the journal unusable, problem=%q", snap.Problem())
	}
	if d := EvaluateArming(snap); !d.Armed {
		t.Errorf("decision = %s, want armed (fail closed)", d)
	}
}

func TestJournalClearedArmingStateIsNotArmed(t *testing.T) {
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{
			KeyArmingState: `{"version":1,"phase":"Cleared"}`,
		}
		doc.Agent.Shutdown = nodestate.ShutdownClean
	})

	snap := j.ReadForArming(context.Background(), "")
	if snap.JournalUnusable() {
		t.Fatalf("snapshot unusable: %s", snap.Problem())
	}
	if snap.HasArmingState() {
		t.Error("a cleared arming state must not count as armed")
	}
	if d := EvaluateArming(snap); d.Armed {
		t.Errorf("decision = %s, want normal boot", d)
	}
}

func TestJournalUncleanShutdownTiers(t *testing.T) {
	tests := []struct {
		name         string
		shutdown     nodestate.ShutdownState
		lastIOOK     bool
		wantArmed    bool
		wantReasonIn string
	}{
		{
			name:         "running shutdown with failed IO arms",
			shutdown:     nodestate.ShutdownRunning,
			lastIOOK:     false,
			wantArmed:    true,
			wantReasonIn: "unclean shutdown while storage was unhealthy",
		},
		{
			name:         "unknown shutdown with failed IO arms",
			shutdown:     nodestate.ShutdownUnknown,
			lastIOOK:     false,
			wantArmed:    true,
			wantReasonIn: "unclean shutdown while storage was unhealthy",
		},
		{
			name:         "unclean shutdown with healthy IO is normal boot",
			shutdown:     nodestate.ShutdownRunning,
			lastIOOK:     true,
			wantArmed:    false,
			wantReasonIn: "storage was healthy",
		},
		{
			name:         "clean shutdown is normal boot",
			shutdown:     nodestate.ShutdownClean,
			lastIOOK:     false,
			wantArmed:    false,
			wantReasonIn: "clean shutdown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newTestJournal(t)
			writeDocument(t, j, func(doc *nodestate.Document) {
				doc.Agent.Shutdown = tt.shutdown
				doc.IO = []nodestate.IOEntry{{OK: tt.lastIOOK}}
			})

			snap := j.ReadForArming(context.Background(), "")
			if snap.JournalUnusable() {
				t.Fatalf("snapshot unusable: %s", snap.Problem())
			}
			if got := snap.IsUncleanShutdown(); got == (tt.shutdown == nodestate.ShutdownClean) {
				t.Errorf("IsUncleanShutdown = %v for shutdown %q", got, tt.shutdown)
			}
			if got := snap.StorageWasUnhealthy(); got == tt.lastIOOK {
				t.Errorf("StorageWasUnhealthy = %v, last IO OK = %v", got, tt.lastIOOK)
			}
			d := EvaluateArming(snap)
			if d.Armed != tt.wantArmed {
				t.Errorf("decision = %s, want armed=%v", d, tt.wantArmed)
			}
			if !strings.Contains(d.Reason, tt.wantReasonIn) {
				t.Errorf("reason %q does not contain %q", d.Reason, tt.wantReasonIn)
			}
		})
	}
}

func TestJournalPathUsesStateRoot(t *testing.T) {
	j, err := NewJournal("")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	if want := HostStateRoot + "/sbr.json"; j.Path() != want {
		t.Errorf("Path() = %q, want %q", j.Path(), want)
	}
}

func TestZeroArmingSnapshotFailsClosed(t *testing.T) {
	// A Journal that could not even be opened is represented by a zero
	// snapshot; it must fail closed, never read as "no evidence".
	var snap ArmingSnapshot
	if !snap.JournalUnusable() {
		t.Fatal("zero snapshot must be unusable")
	}
	if d := EvaluateArming(&snap); !d.Armed {
		t.Errorf("decision = %s, want armed (fail closed)", d)
	}
}

func TestJournalForeignNodeUIDDecisionIsIgnored(t *testing.T) {
	// A replaced Node object means the cluster forgot this host's workloads;
	// a decision recorded for the old UID must neither arm the boot nor
	// carry its incident ID. The host-level tiers still apply.
	ctx := context.Background()
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{
			KeyArmingState: `{"version":1,"phase":"Armed","nodeUID":"old-node-uid","incidentID":"old-incident"}`,
		}
		doc.Agent.Shutdown = nodestate.ShutdownClean // so only tier 1 could arm
	})

	snap := j.ReadForArming(ctx, "current-node-uid")
	if snap.JournalUnusable() {
		t.Fatalf("snapshot unusable: %s", snap.Problem())
	}
	if snap.HasArmingState() {
		t.Error("a decision bound to a different Node UID must not count as armed")
	}
	if !snap.ForeignDecision() {
		t.Error("the foreign decision must be reported so callers can log it")
	}
	if a := snap.Arming(); a != nil {
		t.Errorf("the foreign incident ID must not be carried, got %+v", a)
	}
	if d := EvaluateArming(snap); d.Armed {
		t.Errorf("decision = %s, want normal boot (clean shutdown, foreign decision ignored)", d)
	}
}

func TestJournalNodeUIDBindingAcceptsUnvalidatableStates(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{
			KeyArmingState: `{"version":1,"phase":"Armed","incidentID":"x"}`, // no NodeUID recorded
		}
	})

	// No recorded binding: accepted regardless of the expected UID.
	snap := j.ReadForArming(ctx, "any-uid")
	if !snap.HasArmingState() || snap.ForeignDecision() {
		t.Fatalf("unbound decision must be accepted: has=%v foreign=%v", snap.HasArmingState(), snap.ForeignDecision())
	}
	// No expected UID from the caller: accepted even when bound.
	writeDocument(t, j, func(doc *nodestate.Document) {
		doc.Agent.Extra = map[string]string{
			KeyArmingState: `{"version":1,"phase":"Armed","nodeUID":"some-uid"}`,
		}
	})
	snap = j.ReadForArming(ctx, "")
	if !snap.HasArmingState() || snap.ForeignDecision() {
		t.Fatalf("decision with no expected UID to check against must be accepted: has=%v foreign=%v", snap.HasArmingState(), snap.ForeignDecision())
	}
}

// loadDocument reads the journal's current document for assertions.
func loadDocument(t *testing.T, j *Journal) *nodestate.LoadResult {
	t.Helper()
	res, err := j.store.Load(context.Background())
	if err != nil {
		t.Fatalf("load document: %v", err)
	}
	return res
}

// TestReadForArmingPrefersRecordedVerdict pins the reading half of the
// arming evidence: the recorded SBRStorageUnhealthy verdict is authoritative
// over the newest I/O ring entry, which remains only as the fallback for
// journals written before verdicts were recorded. An Unknown verdict is not
// a verdict and falls back too.
func TestReadForArmingPrefersRecordedVerdict(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name          string
		verdict       string // "", "True", "False", "Unknown"
		lastIOOK      bool
		wantUnhealthy bool
	}{
		{"verdict True wins over a healthy last write", "True", true, true},
		{"verdict False wins over a failed last write", "False", false, false},
		{"no verdict falls back to the failed last write", "", false, true},
		{"no verdict falls back to the healthy last write", "", true, false},
		{"an Unknown verdict falls back to the last write", "Unknown", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j := newTestJournal(t)
			writeDocument(t, j, func(doc *nodestate.Document) {
				doc.Agent.Shutdown = nodestate.ShutdownRunning
				doc.IO = []nodestate.IOEntry{{Op: "write", OK: tt.lastIOOK}}
				if tt.verdict != "" {
					doc.SetCondition(nodestate.ConditionEntry{
						Type:   StorageUnhealthyConditionType,
						Status: tt.verdict,
					})
				}
			})

			snap := j.ReadForArming(ctx, "")
			if snap.JournalUnusable() {
				t.Fatalf("snapshot unusable: %s", snap.Problem())
			}
			if got := snap.StorageWasUnhealthy(); got != tt.wantUnhealthy {
				t.Fatalf("StorageWasUnhealthy = %v, want %v (verdict %q, last IO OK = %v)",
					got, tt.wantUnhealthy, tt.verdict, tt.lastIOOK)
			}
		})
	}
}

// A budget state round-trips through the real nodestate store: write, read
// back, and the Extra entry survives alongside the arming state.
func TestBudgetStateRoundTrip(t *testing.T) {
	j := newTestJournal(t)
	ctx := context.Background()

	// Absent journal: no state, no error.
	if st, err := j.ReadBudgetState(ctx); err != nil || st != nil {
		t.Fatalf("read on absent journal = (%v, %v), want (nil, nil)", st, err)
	}

	want := &budget.State{
		SchemaVersion: budget.SchemaVersion,
		CurrentEpoch:  &budget.Epoch{ID: "local-abc", AutonomousCharged: true},
		Attempts: []budget.Attempt{{
			ID: "incident-1", EpochID: "local-abc", Kind: budget.KindAutonomous,
			NodeUID: "node-uid-1", BootID: "boot-1", Counted: true,
		}},
	}
	if err := j.WriteBudgetState(ctx, func(recovered *budget.State) (*budget.State, error) {
		if recovered != nil {
			t.Fatalf("recovered = %+v, want nil on first write", recovered)
		}
		return want, nil
	}); err != nil {
		t.Fatalf("WriteBudgetState: %v", err)
	}

	got, err := j.ReadBudgetState(ctx)
	if err != nil {
		t.Fatalf("ReadBudgetState: %v", err)
	}
	if got == nil || got.CurrentEpoch == nil || got.CurrentEpoch.ID != "local-abc" ||
		!got.CurrentEpoch.AutonomousCharged || len(got.Attempts) != 1 ||
		got.Attempts[0].ID != "incident-1" || !got.HistoryKnown {
		t.Fatalf("round-tripped state = %+v, want the written state", got)
	}

	// The mutate function sees the recovered state on the second write.
	if err := j.WriteBudgetState(ctx, func(recovered *budget.State) (*budget.State, error) {
		if recovered == nil || recovered.CurrentEpoch == nil {
			t.Fatalf("recovered = %+v, want the previously written state", recovered)
		}
		next := *recovered
		next.Attempts = append(next.Attempts, budget.Attempt{
			ID: "cr-uid-9", EpochID: recovered.CurrentEpoch.ID, Kind: budget.KindRequested,
		})
		return &next, nil
	}); err != nil {
		t.Fatalf("second WriteBudgetState: %v", err)
	}
	got, err = j.ReadBudgetState(ctx)
	if err != nil {
		t.Fatalf("ReadBudgetState after second write: %v", err)
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("attempts after second write = %d, want 2", len(got.Attempts))
	}

	// A nil next state writes nothing.
	if err := j.WriteBudgetState(ctx, func(*budget.State) (*budget.State, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("nil next state must not error: %v", err)
	}
	if got, err := j.ReadBudgetState(ctx); err != nil || len(got.Attempts) != 2 {
		t.Fatalf("nil write changed the state: (%+v, %v)", got, err)
	}
}

// An unsupported schema version in the Extra entry is an error, never a
// silent reinterpretation, and the entry is left in place.
func TestBudgetStateUnsupportedSchema(t *testing.T) {
	j := newTestJournal(t)
	ctx := context.Background()

	encoded, err := budget.Encode(budget.State{SchemaVersion: budget.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	// Hand-write a future version straight into the store: Encode stamps
	// the current version, so a future one can only exist on disk.
	future := `{"schemaVersion":99,"currentEpoch":{"id":"future"}}`
	doc := nodestate.NewDocument(nodestate.ComponentSBR)
	if doc.Agent.Extra == nil {
		doc.Agent.Extra = make(map[string]string)
	}
	doc.Agent.Extra[KeyBudgetState] = future
	if err := j.store.Write(ctx, doc); err != nil {
		t.Fatalf("write future version: %v", err)
	}
	_ = encoded
	if _, err := j.ReadBudgetState(ctx); !errors.Is(err, budget.ErrUnknownSchema) {
		t.Fatalf("read future version = %v, want ErrUnknownSchema", err)
	}
	// And the write path refuses to recover-and-overwrite it.
	if err := j.WriteBudgetState(ctx, func(*budget.State) (*budget.State, error) {
		t.Fatal("mutate must not run on an unrecoverable state")
		return nil, nil
	}); err == nil {
		t.Fatal("write over unsupported schema must fail")
	}
}
