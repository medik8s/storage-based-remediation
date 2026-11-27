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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
	"github.com/medik8s/storage-based-remediation/v5/internal/budget"
	"github.com/medik8s/storage-based-remediation/v5/internal/mocks"
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

// The reset budget refuses a second autonomous attempt in the same outage
// (design §5.6 lines 562-563): the refusal leaves everything untouched —
// no self-fence latch, no decision recorded — so the watchdog keeps being
// fed while storage may still recover. The BudgetExhausted event is the
// only observable side effect. The reboot method "none" keeps the
// permitted path from rebooting anything.
func TestExecuteSelfFencingRefusedWhenBudgetExhausted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		source    string
		exhausted bool
		wantEvent bool
	}{
		{"autonomous with a consumed outage refuses", bootunit.SourceAutonomous, true, true},
		{"autonomous with a fresh outage proceeds", bootunit.SourceAutonomous, false, false},
		{"requested is never gated by the budget", bootunit.SourceRequested, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := record.NewFakeRecorder(8)
			agent := &SBRAgent{
				nodeName:       "worker-1",
				recorder:       rec,
				rebootMethod:   RebootMethodNone,
				recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
			}
			ledger := budget.NewLedger(nil, nil)
			if tc.exhausted {
				if err := ledger.Charge(budget.Attempt{
					ID: "incident-1", EpochID: "local-epoch-1", Kind: budget.KindAutonomous,
				}, time.Unix(0, 0)); err != nil {
					t.Fatal(err)
				}
			}
			agent.budgetLedger = ledger

			committed := agent.executeSelfFencing("storage failures", tc.source)

			sawExhausted := false
		drain:
			for {
				select {
				case e := <-rec.Events:
					if strings.Contains(e, EventReasonBudgetExhausted) {
						sawExhausted = true
					}
				default:
					break drain
				}
			}
			if sawExhausted != tc.wantEvent {
				t.Fatalf("BudgetExhausted event = %v, want %v", sawExhausted, tc.wantEvent)
			}
			if tc.wantEvent && committed {
				t.Fatal("a budget-refused fence must report that it did NOT commit; " +
					"otherwise the watchdog loop stops feeding and the hardware watchdog reboots anyway")
			}
			if !tc.wantEvent && !committed {
				t.Fatal("a permitted fence must report that it committed")
			}
			if latched := agent.isSelfFenceDetected(); latched == tc.wantEvent {
				// wantEvent==true means the fence was REFUSED: the latch
				// must be down so the watchdog keeps being fed. wantEvent
				// ==false means the fence proceeded (method "none": the
				// latch is up but reboots nothing).
				t.Fatalf("self-fence latch = %v; a refused fence must not latch it", latched)
			}
		})
	}
}

// chargeBudget wires a reboot commitment into the ledger: best-effort
// persistence, identity carried from the fence decision. With reboot
// method "none" executeSelfFencing deliberately does not charge (nothing
// reboots, so nothing is committed), which is why this tests the charging
// helper directly.
func TestChargeBudgetConsumesTheOutage(t *testing.T) {
	rec := record.NewFakeRecorder(8)
	agent := &SBRAgent{
		nodeName:       "worker-1",
		recorder:       rec,
		rebootMethod:   RebootMethodNone,
		recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
	}
	var persisted []budget.State
	ledger := budget.NewLedger(nil, func(s budget.State) error {
		persisted = append(persisted, s)
		return nil
	})
	agent.budgetLedger = ledger

	agent.chargeBudget(sourceToBudgetKind(bootunit.SourceAutonomous), "incident-42", "local-epoch-42")

	if ledger.PermitsAutonomous() {
		t.Fatal("an autonomous charge must consume the outage's attempt")
	}
	if len(persisted) != 1 || persisted[0].Attempts[0].ID != "incident-42" {
		t.Fatalf("persisted = %+v, want one charge for incident-42", persisted)
	}

	// A requested charge marks the outage without consuming the allowance.
	agent2 := &SBRAgent{
		nodeName:       "worker-2",
		recorder:       rec,
		recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-2"}},
	}
	ledger2 := budget.NewLedger(nil, nil)
	agent2.budgetLedger = ledger2
	agent2.chargeBudget(sourceToBudgetKind(bootunit.SourceRequested), "cr-uid-7", "local-epoch-7")
	if !ledger2.PermitsAutonomous() {
		t.Fatal("a requested charge must not consume the autonomous allowance")
	}
}

