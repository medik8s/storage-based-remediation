package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type testReader struct {
	calls atomic.Int32
	block chan struct{}
}

func (r *testReader) Read() (uint64, error) {
	n := r.calls.Add(1)
	if r.block != nil {
		<-r.block
	}
	return uint64(n), nil
}

func runtimeFixture(names ...string) (*Runtime, client.Client) {
	w := blockpernode.WatcherTargets{Version: 1, ConfigUID: "config-uid", Group: "group"}
	state := NewNodeState()
	state.available = true
	state.epoch = 1
	var objects []client.Object
	for index, name := range names {
		target := blockpernode.WatcherTarget{Name: name, UID: types.UID(name + "-uid"), OwnershipGeneration: "generation", Timing: &blockpernode.ProbeTiming{ProbeInterval: metav1.Duration{Duration: 10 * time.Millisecond}, IOTimeout: metav1.Duration{Duration: 5 * time.Millisecond}, MaxConsecutiveFailures: 2}, Probe: &blockpernode.ProbeMapping{DevicePath: blockpernode.DevicePath(index)}}
		owner := blockpernode.ConfigurationOwner{Version: 1, NodeUID: target.UID, ConfigUID: w.ConfigUID, OwnershipGeneration: target.OwnershipGeneration, Phase: blockpernode.Active}
		value, _ := json.Marshal(owner)
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: target.UID, ResourceVersion: "1", Annotations: map[string]string{blockpernode.OwnerAnnotation: string(value), blockpernode.GroupAnnotation: w.Group}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeConditionType(blockpernode.StorageCondition), Status: corev1.ConditionTrue}}}}
		state.nodes[name] = node
		objects = append(objects, node)
		w.Targets = append(w.Targets, target)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(objects...).Build()
	return &Runtime{Assignment: w, Nodes: state, Client: c, Readers: map[types.UID]Reader{}, Log: logr.Discard()}, c
}

type countedClient struct {
	client.Client
	gets         atomic.Int32
	patches      atomic.Int32
	lostResponse bool
	changed      chan string
}

func (c *countedClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	c.gets.Add(1)
	return c.Client.Get(ctx, key, object, options...)
}
func (c *countedClient) Status() client.SubResourceWriter {
	return &countedStatus{SubResourceWriter: c.Client.Status(), owner: c}
}

type countedStatus struct {
	client.SubResourceWriter
	owner *countedClient
}

func (c *countedStatus) Patch(ctx context.Context, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
	c.owner.patches.Add(1)
	err := c.SubResourceWriter.Patch(ctx, object, patch, options...)
	if err == nil && c.owner.changed != nil {
		select {
		case c.owner.changed <- object.GetName():
		default:
		}
	}
	if err == nil && c.owner.lostResponse {
		return fmt.Errorf("lost successful response")
	}
	return err
}

func TestBlockedReaderDoesNotPreventOtherTargetTransition(t *testing.T) {
	r, c := runtimeFixture("a", "b")
	blocked := &testReader{block: make(chan struct{})}
	advancing := &testReader{}
	r.Readers["a-uid"], r.Readers["b-uid"] = blocked, advancing
	counted := &countedClient{Client: c, changed: make(chan string, 8)}
	r.Client = counted
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case name := <-counted.changed:
		if name != "b" {
			t.Fatalf("wrong target %s", name)
		}
	case <-ctx.Done():
		t.Fatal("healthy target did not recover")
	}
	if blocked.calls.Load() != 1 {
		t.Fatalf("blocked reader accumulated %d calls", blocked.calls.Load())
	}
	current := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "b"}, current); err != nil {
		t.Fatal(err)
	}
	if condition := blockpernode.Condition(current); condition == nil || condition.Status != corev1.ConditionFalse || condition.Reason != "Recovered" {
		t.Fatalf("no independent transition: %+v", condition)
	}
	cancel()
	<-done
	close(blocked.block)
}

type lateCompletionReader struct {
	calls    atomic.Int32
	sequence atomic.Uint64
	started  chan struct{}
	release  chan struct{}
}

func (r *lateCompletionReader) Read() (uint64, error) {
	switch r.calls.Add(1) {
	case 1:
		return 1, nil
	case 2:
		close(r.started)
		<-r.release
		return 2, nil
	default:
		return r.sequence.Load(), nil
	}
}

