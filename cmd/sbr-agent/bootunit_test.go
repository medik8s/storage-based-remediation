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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// failingClient fails every Get. The disabled gate must not touch the API at
// all — let alone the host — so any call reaching it turns into a test
// failure.
type failingClient struct {
	client.Client
}

func (failingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return errors.New("Get must not be called when the boot unit is disabled")
}

// A disabled boot unit must do nothing: no Node read (and therefore nothing
// that could follow it — no identity write, no install, no gate), and no error
// reported for a state that is a supported deployment, not a fault.
func TestMaybeSetupBootUnitDisabledTouchesNothing(t *testing.T) {
	nodeUID, err := maybeSetupBootUnit(
		context.Background(), failingClient{}, "node-a", nil, false, false)
	if err != nil {
		t.Fatalf("disabled boot unit returned error: %v", err)
	}
	if nodeUID != "" {
		t.Fatalf("disabled boot unit returned node UID %q, want empty", nodeUID)
	}
}

// An enabled boot unit delegates to setupBootUnit, which starts by reading the
// Node: a failing client proves the delegation happens.
func TestMaybeSetupBootUnitEnabledDelegates(t *testing.T) {
	_, err := maybeSetupBootUnit(
		context.Background(), failingClient{}, "node-a", nil, false, true)
	if err == nil {
		t.Fatal("enabled boot unit must propagate the setup error")
	}
}

// stubClearArmingFlag replaces the host-touching clear for a test and counts
// calls. The real function removes /var/lib/medik8s/state/sbr-armed, which a
// unit test must never touch.
func stubClearArmingFlag(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := clearArmingFlag
	t.Cleanup(func() { clearArmingFlag = orig })
	clearArmingFlag = func() error {
		calls++
		return nil
	}
	return &calls
}

// A control-plane node that was enrolled while a worker may still carry the
// boot unit's gate: the promotion must clear it, so the unit cannot cordon a
// control-plane member at the next boot. A fresh control-plane node (no gate)
// still succeeds — the clear is a no-op.
func TestSetupBootUnitClearsGateOnControlPlaneNode(t *testing.T) {
	calls := stubClearArmingFlag(t)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp-1", UID: "uid-cp"}}
	node.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	uid, err := maybeSetupBootUnit(context.Background(), c, "cp-1", nil, false, true)
	if err != nil {
		t.Fatalf("setupBootUnit on control-plane: %v", err)
	}
	if uid != "uid-cp" {
		t.Fatalf("node UID = %q, want uid-cp", uid)
	}
	if *calls != 1 {
		t.Fatalf("gate clear calls = %d, want 1", *calls)
	}
}

// In detect-only mode the gate is cleared BEFORE any read or install work:
// the clear is the safety property of that path and must happen even when
// everything after it fails — here, the very first Node read.
func TestSetupBootUnitDetectOnlyClearsGateBeforeFailingReads(t *testing.T) {
	calls := stubClearArmingFlag(t)

	_, err := maybeSetupBootUnit(context.Background(), failingClient{}, "node-a", nil, true, true)
	if err == nil {
		t.Fatal("want the Node-read error to surface")
	}
	if *calls != 1 {
		t.Fatalf("gate clear calls = %d, want 1 — the clear must precede the failing read", *calls)
	}
}

// Exactly one caller owns the fence: while one executeSelfFencing is in
// progress (here: the mutex is held by the test), a second caller stands
// down without emitting the fence event or touching the journal — the owner
// is already rebooting the host, which is the outcome the second caller
// wanted. The reboot method "none" keeps the test from rebooting anything.
func TestExecuteSelfFencingStandsDownWhenAnotherFenceIsInProgress(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	agent := &SBRAgent{
		nodeName:       "worker-1",
		recorder:       rec,
		rebootMethod:   RebootMethodNone,
		recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
	}

	agent.fenceExecMutex.Lock()
	t.Cleanup(agent.fenceExecMutex.Unlock)

	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.executeSelfFencing("concurrent trigger", bootunit.SourceAutonomous)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the concurrent caller did not stand down; it must not block on the fence owner")
	}
	select {
	case e := <-rec.Events:
		t.Fatalf("the stood-down caller emitted %q; only the fence owner may act", e)
	default:
	}
}
