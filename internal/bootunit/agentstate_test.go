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
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodestate"
)

func TestRecordRunningThenCleanShutdown(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	// An agent that never wrote anything leaves the journal absent, which the
	// boot unit treats as unusable — the state a fresh install starts in.
	if snap := j.ReadForArming(ctx, ""); !snap.JournalUnusable() {
		t.Fatal("a fresh state directory must read as an unusable journal")
	}

	if err := j.RecordRunning(ctx); err != nil {
		t.Fatalf("RecordRunning: %v", err)
	}

	// While the agent runs, the journal says so. A boot that starts with this
	// value means the previous boot ended without the agent's say-so.
	snap := j.ReadForArming(ctx, "")
	if snap.JournalUnusable() {
		t.Fatalf("journal must be usable after RecordRunning: %s", snap.Problem())
	}
	if !snap.IsUncleanShutdown() {
		t.Fatal("a journal recording a running agent must classify as unclean after a boot change")
	}

	if err := j.RecordCleanShutdown(ctx); err != nil {
		t.Fatalf("RecordCleanShutdown: %v", err)
	}

	snap = j.ReadForArming(ctx, "")
	if snap.IsUncleanShutdown() {
		t.Fatal("an orderly stop must classify as clean")
	}
}

func TestRecordRunningIsIdempotent(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	if err := j.RecordRunning(ctx); err != nil {
		t.Fatalf("RecordRunning: %v", err)
	}
	res, err := j.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := res.Document.Revision

	// A container restarted by the kubelet must not rewrite the journal just
	// to record the same value: every write to the node's root filesystem is
	// a small risk taken for nothing.
	if err := j.RecordRunning(ctx); err != nil {
		t.Fatalf("second RecordRunning: %v", err)
	}
	res, err = j.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Document.Revision != first {
		t.Errorf("revision advanced from %d to %d; an unchanged value must not be written",
			first, res.Document.Revision)
	}
}

func TestRecordCleanShutdownSeedsAbsentJournal(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	// The agent can stop before it ever recorded running (an early start-up
	// failure). Seeding is still correct: the document exists and says clean.
	if err := j.RecordCleanShutdown(ctx); err != nil {
		t.Fatalf("RecordCleanShutdown on an absent journal: %v", err)
	}
	snap := j.ReadForArming(ctx, "")
	if snap.JournalUnusable() {
		t.Fatalf("journal must be usable: %s", snap.Problem())
	}
	if snap.IsUncleanShutdown() {
		t.Fatal("a clean stop on a fresh journal must classify as clean")
	}
}

func TestShutdownStateRefusesCorruptJournal(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)
	if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := j.RecordRunning(ctx); err == nil {
		t.Fatal("recording a running agent must be refused on corrupt history")
	}
	if err := j.RecordCleanShutdown(ctx); err == nil {
		t.Fatal("recording a clean shutdown must be refused on corrupt history")
	}
}

func TestRecordIODrivesTheStorageHealthTier(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	// A failing write records the verdict the boot unit reads after an
	// unclean shutdown.
	if err := j.RecordIO(ctx, nodestate.IOEntry{
		Op:     "write",
		Target: "/dev/sbr",
		OK:     false,
		Error:  "input/output error",
	}); err != nil {
		t.Fatalf("RecordIO: %v", err)
	}
	if snap := j.ReadForArming(ctx, ""); !snap.StorageWasUnhealthyAtLastWrite() {
		t.Fatal("the newest I/O entry is a failure, storage must read as unhealthy")
	}

	// The next successful observation clears it: the tier asks about the
	// most recent verdict, not about a failure at any point in history.
	if err := j.RecordIO(ctx, nodestate.IOEntry{Op: "write", Target: "/dev/sbr", OK: true}); err != nil {
		t.Fatalf("RecordIO: %v", err)
	}
	if snap := j.ReadForArming(ctx, ""); snap.StorageWasUnhealthyAtLastWrite() {
		t.Fatal("a newer successful entry must clear the unhealthy verdict")
	}
}

