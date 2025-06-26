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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSBDConfigSpec_GetStaleNodeTimeout(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected time.Duration
	}{
		{
			name: "nil timeout returns default",
			spec: SBDConfigSpec{
				StaleNodeTimeout: nil,
			},
			expected: DefaultStaleNodeTimeout,
		},
		{
			name: "explicit timeout is returned",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 5 * time.Minute},
			},
			expected: 5 * time.Minute,
		},
		{
			name: "zero timeout returns zero",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 0},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetStaleNodeTimeout()
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateStaleNodeTimeout(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
	}{
		{
			name:      "default timeout is valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "valid custom timeout",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 5 * time.Minute},
			},
			wantError: false,
		},
		{
			name: "timeout too small",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 30 * time.Second},
			},
			wantError: true,
		},
		{
			name: "timeout too large",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 25 * time.Hour},
			},
			wantError: true,
		},
		{
			name: "minimum timeout is valid",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: MinStaleNodeTimeout},
			},
			wantError: false,
		},
		{
			name: "maximum timeout is valid",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: MaxStaleNodeTimeout},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateStaleNodeTimeout()
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateStaleNodeTimeout() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// Verify that constants have expected values
	if DefaultStaleNodeTimeout != 1*time.Hour {
		t.Errorf("DefaultStaleNodeTimeout = %v, expected 1h", DefaultStaleNodeTimeout)
	}

	if MinStaleNodeTimeout != 1*time.Minute {
		t.Errorf("MinStaleNodeTimeout = %v, expected 1m", MinStaleNodeTimeout)
	}

	if MaxStaleNodeTimeout != 24*time.Hour {
		t.Errorf("MaxStaleNodeTimeout = %v, expected 24h", MaxStaleNodeTimeout)
	}

	// Verify logical relationships
	if MinStaleNodeTimeout >= DefaultStaleNodeTimeout {
		t.Errorf("MinStaleNodeTimeout (%v) should be less than DefaultStaleNodeTimeout (%v)",
			MinStaleNodeTimeout, DefaultStaleNodeTimeout)
	}

	if DefaultStaleNodeTimeout >= MaxStaleNodeTimeout {
		t.Errorf("DefaultStaleNodeTimeout (%v) should be less than MaxStaleNodeTimeout (%v)",
			DefaultStaleNodeTimeout, MaxStaleNodeTimeout)
	}
}

