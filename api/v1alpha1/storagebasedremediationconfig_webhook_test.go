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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestConfigUpdateEffectiveModes(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock
	perNode := SharedStorageVolumeModeBlockPerNode
	validator := newTestValidator(t)
	for _, oldMode := range []*SharedStorageVolumeModeType{nil, &filesystem, &block, &perNode} {
		for _, newMode := range []*SharedStorageVolumeModeType{nil, &filesystem, &block, &perNode} {
			oldObj := &StorageBasedRemediationConfig{Spec: StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: oldMode, SharedStorageClass: testStorageClass,
			}}
			newObj := &StorageBasedRemediationConfig{Spec: StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: newMode, SharedStorageClass: testStorageClass,
			}}
			_, err := validator.ValidateUpdate(t.Context(), oldObj, newObj)
			wantErr := oldObj.Spec.GetSharedStorageVolumeMode() != newObj.Spec.GetSharedStorageVolumeMode()
			if (err != nil) != wantErr {
				t.Errorf("mode update %v to %v: %v", oldMode, newMode, err)
			}
		}
	}
}

func TestConfigDurationAdmission(t *testing.T) {
	validator := newTestValidator(t)
	perNode := SharedStorageVolumeModeBlockPerNode
	defaults := map[string]string{
		ioTimeoutField: "15s", outageGraceField: "60s", recoveryPeriodField: "5m",
	}
	for _, field := range []string{ioTimeoutField, outageGraceField, recoveryPeriodField} {
		for _, tc := range []struct {
			old, new string
			wantErr  bool
		}{
			{"", "", false},
			{"", defaults[field], false},
			{defaults[field], "", false},
			{"60s", "1m", false},
			{"30s", "31s", field != ioTimeoutField},
			{"30s", "", field != ioTimeoutField},
			{"", "0s", true},
		} {
			t.Run(field+"/"+tc.old+"-to-"+tc.new, func(t *testing.T) {
				objects := make([]*StorageBasedRemediationConfig, 0, 2)
				for _, value := range []string{tc.old, tc.new} {
					obj := &StorageBasedRemediationConfig{Spec: StorageBasedRemediationConfigSpec{
						SharedStorageVolumeMode: &perNode, SharedStorageClass: testStorageClass,
					}}
					if value != "" {
						if err := json.Unmarshal([]byte(`{"`+field+`":"`+value+`"}`), &obj.Spec); err != nil {
							t.Fatal(err)
						}
					}
					objects = append(objects, obj)
				}
				_, err := validator.ValidateUpdate(t.Context(), objects[0], objects[1])
				if (err != nil) != tc.wantErr || (err != nil && !strings.Contains(err.Error(), field)) {
					t.Errorf("ValidateUpdate() = %v, want error %v", err, tc.wantErr)
				}
			})
		}
	}
	invalid := &StorageBasedRemediationConfig{Spec: StorageBasedRemediationConfigSpec{
		IOTimeout: &metav1.Duration{Duration: -time.Second},
	}}
	if _, err := validator.ValidateCreate(t.Context(), invalid); err == nil {
		t.Fatal("create accepted invalid duration")
	}
	if _, err := validator.ValidateUpdate(t.Context(), invalid, invalid); err == nil {
		t.Fatal("update accepted invalid duration")
	}
}

// nodeSelectorOverlaps checks if two node selectors could select overlapping sets of nodes
func nodeSelectorOverlaps(selector1, selector2 map[string]string) bool {
	// If either selector is empty, it matches all nodes, so there's overlap
	if len(selector1) == 0 || len(selector2) == 0 {
		return true
	}

	// For two selectors to overlap, they must be compatible (not contradictory)
	// Check all keys from both selectors to detect incompatibilities

	// Collect all unique keys from both selectors
	allKeys := make(map[string]bool)
	for key := range selector1 {
		allKeys[key] = true
	}
	for key := range selector2 {
		allKeys[key] = true
	}

	// Check each key for compatibility
	for key := range allKeys {
		value1, exists1 := selector1[key]
		value2, exists2 := selector2[key]

		// If key exists in both selectors
		if exists1 && exists2 {
			// If values are different and both are non-empty, selectors are disjoint
			if value1 != value2 && value1 != "" && value2 != "" {
				return false
			}
		}
		// If key exists in only one selector, we need to check for mutual exclusion
		// Special case: node role labels are mutually exclusive
		if exists1 && !exists2 {
			// Check if this key represents a mutually exclusive role
			if isMutuallyExclusiveRole(key) {
				// Check if the other selector has a conflicting role
				for otherKey := range selector2 {
					if isMutuallyExclusiveRole(otherKey) && key != otherKey {
						return false
					}
				}
			}
		}
		if exists2 && !exists1 {
			// Check if this key represents a mutually exclusive role
			if isMutuallyExclusiveRole(key) {
				// Check if the other selector has a conflicting role
				for otherKey := range selector1 {
					if isMutuallyExclusiveRole(otherKey) && key != otherKey {
						return false
					}
				}
			}
		}
	}

	// If we get here, no incompatibilities were found, so there's potential overlap
	return true
}

