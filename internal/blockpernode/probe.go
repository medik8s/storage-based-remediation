package blockpernode

import (
	"context"
	"time"
)

type Prober interface {
	Probe(sequence, completed uint64) error
}

type ProbeProgress struct {
	Sequence, Completed uint64
	Failures            int
	ObservedAt          time.Time
}

type probeResult struct {
	sequence uint64
	err      error
	finished time.Time
}

// RunProbe keeps one unfinished operation and evaluates failures at least P apart.
func RunProbe(ctx context.Context, timing ProbeTiming, device Prober, report func(ProbeProgress)) {
	ticker := time.NewTicker(timing.ProbeInterval.Duration)
	defer ticker.Stop()
	var progress ProbeProgress
	var result <-chan probeResult
	var deadline <-chan time.Time
	var timer *time.Timer
	var started, nextEvaluation time.Time
	late := false
	publish := func() { progress.ObservedAt = time.Now(); report(progress) }
	fail := func(now time.Time) {
		if progress.Failures == 0 {
			progress.Failures = 1
			nextEvaluation = now.Add(timing.ProbeInterval.Duration)
			publish()
		}
	}
	finish := func(sample probeResult) {
		result = nil
		if timer != nil {
			timer.Stop()
		}
		deadline = nil
		if late {
			return
		}
		if sample.err != nil || sample.finished.Sub(started) > timing.IOTimeout.Duration {
			fail(sample.finished)
		} else {
			progress.Completed, progress.Failures = sample.sequence, 0
			publish()
		}
	}
	start := func() {
		if result != nil {
			return
		}
		progress.Sequence++
		sequence, completed := progress.Sequence, progress.Completed
		ch := make(chan probeResult, 1)
		result = ch
		started, late = time.Now(), false
		timer = time.NewTimer(timing.IOTimeout.Duration)
		deadline = timer.C
		go func() {
			err := device.Probe(sequence, completed)
			ch <- probeResult{sequence: sequence, err: err, finished: time.Now()}
		}()
	}
	start()
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
			// A completed success takes precedence over a simultaneous failure evaluation.
			select {
			case sample := <-result:
				finish(sample)
			default:
			}
			now := time.Now()
			if progress.Failures > 0 && !now.Before(nextEvaluation) {
				progress.Failures++
				nextEvaluation = now.Add(timing.ProbeInterval.Duration)
				publish()
			}
			start()
		case <-deadline:
			late, deadline = true, nil
			fail(time.Now())
		case sample := <-result:
			finish(sample)
		}
	}
}
