package watcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestNodeWatchLossInvalidatesSynchronizedState(t *testing.T) {
	state := NewNodeState()
	endWatch := make(chan struct{})
	var lists, watches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("watch") != "true" {
			lists.Add(1)
			_ = json.NewEncoder(w).Encode(&corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, ListMeta: metav1.ListMeta{ResourceVersion: "123"}, Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "a", ResourceVersion: "123"}}}})
			return
		}
		watches.Add(1)
		if request.URL.Query().Get("resourceVersion") != "123" {
			t.Error("watch did not use the list resource version")
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-endWatch:
		case <-request.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "ERROR", "object": metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: http.StatusGone}})
	}))
	defer server.Close()
	c, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { state.Synchronize(ctx, c); close(done) }()
	limit := time.After(time.Second)
	var synchronizedEpoch uint64
	for {
		node, epoch, available := state.Snapshot("a")
		if available && node != nil {
			synchronizedEpoch = epoch
			break
		}
		select {
		case <-limit:
			t.Fatal("Node state did not synchronize")
		case <-time.After(time.Millisecond):
		}
	}
	close(endWatch)
	limit = time.After(time.Second)
	for {
		_, epoch, available := state.Snapshot("a")
		if !available && epoch > synchronizedEpoch {
			break
		}
		select {
		case <-limit:
			t.Fatal("watch loss retained synchronized authority")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	if lists.Load() != 1 || watches.Load() != 1 {
		t.Fatalf("startup lists=%d watches=%d", lists.Load(), watches.Load())
	}
}
