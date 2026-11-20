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

package bootunit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// stubNodeClient counts calls and returns scripted errors: the i-th call
// returns errs[i]; once the script is exhausted, every further call returns
// always. A nil error in either field means success. record, when set, sees
// the context of every call.
type stubNodeClient struct {
	cordonErrs   []error
	cordonAlways error
	condErrs     []error
	condAlways   error

	record func(context.Context)

	cordonCalls int
	condCalls   int
}

func (c *stubNodeClient) Cordon(ctx context.Context) (*nodeidentity.Node, error) {
	if c.record != nil {
		c.record(ctx)
	}
	var err error
	if c.cordonCalls < len(c.cordonErrs) {
		err = c.cordonErrs[c.cordonCalls]
	} else {
		err = c.cordonAlways
	}
	c.cordonCalls++
	return nil, err
}

func (c *stubNodeClient) PatchOwnNodeCondition(ctx context.Context, cond nodeidentity.Condition) (*nodeidentity.Node, error) {
	if c.record != nil {
		c.record(ctx)
	}
	var err error
	if c.condCalls < len(c.condErrs) {
		err = c.condErrs[c.condCalls]
	} else {
		err = c.condAlways
	}
	c.condCalls++
	return nil, err
}

// shrinkRetryDelay makes the retry tests fast; the semantics under test are
// the call counts, not the delays.
func shrinkRetryDelay(t *testing.T) {
	t.Helper()
	orig := cordonRetryDelay
	cordonRetryDelay = 0
	t.Cleanup(func() { cordonRetryDelay = orig })
}

func TestCordonAndAnnounceSucceedsWhenBothWritesLand(t *testing.T) {
	client := &stubNodeClient{}
	svc := NewService(client)

	if err := svc.CordonAndAnnounce(context.Background(), ArmingDecision{Armed: true, Reason: "test"}, "incident-1"); err != nil {
		t.Fatalf("CordonAndAnnounce: %v", err)
	}
	if client.cordonCalls != 1 || client.condCalls != 1 {
		t.Errorf("cordon calls = %d, condition calls = %d; want 1 and 1", client.cordonCalls, client.condCalls)
	}
}

// A cordon whose announcement failed to publish is retried together with it:
// the cordon patch is idempotent, and the release protocol matches on the
// condition, so success is only reported once both have landed.
func TestCordonAndAnnounceRetriesUntilTheConditionLands(t *testing.T) {
	shrinkRetryDelay(t)
	client := &stubNodeClient{
		condErrs: []error{fmt.Errorf("announce: %w", nodeidentity.ErrAPIServer)},
	}
	svc := NewService(client)

	if err := svc.CordonAndAnnounce(context.Background(), ArmingDecision{Armed: true, Reason: "test"}, "incident-1"); err != nil {
		t.Fatalf("CordonAndAnnounce: %v", err)
	}
	if client.cordonCalls != 2 || client.condCalls != 2 {
		t.Errorf("cordon calls = %d, condition calls = %d; want 2 and 2 (the cordon re-applied while the condition caught up)",
			client.cordonCalls, client.condCalls)
	}
}

// A replaced Node fails the UID precondition on every attempt, identically;
// retrying it only delays kubelet for a cordon that can never land.
func TestCordonAndAnnounceStopsOnReplacedNode(t *testing.T) {
	shrinkRetryDelay(t)
	client := &stubNodeClient{
		cordonAlways: fmt.Errorf("cordon: %w", nodeidentity.ErrNodeNameMismatch),
	}
	svc := NewService(client)

	err := svc.CordonAndAnnounce(context.Background(), ArmingDecision{Armed: true, Reason: "test"}, "incident-1")
	if err == nil {
		t.Fatal("want an error for a replaced Node")
	}
	if !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
		t.Errorf("error %v should wrap ErrNodeNameMismatch", err)
	}
	if client.cordonCalls != 1 {
		t.Errorf("a permanent failure was retried: %d cordon calls, want 1", client.cordonCalls)
	}
}

// A malformed recorded identity cannot be repaired by anything this unit
// does; only the agent's next start-up rewrites the descriptor.
func TestCordonAndAnnounceStopsOnInvalidIdentity(t *testing.T) {
	shrinkRetryDelay(t)
	client := &stubNodeClient{
		condAlways: fmt.Errorf("announce: %w", nodeidentity.ErrInvalidConfig),
	}
	svc := NewService(client)

	err := svc.CordonAndAnnounce(context.Background(), ArmingDecision{Armed: true, Reason: "test"}, "incident-1")
	if err == nil {
		t.Fatal("want an error for an invalid identity")
	}
	if !errors.Is(err, nodeidentity.ErrInvalidConfig) {
		t.Errorf("error %v should wrap ErrInvalidConfig", err)
	}
	if client.condCalls != 1 {
		t.Errorf("a permanent failure was retried: %d condition calls, want 1", client.condCalls)
	}
}

// Transient failures — the API still down while the cluster recovers — use
// the whole bounded budget and report the last error.
func TestCordonAndAnnounceRetriesTransientFailures(t *testing.T) {
	shrinkRetryDelay(t)
	client := &stubNodeClient{
		cordonAlways: fmt.Errorf("cordon: %w", nodeidentity.ErrAPIServer),
	}
	svc := NewService(client)

	err := svc.CordonAndAnnounce(context.Background(), ArmingDecision{Armed: true, Reason: "test"}, "incident-1")
	if err == nil {
		t.Fatal("want an error when every attempt fails")
	}
	if client.cordonCalls != CordonRetryAttempts {
		t.Errorf("cordon calls = %d, want the full budget of %d attempts", client.cordonCalls, CordonRetryAttempts)
	}
}

// TestCordonAndAnnounceBoundsEachCall pins the per-call deadline. Without it a
// single hung request spends the whole CordonTotalBudget, so the attempt that
// would publish the announcement never happens and the cordon lands with no
// explanation — the release protocol then has nothing to match.
func TestCordonAndAnnounceBoundsEachCall(t *testing.T) {
	start := time.Now()
	var deadlines []time.Time
	client := &stubNodeClient{
		record: func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("an API call received a context without a deadline")
				return
			}
			deadlines = append(deadlines, deadline)
		},
	}
	svc := NewService(client)

	// A caller budget far longer than one call, so the bound under test can
	// only come from the per-call timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*APITimeout)
	defer cancel()
	if err := svc.CordonAndAnnounce(ctx, ArmingDecision{Armed: true, Reason: "test"}, "incident-1"); err != nil {
		t.Fatalf("CordonAndAnnounce: %v", err)
	}
	if len(deadlines) != 2 {
		t.Fatalf("recorded %d call deadlines, want 2 (cordon and announce)", len(deadlines))
	}

	// Generous slack: the assertion is "bounded by APITimeout", not "exactly
	// APITimeout", so a slow CI machine does not produce a false failure.
	limit := start.Add(APITimeout + 5*time.Second)
	for i, deadline := range deadlines {
		if deadline.After(limit) {
			t.Errorf("call %d deadline %v is later than %v; a hung call would spend the whole budget",
				i, deadline, limit)
		}
	}
}
