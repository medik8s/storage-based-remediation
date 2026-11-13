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
	"fmt"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// Service provides the cordon-and-announce behavior the boot unit performs
// when the arming decision is Armed. It is separate from the arming logic so
// that the decision can be tested without any API interaction.
type Service struct {
	client *nodeidentity.Client
}

// NewService creates a boot unit service from a node identity client. The
// client must already be configured with the pre-recorded identity
// (node name, expected UID, direct API endpoint, cert/key/CA paths).
func NewService(client *nodeidentity.Client) *Service {
	return &Service{client: client}
}

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
// Kubernetes convention.
func (s *Service) CordonAndAnnounce(ctx context.Context, decision ArmingDecision, incidentID string) error {
	var lastErr error
	for attempt := 1; attempt <= CordonRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled before cordon completed: %w", err)
		}

		if err := s.tryCordonAndAnnounce(ctx, decision, incidentID); err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during cordon retry: %w", ctx.Err())
			case <-time.After(CordonRetryDelay):
				continue
			}
		}

		return nil
	}
	return fmt.Errorf("cordon and announce failed after %d attempts: %w", CordonRetryAttempts, lastErr)
}

// tryCordonAndAnnounce performs one cordon-and-announce attempt. Both writes
// must succeed for the attempt to count: the cordon is the safety property,
// and the announcement is what the release protocol and administrators
// match on, so a cordon whose condition failed to publish is retried — the
// cordon patch is idempotent.
func (s *Service) tryCordonAndAnnounce(ctx context.Context, decision ArmingDecision, incidentID string) error {
	// Cordon: block new scheduling on this node.
	_, err := s.client.Cordon(ctx)
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
	_, err = s.client.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
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
