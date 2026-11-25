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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// NodeClient is the subset of the node identity client the cordon-and-announce
// path needs. It is an interface so the retry behavior can be tested without
// an API server; production code passes a *nodeidentity.Client.
type NodeClient interface {
	GetOwnNode(ctx context.Context) (*nodeidentity.Node, error)
	PatchUnschedulableWithAnnotations(ctx context.Context, unschedulable bool, annotations map[string]string) (*nodeidentity.Node, error)
	PatchOwnNodeCondition(ctx context.Context, cond nodeidentity.Condition) (*nodeidentity.Node, error)
}

// ErrForeignCordon reports that the Node is already cordoned by an owner
// other than SBR. The unit claims nothing in that case: stamping its
// ownership key on a foreign cordon would make the release protocol uncordon
// somebody else's restriction.
var ErrForeignCordon = errors.New("bootunit: the node is cordoned by another owner; not claiming it")

// Service provides the cordon-and-announce behavior the boot unit performs
// when the arming decision is Armed. It is separate from the arming logic so
// that the decision can be tested without any API interaction.
type Service struct {
	client NodeClient
}

// NewService creates a boot unit service from a node identity client. The
// client must already be configured with the pre-recorded identity
// (node name, expected UID, direct API endpoint, cert/key/CA paths).
func NewService(client NodeClient) *Service {
	return &Service{client: client}
}

// cordonRetryDelay is CordonRetryDelay as a package variable so tests can
// shrink it; production always uses the constant.
var cordonRetryDelay = CordonRetryDelay

// CordonAndAnnounce cordons the node and publishes the SBRRejoinPending
// condition. It retries with bounded backoff because the API may still be
// down at boot. A late cordon costs scheduling isolation, not correctness —
// kubelet is already off (the unit runs Before=kubelet.service).
//
// The cordon and the announcement are retried together: the cordon patch is
// idempotent, so re-applying it while the announcement catches up is
// harmless, and success is reported only once the condition is published —
// the release protocol matches on that condition, and a cordon without its
// announcement is a cordon nobody can explain. The cordon remains the
// safety property either way: it is applied first on every attempt.
//
// incidentID is the remediation incident the arming state was recorded for,
// and may be empty when the boot was armed by a weaker evidence tier. It is
// carried in the condition message ("incident <id>: <reason>"), which is
// what the release protocol matches to confirm it is releasing the incident
// it decided on — the condition's Reason stays a short CamelCase token per
// Kubernetes convention. The same ID is recorded as the value of the
// RejoinCordonAnnotation ownership key, in the same write that cordons.
func (s *Service) CordonAndAnnounce(ctx context.Context, decision ArmingDecision, incidentID string) error {
	// Foreign-cordon pre-check, once before the retry loop: an administrator
	// may have cordoned (drained) this node before the reboot. Stamping the
	// ownership key on their cordon would make the release protocol uncordon
	// it later, so the unit claims nothing and announces nothing — the
	// foreign cordon already keeps new workloads off the node, which is the
	// protection this unit exists to provide. A cordon that already carries
	// the key (a retry of our own) is re-applied idempotently below.
	//
	// The check runs once, not per attempt, so the CordonTotalBudget math
	// (delays + one in-flight call) is unchanged. A read failure does not
	// block the cordon attempt: the patch's UID precondition still protects
	// against a replaced Node object.
	checkCtx, cancelCheck := context.WithTimeout(ctx, APITimeout)
	current, getErr := s.client.GetOwnNode(checkCtx)
	cancelCheck()
	if getErr == nil && current.Spec.Unschedulable {
		if _, keyed := current.Metadata.Annotations[RejoinCordonAnnotation]; !keyed {
			return ErrForeignCordon
		}
	}

	var lastErr error
	for attempt := 1; attempt <= CordonRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled before cordon completed: %w", err)
		}

		if err := s.tryCordonAndAnnounce(ctx, decision, incidentID); err != nil {
			if permanentCordonFailure(err) {
				// No retry within this unit's lifetime can change the
				// outcome, and every further attempt only delays kubelet
				// for a cordon that will never land.
				return err
			}
			lastErr = err
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during cordon retry: %w", ctx.Err())
			case <-time.After(cordonRetryDelay):
				continue
			}
		}

		return nil
	}
	return fmt.Errorf("cordon and announce failed after %d attempts: %w", CordonRetryAttempts, lastErr)
}

// permanentCordonFailure reports whether err cannot succeed on a later retry
// within the unit's budget.
//
// ErrNodeNameMismatch means the Node object the identity was provisioned for
// was replaced: the UID precondition in every write fails the same way, now
// and on each retry. ErrInvalidConfig means the recorded identity itself is
// malformed, and only the agent's next start-up rewrites the descriptor.
//
// Everything else stays retryable on purpose. Network and API-recovery
// failures are the expected state at this point in the boot; authorization
// and conflict outcomes may clear as the API server recovers; and certificate
// validity errors can be an artifact of a skewed clock that NTP corrects
// while the unit is still retrying. The context budget bounds the rest.
func permanentCordonFailure(err error) bool {
	return errors.Is(err, nodeidentity.ErrNodeNameMismatch) ||
		errors.Is(err, nodeidentity.ErrInvalidConfig)
}

// tryCordonAndAnnounce performs one cordon-and-announce attempt. Both writes
// must succeed for the attempt to count: the cordon is the safety property,
// and the announcement is what the release protocol and administrators
// match on, so a cordon whose condition failed to publish is retried — the
// cordon patch is idempotent.
//
// Each call runs under its own APITimeout, so one hung request cannot spend
// the whole CordonTotalBudget and leave no attempt for the announcement that
// makes the cordon explainable. The bound that always holds is the caller's
// context; a per-call deadline shortens it further and never extends it.
func (s *Service) tryCordonAndAnnounce(ctx context.Context, decision ArmingDecision, incidentID string) error {
	// Cordon: block new scheduling on this node, and record the cordon as
	// SBR-owned in the SAME write. The ownership key is what the release
	// protocol (a peer at CR cleanup, or the local agent when no CR exists)
	// matches on; a cordon without it would be preserved forever as
	// pre-existing. The key is written even when incidentID is empty:
	// presence carries ownership, the value is diagnostic.
	cordonCtx, cancelCordon := context.WithTimeout(ctx, APITimeout)
	_, err := s.client.PatchUnschedulableWithAnnotations(cordonCtx, true,
		map[string]string{RejoinCordonAnnotation: incidentID})
	cancelCordon()
	if err != nil {
		return fmt.Errorf("cordon: %w", err)
	}

	message := decision.Reason
	if incidentID != "" {
		message = fmt.Sprintf("incident %s: %s", incidentID, decision.Reason)
	}

	// Announce: publish the rejoin condition. A failure here fails the
	// attempt so the retry loop publishes it; the cordon already in effect
	// is re-applied idempotently on the next attempt.
	announceCtx, cancelAnnounce := context.WithTimeout(ctx, APITimeout)
	defer cancelAnnounce()
	_, err = s.client.PatchOwnNodeCondition(announceCtx, nodeidentity.Condition{
		Type:    RejoinConditionType,
		Status:  "True",
		Reason:  "BootUnit",
		Message: message,
	})
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}

	return nil
}