func TestSBDConfigSpec_GetSbdWatchdogPath(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected string
	}{
		{
			name: "empty path returns default",
			spec: SBDConfigSpec{
				SbdWatchdogPath: "",
			},
			expected: DefaultWatchdogPath,
		},
		{
			name: "explicit path is returned",
			spec: SBDConfigSpec{
				SbdWatchdogPath: "/dev/watchdog1",
			},
			expected: "/dev/watchdog1",
		},
		{
			name: "custom path is returned",
			spec: SBDConfigSpec{
				SbdWatchdogPath: "/custom/watchdog",
			},
			expected: "/custom/watchdog",
		},
		{
			name: "default path when unset",
			spec: SBDConfigSpec{
				// SbdWatchdogPath not set
			},
			expected: DefaultWatchdogPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSbdWatchdogPath()
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestSBDConfigSpec_GetWatchdogTimeout(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected time.Duration
	}{
		{
			name: "nil timeout returns default",
			spec: SBDConfigSpec{
				WatchdogTimeout: nil,
			},
			expected: DefaultWatchdogTimeout,
		},
		{
			name: "explicit timeout is returned",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 30 * time.Second},
			},
			expected: 30 * time.Second,
		},
		{
			name: "custom timeout is returned",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 120 * time.Second},
			},
			expected: 120 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetWatchdogTimeout()
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSBDConfigSpec_GetPetIntervalMultiple(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected int32
	}{
		{
			name: "nil multiple returns default",
			spec: SBDConfigSpec{
				PetIntervalMultiple: nil,
			},
			expected: DefaultPetIntervalMultiple,
		},
		{
			name: "explicit multiple is returned",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{5}[0],
			},
			expected: 5,
		},
		{
			name: "custom multiple is returned",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{6}[0],
			},
			expected: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetPetIntervalMultiple()
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSBDConfigSpec_GetPetInterval(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected time.Duration
	}{
		{
			name:     "default values",
			spec:     SBDConfigSpec{},
			expected: DefaultWatchdogTimeout / time.Duration(DefaultPetIntervalMultiple), // 60s / 4 = 15s
		},
		{
			name: "custom watchdog timeout with default multiple",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 120 * time.Second},
			},
			expected: 120 * time.Second / time.Duration(DefaultPetIntervalMultiple), // 120s / 4 = 30s
		},
		{
			name: "default watchdog timeout with custom multiple",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{6}[0],
			},
			expected: DefaultWatchdogTimeout / 6, // 60s / 6 = 10s
		},
		{
			name: "custom values",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 90 * time.Second},
				PetIntervalMultiple: &[]int32{5}[0],
			},
			expected: 90 * time.Second / 5, // 90s / 5 = 18s
		},
		{
			name: "minimum pet interval enforced",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 10 * time.Second},
				PetIntervalMultiple: &[]int32{20}[0],
			},
			expected: time.Second, // Would be 500ms but enforced to 1s minimum
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetPetInterval()
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateWatchdogTimeout(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
	}{
		{
			name:      "default timeout is valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "valid custom timeout",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 30 * time.Second},
			},
			wantError: false,
		},
		{
			name: "timeout too small",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 5 * time.Second},
			},
			wantError: true,
		},
		{
			name: "timeout too large",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 400 * time.Second},
			},
			wantError: true,
		},
		{
			name: "minimum timeout is valid",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: MinWatchdogTimeout},
			},
			wantError: false,
		},
		{
			name: "maximum timeout is valid",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: MaxWatchdogTimeout},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateWatchdogTimeout()
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateWatchdogTimeout() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestSBDConfigSpec_ValidatePetIntervalMultiple(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
	}{
		{
			name:      "default multiple is valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "valid custom multiple",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{5}[0],
			},
			wantError: false,
		},
		{
			name: "multiple too small",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{2}[0],
			},
			wantError: true,
		},
		{
			name: "multiple too large",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{25}[0],
			},
			wantError: true,
		},
		{
			name: "minimum multiple is valid",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{MinPetIntervalMultiple}[0],
			},
			wantError: false,
		},
		{
			name: "maximum multiple is valid",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{MaxPetIntervalMultiple}[0],
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidatePetIntervalMultiple()
			if (err != nil) != tt.wantError {
				t.Errorf("ValidatePetIntervalMultiple() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestSBDConfigSpec_ValidatePetIntervalTiming(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
	}{
		{
			name:      "default values are valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "safe configuration",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 60 * time.Second},
				PetIntervalMultiple: &[]int32{4}[0],
			},
			wantError: false,
		},
		{
			name: "pet interval too long - exceeds 1/3 rule",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 90 * time.Second},
				PetIntervalMultiple: &[]int32{2}[0], // Would give 45s pet interval, which is > 30s (90/3)
			},
			wantError: true,
		},
		{
			name: "pet interval equal to watchdog timeout",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 10 * time.Second},
				PetIntervalMultiple: &[]int32{1}[0],
			},
			wantError: true,
		},
		{
			name: "pet interval exactly at 1/3 limit",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 60 * time.Second},
				PetIntervalMultiple: &[]int32{3}[0], // Gives exactly 20s pet interval (60/3)
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidatePetIntervalTiming()
			if (err != nil) != tt.wantError {
				t.Errorf("ValidatePetIntervalTiming() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateAll(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
	}{
		{
			name:      "all defaults are valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "all valid custom values",
			spec: SBDConfigSpec{
				StaleNodeTimeout:    &metav1.Duration{Duration: 2 * time.Hour},
				WatchdogTimeout:     &metav1.Duration{Duration: 90 * time.Second},
				PetIntervalMultiple: &[]int32{5}[0],
			},
			wantError: false,
		},
		{
			name: "invalid stale node timeout",
			spec: SBDConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 30 * time.Second}, // Too small
				WatchdogTimeout:  &metav1.Duration{Duration: 60 * time.Second},
			},
			wantError: true,
		},
		{
			name: "invalid watchdog timeout",
			spec: SBDConfigSpec{
				WatchdogTimeout: &metav1.Duration{Duration: 5 * time.Second}, // Too small
			},
			wantError: true,
		},
		{
			name: "invalid pet interval multiple",
			spec: SBDConfigSpec{
				PetIntervalMultiple: &[]int32{2}[0], // Too small
			},
			wantError: true,
		},
		{
			name: "invalid pet interval timing",
			spec: SBDConfigSpec{
				WatchdogTimeout:     &metav1.Duration{Duration: 60 * time.Second},
				PetIntervalMultiple: &[]int32{2}[0], // Would give 30s pet interval, which is > 20s (60/3)
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateAll()
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateAll() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestWatchdogConstants(t *testing.T) {
	// Verify that constants have expected values
	if DefaultWatchdogTimeout != 60*time.Second {
		t.Errorf("DefaultWatchdogTimeout = %v, expected 60s", DefaultWatchdogTimeout)
	}

	if MinWatchdogTimeout != 10*time.Second {
		t.Errorf("MinWatchdogTimeout = %v, expected 10s", MinWatchdogTimeout)
	}

	if MaxWatchdogTimeout != 300*time.Second {
		t.Errorf("MaxWatchdogTimeout = %v, expected 300s", MaxWatchdogTimeout)
	}

	if DefaultPetIntervalMultiple != 4 {
		t.Errorf("DefaultPetIntervalMultiple = %v, expected 4", DefaultPetIntervalMultiple)
	}

	if MinPetIntervalMultiple != 3 {
		t.Errorf("MinPetIntervalMultiple = %v, expected 3", MinPetIntervalMultiple)
	}

	if MaxPetIntervalMultiple != 20 {
		t.Errorf("MaxPetIntervalMultiple = %v, expected 20", MaxPetIntervalMultiple)
	}

	// Verify logical relationships
	if MinWatchdogTimeout >= DefaultWatchdogTimeout {
		t.Errorf("MinWatchdogTimeout (%v) should be less than DefaultWatchdogTimeout (%v)",
			MinWatchdogTimeout, DefaultWatchdogTimeout)
	}

	if DefaultWatchdogTimeout >= MaxWatchdogTimeout {
		t.Errorf("DefaultWatchdogTimeout (%v) should be less than MaxWatchdogTimeout (%v)",
			DefaultWatchdogTimeout, MaxWatchdogTimeout)
	}

	if MinPetIntervalMultiple >= DefaultPetIntervalMultiple {
		t.Errorf("MinPetIntervalMultiple (%v) should be less than DefaultPetIntervalMultiple (%v)",
			MinPetIntervalMultiple, DefaultPetIntervalMultiple)
	}

	if DefaultPetIntervalMultiple >= MaxPetIntervalMultiple {
		t.Errorf("DefaultPetIntervalMultiple (%v) should be less than MaxPetIntervalMultiple (%v)",
			DefaultPetIntervalMultiple, MaxPetIntervalMultiple)
	}
}

func TestGetImagePullPolicy(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected string
	}{
		{
			name:     "default value",
			spec:     SBDConfigSpec{},
			expected: "IfNotPresent",
		},
		{
			name: "explicit Always",
			spec: SBDConfigSpec{
				ImagePullPolicy: "Always",
			},
			expected: "Always",
		},
		{
			name: "explicit Never",
			spec: SBDConfigSpec{
				ImagePullPolicy: "Never",
			},
			expected: "Never",
		},
		{
			name: "explicit IfNotPresent",
			spec: SBDConfigSpec{
				ImagePullPolicy: "IfNotPresent",
			},
			expected: "IfNotPresent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetImagePullPolicy()
			if result != tt.expected {
				t.Errorf("GetImagePullPolicy() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestValidateImagePullPolicy(t *testing.T) {
	tests := []struct {
		name      string
		spec      SBDConfigSpec
		wantError bool
		errorMsg  string
	}{
		{
			name:      "default value (valid)",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "Always (valid)",
			spec: SBDConfigSpec{
				ImagePullPolicy: "Always",
			},
			wantError: false,
		},
		{
			name: "Never (valid)",
			spec: SBDConfigSpec{
				ImagePullPolicy: "Never",
			},
			wantError: false,
		},
		{
			name: "IfNotPresent (valid)",
			spec: SBDConfigSpec{
				ImagePullPolicy: "IfNotPresent",
			},
			wantError: false,
		},
		{
			name: "invalid value",
			spec: SBDConfigSpec{
				ImagePullPolicy: "InvalidPolicy",
			},
			wantError: true,
			errorMsg:  "invalid image pull policy \"InvalidPolicy\"",
		},
		{
			name: "empty string (should use default)",
			spec: SBDConfigSpec{
				ImagePullPolicy: "",
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateImagePullPolicy()
			if tt.wantError {
				if err == nil {
					t.Errorf("ValidateImagePullPolicy() expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("ValidateImagePullPolicy() error = %v, expected to contain %v", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateImagePullPolicy() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestGetImageWithOperatorImage(t *testing.T) {
	tests := []struct {
		name          string
		spec          SBDConfigSpec
		operatorImage string
		expected      string
	}{
		{
			name:          "explicit image specified",
			spec:          SBDConfigSpec{Image: "custom-registry.com/custom-org/custom-agent:v1.0.0"},
			operatorImage: "quay.io/medik8s/sbd-operator:v1.2.3",
			expected:      "custom-registry.com/custom-org/custom-agent:v1.0.0",
		},
		{
			name:          "no image specified - derive from operator image with tag",
			spec:          SBDConfigSpec{},
			operatorImage: "quay.io/medik8s/sbd-operator:v1.2.3",
			expected:      "quay.io/medik8s/sbd-agent:v1.2.3",
		},
		{
			name:          "no image specified - derive from operator image without tag",
			spec:          SBDConfigSpec{},
			operatorImage: "quay.io/medik8s/sbd-operator",
			expected:      "quay.io/medik8s/sbd-agent:latest",
		},
		{
			name:          "no image specified - simple operator image with tag",
			spec:          SBDConfigSpec{},
			operatorImage: "sbd-operator:v1.0.0",
			expected:      "sbd-agent:v1.0.0",
		},
		{
			name:          "no image specified - simple operator image without tag",
			spec:          SBDConfigSpec{},
			operatorImage: "sbd-operator",
			expected:      "sbd-agent:latest",
		},
		{
			name:          "no image specified - empty operator image",
			spec:          SBDConfigSpec{},
			operatorImage: "",
			expected:      "sbd-agent:latest",
		},
		{
			name:          "no image specified - complex registry path",
			spec:          SBDConfigSpec{},
			operatorImage: "registry.example.com:5000/my-org/my-project/sbd-operator:dev-123",
			expected:      "registry.example.com:5000/my-org/my-project/sbd-agent:dev-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetImageWithOperatorImage(tt.operatorImage)
			if result != tt.expected {
				t.Errorf("GetImageWithOperatorImage() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
