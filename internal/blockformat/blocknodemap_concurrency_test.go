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

package blockformat

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/internal/sbdprotocol"
)

// syncDevice is an in-memory DeviceReadWriterAt whose operations are serialized,
// modelling a real block device that completes one I/O at a time. The mutex keeps
// the concurrent registrations below racing on the node-map protocol rather than on
// the backing byte slice.
type syncDevice struct {
	mu   sync.Mutex
	data []byte
}

func newSyncDevice(size int64) *syncDevice {
	return &syncDevice{data: make([]byte, size)}
}

func (d *syncDevice) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if off < 0 || off >= int64(len(d.data)) {
		return 0, errors.New("offset out of range")
	}
	return copy(p, d.data[off:]), nil
}

func (d *syncDevice) WriteAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if off < 0 || off+int64(len(p)) > int64(len(d.data)) {
		return 0, errors.New("write out of range")
	}
	return copy(d.data[off:], p), nil
}

func (d *syncDevice) Sync() error { return nil }

// stubSBDDevice satisfies sbdprotocol.SBDDevice for NodeManager construction.
// Registration persists exclusively through the injected NodeMapStore, so this
// device is never exercised for node-map data.
type stubSBDDevice struct {
	data []byte
}

func newStubSBDDevice() *stubSBDDevice {
	return &stubSBDDevice{data: make([]byte, BlockMinDeviceSize)}
}

func (s *stubSBDDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(s.data)) {
		return 0, nil
	}
	return copy(p, s.data[off:]), nil
}

func (s *stubSBDDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(s.data)) {
		return 0, nil
	}
	return copy(s.data[off:], p), nil
}

func (s *stubSBDDevice) Sync() error  { return nil }
func (s *stubSBDDevice) Close() error { return nil }
func (s *stubSBDDevice) Path() string { return "" }

const testClusterName = "sbr-test-cluster"

// newRegistrar returns a NodeManager backed by its own BlockNodeMapStore over the
// shared device — one instance per simulated node, matching how independent agents
// each open the same block device in block mode.
func newRegistrar(t *testing.T, dev DeviceReadWriterAt) *sbdprotocol.NodeManager {
	t.Helper()
	nm, err := sbdprotocol.NewNodeManager(newStubSBDDevice(), sbdprotocol.NodeManagerConfig{
		ClusterName:        testClusterName,
		StaleNodeTimeout:   time.Hour,
		FileLockingEnabled: false, // forced off in block mode; the write-verify protocol coordinates
		NodeMapStore:       NewBlockNodeMapStore(dev, logr.Discard()),
		Logger:             logr.Discard(),
	})
	if err != nil {
		t.Fatalf("NewNodeManager: %v", err)
	}
	return nm
}

// convergedNodes reads the shared device directly and returns the sorted set of
// node names present in the persisted map.
func convergedNodes(t *testing.T, dev DeviceReadWriterAt) []string {
	t.Helper()
	data, err := NewBlockNodeMapStore(dev, logr.Discard()).Load()
	if err != nil {
		t.Fatalf("Load converged map: %v", err)
	}
	table, err := sbdprotocol.UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("UnmarshalNodeMapTable: %v", err)
	}
	names := make([]string, 0, len(table.Entries))
	for n := range table.Entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// registerConcurrently fires one registration per node name at the same instant
// (a shared start gate) and waits for all to return. The read/write interleaving
// emerges from the scheduler, not the test.
func registerConcurrently(t *testing.T, dev DeviceReadWriterAt, names []string) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(nodeName string) {
			defer wg.Done()
			nm := newRegistrar(t, dev)
			<-start
			if _, err := nm.GetNodeIDForNode(nodeName); err != nil {
				t.Logf("registration of %s returned error: %v", nodeName, err)
			}
		}(name)
	}
	close(start)
	wg.Wait()
}

// Control: without concurrency the store accumulates every node.
func TestBlockNodeMapStore_SequentialRegistrationConverges(t *testing.T) {
	dev := newSyncDevice(BlockMinDeviceSize)

	want := []string{"node-a", "node-b", "node-c"}
	for _, name := range want {
		if _, err := newRegistrar(t, dev).GetNodeIDForNode(name); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	got := convergedNodes(t, dev)
	if !equalStrings(got, want) {
		t.Fatalf("sequential registration lost nodes: want %v, got %v", want, got)
	}
}

// Edge case: concurrent writers carrying the SAME entry re-write an identical
// payload, so the map must still converge to that one node.
func TestBlockNodeMapStore_ConcurrentSameNodeIdempotent(t *testing.T) {
	dev := newSyncDevice(BlockMinDeviceSize)

	registerConcurrently(t, dev, []string{"node-x", "node-x", "node-x"})

	got := convergedNodes(t, dev)
	if !equalStrings(got, []string{"node-x"}) {
		t.Fatalf("same-node concurrent registration corrupted map: want [node-x], got %v", got)
	}
}

// Concurrent distinct nodes registering at once against an empty device must all
// converge into the shared map; a lost update on the double-buffered write-verify
// protocol would drop one of them.
func TestBlockNodeMapStore_ConcurrentRegistrationConverges(t *testing.T) {
	dev := newSyncDevice(BlockMinDeviceSize)

	want := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		want = append(want, fmt.Sprintf("node-%02d", i))
	}
	registerConcurrently(t, dev, want)

	got := convergedNodes(t, dev)
	if !equalStrings(got, want) {
		t.Fatalf("lost update on concurrent registration: want %d nodes %v, got %d %v",
			len(want), want, len(got), got)
	}
}

// Node-pool scale-up: many nodes joining concurrently against a device that
// already holds an entry must accumulate, preserving the existing entry and every
// joiner.
func TestBlockNodeMapStore_ConcurrentJoinConverges(t *testing.T) {
	dev := newSyncDevice(BlockMinDeviceSize)

	if _, err := newRegistrar(t, dev).GetNodeIDForNode("node-existing"); err != nil {
		t.Fatalf("seed registration: %v", err)
	}

	joiners := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		joiners = append(joiners, fmt.Sprintf("node-j%02d", i))
	}
	registerConcurrently(t, dev, joiners)

	want := append([]string{"node-existing"}, joiners...)
	sort.Strings(want)
	got := convergedNodes(t, dev)
	if !equalStrings(got, want) {
		t.Fatalf("lost update on concurrent join: want %d nodes, got %d %v",
			len(want), len(got), got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
