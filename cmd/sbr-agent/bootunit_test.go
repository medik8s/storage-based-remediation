/*
Copyright 2026.

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

package main

import (
	"context"
	"errors"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// failingClient fails every Get. The disabled gate must not touch the API at
// all — let alone the host — so any call reaching it turns into a test
// failure.
type failingClient struct {
	client.Client
}

func (failingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return errors.New("Get must not be called when the boot unit is disabled")
}

// A disabled boot unit must do nothing: no Node read (and therefore nothing
// that could follow it — no identity write, no install, no gate), and no error
// reported for a state that is a supported deployment, not a fault.
func TestMaybeSetupBootUnitDisabledTouchesNothing(t *testing.T) {
	nodeUID, err := maybeSetupBootUnit(
		context.Background(), failingClient{}, "node-a", nil, false, false)
	if err != nil {
		t.Fatalf("disabled boot unit returned error: %v", err)
	}
	if nodeUID != "" {
		t.Fatalf("disabled boot unit returned node UID %q, want empty", nodeUID)
	}
}

// An enabled boot unit delegates to setupBootUnit, which starts by reading the
// Node: a failing client proves the delegation happens.
func TestMaybeSetupBootUnitEnabledDelegates(t *testing.T) {
	_, err := maybeSetupBootUnit(
		context.Background(), failingClient{}, "node-a", nil, false, true)
	if err == nil {
		t.Fatal("enabled boot unit must propagate the setup error")
	}
}
