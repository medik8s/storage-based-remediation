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

package main

import (
	"context"
	"time"

	"github.com/medik8s/common/pkg/nodestate"
)

// journalWriteTimeout bounds a single journal write.
//
// The journal lives on the node's root filesystem, not on the shared storage
// it describes — that is the whole reason it can record a storage outage. A
// write still must never be able to stall a loop: a fence that is late is a
// fence that did not happen, so every write here is bounded and best effort.
const journalWriteTimeout = 5 * time.Second

// recordRunning records that this agent is alive in this boot, so a boot that
// starts with the journal still saying "running" is recognised as a reset,
// a panic or a power loss rather than an orderly reboot.
func (s *SBRAgent) recordRunning() {
	s.recordShutdownState("running", func(ctx context.Context) error {
		return s.journal.RecordRunning(ctx)
	})
}

// recordCleanShutdown records an orderly stop. Every shutdown path the agent
// controls calls it, including a stop caused by a committed fence: the fence
// decision is recorded separately as the arming state.
func (s *SBRAgent) recordCleanShutdown() {
	s.recordShutdownState("clean", func(ctx context.Context) error {
		return s.journal.RecordCleanShutdown(ctx)
	})
}

// recordShutdownState runs one journal write, logging a failure without
// propagating it: nothing in this file is allowed to change what the agent
// does, only what the next boot knows about it.
func (s *SBRAgent) recordShutdownState(state string, write func(context.Context) error) {
	if s.journal == nil {
		logger.V(1).Info("No journal available; not recording shutdown state", "state", state)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		logger.Error(err, "Failed to record shutdown state in the journal",
			"state", state, "path", s.journal.Path())
	}
}

// recordHeartbeatWrite records the outcome of this node's own heartbeat write.
//
// It is the one operation that proves durable access to shared storage from
// this host, so it is what the boot unit consults, together with the shutdown
// state, after an unclean shutdown. Only a change of outcome is written: the
// boot unit reads the newest entry, so one entry per transition is exactly as
// informative as one per heartbeat and costs a node-local write only when
// something changed.
//
// Peer read failures are deliberately not recorded here. A peer that has not
// written its slot yet reads as a failure without storage being unhealthy,
// and recording it would arm the next boot on evidence that is not about this
// node's access at all.
func (s *SBRAgent) recordHeartbeatWrite(latency time.Duration, writeErr error) {
	if s.journal == nil {
		return
	}

	ok := writeErr == nil
	s.journalIOMutex.Lock()
	alreadyRecorded := s.journalLastIOOK != nil && *s.journalLastIOOK == ok
	s.journalIOMutex.Unlock()
	if alreadyRecorded {
		return
	}

	entry := nodestate.IOEntry{
		TS:        time.Now(),
		Op:        "write",
		Target:    s.heartbeatDevicePath,
		LatencyMS: latency.Milliseconds(),
		OK:        ok,
	}
	if writeErr != nil {
		entry.Error = writeErr.Error()
	}

	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()
	if err := s.journal.RecordIO(ctx, entry); err != nil {
		// Leave the recorded outcome unchanged: the next heartbeat retries
		// this transition rather than losing it.
		logger.Error(err, "Failed to record a storage outcome in the journal",
			"target", s.heartbeatDevicePath, "ok", ok, "path", s.journal.Path())
		return
	}

	s.journalIOMutex.Lock()
	s.journalLastIOOK = &ok
	s.journalIOMutex.Unlock()
}
