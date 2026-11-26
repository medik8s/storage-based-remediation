package blockpernode

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	OwnerAnnotation       = "sbr.medik8s.io/configuration-owner"
	GroupAnnotation       = "sbr.medik8s.io/heartbeat-group"
	ConfigurationUIDLabel = "sbr.medik8s.io/configuration-uid"
	TargetsEnv            = "SBR_WATCHER_TARGETS"
	NodeNameEnv           = "SBR_WATCHER_NODE_NAME"
	ProbeIntervalEnv      = "SBR_PROBE_INTERVAL"
	IOTimeoutEnv          = "SBR_IO_TIMEOUT"
	FailuresEnv           = "SBR_MAX_CONSECUTIVE_FAILURES"
	ProbeVolume           = "storage-probe"
	ProbeDevice           = "/dev/sbr-probe"
	StorageCondition      = "SBRStorageUnhealthy"
	Active                = "Active"
	Conflict              = "Conflict"
	Retiring              = "Retiring"
	Shutdown              = "Shutdown"
	Released              = "Released"
)

type ConfigurationIdentity struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

func (c ConfigurationIdentity) Valid() bool { return c.Namespace != "" && c.Name != "" && c.UID != "" }

type ConfigurationOwner struct {
	Version             int                   `json:"version"`
	NodeUID             types.UID             `json:"nodeUID"`
	Configuration       ConfigurationIdentity `json:"configuration"`
	OwnershipGeneration string                `json:"ownershipGeneration"`
	Phase               string                `json:"phase"`
}

func DecodeOwner(value string) (ConfigurationOwner, error) {
	var owner ConfigurationOwner
	if err := json.Unmarshal([]byte(value), &owner); err != nil {
		return owner, err
	}
	if owner.Version != 1 || owner.NodeUID == "" || !owner.Configuration.Valid() || owner.OwnershipGeneration == "" {
		return owner, fmt.Errorf("incomplete configuration owner")
	}
	switch owner.Phase {
	case Active, Conflict, Retiring, Shutdown, Released:
	default:
		return owner, fmt.Errorf("invalid owner phase %q", owner.Phase)
	}
	return owner, nil
}

type WatcherTargets struct {
	Version       int                   `json:"version"`
	Configuration ConfigurationIdentity `json:"configuration"`
	Group         string                `json:"group"`
	Targets       []WatcherTarget       `json:"targets"`
}

type WatcherTarget struct {
	Name                string        `json:"name"`
	UID                 types.UID     `json:"uid"`
	OwnershipGeneration string        `json:"ownershipGeneration"`
	Timing              *ProbeTiming  `json:"timing,omitempty"`
	Probe               *ProbeMapping `json:"probe,omitempty"`
}

type ProbeTiming struct {
	ProbeInterval          metav1.Duration `json:"probeInterval"`
	IOTimeout              metav1.Duration `json:"ioTimeout"`
	MaxConsecutiveFailures int             `json:"maxConsecutiveFailures"`
}

func (t ProbeTiming) Validate() error {
	if t.ProbeInterval.Duration < time.Second || t.ProbeInterval.Duration > 50*time.Second || t.IOTimeout.Duration < time.Second || t.IOTimeout.Duration > 10*time.Minute || t.MaxConsecutiveFailures < 2 || t.MaxConsecutiveFailures > 32 {
		return fmt.Errorf("invalid probe timing")
	}
	return nil
}

func (t ProbeTiming) ReadDeadline() time.Duration {
	return min(t.ProbeInterval.Duration, t.IOTimeout.Duration)
}
func (t ProbeTiming) Gap() time.Duration { return t.ProbeInterval.Duration + t.ReadDeadline() }
func (t ProbeTiming) Expiry() time.Duration {
	return 2*(t.IOTimeout.Duration+t.ProbeInterval.Duration) + time.Second + time.Duration(t.MaxConsecutiveFailures-1)*t.ProbeInterval.Duration + t.Gap()
}

func ParseTiming(values map[string]string) (*ProbeTiming, error) {
	p, e1 := time.ParseDuration(values[ProbeIntervalEnv])
	i, e2 := time.ParseDuration(values[IOTimeoutEnv])
	m, e3 := strconv.Atoi(values[FailuresEnv])
	t := &ProbeTiming{ProbeInterval: metav1.Duration{Duration: p}, IOTimeout: metav1.Duration{Duration: i}, MaxConsecutiveFailures: m}
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, fmt.Errorf("missing or invalid literal probe settings")
	}
	return t, t.Validate()
}

type ProbeMapping struct {
	AgentPodUID    types.UID `json:"agentPodUID"`
	ClaimNamespace string    `json:"claimNamespace"`
	ClaimName      string    `json:"claimName"`
	ClaimUID       types.UID `json:"claimUID"`
	DevicePath     string    `json:"devicePath"`
}

func DevicePath(index int) string { return fmt.Sprintf("%s-%d", ProbeDevice, index) }

func (w WatcherTargets) Validate(host string) error {
	if w.Version != 1 || !w.Configuration.Valid() || w.Group == "" {
		return fmt.Errorf("invalid watcher assignment")
	}
	uids := map[types.UID]bool{}
	for i, target := range w.Targets {
		if target.Name == "" || target.UID == "" || target.OwnershipGeneration == "" || uids[target.UID] || (i > 0 && w.Targets[i-1].Name >= target.Name) {
			return fmt.Errorf("invalid target identity or order at index %d", i)
		}
		if target.Name == host {
			return fmt.Errorf("watcher host %q occurs in target group", host)
		}
		uids[target.UID] = true
		if target.Timing != nil {
			if err := target.Timing.Validate(); err != nil {
				return err
			}
		}
		if p := target.Probe; p != nil {
			if p.AgentPodUID == "" || p.ClaimNamespace != w.Configuration.Namespace || p.ClaimName == "" || p.ClaimUID == "" || p.DevicePath != DevicePath(i) {
				return fmt.Errorf("invalid probe mapping for %s", target.Name)
			}
		}
	}
	return nil
}

func (w WatcherTargets) Matches(node *corev1.Node, target WatcherTarget) bool {
	if node == nil || node.Name != target.Name || node.UID != target.UID || node.Annotations[GroupAnnotation] != w.Group {
		return false
	}
	o, err := DecodeOwner(node.Annotations[OwnerAnnotation])
	return err == nil && o.NodeUID == target.UID && o.Configuration == w.Configuration && o.OwnershipGeneration == target.OwnershipGeneration && o.Phase == Active
}

func Condition(node *corev1.Node) *corev1.NodeCondition {
	if node != nil {
		for i := range node.Status.Conditions {
			if string(node.Status.Conditions[i].Type) == StorageCondition {
				c := node.Status.Conditions[i]
				return &c
			}
		}
	}
	return nil
}

func SameCondition(a, b *corev1.NodeCondition) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Status == b.Status && a.LastTransitionTime.Equal(&b.LastTransitionTime)
}
