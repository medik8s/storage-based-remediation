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

	"github.com/medik8s/storage-based-remediation/pkg/agent"
)

// testCase represents a generic test case for validation functions
type testCase struct {
	name      string
	spec      StorageBasedRemediationConfigSpec
	wantError bool
}

// runValidationTests is a generic helper for testing validation methods
func runValidationTests(t *testing.T, testName string, tests []testCase, validateFunc func(StorageBasedRemediationConfigSpec) error) {
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
	spec    StorageBasedRemediationConfigSpec
	wantErr bool
}

// runIntervalTests is a generic helper for testing interval validation methods
func runIntervalTests(t *testing.T, testName string, tests []testCaseInterval, validateFunc func(StorageBasedRemediationConfigSpec) error) {
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
	fieldSetter func(*StorageBasedRemediationConfigSpec, *metav1.Duration),
	validValue time.Duration,
	tooSmallValue time.Duration,
	tooLargeValue time.Duration,
	minValue time.Duration,
	maxValue time.Duration,
) []testCase {
	return []testCase{
		{
			name:      "default timeout is valid",
			spec:      StorageBasedRemediationConfigSpec{},
			wantError: false,
		},
		{
			name: "valid custom timeout",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: validValue})
				return spec
			}(),
			wantError: false,
		},
		{
			name: "timeout too small",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooSmallValue})
				return spec
			}(),
			wantError: true,
		},
		{
			name: "timeout too large",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooLargeValue})
				return spec
			}(),
			wantError: true,
		},
		{
			name: "minimum timeout is valid",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: minValue})
				return spec
			}(),
			wantError: false,
		},
		{
			name: "maximum timeout is valid",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: maxValue})
				return spec
			}(),
			wantError: false,
		},
	}
}

// createIntervalValidationTests creates standard test cases for interval validation
func createIntervalValidationTests(
	fieldSetter func(*StorageBasedRemediationConfigSpec, *metav1.Duration),
	validValue time.Duration,
	tooSmallValue time.Duration,
	tooLargeValue time.Duration,
) []testCaseInterval {
	return []testCaseInterval{
		{
			name:    "nil interval uses default (valid)",
			spec:    StorageBasedRemediationConfigSpec{},
			wantErr: false,
		},
		{
			name: "valid interval",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: validValue})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "minimum interval",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: 1 * time.Second})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "maximum interval",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: 60 * time.Second})
				return spec
			}(),
			wantErr: false,
		},
		{
			name: "too small interval",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooSmallValue})
				return spec
			}(),
			wantErr: true,
		},
		{
			name: "too large interval",
			spec: func() StorageBasedRemediationConfigSpec {
				spec := StorageBasedRemediationConfigSpec{}
				fieldSetter(&spec, &metav1.Duration{Duration: tooLargeValue})
				return spec
			}(),
			wantErr: true,
		},
	}
}

