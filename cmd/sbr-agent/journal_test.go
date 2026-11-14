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
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodestate/nodestatetest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func TestRecordFenceDecisionWritesArmingStateAndOpensTheGate(t *testing.T) {
	agent, j := journalAgent(t)
	agent.recorderObject = &corev1.Node{ObjectMeta: metav1.ObjectMeta{UID: "config-uid-1"}}

	flags := 0
	orig := createArmingFlag
	createArmingFlag = func() error {
		flags++
		return nil
	}
	t.Cleanup(func() { createArmingFlag = orig })

	agent.recordFenceDecision(bootunit.SourceAutonomous, "storage unhealthy")

	if flags != 1 {
		t.Errorf("gate opened %d times, want 1", flags)
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

func TestRecordFenceDecisionOpensNoGateWithoutAJournal(t *testing.T) {
	agent := &SBRAgent{}

	flags := 0
	orig := createArmingFlag
	createArmingFlag = func() error {
		flags++
		return nil
	}
	t.Cleanup(func() { createArmingFlag = orig })

	agent.recordFenceDecision(bootunit.SourceAutonomous, "storage unhealthy")

	if flags != 0 {
		t.Error("a gate must never be opened without a journal behind it")
	}
}

func TestClearStaleArmingStateDropsADecisionFromThisBoot(t *testing.T) {
	agent, j := journalAgent(t)
	ctx := context.Background()

	// A decision written in the boot we are still running in: the agent died
	// between the write and the reboot.
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

	agent.clearStaleArmingState()

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got != nil {
		t.Errorf("a decision for a reboot that never happened survived: %+v", got)
	}
}

func TestClearStaleArmingStateKeepsADecisionFromAnotherBoot(t *testing.T) {
	agent, j := journalAgent(t)
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

	agent.clearStaleArmingState()

	got, err := j.ReadArmingState(ctx)
	if err != nil {
		t.Fatalf("ReadArmingState: %v", err)
	}
	if got == nil {
		t.Fatal("a decision recorded for an earlier boot was erased before the boot unit could read it")
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
	if snap := j.ReadForArming(ctx, ""); !snap.StorageWasUnhealthyAtLastWrite() {
		t.Error("the newest outcome is a failure, storage must read as unhealthy")
	}

	// A transition back is recorded as well.
	agent.recordHeartbeatWrite(time.Millisecond, nil)
	if got := ioEntryCount(t, j); got != 3 {
		t.Errorf("recorded %d I/O entries, want 3 after the recovery", got)
	}
	if snap := j.ReadForArming(ctx, ""); snap.StorageWasUnhealthyAtLastWrite() {
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
