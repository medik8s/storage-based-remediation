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
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
)

const (
	testStorageClass    = "test-sc"
	ioTimeoutField      = "ioTimeout"
	outageGraceField    = "totalOutageGracePeriod"
	recoveryPeriodField = "autonomousRebootRecoveryPeriod"
)

func TestStorageModes(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock
	perNode := SharedStorageVolumeModeBlockPerNode
	unknown := SharedStorageVolumeModeType("unknown")
	for _, mode := range []*SharedStorageVolumeModeType{nil, &filesystem, &block, &perNode, &unknown} {
		for _, class := range []string{"", testStorageClass} {
			spec := StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: mode, SharedStorageClass: class}
			effective := filesystem
			if mode != nil {
				effective = *mode
			}
			t.Run(string(effective)+"/"+class, func(t *testing.T) {
				if spec.GetSharedStorageVolumeMode() != effective ||
					spec.IsBlockMode() != (effective == block) || spec.IsBlockPerNodeMode() != (effective == perNode) {
					t.Fatalf("incorrect helpers for mode %v", mode)
				}
				wantErr := effective == unknown || (class == "" && (effective == block || effective == perNode))
				if err := spec.ValidateAll(); (err != nil) != wantErr {
					t.Errorf("ValidateAll() = %v, want error %v", err, wantErr)
				}
			})
		}
	}
}

func TestDurationGetters(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock
	perNode := SharedStorageVolumeModeBlockPerNode
	for _, mode := range []*SharedStorageVolumeModeType{nil, &filesystem, &block, &perNode} {
		for _, explicit := range []*metav1.Duration{nil, {Duration: 0}, {Duration: 23 * time.Second}} {
			spec := StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: mode, IOTimeout: explicit,
				TotalOutageGracePeriod: explicit, AutonomousRebootRecoveryPeriod: explicit,
			}
			before := spec.DeepCopy()
			wantIO, wantOutage, wantRecovery := 2*time.Second, time.Duration(0), time.Duration(0)
			if mode != nil && *mode == perNode {
				wantIO, wantOutage, wantRecovery = 15*time.Second, 60*time.Second, 5*time.Minute
			}
			if explicit != nil {
				wantIO, wantOutage, wantRecovery = explicit.Duration, explicit.Duration, explicit.Duration
			}
			if spec.GetIOTimeout() != wantIO || spec.GetTotalOutageGracePeriod() != wantOutage ||
				spec.GetAutonomousRebootRecoveryPeriod() != wantRecovery {
				t.Errorf("incorrect durations for mode %v, explicit %v", mode, explicit)
			}
			if !reflect.DeepEqual(&spec, before) || spec.IOTimeout != explicit ||
				spec.TotalOutageGracePeriod != explicit || spec.AutonomousRebootRecoveryPeriod != explicit {
				t.Error("getters changed the receiver")
			}
		}
	}
}

func TestDurationValidation(t *testing.T) {
	perNode := SharedStorageVolumeModeBlockPerNode
	for _, tc := range []struct {
		field  string
		values []time.Duration
		valid  func(time.Duration) bool
	}{
		{
			ioTimeoutField,
			[]time.Duration{-time.Nanosecond, 0, time.Second - time.Nanosecond,
				time.Second, 10 * time.Minute, 10*time.Minute + time.Nanosecond},
			func(d time.Duration) bool { return d >= time.Second && d <= 10*time.Minute },
		},
		{
			outageGraceField,
			[]time.Duration{-time.Nanosecond, 0, time.Nanosecond, time.Hour, time.Hour + time.Nanosecond},
			func(d time.Duration) bool { return d >= 0 && d <= time.Hour },
		},
		{
			recoveryPeriodField,
			[]time.Duration{-time.Nanosecond, 0, time.Nanosecond, time.Hour},
			func(d time.Duration) bool { return d > 0 },
		},
	} {
		for _, value := range tc.values {
			t.Run(tc.field+"/"+value.String(), func(t *testing.T) {
				spec := StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &perNode, SharedStorageClass: testStorageClass}
				setDurationField(&spec, tc.field, &metav1.Duration{Duration: value})
				err := spec.ValidateAll()
				if (err == nil) != tc.valid(value) || (err != nil && !strings.Contains(err.Error(), tc.field)) {
					t.Errorf("ValidateAll() = %v", err)
				}
			})
		}
	}
	filesystem, block := SharedStorageVolumeModeFilesystem, SharedStorageVolumeModeBlock
	for _, mode := range []*SharedStorageVolumeModeType{nil, &filesystem, &block} {
		for _, field := range []string{outageGraceField, recoveryPeriodField} {
			spec := StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: mode, SharedStorageClass: testStorageClass}
			setDurationField(&spec, field, &metav1.Duration{Duration: time.Minute})
			if err := spec.ValidateAll(); err == nil || !strings.Contains(err.Error(), field) {
				t.Errorf("expected applicability error for %s, got %v", field, err)
			}
		}
	}
}

func setDurationField(spec *StorageBasedRemediationConfigSpec, field string, value *metav1.Duration) {
	switch field {
	case ioTimeoutField:
		spec.IOTimeout = value
	case outageGraceField:
		spec.TotalOutageGracePeriod = value
	case recoveryPeriodField:
		spec.AutonomousRebootRecoveryPeriod = value
	}
}