// The BudgetExhausted report is latched: the watchdog loop evaluates every
// tick, so an unlatched event would flood the API for a long outage.
func TestReportBudgetExhaustedOnce(t *testing.T) {
	rec := record.NewFakeRecorder(8)
	agent := &SBRAgent{
		nodeName:       "worker-1",
		recorder:       rec,
		recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
	}

	for i := 0; i < 5; i++ {
		agent.reportBudgetExhaustedOnce()
	}

	count := 0
drain:
	for {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, EventReasonBudgetExhausted) {
				count++
			}
		default:
			break drain
		}
	}
	if count != 1 {
		t.Fatalf("BudgetExhausted events = %d, want exactly 1 for one outage", count)
	}

	// Re-arming (what the loop does once the budget permits again) allows a
	// report for the NEXT outage.
	agent.budgetRefusalReported.Store(false)
	agent.reportBudgetExhaustedOnce()
	count = 0
	for i := 0; i < 3; i++ {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, EventReasonBudgetExhausted) {
				count++
			}
		default:
		}
	}
	if count != 1 {
		t.Fatalf("after re-arming, BudgetExhausted events = %d, want 1", count)
	}
}

// autonomousBudgetSpent is the watchdog loop's cheap pre-check and must
// follow the ledger exactly, including the nil-ledger (non-BlockPerNode) case.
func TestAutonomousBudgetSpent(t *testing.T) {
	agent := &SBRAgent{}
	if agent.autonomousBudgetSpent() {
		t.Fatal("no ledger means no budget: the feature is off, not exhausted")
	}

	ledger := budget.NewLedger(nil, nil)
	agent.budgetLedger = ledger
	if agent.autonomousBudgetSpent() {
		t.Fatal("a fresh ledger permits an autonomous attempt")
	}
	if err := ledger.Charge(budget.Attempt{
		ID: "incident-1", EpochID: "local-e1", Kind: budget.KindAutonomous,
	}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if !agent.autonomousBudgetSpent() {
		t.Fatal("a charged outage must read as spent")
	}

	// A requested-only charge marks the outage without spending it.
	ledger2 := budget.NewLedger(nil, nil)
	agent.budgetLedger = ledger2
	if err := ledger2.Charge(budget.Attempt{
		ID: "cr-1", EpochID: "local-e2", Kind: budget.KindRequested,
	}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if agent.autonomousBudgetSpent() {
		t.Fatal("a requested reboot must not spend the autonomous allowance")
	}
}

// HistoryUnknown is reported (design §5.10), and once the ledger has trusted
// history it is not reported again.
func TestLoadBudgetLedgerReportsUnknownHistory(t *testing.T) {
	rec := record.NewFakeRecorder(8)
	agent := &SBRAgent{
		nodeName:       "worker-1",
		recorder:       rec,
		recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
	}
	// No journal: the feature is off, nothing to report.
	if l := agent.loadBudgetLedger(); l != nil {
		t.Fatal("no journal must yield a nil ledger")
	}
	select {
	case e := <-rec.Events:
		t.Fatalf("unexpected event without a journal: %q", e)
	default:
	}
}

// Recovery is measured from the latest charge, not from a healthy clock that
// started earlier: a charge must still cost a full recovery period even when
// storage was already healthy when it landed.
func TestRecordStorageOutcomeMeasuresRecoveryFromTheCharge(t *testing.T) {
	agent := &SBRAgent{autonomousRebootRecoveryPeriod: 5 * time.Minute}
	ledger := budget.NewLedger(nil, nil)
	agent.budgetLedger = ledger

	// Storage has been healthy for well over a period...
	agent.storageHealthySince = time.Now().Add(-time.Hour)
	// ...and a charge lands now.
	if err := ledger.Charge(budget.Attempt{
		ID: "incident-1", EpochID: "local-e1", Kind: budget.KindAutonomous,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	agent.recordStorageOutcome(true)

	if ledger.PermitsAutonomous() {
		t.Fatal("the epoch must survive: the healthy clock predates the charge, " +
			"so no full recovery has been observed since it")
	}
	// The rejoin-release gate still sees the older clock, unchanged.
	if d, ok := agent.storageHealthyFor(); !ok || d < 50*time.Minute {
		t.Fatalf("storageHealthySince was disturbed by the charge: %v (ok=%v)", d, ok)
	}
}

// A spent budget keeps the node alive even when the remediation state cannot be
// read: the autonomous path is closed by design, and a pending requested reboot
// still arrives through the peer's fence path, which the budget never gates.
func TestHandleWatchdogTickSBRUnhealthyPetsWhenBudgetSpentAndLookupFails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		budgetSpent bool
		wantPet     bool
	}{
		{"spent budget pets", true, true},
		{"live budget skips the pet (reboot imminent)", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := record.NewFakeRecorder(8)
			wd := mocks.NewMockWatchdog("/dev/null")
			agent := &SBRAgent{
				nodeName:       "worker-1",
				recorder:       rec,
				recorderObject: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
				watchdog:       wd,
				k8sClient:      failingClient{}, // the remediation lookup always errors
				ctx:            context.Background(),
			}

			agent.handleWatchdogTickSBRUnhealthy(tc.budgetSpent)

			pet := wd.GetPetCount() > 0
			if pet != tc.wantPet {
				t.Fatalf("watchdog pet = %v, want %v (petCount=%d)", pet, tc.wantPet, wd.GetPetCount())
			}
		})
	}
}
