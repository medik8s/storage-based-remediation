package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type Reader interface{ Read() (uint64, error) }

type Runtime struct {
	Assignment blockpernode.WatcherTargets
	Nodes      *NodeState
	Client     client.Client
	Readers    map[types.UID]Reader
	Log        logr.Logger
}

func (r *Runtime) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for _, target := range r.Assignment.Targets {
		if target.Probe == nil || target.Timing == nil {
			reason := "ProbeUnavailable"
			if target.Timing == nil {
				reason = "TimingUnknown"
			}
			r.Log.Info("Target state", "target", target.Name, "classification", "Unknown", "reason", reason)
			continue
		}
		reader := r.Readers[target.UID]
		if reader == nil {
			continue
		}
		workers.Add(1)
		go func() { defer workers.Done(); r.runTarget(ctx, target, reader) }()
	}
	workers.Wait()
}

type readResult struct {
	sequence  uint64
	err       error
	completed time.Time
}

func (r *Runtime) runTarget(ctx context.Context, target blockpernode.WatcherTarget, reader Reader) {
	timing := *target.Timing
	ticker := time.NewTicker(timing.ProbeInterval.Duration)
	defer ticker.Stop()
	var evidence Evidence
	var observed *corev1.NodeCondition
	var epoch uint64
	var result <-chan readResult
	var deadline <-chan time.Time
	var timer *time.Timer
	var started time.Time
	var readEpoch uint64
	var readCondition *corev1.NodeCondition
	late := false
	refresh := func() *corev1.Node {
		node, currentEpoch, available := r.Nodes.Snapshot(target.Name)
		condition := blockpernode.Condition(node)
		if !available || !r.Assignment.Matches(node, target) {
			evidence.Reset("StateUnavailable")
			observed, epoch = condition, currentEpoch
			return nil
		}
		if epoch != currentEpoch || !blockpernode.SameCondition(observed, condition) {
			evidence.Reset("NodeStateChanged")
		}
		observed, epoch = condition, currentEpoch
		return node
	}
	startRead := func() {
		if refresh() == nil {
			return
		}
		if result != nil {
			evidence.Reset("SkippedPoll")
			return
		}
		ch := make(chan readResult, 1)
		result = ch
		started, late = time.Now(), false
		readEpoch, readCondition = epoch, observed
		timer = time.NewTimer(timing.ReadDeadline())
		deadline = timer.C
		go func() {
			sequence, err := reader.Read()
			ch <- readResult{sequence: sequence, err: err, completed: time.Now()}
		}()
	}
	startRead()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			startRead()
		case <-deadline:
			late, deadline = true, nil
			evidence.Reset("ReadTimeout")
			r.Log.Info("Target state", "target", target.Name, "classification", evidence.Classification, "reason", evidence.Reason)
		case sample := <-result:
			result = nil
			if timer != nil {
				timer.Stop()
			}
			deadline = nil
			if late || sample.completed.Sub(started) > timing.ReadDeadline() {
				evidence.Reset("LateRead")
				continue
			}
			if sample.err != nil {
				evidence.Reset("ReadError")
				r.Log.Error(sample.err, "Probe read failed", "target", target.Name)
				continue
			}
			node := refresh()
			if node == nil {
				continue
			}
			if readEpoch != epoch || !blockpernode.SameCondition(readCondition, observed) {
				evidence.Reset("NodeStateChangedDuringRead")
				continue
			}
			status, reason := evidence.Observe(sample.completed, sample.sequence, timing, observed)
			r.Log.Info("Target state", "target", target.Name, "classification", evidence.Classification, "reason", evidence.Reason)
			if status != "" {
				r.transition(ctx, target, node, epoch, status, reason, sample.completed)
			}
		}
	}
}

func (r *Runtime) transition(ctx context.Context, target blockpernode.WatcherTarget, observed *corev1.Node, epoch uint64, status corev1.ConditionStatus, reason string, sampled time.Time) {
	originalCondition := blockpernode.Condition(observed)
	candidate := observed
	for {
		current, currentEpoch, available := r.Nodes.Snapshot(target.Name)
		remaining := target.Timing.Gap() - time.Since(sampled)
		if remaining <= 0 || !available || currentEpoch != epoch || !r.Assignment.Matches(current, target) || !blockpernode.SameCondition(originalCondition, blockpernode.Condition(current)) {
			return
		}
		if condition := blockpernode.Condition(current); condition != nil && condition.Status == status {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, min(target.Timing.ProbeInterval.Duration, remaining))
		if candidate == nil {
			candidate = current
		}
		err := blockpernode.PatchCondition(attempt, r.Client, candidate, status, reason, fmt.Sprintf("Storage probe for target %s: %s", target.Name, reason), time.Now())
		cancel()
		if err == nil {
			return
		}
		remaining = target.Timing.Gap() - time.Since(sampled)
		if remaining <= 0 {
			return
		}
		live := &corev1.Node{}
		getCtx, getCancel := context.WithTimeout(ctx, min(target.Timing.ProbeInterval.Duration, remaining))
		err = r.Client.Get(getCtx, types.NamespacedName{Name: target.Name}, live)
		getCancel()
		if err != nil {
			r.Nodes.Invalidate()
			return
		}
		condition := blockpernode.Condition(live)
		if condition != nil && condition.Status == status {
			return
		}
		if !r.Assignment.Matches(live, target) || !blockpernode.SameCondition(originalCondition, condition) {
			return
		}
		// A live read resolves a failed response without changing the sample time.
		candidate = live
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(100*time.Millisecond, remaining)):
		}
	}
}
