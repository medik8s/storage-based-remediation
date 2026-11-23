/*
Copyright 2025.

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

package v1alpha1

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RHWA-2063 (API-04), part 1: node selection primitives shared by the admission
// webhook and the configuration controller.
//
// Overlap is defined on the *selected nodes*, not on an abstract comparison of
// two selector maps. Comparing maps cannot see a relabelled node and has to
// guess about disjoint keys; intersecting the actual Node list cannot.

const (
	osLabel           = "kubernetes.io/os"
	workerRoleLabel   = "node-role.kubernetes.io/worker"
	ctrlPlaneLabel    = "node-role.kubernetes.io/control-plane"
	zoneLabel         = "topology.kubernetes.io/zone"
	storagePoolLabel  = "storage.example.com/pool"
	nodeWorkerLinuxA  = "worker-linux-a"
	nodeWorkerLinuxB  = "worker-linux-b"
	nodeWorkerWindows = "worker-windows"
	nodeControlPlane  = "control-plane-0"
	nodeUnlabelled    = "unlabelled-0"
)

// testNodes is the shared fixture for every selection test in this package.
func testNodes() []corev1.Node {
	node := func(name string, labels map[string]string) corev1.Node {
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	return []corev1.Node{
		node(nodeWorkerLinuxA, map[string]string{
			workerRoleLabel: "", osLabel: "linux", zoneLabel: "a",
		}),
		node(nodeWorkerLinuxB, map[string]string{
			workerRoleLabel: "", osLabel: "linux", zoneLabel: "b", storagePoolLabel: "fast",
		}),
		node(nodeWorkerWindows, map[string]string{
			workerRoleLabel: "", osLabel: "windows", zoneLabel: "a",
		}),
		node(nodeControlPlane, map[string]string{
			ctrlPlaneLabel: "", osLabel: "linux", zoneLabel: "a",
		}),
		node(nodeUnlabelled, nil),
	}
}

func specWithSelector(selector map[string]string) *StorageBasedRemediationConfigSpec {
	return &StorageBasedRemediationConfigSpec{NodeSelector: selector}
}

func TestEffectiveNodeSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector map[string]string
		want     map[string]string
	}{
		{
			name:     "nil selector falls back to workers and adds linux",
			selector: nil,
			want:     map[string]string{workerRoleLabel: "", osLabel: "linux"},
		},
		{
			name:     "empty selector is treated as unset",
			selector: map[string]string{},
			want:     map[string]string{workerRoleLabel: "", osLabel: "linux"},
		},
		{
			name:     "custom selector replaces the worker default and still adds linux",
			selector: map[string]string{zoneLabel: "a"},
			want:     map[string]string{zoneLabel: "a", osLabel: "linux"},
		},
		{
			name:     "explicit linux is preserved",
			selector: map[string]string{zoneLabel: "a", osLabel: "linux"},
			want:     map[string]string{zoneLabel: "a", osLabel: "linux"},
		},
		{
			// Linux-only is a restriction, not a default: a user cannot opt out
			// of it by asking for another OS.
			name:     "a non-linux OS request is overridden",
			selector: map[string]string{osLabel: "windows"},
			want:     map[string]string{osLabel: "linux"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := specWithSelector(tc.selector).EffectiveNodeSelector()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EffectiveNodeSelector() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEffectiveNodeSelectorDoesNotAliasSpec(t *testing.T) {
	spec := specWithSelector(map[string]string{zoneLabel: "a"})

	first := spec.EffectiveNodeSelector()
	first[zoneLabel] = "mutated"
	delete(first, osLabel)

	second := spec.EffectiveNodeSelector()
	want := map[string]string{zoneLabel: "a", osLabel: "linux"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("spec was mutated through the returned map: got %v, want %v", second, want)
	}
	if spec.NodeSelector[zoneLabel] != "a" {
		t.Errorf("spec.NodeSelector was mutated: %v", spec.NodeSelector)
	}
}

func TestSelectedNodeNames(t *testing.T) {
	tests := []struct {
		name     string
		selector map[string]string
		want     []string
	}{
		{
			name:     "default selector picks linux workers only",
			selector: nil,
			want:     []string{nodeWorkerLinuxA, nodeWorkerLinuxB},
		},
		{
			name:     "zone a excludes the windows worker",
			selector: map[string]string{zoneLabel: "a"},
			want:     []string{nodeWorkerLinuxA, nodeControlPlane},
		},
		{
			name:     "custom storage label",
			selector: map[string]string{storagePoolLabel: "fast"},
			want:     []string{nodeWorkerLinuxB},
		},
		{
			name:     "selector matching no node selects nothing",
			selector: map[string]string{zoneLabel: "nowhere"},
			want:     nil,
		},
		{
			// GetNodeSelector replaces the worker default, so the only surviving
			// requirement is the forced linux one.
			name:     "windows request selects the linux nodes",
			selector: map[string]string{osLabel: "windows"},
			want:     []string{nodeWorkerLinuxA, nodeWorkerLinuxB, nodeControlPlane},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := specWithSelector(tc.selector).SelectedNodeNames(testNodes())
			if !equalNameSets(got, tc.want) {
				t.Errorf("SelectedNodeNames() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectedNodeNamesWithNoNodes(t *testing.T) {
	if got := specWithSelector(nil).SelectedNodeNames(nil); len(got) != 0 {
		t.Errorf("SelectedNodeNames(nil) = %v, want empty", got)
	}
}

func TestIntersectSelectedNodes(t *testing.T) {
	tests := []struct {
		name  string
		a, b  map[string]string
		want  []string
		notes string
	}{
		{
			name: "identical default selectors overlap on every worker",
			a:    nil,
			b:    nil,
			want: []string{nodeWorkerLinuxA, nodeWorkerLinuxB},
		},
		{
			name: "contradictory values for the same key are disjoint",
			a:    map[string]string{zoneLabel: "a"},
			b:    map[string]string{zoneLabel: "b"},
			want: nil,
		},
		{
			name: "a superset overlaps on the narrower set",
			a:    nil,
			b:    map[string]string{zoneLabel: "a"},
			want: []string{nodeWorkerLinuxA},
		},
		{
			name: "two selectors that match nothing do not overlap",
			a:    map[string]string{zoneLabel: "nowhere"},
			b:    map[string]string{zoneLabel: "elsewhere"},
			want: nil,
		},
		{
			// The regression case. An abstract selector comparison reports
			// "different keys => might overlap" here and rejects a legitimate
			// configuration; no node carries both labels.
			name:  "different keys that no single node carries are disjoint",
			a:     map[string]string{zoneLabel: "a"},
			b:     map[string]string{storagePoolLabel: "fast"},
			want:  nil,
			notes: "zone a is worker-linux-a, pool fast is worker-linux-b",
		},
		{
			// Same shape as above, but a node does carry both, so it is a
			// genuine conflict.
			name: "different keys carried by one node do overlap",
			a:    map[string]string{zoneLabel: "b"},
			b:    map[string]string{storagePoolLabel: "fast"},
			want: []string{nodeWorkerLinuxB},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes := testNodes()
			got := IntersectSelectedNodes(specWithSelector(tc.a), specWithSelector(tc.b), nodes)
			if !equalNameSets(got, tc.want) {
				t.Errorf("IntersectSelectedNodes() = %v, want %v (%s)", got, tc.want, tc.notes)
			}

			// Overlap is symmetric.
			reversed := IntersectSelectedNodes(specWithSelector(tc.b), specWithSelector(tc.a), nodes)
			if !equalNameSets(reversed, tc.want) {
				t.Errorf("IntersectSelectedNodes() reversed = %v, want %v", reversed, tc.want)
			}
		})
	}
}

// equalNameSets compares two node-name collections ignoring order and
// nil-vs-empty differences.
func equalNameSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, name := range want {
		counts[name]++
	}
	for _, name := range got {
		counts[name]--
		if counts[name] < 0 {
			return false
		}
	}
	return true
}
