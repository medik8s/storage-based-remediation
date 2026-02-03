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

	"github.com/medik8s/sbd-operator/pkg/agent"
)

// testCase represents a generic test case for validation functions
type testCase struct {
	name      string
	spec      SBDConfigSpec
	wantError bool
}

// runValidationTests is a generic helper for testing validation methods
func runValidationTests(t *testing.T, testName string, tests []testCase, validateFunc func(SBDConfigSpec) error) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFunc(tt.spec)
			if (err != nil) != tt.wantError {
				t.Errorf("%s error = %v, wantError %v", testName, err, tt.wantError)
			}
		})
	}
}

// testCaseInterval represents a generic test case for interval validation functions
type testCaseInterval struct {
	name    string
	spec    SBDConfigSpec
	wantErr bool
}

// runIntervalTests is a generic helper for testing interval validation methods
func runIntervalTests(t *testing.T, testName string, tests []testCaseInterval, validateFunc func(SBDConfigSpec) error) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFunc(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("%s error = %v, wantErr %v", testName, err, tt.wantErr)
			}
		})
	}
}

// createTimeoutValidationTests creates standard test cases for timeout validation
func createTimeoutValidationTests(
	fieldSetter func(*SBDConfigSpec, *metav1.Duration),
	validValue time.Duration,
	tooSmallValue time.Duration,
	tooLargeValue time.Duration,
	minValue time.Duration,
	maxValue time.Duration,
) []testCase {
	return []testCase{
		{
			name:      "default timeout is valid",
			spec:      SBDConfigSpec{},
			wantError: false,
		},
		{
			name: "valid custom timeout",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: validValue})
				return spec
			}(),
			wantError: false,
		},
		{
			name: "timeout too small",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooSmallValue})
				return spec
			}(),
			wantError: true,
		},
		{
			name: "timeout too large",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooLargeValue})
				return spec
			}(),
			wantError: true,
		},
		{
			name: "minimum timeout is valid",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: minValue})
				return spec
			}(),
			wantError: false,
		},
		{
			name: "maximum timeout is valid",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: maxValue})
				return spec
			}(),
			wantError: false,
		},
	}
}

// createIntervalValidationTests creates standard test cases for interval validation
func createIntervalValidationTests(
	fieldSetter func(*SBDConfigSpec, *metav1.Duration),
	validValue time.Duration,
	tooSmallValue time.Duration,
	tooLargeValue time.Duration,
) []testCaseInterval {
	return []testCaseInterval{
		{
			name:    "nil interval uses default (valid)",
			spec:    SBDConfigSpec{},
			wantErr: false,
		},
		{
			name: "valid interval",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: validValue})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "minimum interval",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: 1 * time.Second})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "maximum interval",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: 60 * time.Second})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "too small interval",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooSmallValue})
				return spec
			}(),
			wantErr: true,
		},
		{
			name: "too large interval",
			spec: func() SBDConfigSpec {
				spec := SBDConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooLargeValue})
				return spec
			}(),
			wantErr: true,
		},
	}
}

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
	tests := createTimeoutValidationTests(
		func(spec *SBDConfigSpec, d *metav1.Duration) { spec.StaleNodeTimeout = d },
		5*time.Minute,
		30*time.Second,
		25*time.Hour,
		MinStaleNodeTimeout,
		MaxStaleNodeTimeout,
	)

	runValidationTests(t, "ValidateStaleNodeTimeout()", tests, func(spec SBDConfigSpec) error {
		return spec.ValidateStaleNodeTimeout()
	})
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
	tests := createTimeoutValidationTests(
		func(spec *SBDConfigSpec, d *metav1.Duration) { spec.WatchdogTimeout = d },
		30*time.Second,
		5*time.Second,
		400*time.Second,
		MinWatchdogTimeout,
		MaxWatchdogTimeout,
	)

	runValidationTests(t, "ValidateWatchdogTimeout()", tests, func(spec SBDConfigSpec) error {
		return spec.ValidateWatchdogTimeout()
	})
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

