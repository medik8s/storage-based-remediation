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
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodestate/nodestatetest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// journalAgent returns an agent wired only with what the journal helpers use.
// They deliberately touch nothing else, which is what lets a test exercise
// them without a cluster, a watchdog or a storage device.
func journalAgent(t *testing.T) (*SBRAgent, *bootunit.Journal) {
	t.Helper()
	j, err := bootunit.NewJournal(nodestatetest.TempStateDir(t))
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	return &SBRAgent{
		journal:             j,
		nodeUID:             "node-uid-1",
		heartbeatDevicePath: "/dev/sbr",
	}, j
}

// stubArmingFlag replaces the real gate creation for the duration of a test
// and reports how often it was called. The real function writes to the host's
// state root, which a unit test must never touch: on a root CI runner it would
// create /var/lib/medik8s/state/sbr-armed, and everywhere else it fails
// silently, which makes the test's own behavior depend on who runs it.
func stubArmingFlag(t *testing.T) *int {
	t.Helper()
	flags := 0
	orig := createArmingFlag
	createArmingFlag = func() error {
		flags++
		return nil
	}
	t.Cleanup(func() { createArmingFlag = orig })
	return &flags
}

func TestRecordFenceDecisionWritesArmingStateAndOpensTheGate(t *testing.T) {
	agent, j := journalAgent(t)
	agent.recorderObject = &corev1.Node{ObjectMeta: metav1.ObjectMeta{UID: "config-uid-1"}}
	flags := stubArmingFlag(t)

	agent.recordFenceDecision(bootunit.SourceAutonomous, "storage unhealthy")

	if *flags != 1 {
		t.Errorf("gate opened %d times, want 1", *flags)
	}

	got, err := j.ReadArmingState(context.Background())
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got == nil {
		t.Fatal("no arming state was recorded")
	}
	if got.Phase != bootunit.PhaseArmed {
		t.Errorf("phase = %q, want %q", got.Phase, bootunit.PhaseArmed)
	}
	if got.NodeUID != "node-uid-1" {
		t.Errorf("nodeUID = %q, want the agent's cached value", got.NodeUID)
	}
	if got.ConfigUID != "config-uid-1" {
		t.Errorf("configUID = %q, want the config the agent runs under", got.ConfigUID)
	}
	if got.Source != "Autonomous" {
		t.Errorf("source = %q, want Autonomous for a failure-driven fence", got.Source)
	}
	if len(got.IncidentID) != 2*bootunit.IncidentIDBytes {
		t.Errorf("incident ID %q is not a %d-byte hex identifier", got.IncidentID, bootunit.IncidentIDBytes)
	}
	if got.BeforeBootID == "" {
		t.Error("the decision carries no boot ID; the next start cannot tell it apart from a real reboot")
	}
}

func TestRecordFenceDecisionRecordsTheRequestedSource(t *testing.T) {
	agent, j := journalAgent(t)
	flags := stubArmingFlag(t)

	// A fence requested through a remediation (a peer's fence message) must
	// be distinguishable in the journal from an autonomous one: the release
	// protocol and the budget accounting treat the two differently.
	agent.recordFenceDecision(bootunit.SourceRequested, "fence message received")

	if *flags != 1 {
		t.Errorf("gate opened %d times, want 1", *flags)
	}

	got, err := j.ReadArmingState(context.Background())
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got == nil || got.Source != bootunit.SourceRequested {
		t.Errorf("source = %v, want %q recorded verbatim", got, bootunit.SourceRequested)
	}
}

func TestRecordFenceDecisionOpensNoGateWithoutAJournal(t *testing.T) {
	agent := &SBRAgent{}
	flags := stubArmingFlag(t)

	agent.recordFenceDecision(bootunit.SourceAutonomous, "storage unhealthy")

	if *flags != 0 {
		t.Error("a gate must never be opened without a journal behind it")
	}
}

func TestResumePendingRebootDecisionKeepsADecisionFromThisBoot(t *testing.T) {
	agent, j := journalAgent(t)
	ctx := context.Background()

	// A decision written in the boot we are still running in: the agent died
	// between the write and the reboot. The decision must be kept and watchdog
	// petting withheld, because peers may already have applied OOS on their
	// own deadline.
	before := currentHostBootID()
	if before == "" {
		t.Skip("no host boot ID available on this platform")
	}
	if err := j.WriteArmingState(ctx, &bootunit.ArmingState{
		Version:      1,
		Phase:        bootunit.PhaseArmed,
		NodeUID:      "node-uid-1",
		IncidentID:   "incident-1",
		BeforeBootID: before,
	}); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}

	agent.resumePendingRebootDecision()

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got == nil {
		t.Fatal("the decision was dropped; the armed watchdog would be re-fed and the reboot abandoned")
	}
	if !agent.isSelfFenceDetected() {
		t.Error("watchdog petting was not withheld, so the armed watchdog cannot complete the reboot")
	}
	if !agent.fenceDecisionRecorded {
		t.Error("the decision was not latched; a later fence path could rewrite it with a new incident ID")
	}
}

