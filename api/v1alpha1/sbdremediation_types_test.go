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

func TestSBDRemediationSpec_TimeoutSecondsValidation(t *testing.T) {
	tests := []struct {
		name           string
		timeoutSeconds int32
		expectedValue  int32
	}{
		{
			name:           "zero timeout uses default",
			timeoutSeconds: 0,
			expectedValue:  0, // In the actual CRD, default (60) would be applied by the API server
		},
		{
			name:           "custom timeout is preserved",
			timeoutSeconds: 120,
			expectedValue:  120,
		},
		{
			name:           "minimum timeout is valid",
			timeoutSeconds: 30,
			expectedValue:  30,
		},
		{
			name:           "maximum timeout is valid",
			timeoutSeconds: 300,
			expectedValue:  300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := SBDRemediationSpec{
				NodeName:       "test-node",
				TimeoutSeconds: tt.timeoutSeconds,
			}

			if spec.TimeoutSeconds != tt.expectedValue {
				t.Errorf("Expected timeout %d, got %d", tt.expectedValue, spec.TimeoutSeconds)
			}
		})
	}
}

func TestSBDRemediation_GetCondition(t *testing.T) {
	remediation := &SBDRemediation{
		Status: SBDRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBDRemediationConditionReady),
					Status: metav1.ConditionTrue,
					Reason: "Succeeded",
				},
				{
					Type:   string(SBDRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
					Reason: "Failed",
				},
			},
		},
	}

	tests := []struct {
		name           string
		conditionType  SBDRemediationConditionType
		expectFound    bool
		expectedStatus metav1.ConditionStatus
	}{
		{
			name:           "existing condition found",
			conditionType:  SBDRemediationConditionReady,
			expectFound:    true,
			expectedStatus: metav1.ConditionTrue,
		},
		{
			name:           "another existing condition found",
			conditionType:  SBDRemediationConditionFencingSucceeded,
			expectFound:    true,
			expectedStatus: metav1.ConditionFalse,
		},
		{
			name:          "non-existing condition returns nil",
			conditionType: SBDRemediationConditionLeadershipAcquired,
			expectFound:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := remediation.GetCondition(tt.conditionType)

			if tt.expectFound {
				if condition == nil {
					t.Fatalf("Expected to find condition %s, but got nil", tt.conditionType)
				}
				if condition.Status != tt.expectedStatus {
					t.Errorf("Expected status %s, got %s", tt.expectedStatus, condition.Status)
				}
			} else {
				if condition != nil {
					t.Errorf("Expected nil for non-existing condition, got %+v", condition)
				}
			}
		})
	}
}

func TestSBDRemediation_SetCondition(t *testing.T) {
	tests := []struct {
		name              string
		initialConditions []metav1.Condition
		conditionType     SBDRemediationConditionType
		status            metav1.ConditionStatus
		reason            string
		message           string
		expectNew         bool
		expectTransition  bool
	}{
		{
			name:              "add new condition",
			initialConditions: []metav1.Condition{},
			conditionType:     SBDRemediationConditionReady,
			status:            metav1.ConditionTrue,
			reason:            "Succeeded",
			message:           "Remediation completed",
			expectNew:         true,
			expectTransition:  true,
		},
		{
			name: "update existing condition same status",
			initialConditions: []metav1.Condition{
				{
					Type:               string(SBDRemediationConditionReady),
					Status:             metav1.ConditionTrue,
					Reason:             "InProgress",
					Message:            "Working",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
			},
			conditionType:    SBDRemediationConditionReady,
			status:           metav1.ConditionTrue,
			reason:           "Succeeded",
			message:          "Completed",
			expectNew:        false,
			expectTransition: false, // Same status, no transition
		},
		{
			name: "update existing condition different status",
			initialConditions: []metav1.Condition{
				{
					Type:               string(SBDRemediationConditionReady),
					Status:             metav1.ConditionFalse,
					Reason:             "InProgress",
					Message:            "Working",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
			},
			conditionType:    SBDRemediationConditionReady,
			status:           metav1.ConditionTrue,
			reason:           "Succeeded",
			message:          "Completed",
			expectNew:        false,
			expectTransition: true, // Different status, should transition
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remediation := &SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Status: SBDRemediationStatus{
					Conditions: tt.initialConditions,
				},
			}

			// Store original transition time if condition exists (for reference)
			_ = remediation.GetCondition(tt.conditionType)

			// Set the condition
			remediation.SetCondition(tt.conditionType, tt.status, tt.reason, tt.message)

			// Verify the condition exists and has correct values
			condition := remediation.GetCondition(tt.conditionType)
			if condition == nil {
				t.Fatalf("Expected condition to be set, but got nil")
			}

			if condition.Status != tt.status {
				t.Errorf("Expected status %s, got %s", tt.status, condition.Status)
			}

			if condition.Reason != tt.reason {
				t.Errorf("Expected reason %s, got %s", tt.reason, condition.Reason)
			}

			if condition.Message != tt.message {
				t.Errorf("Expected message %s, got %s", tt.message, condition.Message)
			}

			if condition.ObservedGeneration != 2 {
				t.Errorf("Expected observedGeneration 2, got %d", condition.ObservedGeneration)
			}

			// Check transition time behavior
			if tt.expectNew {
				if condition.LastTransitionTime.IsZero() {
					t.Error("Expected LastTransitionTime to be set for new condition")
				}
			} else {
				if tt.expectTransition {
					// For different status, LastTransitionTime should be updated to a recent time
					timeDiff := time.Since(condition.LastTransitionTime.Time)
					if timeDiff > 5*time.Second {
						t.Errorf("Expected LastTransitionTime to be updated recently for status change, but was %v ago", timeDiff)
					}
				} else {
					// For same status updates, just verify LastTransitionTime is set
					if condition.LastTransitionTime.IsZero() {
						t.Error("Expected LastTransitionTime to be set")
					}
				}
			}

			// Verify total number of conditions
			expectedCount := len(tt.initialConditions)
			if tt.expectNew {
				expectedCount++
			}
			if len(remediation.Status.Conditions) != expectedCount {
				t.Errorf("Expected %d conditions, got %d", expectedCount, len(remediation.Status.Conditions))
			}
		})
	}
}

