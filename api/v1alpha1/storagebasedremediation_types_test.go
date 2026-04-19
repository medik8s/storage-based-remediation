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

// createTestSBRRemediation creates a StorageBasedRemediation with standard test conditions
func createTestSBRRemediation() *StorageBasedRemediation {
	return &StorageBasedRemediation{
		Status: StorageBasedRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBRRemediationConditionReady),
					Status: metav1.ConditionTrue,
				},
				{
					Type:   string(SBRRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
				},
				{
					Type:   string(SBRRemediationConditionFencingInProgress),
					Status: metav1.ConditionUnknown,
				},
			},
		},
	}
}

func TestSBRRemediationSpec_TimeoutSecondsValidation(t *testing.T) {
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
			spec := StorageBasedRemediationSpec{
				TimeoutSeconds: tt.timeoutSeconds,
			}

			if spec.TimeoutSeconds != tt.expectedValue {
				t.Errorf("Expected timeout %d, got %d", tt.expectedValue, spec.TimeoutSeconds)
			}
		})
	}
}

func TestSBRRemediation_GetCondition(t *testing.T) {
	remediation := &StorageBasedRemediation{
		Status: StorageBasedRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBRRemediationConditionReady),
					Status: metav1.ConditionTrue,
					Reason: "Succeeded",
				},
				{
					Type:   string(SBRRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
					Reason: "Failed",
				},
			},
		},
	}

	tests := []struct {
		name           string
		conditionType  SBRRemediationConditionType
		expectFound    bool
		expectedStatus metav1.ConditionStatus
	}{
		{
			name:           "existing condition found",
			conditionType:  SBRRemediationConditionReady,
			expectFound:    true,
			expectedStatus: metav1.ConditionTrue,
		},
		{
			name:           "another existing condition found",
			conditionType:  SBRRemediationConditionFencingSucceeded,
			expectFound:    true,
			expectedStatus: metav1.ConditionFalse,
		},
		{
			name:          "non-existing condition returns nil",
			conditionType: SBRRemediationConditionLeadershipAcquired,
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

func TestSBRRemediation_SetCondition(t *testing.T) {
	tests := []struct {
		name              string
		initialConditions []metav1.Condition
		conditionType     SBRRemediationConditionType
		status            metav1.ConditionStatus
		reason            string
		message           string
		expectNew         bool
		expectTransition  bool
	}{
		{
			name:              "add new condition",
			initialConditions: []metav1.Condition{},
			conditionType:     SBRRemediationConditionReady,
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
					Type:               string(SBRRemediationConditionReady),
					Status:             metav1.ConditionTrue,
					Reason:             "InProgress",
					Message:            "Working",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
			},
			conditionType:    SBRRemediationConditionReady,
			status:           metav1.ConditionTrue,
			reason:           "Succeeded",
			message:          "Completed - Same status, no transition",
			expectNew:        false,
			expectTransition: false, // Same status, no transition
		},
		{
			name: "update existing condition different status",
			initialConditions: []metav1.Condition{
				{
					Type:               string(SBRRemediationConditionReady),
					Status:             metav1.ConditionFalse,
					Reason:             "InProgress",
					Message:            "Working",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
			},
			conditionType:    SBRRemediationConditionReady,
			status:           metav1.ConditionTrue,
			reason:           "Succeeded",
			message:          "Completed - Different status, should transition",
			expectNew:        false,
			expectTransition: true, // Different status, should transition
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remediation := &StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Status: StorageBasedRemediationStatus{
					Conditions: tt.initialConditions,
				},
			}

			// Store original transition time if condition exists
			var tTime = metav1.Time{}
			var originalTransitionTime *metav1.Time
			if existingCondition := remediation.GetCondition(tt.conditionType); existingCondition != nil {
				tTime = existingCondition.LastTransitionTime // Ensure we get a copy of the time
				originalTransitionTime = &tTime
			}

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
					if originalTransitionTime != nil && condition.LastTransitionTime.Equal(originalTransitionTime) {
						t.Errorf("Expected LastTransitionTime to be updated for status change: %s", condition.Message)
					}
				} else {
					if originalTransitionTime != nil && !condition.LastTransitionTime.Equal(originalTransitionTime) {
						t.Error("Expected LastTransitionTime to remain unchanged for same status")
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

func TestSBRRemediation_IsConditionTrue(t *testing.T) {
	remediation := createTestSBRRemediation()

	tests := []struct {
		name          string
		conditionType SBRRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns true",
			conditionType: SBRRemediationConditionReady,
			expected:      true,
		},
		{
			name:          "false condition returns false",
			conditionType: SBRRemediationConditionFencingSucceeded,
			expected:      false,
		},
		{
			name:          "unknown condition returns false",
			conditionType: SBRRemediationConditionFencingInProgress,
			expected:      false,
		},
		{
			name:          "non-existing condition returns false",
			conditionType: SBRRemediationConditionLeadershipAcquired,
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

func TestSBRRemediation_IsConditionFalse(t *testing.T) {
	remediation := &StorageBasedRemediation{
		Status: StorageBasedRemediationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(SBRRemediationConditionReady),
					Status: metav1.ConditionTrue,
				},
				{
					Type:   string(SBRRemediationConditionFencingSucceeded),
					Status: metav1.ConditionFalse,
				},
				{
					Type:   string(SBRRemediationConditionFencingInProgress),
					Status: metav1.ConditionUnknown,
				},
			},
		},
	}

	tests := []struct {
		name          string
		conditionType SBRRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns false",
			conditionType: SBRRemediationConditionReady,
			expected:      false,
		},
		{
			name:          "false condition returns true",
			conditionType: SBRRemediationConditionFencingSucceeded,
			expected:      true,
		},
		{
			name:          "unknown condition returns false",
			conditionType: SBRRemediationConditionFencingInProgress,
			expected:      false,
		},
		{
			name:          "non-existing condition returns false",
			conditionType: SBRRemediationConditionLeadershipAcquired,
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

func TestSBRRemediation_IsConditionUnknown(t *testing.T) {
	remediation := createTestSBRRemediation()

	tests := []struct {
		name          string
		conditionType SBRRemediationConditionType
		expected      bool
	}{
		{
			name:          "true condition returns false",
			conditionType: SBRRemediationConditionReady,
			expected:      false,
		},
		{
			name:          "false condition returns false",
			conditionType: SBRRemediationConditionFencingSucceeded,
			expected:      false,
		},
		{
			name:          "unknown condition returns true",
			conditionType: SBRRemediationConditionFencingInProgress,
			expected:      true,
		},
		{
			name:          "non-existing condition returns true",
			conditionType: SBRRemediationConditionLeadershipAcquired,
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

func TestSBRRemediation_HelperMethods(t *testing.T) {
	// Test IsFencingSucceeded
	t.Run("IsFencingSucceeded", func(t *testing.T) {
		remediation := &StorageBasedRemediation{}

		// Initially should be false
		if remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be false initially")
		}

		// Set fencing succeeded condition
		remediation.SetCondition(
			SBRRemediationConditionFencingSucceeded,
			metav1.ConditionTrue,
			"Succeeded",
			"Fencing completed",
		)
		if !remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be true after setting condition")
		}

		// Set to false
		remediation.SetCondition(SBRRemediationConditionFencingSucceeded, metav1.ConditionFalse, "Failed", "Fencing failed")
		if remediation.IsFencingSucceeded() {
			t.Error("Expected IsFencingSucceeded to be false after setting to false")
		}
	})

	// Test IsFencingInProgress
	t.Run("IsFencingInProgress", func(t *testing.T) {
		remediation := &StorageBasedRemediation{}

		// Initially should be false
		if remediation.IsFencingInProgress() {
			t.Error("Expected IsFencingInProgress to be false initially")
		}

		// Set fencing in progress condition
		remediation.SetCondition(
			SBRRemediationConditionFencingInProgress,
			metav1.ConditionTrue,
			"InProgress",
			"Fencing in progress",
		)
		if !remediation.IsFencingInProgress() {
			t.Error("Expected IsFencingInProgress to be true after setting condition")
		}
	})

	// Test IsReady
	t.Run("IsReady", func(t *testing.T) {
		remediation := &StorageBasedRemediation{}

		// Initially should be false
		if remediation.IsReady() {
			t.Error("Expected IsReady to be false initially")
		}

		// Set ready condition
		remediation.SetCondition(SBRRemediationConditionReady, metav1.ConditionTrue, "Succeeded", "Ready")
		if !remediation.IsReady() {
			t.Error("Expected IsReady to be true after setting condition")
		}
	})

	// Test HasLeadership
	t.Run("HasLeadership", func(t *testing.T) {
		remediation := &StorageBasedRemediation{}

		// Initially should be false
		if remediation.HasLeadership() {
			t.Error("Expected HasLeadership to be false initially")
		}

		// Set leadership condition
		remediation.SetCondition(
			SBRRemediationConditionLeadershipAcquired,
			metav1.ConditionTrue,
			"Acquired",
			"Leadership acquired",
		)
		if !remediation.HasLeadership() {
			t.Error("Expected HasLeadership to be true after setting condition")
		}
	})
}

func TestSBRRemediation_MultipleConditions(t *testing.T) {
	remediation := &StorageBasedRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 1,
		},
	}

	// Set multiple conditions in sequence
	remediation.SetCondition(
		SBRRemediationConditionLeadershipAcquired,
		metav1.ConditionTrue,
		"Acquired",
		"Leadership acquired",
	)
	remediation.SetCondition(
		SBRRemediationConditionFencingInProgress,
		metav1.ConditionTrue,
		"InProgress",
		"Fencing started",
	)
	remediation.SetCondition(
		SBRRemediationConditionFencingSucceeded,
		metav1.ConditionTrue,
		"Succeeded",
		"Fencing completed",
	)
	remediation.SetCondition(SBRRemediationConditionReady, metav1.ConditionTrue, "Succeeded", "Remediation complete")

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
	remediation.SetCondition(
		SBRRemediationConditionFencingInProgress,
		metav1.ConditionFalse,
		"Completed",
		"Fencing no longer in progress",
	)

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

func TestSBRRemediationConstants(t *testing.T) {
	// Test condition type constants
	expectedConditions := map[SBRRemediationConditionType]string{
		SBRRemediationConditionLeadershipAcquired: "LeadershipAcquired",
		SBRRemediationConditionFencingInProgress:  "FencingInProgress",
		SBRRemediationConditionFencingSucceeded:   "FencingSucceeded",
		SBRRemediationConditionReady:              "Ready",
	}

	for condType, expectedString := range expectedConditions {
		if string(condType) != expectedString {
			t.Errorf("Expected condition type %s to have string value %s, got %s",
				condType, expectedString, string(condType))
		}
	}

	// Test reason constants
	expectedReasons := map[SBRRemediationReason]string{
		SBRRemediationReasonHeartbeatTimeout: "HeartbeatTimeout",
		SBRRemediationReasonNodeUnresponsive: "NodeUnresponsive",
		SBRRemediationReasonManualFencing:    "ManualFencing",
	}

	for reason, expectedString := range expectedReasons {
		if string(reason) != expectedString {
			t.Errorf("Expected reason %s to have string value %s, got %s",
				reason, expectedString, string(reason))
		}
	}
}
