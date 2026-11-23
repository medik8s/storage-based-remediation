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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// newReleaseTestAgent returns an agent wired to a fake client preloaded with
// objs, with storage healthy since the given duration ago. The recovery
// period is one minute, so "healthy for 2m" passes and "healthy for 10s"
// does not.
func newReleaseTestAgent(t *testing.T, healthyFor time.Duration, objs ...client.Object) (*SBRAgent, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add v1alpha1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&corev1.Node{}).
		WithObjects(objs...).
		Build()

	agent := &SBRAgent{
		nodeName:                       "worker-1",
		nodeUID:                        "uid-1",
		k8sClient:                      c,
		autonomousRebootRecoveryPeriod: time.Minute,
	}
	if healthyFor >= 0 {
		agent.storageHealthySince = time.Now().Add(-healthyFor)
	}
	return agent, c
}

// cordonedRejoinNode is the node as the boot unit left it: cordoned, keyed,
// and carrying the rejoin condition.
func cordonedRejoinNode() *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "worker-1",
			UID:         "uid-1",
			Annotations: map[string]string{bootunit.RejoinCordonAnnotation: "incident-1"},
		},
	}
	node.Spec.Unschedulable = true
	node.Status.Conditions = []corev1.NodeCondition{
		{Type: bootunit.RejoinConditionType, Status: corev1.ConditionTrue},
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
	}
	return node
}

func nodeAfter(t *testing.T, c client.Client) *corev1.Node {
	t.Helper()
	node := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, node); err != nil {
		t.Fatalf("read node back: %v", err)
	}
	return node
}

func hasRejoinCondition(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == bootunit.RejoinConditionType {
			return true
		}
	}
	return false
}

// The happy path: keyed cordon, sustained health, no CR, no OOS — the agent
// clears the condition, uncordons, and removes the key.
func TestReleaseRejoinCordonHappyPath(t *testing.T) {
	agent, c := newReleaseTestAgent(t, 2*time.Minute, cordonedRejoinNode())

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || !released {
		t.Fatalf("release = %v, %v; want true, nil", released, err)
	}
	node := nodeAfter(t, c)
	if node.Spec.Unschedulable {
		t.Fatal("node is still cordoned after the release")
	}
	if _, keyed := node.Annotations[bootunit.RejoinCordonAnnotation]; keyed {
		t.Fatal("the ownership key survived the release")
	}
	if hasRejoinCondition(node) {
		t.Fatal("the rejoin condition survived the release")
	}
}

// A cordon without the ownership key is someone else's restriction: never
// touched, no matter how healthy the node is.
func TestReleaseSkipsCordonWithoutTheOwnershipKey(t *testing.T) {
	node := cordonedRejoinNode()
	delete(node.Annotations, bootunit.RejoinCordonAnnotation)
	agent, c := newReleaseTestAgent(t, 2*time.Minute, node)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := nodeAfter(t, c); !got.Spec.Unschedulable || !hasRejoinCondition(got) {
		t.Fatal("an unkeyed cordon was modified")
	}
}

// An OutOfService taint means a peer owns the node's restrictions.
func TestReleaseSkipsWhenOutOfServiceIsPresent(t *testing.T) {
	node := cordonedRejoinNode()
	node.Spec.Taints = []corev1.Taint{{Key: outOfServiceTaintKey, Effect: corev1.TaintEffectNoExecute}}
	agent, c := newReleaseTestAgent(t, 2*time.Minute, node)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := nodeAfter(t, c); !got.Spec.Unschedulable || !hasRejoinCondition(got) {
		t.Fatal("a node under OutOfService was modified")
	}
}

// A CR claiming the node — even in a foreign namespace — transfers the
// release to the peer path; the agent must do nothing, ever.
func TestReleaseSkipsWhenACRClaimsTheNode(t *testing.T) {
	cr := &v1alpha1.StorageBasedRemediation{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Namespace: "nhc-namespace"},
	}
	agent, c := newReleaseTestAgent(t, 2*time.Minute, cordonedRejoinNode(), cr)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := nodeAfter(t, c); !got.Spec.Unschedulable || !hasRejoinCondition(got) {
		t.Fatal("a node claimed by a CR was modified")
	}
}