func TestSBDConfigSpec_GetRebootMethod(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected string
	}{
		{
			name: "empty reboot method returns default",
			spec: SBDConfigSpec{
				RebootMethod: "",
			},
			expected: DefaultRebootMethod,
		},
		{
			name: "explicit panic method is returned",
			spec: SBDConfigSpec{
				RebootMethod: "panic",
			},
			expected: "panic",
		},
		{
			name: "explicit systemctl-reboot method is returned",
			spec: SBDConfigSpec{
				RebootMethod: "systemctl-reboot",
			},
			expected: "systemctl-reboot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetRebootMethod()
			if result != tt.expected {
				t.Errorf("GetRebootMethod() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetSBDTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected int32
	}{
		{
			name: "nil timeout returns default",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: nil,
			},
			expected: DefaultSBDTimeoutSeconds,
		},
		{
			name: "explicit timeout is returned",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(60),
			},
			expected: 60,
		},
		{
			name: "minimum timeout is returned",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(10),
			},
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSBDTimeoutSeconds()
			if result != tt.expected {
				t.Errorf("GetSBDTimeoutSeconds() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetSBDUpdateInterval(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected time.Duration
	}{
		{
			name: "nil interval returns default",
			spec: SBDConfigSpec{
				SBDUpdateInterval: nil,
			},
			expected: DefaultSBDUpdateInterval,
		},
		{
			name: "explicit interval is returned",
			spec: SBDConfigSpec{
				SBDUpdateInterval: &metav1.Duration{Duration: 10 * time.Second},
			},
			expected: 10 * time.Second,
		},
		{
			name: "minimum interval is returned",
			spec: SBDConfigSpec{
				SBDUpdateInterval: &metav1.Duration{Duration: 1 * time.Second},
			},
			expected: 1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSBDUpdateInterval()
			if result != tt.expected {
				t.Errorf("GetSBDUpdateInterval() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetPeerCheckInterval(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected time.Duration
	}{
		{
			name: "nil interval returns default",
			spec: SBDConfigSpec{
				PeerCheckInterval: nil,
			},
			expected: DefaultPeerCheckInterval,
		},
		{
			name: "explicit interval is returned",
			spec: SBDConfigSpec{
				PeerCheckInterval: &metav1.Duration{Duration: 3 * time.Second},
			},
			expected: 3 * time.Second,
		},
		{
			name: "maximum interval is returned",
			spec: SBDConfigSpec{
				PeerCheckInterval: &metav1.Duration{Duration: 60 * time.Second},
			},
			expected: 60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetPeerCheckInterval()
			if result != tt.expected {
				t.Errorf("GetPeerCheckInterval() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateRebootMethod(t *testing.T) {
	tests := []struct {
		name    string
		spec    SBDConfigSpec
		wantErr bool
	}{
		{
			name: "valid panic method",
			spec: SBDConfigSpec{
				RebootMethod: "panic",
			},
			wantErr: false,
		},
		{
			name: "valid systemctl-reboot method",
			spec: SBDConfigSpec{
				RebootMethod: "systemctl-reboot",
			},
			wantErr: false,
		},
		{
			name: "valid none method",
			spec: SBDConfigSpec{
				RebootMethod: "none",
			},
			wantErr: false,
		},
		{
			name: "empty method uses default (valid)",
			spec: SBDConfigSpec{
				RebootMethod: "",
			},
			wantErr: false,
		},
		{
			name: "invalid method",
			spec: SBDConfigSpec{
				RebootMethod: "invalid-method",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateRebootMethod()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRebootMethod() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateSBDTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name    string
		spec    SBDConfigSpec
		wantErr bool
	}{
		{
			name: "nil timeout uses default (valid)",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: nil,
			},
			wantErr: false,
		},
		{
			name: "valid timeout",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(60),
			},
			wantErr: false,
		},
		{
			name: "minimum timeout",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(10),
			},
			wantErr: false,
		},
		{
			name: "maximum timeout",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(300),
			},
			wantErr: false,
		},
		{
			name: "too small timeout",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(5),
			},
			wantErr: true,
		},
		{
			name: "too large timeout",
			spec: SBDConfigSpec{
				SBDTimeoutSeconds: func(v int32) *int32 { return &v }(400),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateSBDTimeoutSeconds()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSBDTimeoutSeconds() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSBDConfigSpec_ValidateSBDUpdateInterval(t *testing.T) {
	tests := createIntervalValidationTests(
		func(spec *SBDConfigSpec, d *metav1.Duration) { spec.SBDUpdateInterval = d },
		10*time.Second,
		500*time.Millisecond,
		120*time.Second,
	)

	runIntervalTests(t, "ValidateSBDUpdateInterval()", tests, func(spec SBDConfigSpec) error {
		return spec.ValidateSBDUpdateInterval()
	})
}

func TestSBDConfigSpec_ValidatePeerCheckInterval(t *testing.T) {
	tests := createIntervalValidationTests(
		func(spec *SBDConfigSpec, d *metav1.Duration) { spec.PeerCheckInterval = d },
		5*time.Second,
		500*time.Millisecond,
		90*time.Second,
	)

	runIntervalTests(t, "ValidatePeerCheckInterval()", tests, func(spec SBDConfigSpec) error {
		return spec.ValidatePeerCheckInterval()
	})
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
		{
			name: "invalid I/O timeout - too small",
			spec: SBDConfigSpec{
				IOTimeout: &metav1.Duration{Duration: 50 * time.Millisecond}, // Too small
			},
			wantError: true,
		},
		{
			name: "invalid I/O timeout - too large",
			spec: SBDConfigSpec{
				IOTimeout: &metav1.Duration{Duration: 10 * time.Minute}, // Too large
			},
			wantError: true,
		},
		{
			name: "valid I/O timeout",
			spec: SBDConfigSpec{
				IOTimeout: &metav1.Duration{Duration: 5 * time.Second}, // Valid
			},
			wantError: false,
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

func TestSBDConfigSpec_GetSharedStoragePVCName(t *testing.T) {
	tests := []struct {
		name          string
		spec          SBDConfigSpec
		sbdConfigName string
		expected      string
	}{
		{
			name:          "no storage class configured",
			spec:          SBDConfigSpec{},
			sbdConfigName: "test-config",
			expected:      "",
		},
		{
			name: "storage class configured",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			sbdConfigName: "my-sbd-config",
			expected:      "my-sbd-config-shared-storage",
		},
		{
			name: "complex storage class name",
			spec: SBDConfigSpec{
				SharedStorageClass: "nfs-client",
			},
			sbdConfigName: "sbd-config-with-shared-storage",
			expected:      "sbd-config-with-shared-storage-shared-storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSharedStoragePVCName(tt.sbdConfigName)
			if result != tt.expected {
				t.Errorf("GetSharedStoragePVCName() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetSharedStorageStorageClass(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected string
	}{
		{
			name:     "no storage class configured",
			spec:     SBDConfigSpec{},
			expected: "",
		},
		{
			name: "storage class configured",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			expected: "efs-sc",
		},
		{
			name: "complex storage class name",
			spec: SBDConfigSpec{
				SharedStorageClass: "nfs-client",
			},
			expected: "nfs-client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSharedStorageStorageClass()
			if result != tt.expected {
				t.Errorf("GetSharedStorageStorageClass() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetSharedStorageMountPath(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected string
	}{
		{
			name:     "returns fixed path",
			spec:     SBDConfigSpec{},
			expected: agent.SharedStorageSBDDeviceDirectory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSharedStorageMountPath()
			if result != tt.expected {
				t.Errorf("GetSharedStorageMountPath() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_HasSharedStorage(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected bool
	}{
		{
			name:     "no shared storage configured",
			spec:     SBDConfigSpec{},
			expected: false,
		},
		{
			name: "shared storage class configured",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			expected: true,
		},
		{
			name: "empty storage class name means no shared storage",
			spec: SBDConfigSpec{
				SharedStorageClass: "",
			},
			expected: false,
		},
		{
			name: "only storage class enables shared storage",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.HasSharedStorage()
			if result != tt.expected {
				t.Errorf("HasSharedStorage() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSBDConfigSpec_GetNodeSelector(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		expected map[string]string
	}{
		{
			name: "default node selector - worker nodes only",
			spec: SBDConfigSpec{},
			expected: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
		{
			name: "explicit node selector",
			spec: SBDConfigSpec{
				NodeSelector: map[string]string{
					"custom-label": "custom-value",
				},
			},
			expected: map[string]string{
				"custom-label": "custom-value",
			},
		},
		{
			name: "multiple node selector labels",
			spec: SBDConfigSpec{
				NodeSelector: map[string]string{
					"zone":        "us-east-1a",
					"node-type":   "compute",
					"environment": "production",
				},
			},
			expected: map[string]string{
				"zone":        "us-east-1a",
				"node-type":   "compute",
				"environment": "production",
			},
		},
		{
			name: "empty node selector map returns default",
			spec: SBDConfigSpec{
				NodeSelector: map[string]string{},
			},
			expected: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetNodeSelector()
			if len(result) != len(tt.expected) {
				t.Errorf("GetNodeSelector() length = %v, expected %v", len(result), len(tt.expected))
				return
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("GetNodeSelector()[%q] = %v, expected %v", k, result[k], v)
				}
			}
		})
	}
}

func TestSBDConfigSpec_ValidateSharedStorageClass(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		wantErr  bool
		errorMsg string
	}{
		{
			name:    "no storage class configured - valid",
			spec:    SBDConfigSpec{},
			wantErr: false,
		},
		{
			name: "valid storage class name",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			wantErr: false,
		},
		{
			name: "valid complex storage class name",
			spec: SBDConfigSpec{
				SharedStorageClass: "nfs-client",
			},
			wantErr: false,
		},
		{
			name: "invalid storage class name - starts with hyphen",
			spec: SBDConfigSpec{
				SharedStorageClass: "-invalid-sc",
			},
			wantErr:  true,
			errorMsg: "must start with alphanumeric character",
		},
		{
			name: "invalid storage class name - ends with hyphen",
			spec: SBDConfigSpec{
				SharedStorageClass: "invalid-sc-",
			},
			wantErr:  true,
			errorMsg: "must end with alphanumeric character",
		},
		{
			name: "invalid storage class name - too long",
			spec: SBDConfigSpec{
				SharedStorageClass: strings.Repeat("a", 254),
			},
			wantErr:  true,
			errorMsg: "must be no more than 253 characters",
		},
		{
			name: "valid storage class name - max length",
			spec: SBDConfigSpec{
				SharedStorageClass: strings.Repeat("a", 253),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateSharedStorageClass()
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateSharedStorageClass() expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("ValidateSharedStorageClass() error = %v, expected to contain %v", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateSharedStorageClass() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSBDConfigSpec_ValidateAll_WithSharedStorage(t *testing.T) {
	tests := []struct {
		name     string
		spec     SBDConfigSpec
		wantErr  bool
		errorMsg string
	}{
		{
			name: "valid shared storage configuration",
			spec: SBDConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			wantErr: false,
		},
		{
			name: "invalid storage class name",
			spec: SBDConfigSpec{
				SharedStorageClass: "-invalid-sc",
			},
			wantErr:  true,
			errorMsg: "shared storage PVC validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateAll()
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateAll() expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("ValidateAll() error = %v, expected to contain %v", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateAll() unexpected error = %v", err)
				}
			}
		})
	}
}
