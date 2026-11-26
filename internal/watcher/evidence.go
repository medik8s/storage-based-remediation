package watcher

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type Evidence struct {
	baseline       bool
	completed      uint64
	lastValid      time.Time
	progress       time.Time
	advances       int
	Classification string
	Reason         string
}

func (e *Evidence) Reset(reason string) { *e = Evidence{Classification: "Unknown", Reason: reason} }

func (e *Evidence) Observe(now time.Time, sequence uint64, timing blockpernode.ProbeTiming, condition *corev1.NodeCondition) (corev1.ConditionStatus, string) {
	if e.baseline && now.Sub(e.lastValid) > timing.Gap() {
		e.Reset("ReadGap")
	}
	if !e.baseline {
		e.baseline, e.completed, e.lastValid, e.progress = true, sequence, now, now
		e.Classification, e.Reason = "Unknown", "Baseline"
		return "", ""
	}
	if sequence < e.completed {
		e.Reset("SequenceDecreased")
		return "", ""
	}
	e.lastValid = now
	if sequence > e.completed {
		e.completed, e.progress = sequence, now
		e.advances++
		e.Classification, e.Reason = "Progressing", "CompletedProgress"
		if (condition == nil || condition.Status != corev1.ConditionFalse) && e.advances >= 3 {
			reason := "HeartbeatProgressObserved"
			if condition != nil && condition.Status == corev1.ConditionTrue {
				reason = "Recovered"
			}
			return corev1.ConditionFalse, reason
		}
		return "", ""
	}
	if now.Sub(e.progress) >= timing.Expiry() {
		e.Classification, e.Reason = "Expired", "HeartbeatTimeout"
		if condition == nil || condition.Status != corev1.ConditionTrue {
			return corev1.ConditionTrue, "HeartbeatTimeout"
		}
	}
	return "", ""
}