// A CR carrying this node's UID claims it even under a different name.
func TestReleaseSkipsWhenACRCarriesTheNodeUID(t *testing.T) {
	cr := &v1alpha1.StorageBasedRemediation{
		ObjectMeta: metav1.ObjectMeta{Name: "some-other-name", Namespace: "default"},
	}
	cr.Status.NodeUID = "uid-1"
	agent, c := newReleaseTestAgent(t, 2*time.Minute, cordonedRejoinNode(), cr)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := nodeAfter(t, c); got.Spec.Unschedulable != true {
		t.Fatal("a node claimed by UID was modified")
	}
}

// Health must be sustained for the whole recovery period: recent health and
// no health at all both hold the cordon.
func TestReleaseSkipsWhenStorageNotSustained(t *testing.T) {
	for name, healthyFor := range map[string]time.Duration{
		"healthy for seconds only": 10 * time.Second,
		"never observed healthy":   -1,
	} {
		t.Run(name, func(t *testing.T) {
			agent, c := newReleaseTestAgent(t, healthyFor, cordonedRejoinNode())
			if healthyFor < 0 {
				agent.storageHealthySince = time.Time{}
			}
			released, err := agent.maybeReleaseRejoinCordon(context.Background())
			if err != nil || released {
				t.Fatalf("release = %v, %v; want false, nil", released, err)
			}
			if got := nodeAfter(t, c); !got.Spec.Unschedulable {
				t.Fatal("the cordon was released without sustained health")
			}
		})
	}
}

// A replaced Node object is not the one the unit cordoned.
func TestReleaseSkipsOnReplacedNode(t *testing.T) {
	node := cordonedRejoinNode()
	node.UID = "uid-replaced"
	agent, c := newReleaseTestAgent(t, 2*time.Minute, node)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := nodeAfter(t, c); !got.Spec.Unschedulable || !hasRejoinCondition(got) {
		t.Fatal("a replacement Node was modified")
	}
}

// The condition may already be gone (a failed status write on a previous
// cycle, cleaned up out of band): the cordon and key still release.
func TestReleaseContinuesWhenConditionAlreadyGone(t *testing.T) {
	node := cordonedRejoinNode()
	node.Status.Conditions = node.Status.Conditions[1:] // drop the rejoin condition
	agent, c := newReleaseTestAgent(t, 2*time.Minute, node)

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || !released {
		t.Fatalf("release = %v, %v; want true, nil", released, err)
	}
	got := nodeAfter(t, c)
	if got.Spec.Unschedulable {
		t.Fatal("node is still cordoned")
	}
	if _, keyed := got.Annotations[bootunit.RejoinCordonAnnotation]; keyed {
		t.Fatal("the ownership key survived")
	}
}

// The status write runs FIRST: when it fails, the cordon and the key must be
// intact — which is also what proves the ordering, since a main-resource
// write executed before the status write would have uncordoned the node.
func TestReleaseLeavesCordonIntactWhenStatusWriteFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&corev1.Node{}).
		WithObjects(cordonedRejoinNode()).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return errors.New("status subresource unavailable")
			},
		}).
		Build()
	agent := &SBRAgent{
		nodeName:                       "worker-1",
		nodeUID:                        "uid-1",
		k8sClient:                      c,
		autonomousRebootRecoveryPeriod: time.Minute,
		storageHealthySince:            time.Now().Add(-2 * time.Minute),
	}

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err == nil || released {
		t.Fatalf("release = %v, %v; want false and the status error", released, err)
	}
	got := nodeAfter(t, c)
	if !got.Spec.Unschedulable {
		t.Fatal("the node was uncordoned although the status write failed — the main write must run last")
	}
	if _, keyed := got.Annotations[bootunit.RejoinCordonAnnotation]; !keyed {
		t.Fatal("the ownership key was removed although the status write failed")
	}
}

