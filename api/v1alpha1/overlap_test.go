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
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// RHWA-2063 (API-04), part 1: the single conflict check shared by the admission
// webhook and the configuration controller.
//
// The ticket's complaint is that today the webhook and the controller each
// refer the check to the other (see the two TODOs in
// storagebasedremediationconfig_webhook.go). Both call sites must therefore end
// up in this one function; these tests pin its behaviour, and the call-site
// tests only pin that it is reached.
//
// Note: a fake client does not establish admission behaviour. That the webhook
// is registered and actually rejects the request is e2e territory.

const (
	otherStorageClass = "other-sc"
	nsPrimary         = "sbr-system"
	nsSecondary       = "sbr-other"
)

// overlapScheme is the scheme used by the fake clients in this file.
func overlapScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("add SBR types to scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add core types to scheme: %v", err)
	}
	return s
}

type configOption func(*StorageBasedRemediationConfig)

func withMode(mode SharedStorageVolumeModeType) configOption {
	return func(c *StorageBasedRemediationConfig) { c.Spec.SharedStorageVolumeMode = &mode }
}

func withSelector(selector map[string]string) configOption {
	return func(c *StorageBasedRemediationConfig) { c.Spec.NodeSelector = selector }
}

func withStorageClass(name string) configOption {
	return func(c *StorageBasedRemediationConfig) { c.Spec.SharedStorageClass = name }
}

func withUID(uid string) configOption {
	return func(c *StorageBasedRemediationConfig) { c.UID = types.UID(uid) }
}

// blockPerNodeConfig builds a BlockPerNode configuration with the shared
// StorageClass; options override the defaults.
func blockPerNodeConfig(namespace, name string, opts ...configOption) *StorageBasedRemediationConfig {
	cfg := &StorageBasedRemediationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       StorageBasedRemediationConfigSpec{SharedStorageClass: testStorageClass},
	}
	withMode(SharedStorageVolumeModeBlockPerNode)(cfg)
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// overlapClient returns a reader seeded with the shared node fixture plus the
// given existing configurations.
func overlapClient(t *testing.T, existing ...*StorageBasedRemediationConfig) client.Reader {
	t.Helper()
	objs := make([]client.Object, 0, len(existing)+len(testNodes()))
	for i := range testNodes() {
		node := testNodes()[i]
		objs = append(objs, &node)
	}
	for _, cfg := range existing {
		objs = append(objs, cfg)
	}
	return fake.NewClientBuilder().WithScheme(overlapScheme(t)).WithObjects(objs...).Build()
}

func TestOverlapDisjointCoexist(t *testing.T) {
	existing := blockPerNodeConfig(nsPrimary, "zone-a", withSelector(map[string]string{zoneLabel: "a"}))
	incoming := blockPerNodeConfig(nsPrimary, "zone-b", withSelector(map[string]string{zoneLabel: "b"}))

	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
		t.Errorf("disjoint configurations rejected: %v", err)
	}
}

func TestOverlapSameNamespaceRejected(t *testing.T) {
	existing := blockPerNodeConfig(nsPrimary, "all-workers")
	incoming := blockPerNodeConfig(nsPrimary, "zone-a", withSelector(map[string]string{zoneLabel: "a"}))

	err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming)
	if !errors.Is(err, ErrNodeSelectorOverlap) {
		t.Fatalf("got %v, want ErrNodeSelectorOverlap", err)
	}
	// The reason has to be actionable: which configuration, and which nodes.
	for _, want := range []string{"all-workers", nodeWorkerLinuxA} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestOverlapCrossNamespaceRejected(t *testing.T) {
	existing := blockPerNodeConfig(nsSecondary, "all-workers")
	incoming := blockPerNodeConfig(nsPrimary, "also-all-workers")

	err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming)
	if !errors.Is(err, ErrNodeSelectorOverlap) {
		t.Fatalf("got %v, want ErrNodeSelectorOverlap", err)
	}
	if !strings.Contains(err.Error(), nsSecondary) {
		t.Errorf("error %q does not name the other namespace %q", err, nsSecondary)
	}
}

func TestOverlapIgnoresSelf(t *testing.T) {
	tests := []struct {
		name              string
		existing, subject *StorageBasedRemediationConfig
	}{
		{
			// Update: the stored copy and the incoming object share a UID.
			name:     "same uid",
			existing: blockPerNodeConfig(nsPrimary, "self", withUID("uid-self")),
			subject:  blockPerNodeConfig(nsPrimary, "self", withUID("uid-self")),
		},
		{
			// Create: the incoming object has no UID yet, so identity falls back
			// to namespace/name.
			name:     "same namespace and name, no uid on the incoming object",
			existing: blockPerNodeConfig(nsPrimary, "self", withUID("uid-self")),
			subject:  blockPerNodeConfig(nsPrimary, "self"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, tc.existing), tc.subject)
			if err != nil {
				t.Errorf("configuration conflicted with itself: %v", err)
			}
		})
	}
}

func TestOverlapIgnoresNonBlockPerNodeConfigs(t *testing.T) {
	// AC 5: a shared-storage configuration and a BlockPerNode configuration may
	// select the same nodes. Only BlockPerNode gives a node a watchdog owner.
	for _, mode := range []SharedStorageVolumeModeType{
		SharedStorageVolumeModeFilesystem,
		SharedStorageVolumeModeBlock,
	} {
		t.Run(string(mode), func(t *testing.T) {
			existing := blockPerNodeConfig(nsPrimary, "shared", withMode(mode))
			incoming := blockPerNodeConfig(nsPrimary, "per-node")

			if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
				t.Errorf("%s configuration treated as a conflict: %v", mode, err)
			}
		})
	}
}