func TestStorageBasedRemediationConfigSpec_GetStaleNodeTimeout(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected time.Duration
	}{
		{
			name: "nil timeout returns default",
			spec: StorageBasedRemediationConfigSpec{
				StaleNodeTimeout: nil,
			},
			expected: DefaultStaleNodeTimeout,
		},
		{
			name: "explicit timeout is returned",
			spec: StorageBasedRemediationConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 5 * time.Minute},
			},
			expected: 5 * time.Minute,
		},
		{
			name: "zero timeout returns zero",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_ValidateStaleNodeTimeout(t *testing.T) {
	tests := createTimeoutValidationTests(
		func(spec *StorageBasedRemediationConfigSpec, d *metav1.Duration) { spec.StaleNodeTimeout = d },
		5*time.Minute,
		30*time.Second,
		25*time.Hour,
		MinStaleNodeTimeout,
		MaxStaleNodeTimeout,
	)

	runValidationTests(t, "ValidateStaleNodeTimeout()", tests, func(spec StorageBasedRemediationConfigSpec) error {
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

func TestStorageBasedRemediationConfigSpec_GetWatchdogPath(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected string
	}{
		{
			name: "empty path returns default",
			spec: StorageBasedRemediationConfigSpec{
				WatchdogPath: "",
			},
			expected: DefaultWatchdogPath,
		},
		{
			name: "explicit path is returned",
			spec: StorageBasedRemediationConfigSpec{
				WatchdogPath: "/dev/watchdog1",
			},
			expected: "/dev/watchdog1",
		},
		{
			name: "custom path is returned",
			spec: StorageBasedRemediationConfigSpec{
				WatchdogPath: "/custom/watchdog",
			},
			expected: "/custom/watchdog",
		},
		{
			name: "default path when unset",
			spec: StorageBasedRemediationConfigSpec{
				// WatchdogPath not set
			},
			expected: DefaultWatchdogPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetWatchdogPath()
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_GetRebootMethod(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected string
	}{
		{
			name: "empty reboot method returns default",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "",
			},
			expected: DefaultRebootMethod,
		},
		{
			name: "explicit panic method is returned",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "panic",
			},
			expected: "panic",
		},
		{
			name: "explicit systemctl-reboot method is returned",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_GetSBRTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected int32
	}{
		{
			name: "nil timeout returns default",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: nil,
			},
			expected: DefaultSBRTimeoutSeconds,
		},
		{
			name: "explicit timeout is returned",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(60),
			},
			expected: 60,
		},
		{
			name: "minimum timeout is returned",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(10),
			},
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSBRTimeoutSeconds()
			if result != tt.expected {
				t.Errorf("GetSBRTimeoutSeconds() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_GetSBRUpdateInterval(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected time.Duration
	}{
		{
			name: "nil interval returns default",
			spec: StorageBasedRemediationConfigSpec{
				SBRUpdateInterval: nil,
			},
			expected: DefaultSBRUpdateInterval,
		},
		{
			name: "explicit interval is returned",
			spec: StorageBasedRemediationConfigSpec{
				SBRUpdateInterval: &metav1.Duration{Duration: 10 * time.Second},
			},
			expected: 10 * time.Second,
		},
		{
			name: "minimum interval is returned",
			spec: StorageBasedRemediationConfigSpec{
				SBRUpdateInterval: &metav1.Duration{Duration: 1 * time.Second},
			},
			expected: 1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSBRUpdateInterval()
			if result != tt.expected {
				t.Errorf("GetSBRUpdateInterval() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_GetPeerCheckInterval(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected time.Duration
	}{
		{
			name: "nil interval returns default",
			spec: StorageBasedRemediationConfigSpec{
				PeerCheckInterval: nil,
			},
			expected: DefaultPeerCheckInterval,
		},
		{
			name: "explicit interval is returned",
			spec: StorageBasedRemediationConfigSpec{
				PeerCheckInterval: &metav1.Duration{Duration: 3 * time.Second},
			},
			expected: 3 * time.Second,
		},
		{
			name: "maximum interval is returned",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_ValidateRebootMethod(t *testing.T) {
	tests := []struct {
		name    string
		spec    StorageBasedRemediationConfigSpec
		wantErr bool
	}{
		{
			name: "valid panic method",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "panic",
			},
			wantErr: false,
		},
		{
			name: "valid systemctl-reboot method",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "systemctl-reboot",
			},
			wantErr: false,
		},
		{
			name: "valid none method",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "none",
			},
			wantErr: false,
		},
		{
			name: "empty method uses default (valid)",
			spec: StorageBasedRemediationConfigSpec{
				RebootMethod: "",
			},
			wantErr: false,
		},
		{
			name: "invalid method",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_ValidateSBRTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name    string
		spec    StorageBasedRemediationConfigSpec
		wantErr bool
	}{
		{
			name: "nil timeout uses default (valid)",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: nil,
			},
			wantErr: false,
		},
		{
			name: "valid timeout",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(60),
			},
			wantErr: false,
		},
		{
			name: "minimum timeout",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(10),
			},
			wantErr: false,
		},
		{
			name: "maximum timeout",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(300),
			},
			wantErr: false,
		},
		{
			name: "too small timeout",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(5),
			},
			wantErr: true,
		},
		{
			name: "too large timeout",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(400),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateSBRTimeoutSeconds()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSBRTimeoutSeconds() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_ValidateSBRUpdateInterval(t *testing.T) {
	tests := createIntervalValidationTests(
		func(spec *StorageBasedRemediationConfigSpec, d *metav1.Duration) { spec.SBRUpdateInterval = d },
		10*time.Second,
		500*time.Millisecond,
		120*time.Second,
	)

	runIntervalTests(t, "ValidateSBRUpdateInterval()", tests, func(spec StorageBasedRemediationConfigSpec) error {
		return spec.ValidateSBRUpdateInterval()
	})
}

func TestStorageBasedRemediationConfigSpec_ValidatePeerCheckInterval(t *testing.T) {
	tests := createIntervalValidationTests(
		func(spec *StorageBasedRemediationConfigSpec, d *metav1.Duration) { spec.PeerCheckInterval = d },
		5*time.Second,
		500*time.Millisecond,
		90*time.Second,
	)

	runIntervalTests(t, "ValidatePeerCheckInterval()", tests, func(spec StorageBasedRemediationConfigSpec) error {
		return spec.ValidatePeerCheckInterval()
	})
}

func TestStorageBasedRemediationConfigSpec_ValidateAll(t *testing.T) {
	tests := []struct {
		name      string
		spec      StorageBasedRemediationConfigSpec
		wantError bool
	}{
		{
			name:      "all defaults are valid",
			spec:      StorageBasedRemediationConfigSpec{},
			wantError: false,
		},
		{
			name: "all valid custom values",
			spec: StorageBasedRemediationConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 2 * time.Hour},
			},
			wantError: false,
		},
		{
			name: "invalid stale node timeout",
			spec: StorageBasedRemediationConfigSpec{
				StaleNodeTimeout: &metav1.Duration{Duration: 30 * time.Second}, // Too small
			},
			wantError: true,
		},
		{
			name: "invalid I/O timeout - too small",
			spec: StorageBasedRemediationConfigSpec{
				IOTimeout: &metav1.Duration{Duration: 50 * time.Millisecond}, // Too small
			},
			wantError: true,
		},
		{
			name: "invalid I/O timeout - too large",
			spec: StorageBasedRemediationConfigSpec{
				IOTimeout: &metav1.Duration{Duration: 10 * time.Minute}, // Too large
			},
			wantError: true,
		},
		{
			name: "valid I/O timeout",
			spec: StorageBasedRemediationConfigSpec{
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
	// Verify PetIntervalMultiple constant in agent package is defined
	// (specific value is an implementation detail)
	if agent.PetIntervalMultiple < 1 || agent.PetIntervalMultiple > 100 {
		t.Errorf("agent.PetIntervalMultiple = %v is out of reasonable range", agent.PetIntervalMultiple)
	}
}

func TestGetImagePullPolicy(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected string
	}{
		{
			name:     "default value",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: "IfNotPresent",
		},
		{
			name: "explicit Always",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "Always",
			},
			expected: "Always",
		},
		{
			name: "explicit Never",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "Never",
			},
			expected: "Never",
		},
		{
			name: "explicit IfNotPresent",
			spec: StorageBasedRemediationConfigSpec{
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
		spec      StorageBasedRemediationConfigSpec
		wantError bool
		errorMsg  string
	}{
		{
			name:      "default value (valid)",
			spec:      StorageBasedRemediationConfigSpec{},
			wantError: false,
		},
		{
			name: "Always (valid)",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "Always",
			},
			wantError: false,
		},
		{
			name: "Never (valid)",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "Never",
			},
			wantError: false,
		},
		{
			name: "IfNotPresent (valid)",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "IfNotPresent",
			},
			wantError: false,
		},
		{
			name: "invalid value",
			spec: StorageBasedRemediationConfigSpec{
				ImagePullPolicy: "InvalidPolicy",
			},
			wantError: true,
			errorMsg:  "invalid image pull policy \"InvalidPolicy\"",
		},
		{
			name: "empty string (should use default)",
			spec: StorageBasedRemediationConfigSpec{
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
		spec          StorageBasedRemediationConfigSpec
		operatorImage string
		expected      string
		wantErr       bool
	}{
		{
			name:          "explicit image specified",
			spec:          StorageBasedRemediationConfigSpec{Image: "custom-registry.com/custom-org/custom-agent:v1.0.0"},
			operatorImage: "quay.io/medik8s/sbr-operator:v1.2.3",
			expected:      "custom-registry.com/custom-org/custom-agent:v1.0.0",
		},
		{
			name:          "no image specified - derive from operator image with tag",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "quay.io/medik8s/sbr-operator:v1.2.3",
			expected:      "quay.io/medik8s/sbr-agent:v1.2.3",
		},
		{
			name:          "no image specified - derive from operator image without tag",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "quay.io/medik8s/sbr-operator",
			expected:      "quay.io/medik8s/sbr-agent:latest",
		},
		{
			name:          "no image specified - simple operator image with tag",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "sbr-operator:v1.0.0",
			expected:      "sbr-agent:v1.0.0",
		},
		{
			name:          "no image specified - simple operator image without tag",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "sbr-operator",
			expected:      "sbr-agent:latest",
		},
		{
			name:          "no image specified - empty operator image",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "",
			wantErr:       true,
		},
		{
			name:          "no image specified - complex registry path",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "registry.example.com:5000/my-org/my-project/sbr-operator:dev-123",
			expected:      "registry.example.com:5000/my-org/my-project/sbr-agent:dev-123",
		},
		{
			name:          "no image specified - storage-based-remediation operator image (RH naming)",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "registry.redhat.io/workload-availability/storage-based-remediation-rhel9-operator:v0.1.0",
			expected:      "registry.redhat.io/workload-availability/storage-based-remediation-agent-rhel9:v0.1.0",
		},
		{
			name:          "no image specified - already agent image (e.g. controller fallback)",
			spec:          StorageBasedRemediationConfigSpec{},
			operatorImage: "sbr-agent:latest",
			expected:      "sbr-agent:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.spec.GetImageWithOperatorImage(tt.operatorImage)
			if tt.wantErr {
				if err == nil {
					t.Errorf("GetImageWithOperatorImage() expected error for operator image %q", tt.operatorImage)
				}
				return
			}
			if err != nil {
				t.Errorf("GetImageWithOperatorImage() unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("GetImageWithOperatorImage() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_GetSharedStoragePVCName(t *testing.T) {
	tests := []struct {
		name          string
		spec          StorageBasedRemediationConfigSpec
		sbrConfigName string
		expected      string
	}{
		{
			name:          "no storage class configured",
			spec:          StorageBasedRemediationConfigSpec{},
			sbrConfigName: "test-config",
			expected:      "",
		},
		{
			name: "storage class configured",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			sbrConfigName: "my-sbr-config",
			expected:      "my-sbr-config-shared-storage",
		},
		{
			name: "complex storage class name",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "nfs-client",
			},
			sbrConfigName: "sbr-config-with-shared-storage",
			expected:      "sbr-config-with-shared-storage-shared-storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSharedStoragePVCName(tt.sbrConfigName)
			if result != tt.expected {
				t.Errorf("GetSharedStoragePVCName() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_GetSharedStorageStorageClass(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected string
	}{
		{
			name:     "no storage class configured",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: "",
		},
		{
			name: "storage class configured",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			expected: "efs-sc",
		},
		{
			name: "complex storage class name",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_GetSharedStorageMountPath(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected string
	}{
		{
			name:     "returns fixed path",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: agent.SharedStorageSBRDeviceDirectory,
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

func TestStorageBasedRemediationConfigSpec_HasSharedStorage(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected bool
	}{
		{
			name:     "no shared storage configured",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: false,
		},
		{
			name: "shared storage class configured",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			expected: true,
		},
		{
			name: "empty storage class name means no shared storage",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "",
			},
			expected: false,
		},
		{
			name: "only storage class enables shared storage",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_GetNodeSelector(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected map[string]string
	}{
		{
			name: "default node selector - worker nodes only",
			spec: StorageBasedRemediationConfigSpec{},
			expected: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
		{
			name: "explicit node selector",
			spec: StorageBasedRemediationConfigSpec{
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
			spec: StorageBasedRemediationConfigSpec{
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
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_ValidateSharedStorageClass(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		wantErr  bool
		errorMsg string
	}{
		{
			name:    "no storage class configured - valid",
			spec:    StorageBasedRemediationConfigSpec{},
			wantErr: false,
		},
		{
			name: "valid storage class name",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			wantErr: false,
		},
		{
			name: "valid complex storage class name",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "nfs-client",
			},
			wantErr: false,
		},
		{
			name: "invalid storage class name - starts with hyphen",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "-invalid-sc",
			},
			wantErr:  true,
			errorMsg: "must start with alphanumeric character",
		},
		{
			name: "invalid storage class name - ends with hyphen",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "invalid-sc-",
			},
			wantErr:  true,
			errorMsg: "must end with alphanumeric character",
		},
		{
			name: "invalid storage class name - too long",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: strings.Repeat("a", 254),
			},
			wantErr:  true,
			errorMsg: "must be no more than 253 characters",
		},
		{
			name: "valid storage class name - max length",
			spec: StorageBasedRemediationConfigSpec{
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

func TestStorageBasedRemediationConfigSpec_ValidateAll_WithSharedStorage(t *testing.T) {
	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		wantErr  bool
		errorMsg string
	}{
		{
			name: "valid shared storage configuration",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageClass: "efs-sc",
			},
			wantErr: false,
		},
		{
			name: "invalid storage class name",
			spec: StorageBasedRemediationConfigSpec{
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
