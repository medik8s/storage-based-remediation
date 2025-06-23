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
	if DefaultStaleNodeTimeout != 10*time.Minute {
		t.Errorf("DefaultStaleNodeTimeout = %v, expected 10m", DefaultStaleNodeTimeout)
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