func TestOverlapSubjectNotBlockPerNode(t *testing.T) {
	// The rule is BlockPerNode against BlockPerNode. A Filesystem configuration
	// landing on the same nodes as a BlockPerNode one is allowed.
	existing := blockPerNodeConfig(nsPrimary, "per-node")
	incoming := blockPerNodeConfig(nsPrimary, "filesystem", withMode(SharedStorageVolumeModeFilesystem))

	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
		t.Errorf("non-BlockPerNode subject rejected: %v", err)
	}
}

func TestOverlapStorageClassMismatch(t *testing.T) {
	// One StorageClass per release (6.5 defers the extension). This is
	// cluster-wide and independent of node overlap, so it must fire even when
	// the node sets are disjoint.
	existing := blockPerNodeConfig(nsSecondary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withStorageClass(testStorageClass))
	incoming := blockPerNodeConfig(nsPrimary, "zone-b",
		withSelector(map[string]string{zoneLabel: "b"}), withStorageClass(otherStorageClass))

	err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming)
	if !errors.Is(err, ErrStorageClassConflict) {
		t.Fatalf("got %v, want ErrStorageClassConflict", err)
	}
	for _, want := range []string{testStorageClass, otherStorageClass} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention StorageClass %q", err, want)
		}
	}
}

func TestOverlapSameStorageClassDisjointAccepted(t *testing.T) {
	existing := blockPerNodeConfig(nsSecondary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withStorageClass(testStorageClass))
	incoming := blockPerNodeConfig(nsPrimary, "pool-fast",
		withSelector(map[string]string{storagePoolLabel: "fast"}), withStorageClass(testStorageClass))

	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
		t.Errorf("disjoint configurations on one StorageClass rejected: %v", err)
	}
}

func TestOverlapStorageClassIgnoresNonBlockPerNode(t *testing.T) {
	existing := blockPerNodeConfig(nsPrimary, "shared",
		withMode(SharedStorageVolumeModeBlock), withStorageClass(otherStorageClass))
	incoming := blockPerNodeConfig(nsPrimary, "per-node",
		withSelector(map[string]string{zoneLabel: "a"}), withStorageClass(testStorageClass))

	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
		t.Errorf("shared-storage StorageClass treated as a conflict: %v", err)
	}
}

func TestOverlapListErrorIsNotSafe(t *testing.T) {
	// Fail-safe. Two watchdog owners on one node is the failure mode, so an
	// unreadable cluster must never answer "no conflict".
	listErr := errors.New("boom")
	tests := []struct {
		name string
		fail func(list client.ObjectList) bool
	}{
		{
			name: "configuration list fails",
			fail: func(list client.ObjectList) bool {
				_, ok := list.(*StorageBasedRemediationConfigList)
				return ok
			},
		},
		{
			name: "node list fails",
			fail: func(list client.ObjectList) bool {
				_, ok := list.(*corev1.NodeList)
				return ok
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := fake.NewClientBuilder().
				WithScheme(overlapScheme(t)).
				WithObjects(blockPerNodeConfig(nsPrimary, "all-workers")).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if tc.fail(list) {
							return listErr
						}
						return c.List(ctx, list, opts...)
					},
				}).
				Build()

			err := CheckBlockPerNodeConflicts(context.Background(), reader, blockPerNodeConfig(nsPrimary, "incoming"))
			if err == nil {
				t.Fatal("unreadable cluster state reported as no conflict")
			}
			if !errors.Is(err, ErrConflictCheckUnavailable) {
				t.Errorf("got %v, want ErrConflictCheckUnavailable", err)
			}
			if !errors.Is(err, listErr) {
				t.Errorf("error %v does not wrap the underlying list error", err)
			}
		})
	}
}

func TestOverlapNoNodesVisibleIsIndeterminate(t *testing.T) {
	// A real cluster always has at least one Node. An empty list means the
	// reader cannot see them (RBAC, cold cache), which is the same fail-safe
	// case as a list error rather than a silent pass.
	//
	// If the implementation decides an empty cluster is legitimately
	// conflict-free, this is the test to change, deliberately.
	reader := fake.NewClientBuilder().
		WithScheme(overlapScheme(t)).
		WithObjects(blockPerNodeConfig(nsPrimary, "all-workers")).
		Build()

	err := CheckBlockPerNodeConflicts(context.Background(), reader, blockPerNodeConfig(nsPrimary, "incoming"))
	if !errors.Is(err, ErrConflictCheckUnavailable) {
		t.Fatalf("got %v, want ErrConflictCheckUnavailable", err)
	}
}

func TestOverlapNoExistingConfigs(t *testing.T) {
	// First configuration in the cluster: nodes are visible, nothing to conflict
	// with.
	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t), blockPerNodeConfig(nsPrimary, "first")); err != nil {
		t.Errorf("first configuration rejected: %v", err)
	}
}

func TestOverlapNilClient(t *testing.T) {
	// A conflict check with nothing to read cannot pass.
	err := CheckBlockPerNodeConflicts(context.Background(), nil, blockPerNodeConfig(nsPrimary, "incoming"))
	if !errors.Is(err, ErrConflictCheckUnavailable) {
		t.Fatalf("got %v, want ErrConflictCheckUnavailable", err)
	}
}
