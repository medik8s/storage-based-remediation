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
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/medik8s/storage-based-remediation/v5/internal/hostlock"
)

// conflictTestAgent returns an agent with a fake recorder, so the test can read the
// events it emits (RHWA-2065, AGT-01).
func conflictTestAgent(nodeName string) (*SBRAgent, *record.FakeRecorder) {
	recorder := record.NewFakeRecorder(10)
	return &SBRAgent{
		nodeName:       nodeName,
		recorder:       recorder,
		recorderObject: &corev1.Node{},
	}, recorder
}

// TestLosingTheHostLockReportsLocalAgentConflict covers the ticket's first acceptance
// criterion: the second agent process on one host reports LocalAgentConflict, naming the
// node so an administrator can tell which host is unprotected.
func TestLosingTheHostLockReportsLocalAgentConflict(t *testing.T) {
	agent, recorder := conflictTestAgent("worker-2")

	agent.setBlockPerNodeState(true)

	if !agent.localAgentConflict {
		t.Error("localAgentConflict = false, want true after losing the host lock")
	}

	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, hostlock.ReasonLocalAgentConflict) {
			t.Errorf("event = %q, want it to report %q", event, hostlock.ReasonLocalAgentConflict)
		}
		if !strings.Contains(event, "worker-2") {
			t.Errorf("event = %q, want it to name the node it came from", event)
		}
		if !strings.Contains(event, corev1.EventTypeWarning) {
			t.Errorf("event = %q, want a Warning: an unprotected node is not normal", event)
		}
	default:
		t.Fatal("no event emitted; an administrator cannot see that this node has no local agent")
	}
}

// TestHoldingTheHostLockReportsNothing keeps the conflict report truthful: the process
// that owns the host must not warn about itself.
func TestHoldingTheHostLockReportsNothing(t *testing.T) {
	agent, recorder := conflictTestAgent("worker-1")

	agent.setBlockPerNodeState(false)

	if agent.localAgentConflict {
		t.Error("localAgentConflict = true, want false while this process holds the lock")
	}
	select {
	case event := <-recorder.Events:
		t.Errorf("unexpected event %q from the lock holder", event)
	default:
	}
}

// TestSecondAgentOnOneHostIsTheOneThatReports ties the lock to the report: with the lock
// already held on this host, the next agent derives the conflict and warns about it.
func TestSecondAgentOnOneHostIsTheOneThatReports(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	firstLock, err := hostlock.Acquire(lockPath)
	if err != nil {
		t.Fatalf("first Acquire() error = %v, want the lock", err)
	}
	t.Cleanup(func() { _ = firstLock.Release() })

	_, secondErr := hostlock.Acquire(lockPath)
	if !hostlock.IsConflict(secondErr) {
		t.Fatalf("second Acquire() error = %v, want a conflict", secondErr)
	}

	agent, recorder := conflictTestAgent("worker-2")
	agent.setBlockPerNodeState(secondErr != nil)

	if !agent.localAgentConflict {
		t.Error("the second agent on a host must record the conflict")
	}
	select {
	case <-recorder.Events:
	default:
		t.Error("the second agent on a host must report the conflict")
	}
}

// TestConflictReportSurvivesAMissingRecorderObject keeps a startup without a resolvable
// config from panicking: the agent still records the conflict, it just has nothing to
// attach the event to.
func TestConflictReportSurvivesAMissingRecorderObject(t *testing.T) {
	agent := &SBRAgent{nodeName: "worker-2", recorder: record.NewFakeRecorder(1)}

	agent.setBlockPerNodeState(true)

	if !agent.localAgentConflict {
		t.Error("localAgentConflict = false, want true even without an event target")
	}
}