func TestResumePendingRebootDecisionReportsTheResumedDecision(t *testing.T) {
	agent, j := journalAgent(t)
	rec := record.NewFakeRecorder(1)
	agent.recorder = rec
	agent.recorderObject = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}

	ctx := context.Background()
	before := currentHostBootID()
	if before == "" {
		t.Skip("no host boot ID available on this platform")
	}
	if err := j.WriteArmingState(ctx, &bootunit.ArmingState{
		Version:      1,
		Phase:        bootunit.PhaseArmed,
		NodeUID:      "node-uid-1",
		IncidentID:   "incident-3",
		BeforeBootID: before,
		Source:       bootunit.SourceAutonomous,
	}); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}

	agent.resumePendingRebootDecision()

	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, EventReasonRebootDecisionResumed) {
			t.Errorf("event %q does not carry reason %s", ev, EventReasonRebootDecisionResumed)
		}
		if !strings.Contains(ev, "incident-3") {
			t.Errorf("event %q does not identify the resumed incident", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event was emitted for a resumed reboot decision")
	}
}

func TestResumePendingRebootDecisionLeavesAnEarlierBootsDecision(t *testing.T) {
	agent, j := journalAgent(t)
	rec := record.NewFakeRecorder(1)
	agent.recorder = rec
	agent.recorderObject = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}
	ctx := context.Background()

	if err := j.WriteArmingState(ctx, &bootunit.ArmingState{
		Version:      1,
		Phase:        bootunit.PhaseArmed,
		NodeUID:      "node-uid-1",
		IncidentID:   "incident-2",
		BeforeBootID: "a-different-boot",
	}); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}

	agent.resumePendingRebootDecision()

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got == nil {
		t.Fatal("a decision recorded for an earlier boot was erased before the boot unit could read it")
	}
	if agent.isSelfFenceDetected() {
		t.Error("an earlier boot's decision must not withhold watchdog petting in this boot")
	}
	select {
	case ev := <-rec.Events:
		t.Errorf("an earlier boot's decision emitted %q, want no event", ev)
	default:
	}
}

func TestRecordHeartbeatWriteWritesOnlyOnChange(t *testing.T) {
	agent, j := journalAgent(t)
	ctx := context.Background()

	agent.recordHeartbeatWrite(time.Millisecond, nil)
	agent.recordHeartbeatWrite(time.Millisecond, nil)
	agent.recordHeartbeatWrite(time.Millisecond, context.DeadlineExceeded)
	agent.recordHeartbeatWrite(time.Millisecond, context.DeadlineExceeded)

	// Four observations, two verdicts: healthy, then failing. The boot unit
	// reads the newest entry and needs nothing more, and the node's root
	// filesystem does not get a write per heartbeat.
	if got := ioEntryCount(t, j); got != 2 {
		t.Errorf("recorded %d I/O entries for 4 observations, want 2 (one per transition)", got)
	}
	if snap := j.ReadForArming(ctx, ""); !snap.StorageWasUnhealthy() {
		t.Error("the newest outcome is a failure, storage must read as unhealthy")
	}

	// A transition back is recorded as well.
	agent.recordHeartbeatWrite(time.Millisecond, nil)
	if got := ioEntryCount(t, j); got != 3 {
		t.Errorf("recorded %d I/O entries, want 3 after the recovery", got)
	}
	if snap := j.ReadForArming(ctx, ""); snap.StorageWasUnhealthy() {
		t.Error("a recovered write must clear the unhealthy verdict")
	}
}

// ioEntryCount reads the journal file the way a post-mortem would and counts
// the diagnostic ring, so a test can prove that unchanged outcomes are not
// written.
func ioEntryCount(t *testing.T, j *bootunit.Journal) int {
	t.Helper()
	raw, err := os.ReadFile(j.Path())
	if err != nil {
		t.Fatalf("read journal %s: %v", j.Path(), err)
	}
	var doc struct {
		IO []json.RawMessage `json:"io"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode journal: %v", err)
	}
	return len(doc.IO)
}

// TestSetSBRHealthyRecordsVerdictTransitions pins the agent half of the
// arming evidence: the storage verdict reaches the journal exactly on
// transitions, so the boot unit reads the published verdict after an unclean
// shutdown instead of the raw outcome of the last write.
func TestSetSBRHealthyRecordsVerdictTransitions(t *testing.T) {
	agent, j := journalAgent(t)
	ctx := context.Background()

	// The agent starts unhealthy (zero value); the first healthy verdict is
	// a transition and must be recorded.
	agent.setSBRHealthy(true)
	snap := j.ReadForArming(ctx, "")
	if snap.StorageWasUnhealthy() {
		t.Fatal("a healthy verdict must be recorded as healthy")
	}

	// The threshold crossing is the transition that matters: unhealthy.
	agent.setSBRHealthy(false)
	snap = j.ReadForArming(ctx, "")
	if !snap.StorageWasUnhealthy() {
		t.Fatal("an unhealthy verdict must be recorded as unhealthy")
	}

	// Repeated verdicts are not transitions and must not write: a healthy
	// node must not touch its journal once per heartbeat.
	res, err := j.Revision(ctx)
	if err != nil {
		t.Fatalf("read revision: %v", err)
	}
	agent.setSBRHealthy(false)
	if got, err := j.Revision(ctx); err != nil || got != res {
		t.Fatalf("revision moved from %d to %d (%v); a repeated verdict must not write", res, got, err)
	}

	// Recovery is a transition again.
	agent.setSBRHealthy(true)
	snap = j.ReadForArming(ctx, "")
	if snap.StorageWasUnhealthy() {
		t.Fatal("a recovered verdict must be recorded as healthy")
	}
}
