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
	"testing"
)

// nodeSelectorOverlaps checks if two node selectors could select overlapping sets of nodes
func nodeSelectorOverlaps(selector1, selector2 map[string]string) bool {
	// If either selector is empty, it matches all nodes, so there's overlap
	if len(selector1) == 0 || len(selector2) == 0 {
		return true
	}

	// For two selectors to overlap, they must be compatible (not contradictory)
	// This means for each label key that appears in both selectors,
	// the values must be compatible (same value or at least one is empty)

	// Find common label keys
	commonKeys := make(map[string]bool)
	for key := range selector1 {
		if _, exists := selector2[key]; exists {
			commonKeys[key] = true
		}
	}

	// If there are no common keys, the selectors could select overlapping nodes
	// (each selector constrains different dimensions)
	if len(commonKeys) == 0 {
		return true
	}

	// Check if the common keys have compatible values
	for key := range commonKeys {
		value1 := selector1[key]
		value2 := selector2[key]

		// If values are different and both are non-empty, selectors are disjoint
		if value1 != value2 && value1 != "" && value2 != "" {
			return false
		}
	}

	// If we get here, all common keys have compatible values, so there's potential overlap
	return true
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