// isMutuallyExclusiveRole checks if a label key represents a mutually exclusive node role
func isMutuallyExclusiveRole(key string) bool {
	mutuallyExclusiveRoles := []string{
		"node-role.kubernetes.io/worker",
		"node-role.kubernetes.io/control-plane",
		"node-role.kubernetes.io/master", // Legacy key
	}

	for _, role := range mutuallyExclusiveRoles {
		if key == role {
			return true
		}
	}
	return false
}

func TestStorageBasedRemediationConfigValidator_ValidateUpdate_VolumeModeImmutability(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock
	validator := newTestValidator(t)

	tests := []struct {
		name    string
		oldMode *SharedStorageVolumeModeType
		newMode *SharedStorageVolumeModeType
		wantErr bool
	}{
		{
			name:    "nil to nil is allowed",
			oldMode: nil,
			newMode: nil,
			wantErr: false,
		},
		{
			name:    "nil to Filesystem is allowed (both resolve to Filesystem)",
			oldMode: nil,
			newMode: &filesystem,
			wantErr: false,
		},
		{
			name:    "Filesystem to nil is allowed (both resolve to Filesystem)",
			oldMode: &filesystem,
			newMode: nil,
			wantErr: false,
		},
		{
			name:    "Filesystem to Filesystem is allowed",
			oldMode: &filesystem,
			newMode: &filesystem,
			wantErr: false,
		},
		{
			name:    "Block to Block is allowed",
			oldMode: &block,
			newMode: &block,
			wantErr: false,
		},
		{
			name:    "nil to Block is rejected",
			oldMode: nil,
			newMode: &block,
			wantErr: true,
		},
		{
			name:    "Filesystem to Block is rejected",
			oldMode: &filesystem,
			newMode: &block,
			wantErr: true,
		},
		{
			name:    "Block to Filesystem is rejected",
			oldMode: &block,
			newMode: &filesystem,
			wantErr: true,
		},
		{
			name:    "Block to nil is rejected",
			oldMode: &block,
			newMode: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldObj := &StorageBasedRemediationConfig{
				Spec: StorageBasedRemediationConfigSpec{
					SharedStorageVolumeMode: tt.oldMode,
					SharedStorageClass:      "test-sc",
				},
			}
			newObj := &StorageBasedRemediationConfig{
				Spec: StorageBasedRemediationConfigSpec{
					SharedStorageVolumeMode: tt.newMode,
					SharedStorageClass:      "test-sc",
				},
			}
			_, err := validator.ValidateUpdate(t.Context(), oldObj, newObj)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_nodeSelectorOverlaps(t *testing.T) {
	tests := []struct {
		name      string
		selector1 map[string]string
		selector2 map[string]string
		expected  bool
	}{
		{
			name:      "both empty - overlaps",
			selector1: map[string]string{},
			selector2: map[string]string{},
			expected:  true,
		},
		{
			name:      "one empty - overlaps",
			selector1: map[string]string{},
			selector2: map[string]string{"role": "worker"},
			expected:  true,
		},
		{
			name:      "identical selectors - overlaps",
			selector1: map[string]string{"role": "worker"},
			selector2: map[string]string{"role": "worker"},
			expected:  true,
		},
		{
			name:      "different values for same key - no overlap",
			selector1: map[string]string{"role": "worker"},
			selector2: map[string]string{"role": "control-plane"},
			expected:  false,
		},
		{
			name:      "different keys - overlaps (could select same nodes)",
			selector1: map[string]string{"role": "worker"},
			selector2: map[string]string{"zone": "us-west-2a"},
			expected:  true,
		},
		{
			name: "compatible selectors - overlaps",
			selector1: map[string]string{
				"role": "worker",
				"zone": "us-west-2a",
			},
			selector2: map[string]string{
				"role": "worker",
				"env":  "production",
			},
			expected: true,
		},
		{
			name: "incompatible selectors - no overlap",
			selector1: map[string]string{
				"role": "worker",
				"zone": "us-west-2a",
			},
			selector2: map[string]string{
				"role": "control-plane",
				"zone": "us-west-2a",
			},
			expected: false,
		},
		{
			name: "complex compatible case - overlaps",
			selector1: map[string]string{
				"node-role.kubernetes.io/worker": "",
				"topology.kubernetes.io/zone":    "us-west-2a",
			},
			selector2: map[string]string{
				"node-role.kubernetes.io/worker": "",
				"app":                            "database",
			},
			expected: true,
		},
		{
			name: "complex incompatible case - no overlap",
			selector1: map[string]string{
				"node-role.kubernetes.io/worker": "",
				"topology.kubernetes.io/zone":    "us-west-2a",
			},
			selector2: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
				"topology.kubernetes.io/zone":           "us-west-2a",
			},
			expected: false,
		},
		{
			name: "empty string values - overlaps",
			selector1: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
			selector2: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
			expected: true,
		},
		{
			name: "mix of empty and non-empty values - overlaps",
			selector1: map[string]string{
				"role": "",
			},
			selector2: map[string]string{
				"role": "worker",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := nodeSelectorOverlaps(tt.selector1, tt.selector2)
			if result != tt.expected {
				t.Errorf("nodeSelectorOverlaps() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// RHWA-2063 (API-04), part 1: the webhook call site.
//
// The two TODOs in storagebasedremediationconfig_webhook.go defer node-selector
// overlap validation to the controller, and the controller defers it back. The
// validator therefore has to carry a reader and reach the shared check. These
// tests pin only that it is reached and that spec validation still runs first;
// the check's own behaviour is covered in overlap_test.go.

// newTestValidator builds a validator backed by the shared node fixture plus
// the given existing configurations.
func newTestValidator(t *testing.T, existing ...*StorageBasedRemediationConfig) *StorageBasedRemediationConfigValidator {
	t.Helper()
	return &StorageBasedRemediationConfigValidator{Client: overlapClient(t, existing...)}
}

func TestValidateCreateRejectsOverlap(t *testing.T) {
	existing := blockPerNodeConfig(nsSecondary, "all-workers")
	validator := newTestValidator(t, existing)

	incoming := blockPerNodeConfig(nsPrimary, "zone-a", withSelector(map[string]string{zoneLabel: "a"}))
	_, err := validator.ValidateCreate(t.Context(), incoming)
	if !errors.Is(err, ErrNodeSelectorOverlap) {
		t.Fatalf("ValidateCreate() = %v, want ErrNodeSelectorOverlap", err)
	}
	if !strings.Contains(err.Error(), "all-workers") {
		t.Errorf("error %q does not name the conflicting configuration", err)
	}
}

func TestValidateCreateRejectsSecondStorageClass(t *testing.T) {
	existing := blockPerNodeConfig(nsPrimary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withStorageClass(testStorageClass))
	validator := newTestValidator(t, existing)

	incoming := blockPerNodeConfig(nsPrimary, "pool-fast",
		withSelector(map[string]string{storagePoolLabel: "fast"}), withStorageClass(otherStorageClass))
	if _, err := validator.ValidateCreate(t.Context(), incoming); !errors.Is(err, ErrStorageClassConflict) {
		t.Fatalf("ValidateCreate() = %v, want ErrStorageClassConflict", err)
	}
}

func TestValidateCreateAcceptsDisjointSets(t *testing.T) {
	existing := blockPerNodeConfig(nsSecondary, "zone-a", withSelector(map[string]string{zoneLabel: "a"}))
	validator := newTestValidator(t, existing)

	incoming := blockPerNodeConfig(nsPrimary, "pool-fast", withSelector(map[string]string{storagePoolLabel: "fast"}))
	if _, err := validator.ValidateCreate(t.Context(), incoming); err != nil {
		t.Errorf("ValidateCreate() rejected disjoint configurations: %v", err)
	}
}

func TestValidateUpdateRejectsWidenedSelector(t *testing.T) {
	// A selector may be narrowed or moved freely until it reaches nodes another
	// BlockPerNode configuration already owns.
	existing := blockPerNodeConfig(nsSecondary, "pool-fast",
		withSelector(map[string]string{storagePoolLabel: "fast"}), withUID("uid-other"))
	validator := newTestValidator(t, existing)

	oldObj := blockPerNodeConfig(nsPrimary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withUID("uid-subject"))
	widened := blockPerNodeConfig(nsPrimary, "zone-a", withUID("uid-subject")) // default: all linux workers

	if _, err := validator.ValidateUpdate(t.Context(), oldObj, widened); !errors.Is(err, ErrNodeSelectorOverlap) {
		t.Fatalf("ValidateUpdate() = %v, want ErrNodeSelectorOverlap", err)
	}
}

func TestValidateUpdateIgnoresTheStoredCopyOfItself(t *testing.T) {
	// The subject is already in the cluster; re-admitting it must not conflict
	// with its own stored copy.
	stored := blockPerNodeConfig(nsPrimary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withUID("uid-subject"))
	validator := newTestValidator(t, stored)

	updated := blockPerNodeConfig(nsPrimary, "zone-a",
		withSelector(map[string]string{zoneLabel: "a"}), withUID("uid-subject"))
	updated.Spec.SBRTimeoutSeconds = ptr.To(int32(60))

	if _, err := validator.ValidateUpdate(t.Context(), stored, updated); err != nil {
		t.Errorf("ValidateUpdate() conflicted with the subject's own stored copy: %v", err)
	}
}

func TestValidatorWithoutClientFailsClosed(t *testing.T) {
	// A validator wired without a reader silently skipped the check before; that
	// is how the TODO survived. It has to be an error, not a pass.
	validator := &StorageBasedRemediationConfigValidator{}
	cfg := blockPerNodeConfig(nsPrimary, "incoming")

	if _, err := validator.ValidateCreate(t.Context(), cfg); !errors.Is(err, ErrConflictCheckUnavailable) {
		t.Errorf("ValidateCreate() = %v, want ErrConflictCheckUnavailable", err)
	}
	if _, err := validator.ValidateUpdate(t.Context(), cfg, cfg); !errors.Is(err, ErrConflictCheckUnavailable) {
		t.Errorf("ValidateUpdate() = %v, want ErrConflictCheckUnavailable", err)
	}
}

func TestValidateSpecFailsBeforeTheConflictCheck(t *testing.T) {
	// Spec validation must not depend on cluster reads: an invalid spec is
	// rejected on its own terms even with no reader at all.
	validator := &StorageBasedRemediationConfigValidator{}
	invalid := blockPerNodeConfig(nsPrimary, "no-storage-class", withStorageClass(""))

	_, err := validator.ValidateCreate(t.Context(), invalid)
	if err == nil {
		t.Fatal("ValidateCreate() accepted BlockPerNode without a storage class")
	}
	if errors.Is(err, ErrConflictCheckUnavailable) {
		t.Errorf("conflict check ran before spec validation: %v", err)
	}
}

func TestValidateDeleteNeedsNoClient(t *testing.T) {
	validator := &StorageBasedRemediationConfigValidator{}
	if _, err := validator.ValidateDelete(t.Context(), blockPerNodeConfig(nsPrimary, "going-away")); err != nil {
		t.Errorf("ValidateDelete() = %v, want nil", err)
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
	// A shared-storage existing configuration and a BlockPerNode incoming
	// configuration on the same nodes must be rejected: every mode except
	// detect-only opens the watchdog, so two configurations on one node means
	// two watchdog owners.
	for _, mode := range []SharedStorageVolumeModeType{
		SharedStorageVolumeModeFilesystem,
		SharedStorageVolumeModeBlock,
	} {
		t.Run(string(mode), func(t *testing.T) {
			existing := blockPerNodeConfig(nsPrimary, "shared", withMode(mode))
			incoming := blockPerNodeConfig(nsPrimary, "per-node")

			err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming)
			if !errors.Is(err, ErrNodeSelectorOverlap) {
				t.Fatalf("got %v, want ErrNodeSelectorOverlap", err)
			}
		})
	}
}

func TestOverlapSubjectNotBlockPerNode(t *testing.T) {
	// A Filesystem incoming configuration landing on the same nodes as an
	// existing BlockPerNode one must be rejected: both modes open the watchdog.
	existing := blockPerNodeConfig(nsPrimary, "per-node")
	incoming := blockPerNodeConfig(nsPrimary, "filesystem", withMode(SharedStorageVolumeModeFilesystem))

	err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming)
	if !errors.Is(err, ErrNodeSelectorOverlap) {
		t.Fatalf("got %v, want ErrNodeSelectorOverlap", err)
	}
}

func TestOverlapSharedStorageAndBlockPerNodeDisjointAccepted(t *testing.T) {
	// A shared-storage configuration and a BlockPerNode configuration on
	// disjoint node sets are allowed.
	existing := blockPerNodeConfig(nsPrimary, "shared",
		withMode(SharedStorageVolumeModeFilesystem), withSelector(map[string]string{zoneLabel: "a"}))
	incoming := blockPerNodeConfig(nsPrimary, "per-node",
		withSelector(map[string]string{zoneLabel: "b"}))

	if err := CheckBlockPerNodeConflicts(context.Background(), overlapClient(t, existing), incoming); err != nil {
		t.Errorf("disjoint shared-storage and BlockPerNode rejected: %v", err)
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
		withMode(SharedStorageVolumeModeBlock),
		withStorageClass(otherStorageClass),
		withSelector(map[string]string{zoneLabel: "b"}))
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

func TestOverlapNoNodesIsConflictFree(t *testing.T) {
	// An empty Node list without an error means the cluster has no Nodes,
	// so no configuration can overlap another.
	reader := fake.NewClientBuilder().
		WithScheme(overlapScheme(t)).
		WithObjects(blockPerNodeConfig(nsPrimary, "all-workers")).
		Build()

	if err := CheckBlockPerNodeConflicts(context.Background(), reader, blockPerNodeConfig(nsPrimary, "incoming")); err != nil {
		t.Errorf("empty cluster reported a conflict: %v", err)
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
