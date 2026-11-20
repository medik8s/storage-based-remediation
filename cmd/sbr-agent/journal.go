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
	"errors"
	"fmt"
	"time"

	"github.com/medik8s/common/pkg/nodestate"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// journalWriteTimeout bounds a single journal write.
//
// The journal lives on the node's root filesystem, not on the shared storage
// it describes — that is the whole reason it can record a storage outage. A
// write still must never be able to stall a loop: a fence that is late is a
// fence that did not happen, so every write here is bounded and best effort.
const journalWriteTimeout = 5 * time.Second

// createArmingFlag opens the boot unit's gate. It is a package variable so a
// test can observe the ordering without writing to the host's state root.
var createArmingFlag = bootunit.CreateArmingFlag

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

// currentHostBootID returns the boot ID of the running host kernel. In the
// agent container /proc is the host's, so this is the host's boot ID — the
// value the boot unit will compare against.
func currentHostBootID() string {
	id, err := nodestate.ReadBootID()
	if err != nil {
		logger.V(1).Info("Cannot read the host boot ID", "error", err.Error())
		return ""
	}
	return id
}

// resumePendingRebootDecision handles a reboot decision recorded in the boot
// this process is still running in. The decision is written immediately before
// the agent asks the host to reboot, so an unchanged boot ID means the reboot
// never took place: the agent died between the write and the reboot, or the
// reboot failed.
//
// The decision is kept, not dropped. A peer can apply the OutOfService taint on
// its own deadline without waiting for boot evidence, so abandoning the reboot
// now would leave the node running while the cluster has already moved its
// workloads. The agent therefore stops petting the watchdog — the armed
// watchdog completes the reboot — and keeps the journal decision for the boot
// unit. A decision recorded in an earlier boot is the boot unit's evidence and
// is left untouched.
func (s *SBRAgent) resumePendingRebootDecision() {
	if s.journal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()

	arming, err := s.journal.ReadArmingState(ctx)
	if err != nil {
		logger.Error(err, "Cannot read the journal for a reboot decision",
			"path", s.journal.Path())
		return
	}
	if arming == nil || arming.Phase != bootunit.PhaseArmed {
		return
	}
	current := currentHostBootID()
	if current == "" {
		logger.V(1).Info("Cannot read the host boot ID; leaving any arming decision to the boot unit",
			"path", s.journal.Path())
		return
	}
	if arming.BeforeBootID != current {
		// Recorded in an earlier boot: the reboot happened, and the boot unit
		// reads this decision at the next boot.
		return
	}

	// Latch the decision so a later fence path cannot rewrite it with a new
	// incident ID, and withhold watchdog petting so the armed watchdog
	// completes the reboot that was chosen.
	s.fenceDecisionRecorded = true
	s.setSelfFenceDetected(true)
	rebootDecisionsResumedCounter.Inc()
	if s.recorder != nil && s.recorderObject != nil {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonRebootDecisionResumed,
			fmt.Sprintf("Resumed a reboot decision recorded in this boot (incident %s, source %s); watchdog petting is withheld",
				arming.IncidentID, arming.Source))
	}
	logger.Info("Resuming a reboot decision recorded in this boot; watchdog petting is withheld so the armed watchdog completes it",
		"incidentID", arming.IncidentID, "source", arming.Source, "path", s.journal.Path())
}

// recordFenceDecision writes the reboot decision and the arming state to the
// journal in one durable write, and then opens the boot unit's gate.
//
// The order is the design's: the decision is durable before the reboot, so
// there is never an arming state without a decision and never a reboot before
// the write. It runs immediately before the agent reboots, so the pair is as
// close to atomic as a host process can make it, and the boot-ID comparison in
// resumePendingRebootDecision at the next start catches the window where they
// come apart.
//
// A failure here is logged and not propagated: the reboot is the fence and it
// must proceed. The weaker journal tiers exist for exactly this case — an
// agent that dies without recording a decision still leaves the shutdown state
// and the storage verdict behind.
func (s *SBRAgent) recordFenceDecision(source, reason string) {
	if s.journal == nil {
		logger.V(1).Info("No journal available; not recording the reboot decision", "reason", reason)
		return
	}

	incidentID, err := bootunit.NewIncidentID()
	if err != nil {
		logger.Error(err, "Cannot generate an incident ID; not recording the reboot decision")
		return
	}

	arming := &bootunit.ArmingState{
		Version:      1,
		Phase:        bootunit.PhaseArmed,
		NodeUID:      s.nodeUID,
		ConfigUID:    s.configUID(),
		IncidentID:   incidentID,
		BeforeBootID: currentHostBootID(),
		Source:       source,
	}

	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()
	if err := s.journal.WriteArmingState(ctx, arming); err != nil {
		if errors.Is(err, nodestate.ErrDurabilityUnconfirmed) {
			// The decision is on disk and the next boot can read it, but a
			// power loss could take it back off. Distinguish it from a plain
			// failure: the reboot proceeds either way (it was authorized by
			// the fence path, not by this write), and the weaker journal
			// tiers remain as the fallback.
			logger.Error(err, "Reboot decision written but its durability is unconfirmed",
				"incidentID", incidentID, "path", s.journal.Path())
		} else {
			logger.Error(err, "Failed to record the reboot decision in the journal",
				"incidentID", incidentID, "path", s.journal.Path())
		}
		return
	}
	logger.Info("Recorded the reboot decision in the journal",
		"incidentID", incidentID, "source", source, "beforeBootID", arming.BeforeBootID,
		"path", s.journal.Path())

	// Open the gate only after the decision it relies on is durable: the unit
	// must never run with a flag and no journal behind it.
	if err := createArmingFlag(); err != nil {
		logger.Error(err, "Failed to open the boot unit gate after recording the decision",
			"incidentID", incidentID)
	}
}

// configUID identifies the configuration this agent is operating under, so a
// decision cannot outlive a replaced config.
func (s *SBRAgent) configUID() string {
	obj, ok := s.recorderObject.(metav1.Object)
	if !ok || obj == nil {
		return ""
	}
	return string(obj.GetUID())
}
