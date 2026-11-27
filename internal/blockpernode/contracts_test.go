package blockpernode

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func contractFixture() WatcherTargets {
	return WatcherTargets{Version: 1, ConfigUID: "config-uid", Group: "group", Targets: []WatcherTarget{{Name: "a", UID: "a-uid", OwnershipGeneration: "generation", Timing: &ProbeTiming{ProbeInterval: metav1.Duration{Duration: 5 * time.Second}, IOTimeout: metav1.Duration{Duration: 15 * time.Second}, MaxConsecutiveFailures: 7}, Probe: &ProbeMapping{AgentPodUID: "pod", ClaimName: "claim", ClaimUID: "claim-uid", DevicePath: DevicePath(0)}}, {Name: "b", UID: "b-uid", OwnershipGeneration: "generation"}}}
}

func TestWatcherContractsRejectInvalidAssignments(t *testing.T) {
	base := contractFixture()
	if err := base.Validate("host"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*WatcherTargets){
		"version":        func(w *WatcherTargets) { w.Version = 2 },
		"configuration":  func(w *WatcherTargets) { w.ConfigUID = "" },
		"duplicate name": func(w *WatcherTargets) { w.Targets[1].Name = "a" },
		"duplicate UID":  func(w *WatcherTargets) { w.Targets[1].UID = "a-uid" },
		"order":          func(w *WatcherTargets) { w.Targets[0], w.Targets[1] = w.Targets[1], w.Targets[0] },
		"path":           func(w *WatcherTargets) { w.Targets[0].Probe.DevicePath = DevicePath(1) },
		"partial":        func(w *WatcherTargets) { w.Targets[0].Probe.AgentPodUID = "" },
		"timing":         func(w *WatcherTargets) { w.Targets[0].Timing.MaxConsecutiveFailures = 1 },
	}
	data, _ := json.Marshal(base)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var w WatcherTargets
			_ = json.Unmarshal(data, &w)
			mutate(&w)
			if w.Validate("host") == nil {
				t.Fatal("accepted invalid assignment")
			}
		})
	}
	if base.Validate("b") == nil {
		t.Fatal("unmapped self-host target was accepted")
	}
	if got := base.Targets[0].Timing.Expiry(); got != 81*time.Second {
		t.Fatalf("expiry %s", got)
	}
}

func TestOwnerAndAssignmentChecks(t *testing.T) {
	w := contractFixture()
	o := ConfigurationOwner{Version: 1, NodeUID: "a-uid", ConfigUID: w.ConfigUID, OwnershipGeneration: "generation", Phase: Active}
	b, _ := json.Marshal(o)
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "a-uid", Annotations: map[string]string{OwnerAnnotation: string(b), GroupAnnotation: w.Group}}}
	if !w.Matches(n, w.Targets[0]) {
		t.Fatal("valid owner rejected")
	}
	for _, phase := range []string{Retiring, Shutdown, Released} {
		o.Phase = phase
		b, _ = json.Marshal(o)
		n.Annotations[OwnerAnnotation] = string(b)
		if w.Matches(n, w.Targets[0]) {
			t.Fatalf("accepted phase %s", phase)
		}
	}
	for _, value := range []string{"{}", `{"version":2}`, `{"version":1,"phase":"other"}`} {
		if _, err := DecodeOwner(value); err == nil {
			t.Fatal("accepted invalid owner")
		}
	}
}
