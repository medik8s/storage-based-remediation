package watcher

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

func defaultTiming() blockpernode.ProbeTiming {
	return blockpernode.ProbeTiming{ProbeInterval: metav1.Duration{Duration: 5 * time.Second}, IOTimeout: metav1.Duration{Duration: 15 * time.Second}, MaxConsecutiveFailures: 7}
}

func TestExpiryUsesContinuousCompletedProgress(t *testing.T) {
	timing := defaultTiming()
	start := time.Now()
	for _, initial := range []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionUnknown, ""} {
		t.Run(string(initial), func(t *testing.T) {
			var condition *corev1.NodeCondition
			if initial != "" {
				condition = &corev1.NodeCondition{Status: initial}
			}
			var e Evidence
			for second := 0; second <= 80; second += 5 {
				if status, _ := e.Observe(start.Add(time.Duration(second)*time.Second), 40, timing, condition); status != "" {
					t.Fatalf("early expiry at %d", second)
				}
			}
			if status, reason := e.Observe(start.Add(81*time.Second), 40, timing, condition); status != corev1.ConditionTrue || reason != "HeartbeatTimeout" {
				t.Fatalf("threshold result %s %s", status, reason)
			}
		})
	}
	var e Evidence
	condition := &corev1.NodeCondition{Status: corev1.ConditionFalse}
	for second := 0; second <= 80; second += 5 {
		e.Observe(start.Add(time.Duration(second)*time.Second), 40, timing, condition)
	}
	if status, _ := e.Observe(start.Add(81*time.Second), 41, timing, condition); status != "" || e.Classification != "Progressing" {
		t.Fatal("advance did not precede boundary expiry")
	}
	e.Reset("Restart")
	first := 0
	for second := 0; second <= 85; second += 5 {
		if status, _ := e.Observe(start.Add(time.Duration(second)*time.Second), 40, timing, condition); status == corev1.ConditionTrue {
			first = second
		}
	}
	if first != 85 {
		t.Fatalf("first fixed poll %d", first)
	}
}

func TestRecoveryRequiresThreeDistinctAdvances(t *testing.T) {
	timing := defaultTiming()
	start := time.Now()
	for _, initial := range []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionUnknown, ""} {
		var condition *corev1.NodeCondition
		if initial != "" {
			condition = &corev1.NodeCondition{Status: initial}
		}
		var e Evidence
		for i, sequence := range []uint64{1, 2, 2, 4, 5} {
			status, reason := e.Observe(start.Add(time.Duration(i)*5*time.Second), sequence, timing, condition)
			if i < 4 && status != "" {
				t.Fatalf("early recovery at %d", i)
			}
			if i == 4 {
				want := "HeartbeatProgressObserved"
				if initial == corev1.ConditionTrue {
					want = "Recovered"
				}
				if status != corev1.ConditionFalse || reason != want || e.advances != 3 {
					t.Fatalf("recovery %s %s count=%d", status, reason, e.advances)
				}
			}
		}
	}
}

func TestGapsDecreaseAndRestartDiscardEvidence(t *testing.T) {
	timing := defaultTiming()
	start := time.Now()
	condition := &corev1.NodeCondition{Status: corev1.ConditionTrue}
	var e Evidence
	e.Observe(start, 10, timing, condition)
	e.Observe(start.Add(5*time.Second), 11, timing, condition)
	e.Observe(start.Add(10*time.Second), 12, timing, condition)
	e.Observe(start.Add(15*time.Second), 1, timing, condition)
	if e.baseline || e.advances != 0 || e.Reason != "SequenceDecreased" {
		t.Fatal("decrease retained evidence")
	}
	e.Observe(start.Add(20*time.Second), 2, timing, condition)
	if e.advances != 0 {
		t.Fatal("new baseline earned recovery credit")
	}
	e.Observe(start.Add(31*time.Second), 100, timing, condition)
	if e.advances != 0 {
		t.Fatal("gap retained evidence")
	}
	e.Reset("ReadError")
	for i, sequence := range []uint64{100, 101, 102} {
		if status, _ := e.Observe(start.Add(time.Duration(40+i*5)*time.Second), sequence, timing, condition); status != "" {
			t.Fatal("restart reused recovery credit")
		}
	}
}
