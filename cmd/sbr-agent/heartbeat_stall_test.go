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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

func heartbeatStallAgent(t *testing.T, remediation bool) *SBRAgent {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := medik8sv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var objects []client.Object
	if remediation {
		objects = append(objects, &medik8sv1alpha1.StorageBasedRemediation{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-3", Namespace: "test-ns"},
		})
	}
	return &SBRAgent{
		ctx: context.Background(), nodeName: "worker-3", controllerNamespace: "test-ns",
		k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		recorder:  record.NewFakeRecorder(10), recorderObject: &corev1.Node{},
		heartbeatInterval: time.Second, lastHeartbeatTime: time.Now().Add(-time.Minute),
		// Reproduce the failure: storage I/O is stuck after only two returned errors.
		// The old health flag stays true and the counter never reaches its threshold.
		sbrHealthy: true, heartbeatFailureCount: 2,
	}
}

func TestHeartbeatStallFencesWithRemediation(t *testing.T) {
	agent := heartbeatStallAgent(t, true)
	if fence, reason := agent.shouldTriggerSelfFence(); !fence {
		t.Fatalf("stalled heartbeat with active remediation must fence despite stale healthy flag: %s", reason)
	}
}

func TestHeartbeatStallWaitsForNHCWithoutRemediation(t *testing.T) {
	agent := heartbeatStallAgent(t, false)
	if fence, reason := agent.shouldTriggerSelfFence(); fence {
		t.Fatalf("stalled storage alone must not bypass NHC: %s", reason)
	}
}

func TestHeartbeatStallClearsAfterSuccessfulWrite(t *testing.T) {
	agent := heartbeatStallAgent(t, true)
	agent.recordHeartbeatSuccess()
	if fence, reason := agent.shouldTriggerSelfFence(); fence {
		t.Fatalf("a fresh completed heartbeat must clear the stall: %s", reason)
	}
}

func TestHeartbeatStallAllowsStartupGrace(t *testing.T) {
	agent := heartbeatStallAgent(t, true)
	agent.lastHeartbeatTime = time.Now()
	if fence, reason := agent.shouldTriggerSelfFence(); fence {
		t.Fatalf("startup must allow a heartbeat interval before deciding to fence: %s", reason)
	}
}