func TestConfigDurationJSONAndDeepCopy(t *testing.T) {
	for _, field := range []string{ioTimeoutField, outageGraceField, recoveryPeriodField} {
		var spec StorageBasedRemediationConfigSpec
		if err := json.Unmarshal([]byte(`{"`+field+`":"invalid"}`), &spec); err == nil {
			t.Errorf("decoded malformed %s", field)
		}
		setDurationField(&spec, field, &metav1.Duration{})
		data, err := json.Marshal(spec)
		if err != nil || !strings.Contains(string(data), `"`+field+`":"0s"`) {
			t.Fatalf("explicit zero lost for %s: %s, %v", field, data, err)
		}
		var decoded StorageBasedRemediationConfigSpec
		if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(spec, decoded) {
			t.Fatalf("round trip failed: %v", err)
		}
	}
	data, err := json.Marshal(StorageBasedRemediationConfigSpec{})
	if err != nil || string(data) != "{}" {
		t.Fatalf("omitted fields changed: %s, %v", data, err)
	}
	original := &StorageBasedRemediationConfigSpec{
		IOTimeout:                      &metav1.Duration{Duration: time.Second},
		TotalOutageGracePeriod:         &metav1.Duration{Duration: time.Minute},
		AutonomousRebootRecoveryPeriod: &metav1.Duration{Duration: 5 * time.Minute},
	}
	copy := original.DeepCopy()
	copy.IOTimeout.Duration++
	copy.TotalOutageGracePeriod.Duration++
	copy.AutonomousRebootRecoveryPeriod.Duration++
	if original.IOTimeout.Duration != time.Second || original.TotalOutageGracePeriod.Duration != time.Minute ||
		original.AutonomousRebootRecoveryPeriod.Duration != 5*time.Minute {
		t.Fatal("deepcopy shares duration pointers")
	}
}

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

func TestStorageBasedRemediationConfigSpec_GetDerivedTimingIntervals(t *testing.T) {
	tests := []struct {
		name              string
		spec              StorageBasedRemediationConfigSpec
		wantHeartbeat     time.Duration
		wantUpdateAndPeer time.Duration
	}{
		{
			name:              "default timeout yields 15s heartbeat and 5s update/peer intervals",
			spec:              StorageBasedRemediationConfigSpec{},
			wantHeartbeat:     15 * time.Second,
			wantUpdateAndPeer: 5 * time.Second,
		},
		{
			name: "60s timeout yields 30s heartbeat and 10s update/peer intervals",
			spec: StorageBasedRemediationConfigSpec{
				SBRTimeoutSeconds: func(v int32) *int32 { return &v }(60),
			},
			wantHeartbeat:     30 * time.Second,
			wantUpdateAndPeer: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.GetHeartbeatInterval(); got != tt.wantHeartbeat {
				t.Errorf("GetHeartbeatInterval() = %v, expected %v", got, tt.wantHeartbeat)
			}
			if got := tt.spec.GetSBRUpdateInterval(); got != tt.wantUpdateAndPeer {
				t.Errorf("GetSBRUpdateInterval() = %v, expected %v", got, tt.wantUpdateAndPeer)
			}
			if got := tt.spec.GetPeerCheckInterval(); got != tt.wantUpdateAndPeer {
				t.Errorf("GetPeerCheckInterval() = %v, expected %v", got, tt.wantUpdateAndPeer)
			}
		})
	}
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

func TestStorageBasedRemediationConfigSpec_GetSharedStorageVolumeMode(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock

	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected SharedStorageVolumeModeType
	}{
		{
			name:     "nil defaults to Filesystem",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: SharedStorageVolumeModeFilesystem,
		},
		{
			name:     "explicit Filesystem",
			spec:     StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &filesystem},
			expected: SharedStorageVolumeModeFilesystem,
		},
		{
			name:     "explicit Block",
			spec:     StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &block},
			expected: SharedStorageVolumeModeBlock,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.GetSharedStorageVolumeMode()
			if result != tt.expected {
				t.Errorf("GetSharedStorageVolumeMode() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_IsBlockMode(t *testing.T) {
	filesystem := SharedStorageVolumeModeFilesystem
	block := SharedStorageVolumeModeBlock

	tests := []struct {
		name     string
		spec     StorageBasedRemediationConfigSpec
		expected bool
	}{
		{
			name:     "nil is not block mode",
			spec:     StorageBasedRemediationConfigSpec{},
			expected: false,
		},
		{
			name:     "Filesystem is not block mode",
			spec:     StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &filesystem},
			expected: false,
		},
		{
			name:     "Block is block mode",
			spec:     StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &block},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.spec.IsBlockMode()
			if result != tt.expected {
				t.Errorf("IsBlockMode() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestStorageBasedRemediationConfigSpec_ValidateAll_BlockRequiresStorageClass(t *testing.T) {
	block := SharedStorageVolumeModeBlock
	filesystem := SharedStorageVolumeModeFilesystem

	tests := []struct {
		name    string
		spec    StorageBasedRemediationConfigSpec
		wantErr bool
	}{
		{
			name:    "nil volume mode is valid",
			spec:    StorageBasedRemediationConfigSpec{},
			wantErr: false,
		},
		{
			name:    "Filesystem without storage class is valid",
			spec:    StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &filesystem},
			wantErr: false,
		},
		{
			name: "Block with storage class is valid",
			spec: StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: &block,
				SharedStorageClass:      "ocs-storagecluster-ceph-rbd",
			},
			wantErr: false,
		},
		{
			name:    "Block without storage class is invalid",
			spec:    StorageBasedRemediationConfigSpec{SharedStorageVolumeMode: &block},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.ValidateAll()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAll() error = %v, wantErr %v", err, tt.wantErr)
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