func TestLateReadCannotContributeRecoveryWhileOtherTargetContinues(t *testing.T) {
	r, c := runtimeFixture("a", "b")
	for index := range r.Assignment.Targets {
		r.Assignment.Targets[index].Timing.ProbeInterval.Duration = 50 * time.Millisecond
		r.Assignment.Targets[index].Timing.IOTimeout.Duration = 40 * time.Millisecond
	}
	late := &lateCompletionReader{started: make(chan struct{}), release: make(chan struct{})}
	late.sequence.Store(3)
	defer func() {
		select {
		case <-late.release:
		default:
			close(late.release)
		}
	}()
	independent := &testReader{}
	r.Readers["a-uid"], r.Readers["b-uid"] = late, independent
	counted := &countedClient{Client: c, changed: make(chan string, 8)}
	r.Client = counted
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-late.started:
	case <-ctx.Done():
		t.Fatal("second read did not block")
	}
	before := independent.calls.Load()
	for independent.calls.Load() < before+3 {
		select {
		case <-ctx.Done():
			t.Fatal("independent target stopped while read was blocked")
		case <-time.After(time.Millisecond):
		}
	}
	if late.calls.Load() != 2 {
		t.Fatalf("blocked device accumulated calls=%d", late.calls.Load())
	}
	select {
	case name := <-counted.changed:
		if name != "b" {
			t.Fatalf("blocked target published %s", name)
		}
	case <-ctx.Done():
		t.Fatal("independent target did not recover")
	}
	other := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "b"}, other); err != nil {
		t.Fatal(err)
	}
	r.Nodes.mu.Lock()
	r.Nodes.nodes["b"] = other.DeepCopy()
	r.Nodes.mu.Unlock()
	close(late.release)
	observe := func(sequence uint64) {
		t.Helper()
		before := late.calls.Load()
		late.sequence.Store(sequence)
		for late.calls.Load() < before+3 {
			select {
			case <-ctx.Done():
				t.Fatal("late device did not resume timely reads")
			case <-time.After(time.Millisecond):
			}
		}
	}
	observe(3)
	observe(4)
	observe(5)
	target := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "a"}, target); err != nil {
		t.Fatal(err)
	}
	if condition := blockpernode.Condition(target); condition == nil || condition.Status != corev1.ConditionTrue {
		t.Fatalf("late completion contributed recovery: %+v", condition)
	}
	continued := independent.calls.Load()
	observe(6)
	if independent.calls.Load() <= continued {
		t.Fatal("independent target stopped after the late read completed")
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "a"}, target); err != nil {
		t.Fatal(err)
	}
	if condition := blockpernode.Condition(target); condition == nil || condition.Status != corev1.ConditionFalse || condition.Reason != "Recovered" {
		t.Fatalf("three timely advances did not recover: %+v", condition)
	}
	cancel()
	<-done
}

func TestExpiryIsEqualForEveryNodeReadyState(t *testing.T) {
	states := []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionUnknown, ""}
	names := []string{"ready", "not-ready", "unknown", "missing"}
	r, c := runtimeFixture(names...)
	for index, name := range names {
		node, _, _ := r.Nodes.Snapshot(name)
		node.Status.Conditions[0].Status = corev1.ConditionFalse
		if states[index] != "" {
			node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeReady, Status: states[index]})
		}
		if err := c.Status().Update(context.Background(), node); err != nil {
			t.Fatalf("set Ready=%s: %v", states[index], err)
		}
		r.Nodes.nodes[name] = node.DeepCopy()
		reader := &controlledReader{}
		reader.sequence.Store(1)
		r.Readers[node.UID] = reader
		r.Assignment.Targets[index].Timing.ProbeInterval.Duration = 30 * time.Millisecond
		r.Assignment.Targets[index].Timing.IOTimeout.Duration = 20 * time.Millisecond
	}
	counted := &countedClient{Client: c, changed: make(chan string, len(names))}
	r.Client = counted
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	started := time.Now()
	go func() { r.Run(ctx); close(done) }()
	observed := map[string]bool{}
	for len(observed) < len(names) {
		select {
		case name := <-counted.changed:
			if time.Since(started) < r.Assignment.Targets[0].Timing.Expiry() {
				t.Fatalf("Ready state %s expired before the common threshold", name)
			}
			observed[name] = true
		case <-ctx.Done():
			t.Fatalf("Ready states did not all expire: %+v", observed)
		}
	}
	cancel()
	<-done
	for index, name := range names {
		node := &corev1.Node{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: name}, node); err != nil {
			t.Fatal(err)
		}
		if condition := blockpernode.Condition(node); condition == nil || condition.Status != corev1.ConditionTrue || condition.Reason != "HeartbeatTimeout" {
			t.Fatalf("Ready=%s changed expiry: %+v", states[index], condition)
		}
		var ready corev1.ConditionStatus
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				ready = condition.Status
			}
		}
		if ready != states[index] {
			t.Fatalf("storage transition changed Ready: expected=%s actual=%s", states[index], ready)
		}
	}
}

