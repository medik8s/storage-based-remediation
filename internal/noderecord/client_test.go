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

package noderecord

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func nodeRecordTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return scheme
}

func minimalRecord(nodeUID types.UID) *Record {
	return &Record{
		SchemaVersion:        SchemaVersion,
		NodeUID:              nodeUID,
		Budget:               Budget{},
		Restrictions:         Restrictions{},
		CoordinationRevision: 1,
	}
}

func buildTestNode(t *testing.T, name string, uid types.UID, rec *Record) *corev1.Node {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid}}
	if rec != nil {
		encoded, err := Encode(rec)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		node.Annotations = map[string]string{AnnotationKey: encoded}
	}
	return node
}

func fakeNodeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(nodeRecordTestScheme(t)).
		WithObjects(objs...).
		Build()
}

func decodeStoredNode(t *testing.T, c client.Client, name string) *Record {
	t.Helper()
	node := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, node); err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	rec, err := Decode(node)
	if err != nil {
		t.Fatalf("Decode after update: %v", err)
	}
	return rec
}

func TestGetNodeAndRecord_NodeNotFound(t *testing.T) {
	c := fakeNodeClient(t)

	_, _, err := getNodeAndRecord(context.Background(), c, "missing-node")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestGetNodeAndRecord_MissingAnnotation(t *testing.T) {
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), nil))

	_, _, err := getNodeAndRecord(context.Background(), c, "node-1")
	if !errors.Is(err, ErrRecordMissing) {
		t.Fatalf("expected ErrRecordMissing, got %v", err)
	}
}

func TestGetNodeAndRecord_InvalidAnnotation(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			UID:  types.UID("uid-1"),
			Annotations: map[string]string{
				AnnotationKey: "{not valid json",
			},
		},
	}
	c := fakeNodeClient(t, node)

	_, _, err := getNodeAndRecord(context.Background(), c, "node-1")
	if !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("expected ErrRecordInvalid, got %v", err)
	}
}

func TestGetNodeAndRecord_NodeUIDMismatch(t *testing.T) {
	rec := minimalRecord(types.UID("other-uid"))
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), rec))

	_, _, err := getNodeAndRecord(context.Background(), c, "node-1")
	if !errors.Is(err, ErrNodeUIDMismatch) {
		t.Fatalf("expected ErrNodeUIDMismatch, got %v", err)
	}
}

func TestGetNodeAndRecord_Success(t *testing.T) {
	rec := minimalRecord(types.UID("uid-1"))
	rec.CoordinationRevision = 42
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), rec))

	node, got, err := getNodeAndRecord(context.Background(), c, "node-1")
	if err != nil {
		t.Fatalf("getNodeAndRecord: %v", err)
	}
	if node == nil || node.Name != "node-1" {
		t.Fatalf("expected node-1, got %+v", node)
	}
	if got == nil || got.CoordinationRevision != 42 {
		t.Fatalf("expected coordinationRevision 42, got %+v", got)
	}
}

func TestUpdate_MissingRecordReturnsErrorWithoutCallingMutate(t *testing.T) {
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), nil))

	called := false
	mutate := func(node *corev1.Node, rec *Record) error {
		called = true
		return nil
	}

	err := Update(context.Background(), c, c, "node-1", mutate)
	if !errors.Is(err, ErrRecordMissing) {
		t.Fatalf("expected ErrRecordMissing, got %v", err)
	}
	if called {
		t.Fatal("mutate must not be called when the record is missing")
	}
}

func TestUpdate_NodeUIDMismatchReturnsErrorWithoutCallingMutate(t *testing.T) {
	rec := minimalRecord(types.UID("other-uid"))
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), rec))

	called := false
	mutate := func(node *corev1.Node, rec *Record) error {
		called = true
		return nil
	}

	err := Update(context.Background(), c, c, "node-1", mutate)
	if !errors.Is(err, ErrNodeUIDMismatch) {
		t.Fatalf("expected ErrNodeUIDMismatch, got %v", err)
	}
	if called {
		t.Fatal("mutate must not be called on a nodeUID mismatch")
	}
}

func TestUpdate_MutateErrorPropagatesAndLeavesRecordUnchanged(t *testing.T) {
	rec := minimalRecord(types.UID("uid-1"))
	rec.CoordinationRevision = 5
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), rec))

	wantErr := errors.New("mutate rejected the transition")
	mutate := func(node *corev1.Node, rec *Record) error {
		rec.CoordinationRevision = 99
		return wantErr
	}

	err := Update(context.Background(), c, c, "node-1", mutate)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the mutate error to propagate, got %v", err)
	}

	stored := decodeStoredNode(t, c, "node-1")
	if stored.CoordinationRevision != 5 {
		t.Fatalf("a rejected mutation must not be persisted, got coordinationRevision=%d", stored.CoordinationRevision)
	}
}

func TestUpdate_Success(t *testing.T) {
	rec := minimalRecord(types.UID("uid-1"))
	rec.CoordinationRevision = 1
	c := fakeNodeClient(t, buildTestNode(t, "node-1", types.UID("uid-1"), rec))

	mutate := func(node *corev1.Node, rec *Record) error {
		rec.CoordinationRevision++
		return nil
	}

	if err := Update(context.Background(), c, c, "node-1", mutate); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored := decodeStoredNode(t, c, "node-1")
	if stored.CoordinationRevision != 2 {
		t.Fatalf("expected coordinationRevision 2, got %d", stored.CoordinationRevision)
	}
}

func TestUpdate_RetriesOnConflictAndReappliesMutate(t *testing.T) {
	rec := minimalRecord(types.UID("uid-1"))
	rec.CoordinationRevision = 1
	node := buildTestNode(t, "node-1", types.UID("uid-1"), rec)

	underlying := fakeNodeClient(t, node)
	attempts := 0
	intercepted := interceptor.NewClient(underlying, interceptor.Funcs{
		Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			attempts++
			if attempts == 1 {
				return apierrors.NewConflict(corev1.Resource("nodes"), obj.GetName(), errors.New("stale resourceVersion"))
			}
			return cli.Update(ctx, obj, opts...)
		},
	})

	mutateCalls := 0
	mutate := func(node *corev1.Node, rec *Record) error {
		mutateCalls++
		rec.CoordinationRevision++
		return nil
	}

	if err := Update(context.Background(), intercepted, intercepted, "node-1", mutate); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected exactly one retry (2 attempts), got %d", attempts)
	}
	if mutateCalls != 2 {
		t.Fatalf("expected mutate to be re-applied after the conflict re-read, got %d calls", mutateCalls)
	}

	stored := decodeStoredNode(t, underlying, "node-1")
	if stored.CoordinationRevision != 2 {
		t.Fatalf("expected coordinationRevision 2 after the retried update, got %d", stored.CoordinationRevision)
	}
}

func TestUpdate_ConflictRetriesExhausted(t *testing.T) {
	rec := minimalRecord(types.UID("uid-1"))
	node := buildTestNode(t, "node-1", types.UID("uid-1"), rec)

	underlying := fakeNodeClient(t, node)
	intercepted := interceptor.NewClient(underlying, interceptor.Funcs{
		Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return apierrors.NewConflict(corev1.Resource("nodes"), obj.GetName(), errors.New("always stale"))
		},
	})

	mutate := func(node *corev1.Node, rec *Record) error {
		rec.CoordinationRevision++
		return nil
	}

	err := Update(context.Background(), intercepted, intercepted, "node-1", mutate)
	if err == nil {
		t.Fatal("expected an error when conflict retries are exhausted")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected the exhausted retry error to still be a conflict error, got %v", err)
	}
}
