package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type NodeState struct {
	mu        sync.RWMutex
	nodes     map[string]*corev1.Node
	available bool
	epoch     uint64
	restart   chan struct{}
}

func NewNodeState() *NodeState {
	return &NodeState{nodes: map[string]*corev1.Node{}, restart: make(chan struct{}, 1)}
}

func (s *NodeState) Snapshot(name string) (*corev1.Node, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[name].DeepCopy(), s.epoch, s.available
}

func (s *NodeState) Invalidate() {
	s.mu.Lock()
	s.available = false
	s.epoch++
	s.mu.Unlock()
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

// Synchronize marks state unavailable whenever the continuing Node watch ends.
func (s *NodeState) Synchronize(ctx context.Context, c kubernetes.Interface) {
	for ctx.Err() == nil {
		select {
		case <-s.restart:
		default:
		}
		err := s.listAndWatch(ctx, c)
		s.mu.Lock()
		s.available = false
		s.epoch++
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		_ = err
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *NodeState) listAndWatch(ctx context.Context, c kubernetes.Interface) error {
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	w, err := c.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{ResourceVersion: nodes.ResourceVersion, AllowWatchBookmarks: true})
	if err != nil {
		return err
	}
	defer w.Stop()
	s.mu.Lock()
	s.nodes = map[string]*corev1.Node{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		s.nodes[n.Name] = n.DeepCopy()
	}
	s.available = true
	s.epoch++
	s.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.restart:
			return fmt.Errorf("Node state requires synchronization")
		case event, ok := <-w.ResultChan():
			if !ok || event.Type == watch.Error {
				return fmt.Errorf("Node watch ended")
			}
			if event.Type == watch.Bookmark {
				continue
			}
			n, ok := event.Object.(*corev1.Node)
			if !ok {
				return fmt.Errorf("unexpected Node watch object")
			}
			s.mu.Lock()
			if event.Type == watch.Deleted {
				delete(s.nodes, n.Name)
			} else {
				s.nodes[n.Name] = n.DeepCopy()
			}
			s.mu.Unlock()
		}
	}
}
