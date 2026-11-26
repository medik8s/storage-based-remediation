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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
	"github.com/medik8s/storage-based-remediation/v5/internal/budget"
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

// clearArmingFlag closes the boot unit's gate. A package variable for the
// same reason as createArmingFlag: setupBootUnit clears the gate in
// detect-only and control-plane paths, and tests must observe that without
// touching the host's state root.
var clearArmingFlag = bootunit.ClearArmingFlag

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

// recordStorageVerdict writes the agent's storage verdict into the journal
// as a condition entry, on transitions only (setSBRHealthy gates the calls).
// This is the evidence the boot unit's arming tier consults after an unclean
// shutdown: the published verdict — the state that drives the
// SBRStorageUnhealthy Node condition — rather than the raw outcome of the
// last probe write, which a single hiccup can flip. Best effort like every
// journal write: a failure is logged and never changes what the agent does.
func (s *SBRAgent) recordStorageVerdict(healthy bool) {
	if s.journal == nil {
		return
	}
	// Serialized so disk order follows transition order (see the field
	// comment on verdictWriteMutex); the health lock is not held here.
	s.verdictWriteMutex.Lock()
	defer s.verdictWriteMutex.Unlock()
	entry := nodestate.ConditionEntry{
		Type:   bootunit.StorageUnhealthyConditionType,
		Status: "False",
		Reason: "StorageRecovered",
	}
	if !healthy {
		entry.Status = "True"
		entry.Reason = "StorageFailuresExceededThreshold"
	}

	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()
	if err := s.journal.RecordCondition(ctx, entry); err != nil {
		logger.Error(err, "Failed to record the storage verdict in the journal",
			"healthy", healthy, "path", s.journal.Path())
	}
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
	// A decision bound to a different Node object must not drive this host's
	// watchdog: the cluster replaced the Node (deleting it is itself the
	// evacuation), and completing the old Node's reboot now would fence the
	// new incarnation for an incident it never had. Reject only when both
	// UIDs are known — an unknown binding cannot be validated, and the
	// host-level evidence (the boot never happened) still stands.
	if s.nodeUID != "" && arming.NodeUID != "" && arming.NodeUID != s.nodeUID {
		logger.Info("Recorded reboot decision belongs to a different Node object; not resuming it",
			"decisionNodeUID", arming.NodeUID, "currentNodeUID", s.nodeUID,
			"incidentID", arming.IncidentID, "path", s.journal.Path())
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
func (s *SBRAgent) recordFenceDecision(source, reason, incidentID string) {
	if s.journal == nil {
		logger.V(1).Info("No journal available; not recording the reboot decision", "reason", reason)
		return
	}

	var err error
	if incidentID == "" {
		if incidentID, err = bootunit.NewIncidentID(); err != nil {
			logger.Error(err, "Cannot generate an incident ID; not recording the reboot decision")
			return
		}
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

// resolveNodeUID reads this Node object's UID. setupBootUnit supplies it when
// the boot unit runs, but the unit is optional and off by default while the
// reset budget is not: without the UID a recovered epoch cannot be checked
// against the Node that owns it, and a replacement Node would inherit its
// predecessor's spent budget.
//
// Best effort. An unreachable API leaves the binding unknown, which the budget
// treats as unverifiable and keeps — the conservative direction.
func resolveNodeUID(ctx context.Context, c client.Client, nodeName string) string {
	if c == nil || nodeName == "" {
		return ""
	}
	readCtx, cancel := context.WithTimeout(ctx, bootUnitSetupTimeout)
	defer cancel()
	node := &corev1.Node{}
	if err := c.Get(readCtx, client.ObjectKey{Name: nodeName}, node); err != nil {
		logger.Error(err, "Cannot read this Node's UID; the reset budget will treat its history as unbound",
			"nodeName", nodeName)
		return ""
	}
	return string(node.UID)
}

// startupNodeUID returns the Node binding the reset budget uses: the value the
// boot unit reported when it ran, otherwise a direct read of the Node object.
// The boot unit is optional and off by default, so the budget cannot depend on
// it for an identity it needs in every BlockPerNode deployment.
func startupNodeUID(ctx context.Context, c client.Client, nodeName, fromBootUnit string) string {
	if fromBootUnit != "" {
		return fromBootUnit
	}
	return resolveNodeUID(ctx, c, nodeName)
}

// loadBudgetLedger builds the reset-budget ledger (design §5.6) over the
// node-local journal. It is called once at startup, only when the journal
// exists: outside BlockPerNode there is no journal and no budget. An
// unreadable or foreign journal yields unknown history — the ledger runs
// in memory, attempts stay permitted after the fresh §5.6 checks, and the
// condition is reported through budget.ReasonHistoryUnknown rather than
// silently treated as an unused allowance.
func (s *SBRAgent) loadBudgetLedger() *budget.Ledger {
	if s.journal == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()

	recovered, err := s.journal.ReadBudgetState(ctx)
	switch {
	case err == nil:
		if recovered == nil {
			logger.Info("No reset-budget history in the journal; starting with unknown history",
				"path", s.journal.Path())
			s.reportBudgetHistoryUnknown("the journal holds no reset-budget history")
		}
	case errors.Is(err, budget.ErrUnknownSchema):
		// A newer build wrote this ledger. Never overwrite it; run with
		// unknown history and say so.
		logger.Error(err, "Reset-budget ledger has an unsupported schema version; "+
			"running with unknown history and not overwriting it",
			"path", s.journal.Path())
		s.reportBudgetHistoryUnknown("the journal's reset-budget ledger has an unsupported schema version")
	default:
		logger.Error(err, "Cannot read the reset-budget ledger; running with unknown history",
			"path", s.journal.Path())
		s.reportBudgetHistoryUnknown("the reset-budget ledger could not be read")
	}

	persist := func(state budget.State) error {
		wctx, wcancel := context.WithTimeout(context.Background(), journalWriteTimeout)
		defer wcancel()
		err := s.journal.WriteBudgetState(wctx, func(*budget.State) (*budget.State, error) {
			// This process is the budget's only writer (the host lock is
			// deferred work); the in-memory state is the authority and the
			// journal is its mirror.
			next := state
			return &next, nil
		})
		if err != nil {
			// The ledger treats persistence as best effort and cannot report
			// this itself, so it is reported here: without it a budget that
			// never reaches the journal would be invisible.
			logger.Error(err, "Failed to persist the reset-budget ledger; the in-memory budget stands and the write is retried",
				"path", s.journal.Path())
		}
		return err
	}
	ledger := budget.NewLedger(recovered, persist)
	// A ledger charged by a replaced Node must not limit this one: deleting
	// the Node object is itself how the cluster evacuates it, so the new
	// incarnation starts without a spent budget.
	if ledger.DropForeignEpoch(s.nodeUID) {
		logger.Info("Discarded a reset-budget epoch charged by a different Node object",
			"nodeUID", s.nodeUID, "path", s.journal.Path())
	}
	return ledger
}

// reportBudgetHistoryUnknown publishes the HistoryUnknown token (design
// §5.10) when the budget runs without trusted history. An unknown history
// still permits an attempt after the fresh §5.6 checks, so this is not a
// refusal — it tells operators that the accounting is incomplete and that a
// reboot loop is therefore less constrained than usual.
func (s *SBRAgent) reportBudgetHistoryUnknown(cause string) {
	if s.recorder == nil {
		return
	}
	s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, budget.ReasonHistoryUnknown,
		fmt.Sprintf("Reset budget history unknown on (%s, %d): %s; "+
			"autonomous reboots remain permitted after the fresh checks", s.nodeName, s.nodeID, cause))
}

// chargeBudget records a reboot commitment against the reset budget
// (design §5.6/§5.7). Best-effort by contract: a failed journal write never
// blocks a permitted reboot and never reverses the in-memory charge, which
// the next successful write catches up. A nil ledger (no journal, non
// BlockPerNode modes) is a no-op.
func (s *SBRAgent) chargeBudget(kind budget.AttemptKind, attemptID, epochID string) {
	if s.budgetLedger == nil {
		return
	}
	if attemptID == "" {
		// Never skip the charge for want of an ID: an uncharged permitted
		// reboot is an unrecorded one, and a lost charge is how a reboot
		// loop comes back.
		id, err := bootunit.NewIncidentID()
		if err != nil {
			logger.Error(err, "Cannot generate a fallback attempt ID; the reboot proceeds but the budget charge is lost")
			return
		}
		attemptID = id
	}
	attempt := budget.Attempt{
		ID:        attemptID,
		EpochID:   epochID,
		Kind:      kind,
		NodeUID:   s.nodeUID,
		ConfigUID: s.configUID(),
		BootID:    currentHostBootID(),
	}
	if err := s.budgetLedger.Charge(attempt, time.Now()); err != nil {
		logger.Error(err, "Cannot persist the reset-budget charge; the in-memory budget is charged and the reboot proceeds",
			"attemptID", attemptID, "kind", kind, "epochID", epochID)
	}
}

// sourceToBudgetKind maps the fence source onto the budget's attempt kind:
// only autonomous attempts consume the outage's allowance (design §5.6
// line 620 — a requested reboot marks the outage without counting toward
// the autonomous limit).
func sourceToBudgetKind(source string) budget.AttemptKind {
	if source == bootunit.SourceRequested {
		return budget.KindRequested
	}
	return budget.KindAutonomous
}