func TestOrdinaryPollsMakeNoAPIRequests(t *testing.T) {
	r, c := runtimeFixture("a")
	node, _, _ := r.Nodes.Snapshot("a")
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	r.Nodes.nodes["a"] = node
	reader := &testReader{}
	r.Readers["a-uid"] = reader
	counted := &countedClient{Client: c}
	r.Client = counted
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Millisecond)
	defer cancel()
	r.Run(ctx)
	if reader.calls.Load() < 3 || counted.gets.Load() != 0 || counted.patches.Load() != 0 {
		t.Fatalf("poll calls reads=%d gets=%d patches=%d", reader.calls.Load(), counted.gets.Load(), counted.patches.Load())
	}
}

func TestTransitionUsesOriginalAgeAndResolvesLostResponse(t *testing.T) {
	r, c := runtimeFixture("a")
	target := r.Assignment.Targets[0]
	node, epoch, _ := r.Nodes.Snapshot("a")
	counted := &countedClient{Client: c, lostResponse: true}
	r.Client = counted
	r.transition(context.Background(), target, node, epoch, corev1.ConditionFalse, "Recovered", time.Now().Add(-time.Second))
	if counted.patches.Load() != 0 {
		t.Fatal("stale evidence caused request")
	}
	r.transition(context.Background(), target, node, epoch, corev1.ConditionFalse, "Recovered", time.Now())
	if counted.patches.Load() != 1 || counted.gets.Load() != 1 {
		t.Fatalf("lost response caused duplicate request: patches=%d gets=%d", counted.patches.Load(), counted.gets.Load())
	}
	r.Nodes.Invalidate()
	r.transition(context.Background(), target, node, epoch, corev1.ConditionFalse, "Recovered", time.Now())
	if counted.patches.Load() != 1 {
		t.Fatal("unavailable state caused patch")
	}
}

type controlledReader struct {
	sequence atomic.Uint64
	calls    atomic.Int32
}

func (r *controlledReader) Read() (uint64, error) {
	sequence := r.sequence.Load()
	r.calls.Add(1)
	return sequence, nil
}

