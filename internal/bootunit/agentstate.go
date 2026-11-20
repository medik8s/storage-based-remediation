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
	"fmt"
	"time"

	"github.com/medik8s/common/pkg/nodestate"
)

// The agent-side journal writes. The boot unit reads three facts out of the
// journal to decide whether the current boot needs protection: a recorded
// reboot decision (ReadForArming), whether the previous shutdown was orderly
// (Agent.Shutdown) and whether storage was failing when the journal was last
// written (the newest I/O ring entry). The record exists because nobody
// fulfills a fence during a total outage — the agent records what it knows
// while it still can, and the next boot decides from that record.

// RecordRunning records that an agent is alive in this boot.
//
// It must be called once at start-up, before the arming flag is created and
// before any work that a crash could interrupt. A journal left at
// ShutdownRunning when the next boot starts is how the boot unit tells a
// watchdog reset, a panic or a power loss from an orderly reboot: the value
// is written while the agent runs and replaced on the way out.
func (j *Journal) RecordRunning(ctx context.Context) error {
	return j.setShutdownState(ctx, nodestate.ShutdownRunning)
}

// RecordCleanShutdown records that the agent stopped in an orderly way.
//
// It must be called on every shutdown path the agent controls, including
// shutdown caused by a committed fence: the decision itself is recorded
// separately as the arming state and carries its own evidence, while this
// value only answers "did the process get to stop itself".
func (j *Journal) RecordCleanShutdown(ctx context.Context) error {
	return j.setShutdownState(ctx, nodestate.ShutdownClean)
}

// setShutdownState writes state into the journal, seeding an absent journal
// and refusing an untrusted one.
func (j *Journal) setShutdownState(ctx context.Context, state nodestate.ShutdownState) error {
	res, err := j.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("read journal before recording %q: %w", state, err)
	}
	switch {
	case res.Status == nodestate.StatusAbsent:
		doc := nodestate.NewDocument(nodestate.ComponentSBR)
		doc.Agent.Shutdown = state
		return j.store.Write(ctx, doc)
	case !res.Status.Trusted():
		return fmt.Errorf("cannot record %q on %s journal: %w", state, res.Status, res.Cause)
	}

	_, err = j.store.Update(ctx, func(doc *nodestate.Document) error {
		if doc.Agent.Shutdown == state {
			// An agent restarted by the kubelet must not rewrite the
			// journal on every start; the value is already correct.
			return nodestate.ErrNoChange
		}
		doc.Agent.Shutdown = state
		return nil
	})
	return err
}

// RecordIO appends one storage-operation observation to the journal's
// diagnostic ring. The ring is bounded by the store's retention policy, so a
// caller may record every observation; the agent records only changes in
// outcome, because what the boot unit needs is the most recent verdict and a
// healthy node must not write the journal on every heartbeat.
//
// A missing TS is stamped with the current time. Recording against a corrupt
// history is refused for the same reason arming is: the record would sit next
// to state nobody can trust.
func (j *Journal) RecordIO(ctx context.Context, entry nodestate.IOEntry) error {
	if entry.TS.IsZero() {
		entry.TS = time.Now()
	}

	res, err := j.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("read journal before recording I/O: %w", err)
	}
	switch {
	case res.Status == nodestate.StatusAbsent:
		doc := nodestate.NewDocument(nodestate.ComponentSBR)
		doc.IO = append(doc.IO, entry)
		return j.store.Write(ctx, doc)
	case !res.Status.Trusted():
		return fmt.Errorf("cannot record I/O on %s journal: %w", res.Status, res.Cause)
	}

	_, err = j.store.Update(ctx, func(doc *nodestate.Document) error {
		doc.IO = append(doc.IO, entry)
		return nil
	})
	return err
}