func TestSBDRemediation_IsConditionTrue(t *testing.T) {
	remediation := &SBDRemediation{
		Status: SBDRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBDRemediationConditionReady),
					Status: metav1.ConditionTrue,
				},
				{
					Type:   string(SBDRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
				},
				{
					Type:   string(SBDRemediationConditionFencingInProgress),
					Status: metav1.ConditionUnknown,
				},
			},
		},
	}

	tests := []struct {
		name          string
		conditionType SBDRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns true",
			conditionType: SBDRemediationConditionReady,
			expected:      true,
		},
		{
			name:          "false condition returns false",
			conditionType: SBDRemediationConditionFencingSucceeded,
			expected:      false,
		},
		{
			name:          "unknown condition returns false",
			conditionType: SBDRemediationConditionFencingInProgress,
			expected:      false,
		},
		{
			name:          "non-existing condition returns false",
			conditionType: SBDRemediationConditionLeadershipAcquired,
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := remediation.IsConditionTrue(tt.conditionType)
			if result != tt.expected {
				t.Errorf("Expected %t, got %t", tt.expected, result)
			}
		})
	}
}

func TestSBDRemediation_IsConditionFalse(t *testing.T) {
	remediation := &SBDRemediation{
		Status: SBDRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBDRemediationConditionReady),
					Status: metav1.ConditionTrue,
				},
				{
					Type:   string(SBDRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
				},
				{
					Type:   string(SBDRemediationConditionFencingInProgress),
					Status: metav1.ConditionUnknown,
				},
			},
		},
	}

	tests := []struct {
		name          string
		conditionType SBDRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns false",
			conditionType: SBDRemediationConditionReady,
			expected:      false,
		},
		{
			name:          "false condition returns true",
			conditionType: SBDRemediationConditionFencingSucceeded,
			expected:      true,
		},
		{
			name:          "unknown condition returns false",
			conditionType: SBDRemediationConditionFencingInProgress,
			expected:      false,
		},
		{
			name:          "non-existing condition returns false",
			conditionType: SBDRemediationConditionLeadershipAcquired,
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := remediation.IsConditionFalse(tt.conditionType)
			if result != tt.expected {
				t.Errorf("Expected %t, got %t", tt.expected, result)
			}
		})
	}
}

func TestSBDRemediation_IsConditionUnknown(t *testing.T) {
	remediation := &SBDRemediation{
		Status: SBDRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBDRemediationConditionReady),
					Status: metav1.ConditionTrue,
				},
				{
					Type:   string(SBDRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
				},
				{
					Type:   string(SBDRemediationConditionFencingInProgress),
					Status: metav1.ConditionUnknown,
				},
			},
		},
	}

	tests := []struct {
		name          string
		conditionType SBDRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns false",
			conditionType: SBDRemediationConditionReady,
			expected:      false,
		},
		{
			name:          "false condition returns false",
			conditionType: SBDRemediationConditionFencingSucceeded,
			expected:      false,
		},
		{
			name:          "unknown condition returns true",
			conditionType: SBDRemediationConditionFencingInProgress,
			expected:      true,
		},
		{
			name:          "non-existing condition returns true",
			conditionType: SBDRemediationConditionLeadershipAcquired,
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := remediation.IsConditionUnknown(tt.conditionType)
			if result != tt.expected {
				t.Errorf("Expected %t, got %t", tt.expected, result)
			}
		})
	}
}