// An unreadable CR list is an error, never a "no CR": releasing on
// unverified checks is the one forbidden outcome.
func TestReleaseErrorsWhenCRListFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&corev1.Node{}).
		WithObjects(cordonedRejoinNode()).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return errors.New("list unavailable")
			},
		}).
		Build()
	agent := &SBRAgent{
		nodeName:                       "worker-1",
		nodeUID:                        "uid-1",
		k8sClient:                      c,
		autonomousRebootRecoveryPeriod: time.Minute,
		storageHealthySince:            time.Now().Add(-2 * time.Minute),
	}

	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err == nil || released {
		t.Fatalf("release = %v, %v; want false and the list error", released, err)
	}
	if got := nodeAfter(t, c); !got.Spec.Unschedulable || !hasRejoinCondition(got) {
		t.Fatal("the node was modified although verification failed")
	}
}

// The sustained-health clock: healthy latches, failure clears, and the
// cleared clock never satisfies the recovery period.
func TestRecordStorageOutcomeLatchesAndClears(t *testing.T) {
	agent := &SBRAgent{}

	if _, ok := agent.storageHealthyFor(); ok {
		t.Fatal("never-observed storage must not read as healthy")
	}

	agent.recordStorageOutcome(true)
	if _, ok := agent.storageHealthyFor(); !ok {
		t.Fatal("a healthy outcome must latch the clock")
	}

	agent.recordStorageOutcome(false)
	if _, ok := agent.storageHealthyFor(); ok {
		t.Fatal("a failure must clear the clock")
	}

	agent.recordStorageOutcome(true)
	agent.recordStorageOutcome(true)
	healthyFor, ok := agent.storageHealthyFor()
	if !ok || healthyFor > time.Minute {
		t.Fatalf("healthy for = %v, %v; want a fresh latch", healthyFor, ok)
	}
}

// gaugeValue reads a Prometheus gauge without the testutil package.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// The held state is observable: the gauge reflects the keyed cordon on every
// evaluation — including the paths that do not release — clears on release,
// and the held event fires exactly once per transition into held.
func TestRejoinCordonHeldObservability(t *testing.T) {
	agent, c := newReleaseTestAgent(t, 2*time.Minute, cordonedRejoinNode())
	rec := record.NewFakeRecorder(4)
	agent.recorder = rec
	agent.recorderObject = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}

	// A held node the loop will NOT release (a CR claims it) still reports
	// held: the alert must fire for waiting nodes, not just releasable ones.
	cr := &v1alpha1.StorageBasedRemediation{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Namespace: "nhc"},
	}
	if err := c.Create(context.Background(), cr); err != nil {
		t.Fatalf("create CR: %v", err)
	}
	released, err := agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || released {
		t.Fatalf("release = %v, %v; want false, nil", released, err)
	}
	if got := gaugeValue(t, rejoinCordonHeldGauge); got != 1 {
		t.Fatalf("gauge = %v, want 1 while held", got)
	}
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, EventReasonRejoinCordonHeld) {
			t.Fatalf("event %q does not carry %q", e, EventReasonRejoinCordonHeld)
		}
	default:
		t.Fatal("the first held observation must emit the event")
	}

	// A repeated evaluation of the same held state emits nothing more.
	if _, err := agent.maybeReleaseRejoinCordon(context.Background()); err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	select {
	case e := <-rec.Events:
		t.Fatalf("repeated held observation emitted %q; the event is once per transition", e)
	default:
	}

	// The CR goes away, storage is healthy: the release clears the gauge.
	if err := c.Delete(context.Background(), cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	released, err = agent.maybeReleaseRejoinCordon(context.Background())
	if err != nil || !released {
		t.Fatalf("release = %v, %v; want true, nil", released, err)
	}
	if got := gaugeValue(t, rejoinCordonHeldGauge); got != 0 {
		t.Fatalf("gauge = %v, want 0 after release", got)
	}
}
