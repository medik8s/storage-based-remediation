/*
Copyright 2026.

Licensed under the License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bootunit

import (
	"strings"
	"testing"
)

// fakeJournal implements ArmingJournal for testing.
type fakeJournal struct {
	hasArmingState   bool
	uncleanShutdown  bool
	storageUnhealthy bool
}

func (f *fakeJournal) HasArmingState() bool                 { return f.hasArmingState }
func (f *fakeJournal) IsUncleanShutdown() bool              { return f.uncleanShutdown }
func (f *fakeJournal) StorageWasUnhealthyAtLastWrite() bool { return f.storageUnhealthy }

func TestEvaluateArming(t *testing.T) {
	tests := []struct {
		name      string
		journal   ArmingJournal
		wantArmed bool
		wantIn    string // substring of the reason
	}{
		{
			name:      "recorded decision arms the boot",
			journal:   &fakeJournal{hasArmingState: true},
			wantArmed: true,
			wantIn:    "recorded reboot decision",
		},
		{
			name:      "unclean shutdown with unhealthy storage arms the boot",
			journal:   &fakeJournal{uncleanShutdown: true, storageUnhealthy: true},
			wantArmed: true,
			wantIn:    "unclean shutdown while storage was unhealthy",
		},
		{
			name:      "unclean shutdown with healthy storage is normal boot",
			journal:   &fakeJournal{uncleanShutdown: true, storageUnhealthy: false},
			wantArmed: false,
			wantIn:    "storage was healthy",
		},
		{
			name:      "clean shutdown is normal boot",
			journal:   &fakeJournal{},
			wantArmed: false,
			wantIn:    "clean shutdown",
		},
		{
			name:      "nil journal is not armed",
			journal:   nil,
			wantArmed: false,
			wantIn:    "no journal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateArming(tt.journal)
			if got.Armed != tt.wantArmed {
				t.Errorf("Armed = %v, want %v", got.Armed, tt.wantArmed)
			}
			if !strings.Contains(got.Reason, tt.wantIn) {
				t.Errorf("Reason = %q, want to contain %q", got.Reason, tt.wantIn)
			}
		})
	}
}

func TestArmingDecisionPrecedence(t *testing.T) {
	// A recorded decision takes precedence over a clean-shutdown signal:
	// even if the shutdown was clean, an arming state means SBR decided
	// to reboot, and the boot should be protected.
	j := &fakeJournal{hasArmingState: true}
	got := EvaluateArming(j)
	if !got.Armed {
		t.Fatal("recorded decision must arm even with a clean shutdown")
	}
}
