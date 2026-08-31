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

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/internal/mocks"
	"github.com/medik8s/storage-based-remediation/internal/sbdprotocol"
)

// newCompletionReconciler builds a reconciler with a mock heartbeat device + node manager and
// registers worker-1..5, returning the reconciler and the target node's ID.
func newCompletionReconciler(t *testing.T, blockMode bool) (*SBRRemediationReconciler, uint16) {
	t.Helper()
	dev := mocks.NewMockBlockDevice("/tmp/test-completion", 1024*1024)
	r := &SBRRemediationReconciler{}
	r.SetSBRDevices(dev, dev)
	if blockMode {
		r.SetBlockMode(true, testBlockSlotSize, make([]byte, testBlockSlotSize), make([]byte, testBlockSlotSize))
	}

	nm, err := sbdprotocol.NewNodeManager(dev, sbdprotocol.NodeManagerConfig{
		ClusterName:        "test-cluster",
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   10 * time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewNodeManager: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := nm.GetNodeIDForNode(nodeName(i)); err != nil {
			t.Fatalf("register %s: %v", nodeName(i), err)
		}
	}
	r.SetNodeManager(nm)

	targetID, err := nm.GetNodeIDForNode("worker-1")
	if err != nil {
		t.Fatalf("lookup worker-1: %v", err)
	}
	return r, targetID
}

func nodeName(i int) string {
	return "worker-" + string(rune('0'+i))
}

// writeHeartbeat seeds a valid heartbeat for nodeID into its slot with the given age.
func writeHeartbeat(t *testing.T, r *SBRRemediationReconciler, nodeID uint16, age time.Duration) {
	t.Helper()
	h := sbdprotocol.NewHeartbeat(nodeID, 1)
	h.Timestamp = uint64(time.Now().Add(-age).UnixNano())
	data, err := sbdprotocol.Marshal(h)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if _, err := r.sbrDevice.WriteAt(data, r.slotOffset(nodeID)); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
}

// TestHasNodeStoppedHeartbeating covers the liveness read in both slot geometries. The block-mode
// case is a regression guard for the O_DIRECT slot geometry (read at nodeID*4096, not *512).
func TestHasNodeStoppedHeartbeating(t *testing.T) {
	cases := []struct {
		name      string
		blockMode bool
		age       time.Duration
		wantStop  bool
	}{
		{name: "fs-fresh-alive", blockMode: false, age: 5 * time.Second, wantStop: false},
		{name: "fs-stale-dead", blockMode: false, age: 120 * time.Second, wantStop: true},
		{name: "block-fresh-alive", blockMode: true, age: 5 * time.Second, wantStop: false},
		{name: "block-stale-dead", blockMode: true, age: 120 * time.Second, wantStop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, targetID := newCompletionReconciler(t, tc.blockMode)
			writeHeartbeat(t, r, targetID, tc.age)

			stopped, err := r.hasNodeStoppedHeartbeating("worker-1", logr.Discard())
			if err != nil {
				t.Fatalf("hasNodeStoppedHeartbeating: %v", err)
			}
			if stopped != tc.wantStop {
				t.Fatalf("stopped = %v, want %v (age %s)", stopped, tc.wantStop, tc.age)
			}
		})
	}
}

// TestCheckFencingCompletionSafety pins the fencing decision. The critical case is
// partition-with-fresh-heartbeat: NodeReady=Unknown must NOT be treated as fenced, otherwise the OOS
// taint releases storage while the victim is still writing to it (dual writer).
func TestCheckFencingCompletionSafety(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := medik8sv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	// worker-1 is NodeReady=Unknown throughout: an apiserver partition, indistinguishable from a
	// live-but-isolated node.
	partitionedNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}},
		},
	}

	remediation := func(startedAgo time.Duration) *medik8sv1alpha1.StorageBasedRemediation {
		return &medik8sv1alpha1.StorageBasedRemediation{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Namespace: "default"},
			Status: medik8sv1alpha1.StorageBasedRemediationStatus{
				Conditions: []metav1.Condition{{
					Type:               string(medik8sv1alpha1.SBRRemediationConditionFencingInProgress),
					Status:             metav1.ConditionTrue,
					Reason:             "InProgress",
					Message:            "fencing",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-startedAgo)),
				}},
			},
		}
	}

	cases := []struct {
		name         string
		heartbeatAge time.Duration
		fencingAgo   time.Duration
		want         fencingOutcome
	}{
		{
			name:         "partition-with-fresh-heartbeat-is-not-fenced",
			heartbeatAge: 5 * time.Second,
			fencingAgo:   30 * time.Second,
			want:         fencingPending,
		},
		{
			name:         "heartbeat-stopped-is-fenced",
			heartbeatAge: 120 * time.Second,
			fencingAgo:   30 * time.Second,
			want:         fencingComplete,
		},
		{
			name:         "timeout-without-heartbeat-stop-is-failure",
			heartbeatAge: 5 * time.Second,
			fencingAgo:   time.Duration(DefaultFencingMonitorTimeoutSeconds+10) * time.Second,
			want:         fencingTimedOut,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, targetID := newCompletionReconciler(t, false)
			r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(partitionedNode).Build()
			writeHeartbeat(t, r, targetID, tc.heartbeatAge)

			got := r.checkFencingCompletion(context.Background(), remediation(tc.fencingAgo), logr.Discard())
			if got != tc.want {
				t.Fatalf("checkFencingCompletion = %d, want %d", got, tc.want)
			}
		})
	}
}