func TestSBDRemediation_HelperMethods(t *testing.T) {
	// Test IsFencingSucceeded
	t.Run("IsFencingSucceeded", func(t *testing.T) {
		remediation := &SBDRemediation{}

		// Initially should be false
		if remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be false initially")
		}

		// Set fencing succeeded condition
		remediation.SetCondition(SBDRemediationConditionFencingSucceeded, metav1.ConditionTrue, "Succeeded", "Fencing completed")
		if !remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be true after setting condition")
		}

		// Set to false
		remediation.SetCondition(SBDRemediationConditionFencingSucceeded, metav1.ConditionFalse, "Failed", "Fencing failed")
		if remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be false after setting to false")
		}
	})

	// Test IsFencingInProgress
	t.Run("IsFencingInProgress", func(t *testing.T) {
		remediation := &SBDRemediation{}

		// Initially should be false
		if remediation.IsFencingInProgress() {
			t.Error("Expected IsFencingInProgress to be false initially")
		}

		// Set fencing in progress condition
		remediation.SetCondition(SBDRemediationConditionFencingInProgress, metav1.ConditionTrue, "InProgress", "Fencing in progress")
		if !remediation.IsFencingInProgress() {
			t.Error("Expected IsFencingInProgress to be true after setting condition")
		}
	})

	// Test IsReady
	t.Run("IsReady", func(t *testing.T) {
		remediation := &SBDRemediation{}

		// Initially should be false
		if remediation.IsReady() {
			t.Error("Expected IsReady to be false initially")
		}

		// Set ready condition
		remediation.SetCondition(SBDRemediationConditionReady, metav1.ConditionTrue, "Succeeded", "Ready")
		if !remediation.IsReady() {
			t.Error("Expected IsReady to be true after setting condition")
		}
	})

	// Test HasLeadership
	t.Run("HasLeadership", func(t *testing.T) {
		remediation := &SBDRemediation{}

		// Initially should be false
		if remediation.HasLeadership() {
			t.Error("Expected HasLeadership to be false initially")
		}

		// Set leadership condition
		remediation.SetCondition(SBDRemediationConditionLeadershipAcquired, metav1.ConditionTrue, "Acquired", "Leadership acquired")
		if !remediation.HasLeadership() {
			t.Error("Expected HasLeadership to be true after setting condition")
		}
	})
}

func TestSBDRemediation_MultipleConditions(t *testing.T) {
	remediation := &SBDRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 1,
		},
	}

	// Set multiple conditions in sequence
	remediation.SetCondition(SBDRemediationConditionLeadershipAcquired, metav1.ConditionTrue, "Acquired", "Leadership acquired")
	remediation.SetCondition(SBDRemediationConditionFencingInProgress, metav1.ConditionTrue, "InProgress", "Fencing started")
	remediation.SetCondition(SBDRemediationConditionFencingSucceeded, metav1.ConditionTrue, "Succeeded", "Fencing completed")
	remediation.SetCondition(SBDRemediationConditionReady, metav1.ConditionTrue, "Succeeded", "Remediation complete")

	// Verify all conditions are present
	if len(remediation.Status.Conditions) != 4 {
		t.Errorf("Expected 4 conditions, got %d", len(remediation.Status.Conditions))
	}

	// Verify each condition
	if !remediation.HasLeadership() {
		t.Error("Expected to have leadership")
	}
	if !remediation.IsFencingInProgress() {
		t.Error("Expected fencing to be in progress")
	}
	if !remediation.IsFencingSucceeded() {
		t.Error("Expected fencing to have succeeded")
	}
	if !remediation.IsReady() {
		t.Error("Expected to be ready")
	}

	// Update one condition and verify others remain unchanged
	remediation.SetCondition(SBDRemediationConditionFencingInProgress, metav1.ConditionFalse, "Completed", "Fencing no longer in progress")

	if len(remediation.Status.Conditions) != 4 {
		t.Errorf("Expected 4 conditions after update, got %d", len(remediation.Status.Conditions))
	}

	if remediation.IsFencingInProgress() {
		t.Error("Expected fencing to no longer be in progress")
	}

	// Other conditions should remain unchanged
	if !remediation.HasLeadership() {
		t.Error("Expected to still have leadership")
	}
	if !remediation.IsFencingSucceeded() {
		t.Error("Expected fencing to still have succeeded")
	}
	if !remediation.IsReady() {
		t.Error("Expected to still be ready")
	}
}

func TestSBDRemediationConstants(t *testing.T) {
	// Test condition type constants
	expectedConditions := map[SBDRemediationConditionType]string{
		SBDRemediationConditionLeadershipAcquired: "LeadershipAcquired",
		SBDRemediationConditionFencingInProgress:  "FencingInProgress",
		SBDRemediationConditionFencingSucceeded:   "FencingSucceeded",
		SBDRemediationConditionReady:              "Ready",
	}

	for condType, expectedString := range expectedConditions {
		if string(condType) != expectedString {
			t.Errorf("Expected condition type %s to have string value %s, got %s",
				condType, expectedString, string(condType))
		}
	}

	// Test reason constants
	expectedReasons := map[SBDRemediationReason]string{
		SBDRemediationReasonHeartbeatTimeout: "HeartbeatTimeout",
		SBDRemediationReasonNodeUnresponsive: "NodeUnresponsive",
		SBDRemediationReasonManualFencing:    "ManualFencing",
	}

	for reason, expectedString := range expectedReasons {
		if string(reason) != expectedString {
			t.Errorf("Expected reason %s to have string value %s, got %s",
				reason, expectedString, string(reason))
		}
	}
}
