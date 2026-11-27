package blockpernode

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type sequenceProber struct {
	mu      sync.Mutex
	inputs  [][2]uint64
	blocked chan struct{}
}

func (p *sequenceProber) Probe(sequence, completed uint64) error {
	p.mu.Lock()
	p.inputs = append(p.inputs, [2]uint64{sequence, completed})
	index := len(p.inputs)
	p.mu.Unlock()
	if p.blocked != nil {
		<-p.blocked
	}
	if index == 2 {
		return fmt.Errorf("verification failed")
	}
	return nil
}

func TestProbePublishesOnlyVerifiedCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := &sequenceProber{}
	timing := ProbeTiming{ProbeInterval: metav1.Duration{Duration: 10 * time.Millisecond}, IOTimeout: metav1.Duration{Duration: 5 * time.Millisecond}, MaxConsecutiveFailures: 2}
	RunProbe(ctx, timing, p, func(progress ProbeProgress) {
		if progress.Sequence == 3 {
			cancel()
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inputs) < 3 || p.inputs[0] != [2]uint64{1, 0} || p.inputs[1] != [2]uint64{2, 1} || p.inputs[2] != [2]uint64{3, 1} {
		t.Fatalf("published unverified progress: %v", p.inputs)
	}
}

func TestProbeKeepsOneBlockedOperationAndDiscardsLateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &sequenceProber{blocked: make(chan struct{})}
	timing := ProbeTiming{ProbeInterval: metav1.Duration{Duration: 10 * time.Millisecond}, IOTimeout: metav1.Duration{Duration: 5 * time.Millisecond}, MaxConsecutiveFailures: 2}
	done := make(chan struct{})
	reports := make(chan ProbeProgress, 32)
	go func() { RunProbe(ctx, timing, p, func(progress ProbeProgress) { reports <- progress }); close(done) }()
	select {
	case progress := <-reports:
		if progress.Completed != 0 {
			t.Fatal("blocked probe advanced")
		}
	case <-time.After(time.Second):
		t.Fatal("no timeout")
	}
	time.Sleep(25 * time.Millisecond)
	p.mu.Lock()
	calls := len(p.inputs)
	p.mu.Unlock()
	if calls != 1 {
		t.Fatalf("unfinished calls %d", calls)
	}
	close(p.blocked)
	for {
		select {
		case progress := <-reports:
			if progress.Completed == 1 {
				t.Fatal("late probe advanced")
			}
			if progress.Sequence >= 2 {
				cancel()
				<-done
				return
			}
		case <-time.After(time.Second):
			t.Fatal("probe did not resume")
		}
	}
}

type slowErrorProber struct{ calls int }

func (p *slowErrorProber) Probe(sequence, completed uint64) error {
	p.calls++
	if p.calls == 1 {
		time.Sleep(15 * time.Millisecond)
	}
	return fmt.Errorf("probe failed")
}

func TestProbeFailureEvaluationsWaitAfterSlowError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	timing := ProbeTiming{ProbeInterval: metav1.Duration{Duration: 20 * time.Millisecond}, IOTimeout: metav1.Duration{Duration: 50 * time.Millisecond}, MaxConsecutiveFailures: 2}
	var first, second time.Time
	RunProbe(ctx, timing, &slowErrorProber{}, func(progress ProbeProgress) {
		if progress.Failures == 1 {
			first = progress.ObservedAt
		}
		if progress.Failures == 2 {
			second = progress.ObservedAt
			cancel()
		}
	})
	if first.IsZero() || second.IsZero() || second.Sub(first) < 19*time.Millisecond {
		t.Fatalf("failed evaluations were too close: %s", second.Sub(first))
	}
}
