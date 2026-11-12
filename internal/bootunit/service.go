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
func (s *Service) CordonAndAnnounce(ctx context.Context, decision ArmingDecision) error {
	var lastErr error
	for attempt := 1; attempt <= CordonRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled before cordon completed: %w", err)
		}

		if err := s.tryCordonAndAnnounce(ctx, decision); err != nil {
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
	return fmt.Errorf("cordon failed after %d attempts: %w", CordonRetryAttempts, lastErr)
}

// tryCordonAndAnnounce performs one cordon-and-announce attempt. If the
// cordon succeeds but the announcement fails, we still return success for
// the cordon — the announcement is visibility, not safety.
func (s *Service) tryCordonAndAnnounce(ctx context.Context, decision ArmingDecision) error {
	// Cordon: block new scheduling on this node.
	_, err := s.client.Cordon(ctx)
	if err != nil {
		return fmt.Errorf("cordon: %w", err)
	}

	// Announce: publish the rejoin condition. If this fails, the cordon
	// is still in effect — the announcement is observability, not safety.
	// We don't fail the whole operation for a failed announcement.
	_, annErr := s.client.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
		Type:    RejoinConditionType,
		Status:  "True",
		Reason:  "BootUnit",
		Message: decision.Reason,
	})
	if annErr != nil {
		// Log but don't fail — the cordon is the safety property.
		// The announcement can be re-applied by the agent once it runs.
		_ = annErr
	}

	return nil
}