func TestAPIRecoveryRequiresRelistAndThreeFreshAdvances(t *testing.T) {
	r, c := runtimeFixture("a")
	node, _, _ := r.Nodes.Snapshot("a")
	r.Nodes = NewNodeState()
	r.Assignment.Targets[0].Timing.ProbeInterval.Duration = 30 * time.Millisecond
	r.Assignment.Targets[0].Timing.IOTimeout.Duration = 20 * time.Millisecond
	reader := &controlledReader{}
	reader.sequence.Store(1)
	r.Readers["a-uid"] = reader
	counted := &countedClient{Client: c, changed: make(chan string, 1)}
	r.Client = counted
	var outage atomic.Bool
	var failedLists, lists, watches atomic.Int32
	endFirstWatch := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("watch") != "true" {
			if outage.Load() {
				failedLists.Add(1)
				http.Error(w, "API unavailable", http.StatusServiceUnavailable)
				return
			}
			lists.Add(1)
			_ = json.NewEncoder(w).Encode(&corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []corev1.Node{*node}})
			return
		}
		watchNumber := watches.Add(1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if watchNumber == 1 {
			select {
			case <-endFirstWatch:
			case <-request.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "ERROR", "object": metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: http.StatusGone}})
			return
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	apiClient, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	synchronized := make(chan struct{})
	go func() { r.Nodes.Synchronize(ctx, apiClient); close(synchronized) }()
	wait := func(description string, check func() bool) {
		t.Helper()
		limit := time.NewTimer(4 * time.Second)
		defer limit.Stop()
		for !check() {
			select {
			case <-limit.C:
				t.Fatal(description)
			case <-time.After(time.Millisecond):
			}
		}
	}
	wait("initial Node list did not synchronize", func() bool { _, _, available := r.Nodes.Snapshot("a"); return available })
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	observe := func(sequence uint64) {
		t.Helper()
		before := reader.calls.Load()
		reader.sequence.Store(sequence)
		wait("reader did not consume the selected sequence", func() bool { return reader.calls.Load() >= before+3 })
	}
	observe(1)
	observe(2)
	observe(3)
	if counted.patches.Load() != 0 {
		t.Fatal("two advances caused recovery")
	}
	outage.Store(true)
	close(endFirstWatch)
	wait("watch failure did not remove synchronized authority", func() bool { _, _, available := r.Nodes.Snapshot("a"); return !available })
	reader.sequence.Store(100)
	wait("outage did not prevent a replacement list", func() bool { return failedLists.Load() > 0 })
	if counted.patches.Load() != 0 {
		t.Fatal("unavailable API state retained recovery evidence")
	}
	outage.Store(false)
	wait("API recovery did not establish a new list and watch", func() bool {
		_, _, available := r.Nodes.Snapshot("a")
		return available && lists.Load() == 2 && watches.Load() == 2
	})
	observe(100)
	observe(101)
	observe(102)
	if counted.patches.Load() != 0 {
		t.Fatal("recovery retained advances from before the outage")
	}
	observe(103)
	select {
	case <-counted.changed:
	case <-time.After(time.Second):
		t.Fatal("three fresh advances did not recover the target")
	}
	if counted.gets.Load() != 0 || counted.patches.Load() != 1 {
		t.Fatalf("unexpected transition requests: gets=%d patches=%d", counted.gets.Load(), counted.patches.Load())
	}
	cancel()
	<-done
	<-synchronized
}

func TestLocalRecoveryBeforeThirdAdvanceResetsWatcherEvidence(t *testing.T) {
	r, c := runtimeFixture("a")
	r.Assignment.Targets[0].Timing.ProbeInterval.Duration = 30 * time.Millisecond
	r.Assignment.Targets[0].Timing.IOTimeout.Duration = 20 * time.Millisecond
	reader := &controlledReader{}
	reader.sequence.Store(1)
	r.Readers["a-uid"] = reader
	counted := &countedClient{Client: c}
	r.Client = counted
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	waitReads := func() {
		t.Helper()
		before := reader.calls.Load()
		limit := time.NewTimer(time.Second)
		defer limit.Stop()
		for reader.calls.Load() < before+3 {
			select {
			case <-limit.C:
				t.Fatal("reader did not continue")
			case <-time.After(time.Millisecond):
			}
		}
	}
	waitReads()
	reader.sequence.Store(2)
	waitReads()
	if counted.patches.Load() != 0 {
		t.Fatal("one watcher advance caused recovery")
	}
	local := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "a"}, local); err != nil {
		t.Fatal(err)
	}
	if err := blockpernode.PatchCondition(ctx, c, local, corev1.ConditionFalse, "StorageProbeRecovered", "local verified probe", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "a"}, local); err != nil {
		t.Fatal(err)
	}
	r.Nodes.mu.Lock()
	r.Nodes.nodes["a"] = local.DeepCopy()
	r.Nodes.mu.Unlock()
	reader.sequence.Store(3)
	waitReads()
	reader.sequence.Store(4)
	waitReads()
	if counted.patches.Load() != 0 {
		t.Fatal("watcher wrote after independent local recovery")
	}
	if err := blockpernode.PatchCondition(ctx, c, local, corev1.ConditionTrue, "StorageProbeFailed", "later local failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "a"}, local); err != nil {
		t.Fatal(err)
	}
	r.Nodes.mu.Lock()
	r.Nodes.nodes["a"] = local.DeepCopy()
	r.Nodes.mu.Unlock()
	waitReads()
	reader.sequence.Store(5)
	waitReads()
	reader.sequence.Store(6)
	waitReads()
	if counted.patches.Load() != 0 {
		t.Fatal("watcher retained advances across local transitions")
	}
	reader.sequence.Store(7)
	waitReads()
	if counted.patches.Load() != 1 {
		t.Fatal("three new advances did not recover the new local failure")
	}
	cancel()
	<-done
}