func TestRecordIOSeedsAbsentJournalAndStampsTime(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	before := time.Now().Add(-time.Second)
	if err := j.RecordIO(ctx, nodestate.IOEntry{Op: "write", Target: "/dev/sbr", OK: true}); err != nil {
		t.Fatalf("RecordIO on an absent journal: %v", err)
	}

	res, err := j.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Document.IO) != 1 {
		t.Fatalf("got %d I/O entries, want 1", len(res.Document.IO))
	}
	if entry := res.Document.IO[0]; entry.TS.Before(before) || entry.TS.After(time.Now().Add(time.Second)) {
		t.Errorf("entry was not stamped with the current time: %v", entry.TS)
	}

	// A caller-supplied timestamp is preserved.
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := j.RecordIO(ctx, nodestate.IOEntry{TS: at, Op: "read", OK: true}); err != nil {
		t.Fatal(err)
	}
	res, err = j.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Document.IO[1].TS.Equal(at) {
		t.Errorf("caller timestamp = %v, want %v", res.Document.IO[1].TS, at)
	}
}

func TestRecordIORefusesCorruptJournal(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)
	if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordIO(ctx, nodestate.IOEntry{Op: "write", OK: true}); err == nil {
		t.Fatal("recording I/O on corrupt history must be refused")
	}
}

func TestClearStaleArmingState(t *testing.T) {
	ctx := context.Background()

	t.Run("same boot is cleared", func(t *testing.T) {
		j := newTestJournal(t)
		st := armedState()
		st.BeforeBootID = "boot-now"
		if err := j.WriteArmingState(ctx, st); err != nil {
			t.Fatal(err)
		}
		cleared, err := j.ClearStaleArmingState(ctx, "boot-now")
		if err != nil {
			t.Fatalf("ClearStaleArmingState: %v", err)
		}
		if !cleared {
			t.Fatal("a decision for the current boot must be reported as cleared")
		}
		if got, err := j.ReadArmingState(ctx); err != nil || got != nil {
			t.Fatalf("decision survived: %+v, %v", got, err)
		}
	})

	t.Run("another boot is kept", func(t *testing.T) {
		j := newTestJournal(t)
		st := armedState()
		st.BeforeBootID = "boot-before"
		if err := j.WriteArmingState(ctx, st); err != nil {
			t.Fatal(err)
		}
		cleared, err := j.ClearStaleArmingState(ctx, "boot-now")
		if err != nil {
			t.Fatal(err)
		}
		if cleared {
			t.Fatal("a decision recorded before a real reboot must not be cleared")
		}
		if got, err := j.ReadArmingState(ctx); err != nil || got == nil {
			t.Fatalf("decision was erased: %+v, %v", got, err)
		}
	})

	t.Run("no decision is a no-op", func(t *testing.T) {
		j := newTestJournal(t)
		cleared, err := j.ClearStaleArmingState(ctx, "boot-now")
		if err != nil {
			t.Fatalf("ClearStaleArmingState on an absent journal: %v", err)
		}
		if cleared {
			t.Fatal("nothing to clear must report false")
		}
	})

	t.Run("an unknown boot is refused", func(t *testing.T) {
		j := newTestJournal(t)
		if _, err := j.ClearStaleArmingState(ctx, ""); err == nil {
			t.Fatal("an empty boot ID cannot judge a decision and must be refused")
		}
	})

	t.Run("corrupt history is refused", func(t *testing.T) {
		j := newTestJournal(t)
		if err := os.MkdirAll(filepath.Dir(j.Path()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(j.Path(), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := j.ClearStaleArmingState(ctx, "boot-now"); err == nil {
			t.Fatal("clearing on corrupt history must be refused")
		}
	})
}

func TestNewIncidentID(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id, err := NewIncidentID()
		if err != nil {
			t.Fatalf("NewIncidentID: %v", err)
		}
		if len(id) != 2*IncidentIDBytes {
			t.Fatalf("id %q is %d characters, want %d", id, len(id), 2*IncidentIDBytes)
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("id %q is not hex: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate incident ID %q in 100 draws", id)
		}
		seen[id] = struct{}{}
	}
}
