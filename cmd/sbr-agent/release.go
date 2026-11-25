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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

const (
	// releaseCheckInterval is how often the agent looks for its own keyed
	// cordon to release. The check is cheap (one Node read, one CR list) and
	// only acts once per boot at most, so a fixed cadence is enough: the loop
	// is the retry mechanism, and a failed attempt costs one interval.
	releaseCheckInterval = 30 * time.Second

	// releaseTimeout bounds one whole release attempt, reads and both writes.
	releaseTimeout = 30 * time.Second

	// outOfServiceTaintKey is the taint a peer applies under an active CR
	// (design §5.9). Its presence on the Node means the peer owns this
	// node's restrictions, and the agent must not release anything.
	outOfServiceTaintKey = "node.kubernetes.io/out-of-service"
)

// recordStorageOutcome records the verdict of this node's own heartbeat
// write: healthy storage latches a timestamp, any failure clears it. It is
// the sustained-health clock the rejoin-release rule reads — the same
// first-person evidence, inverted, that authorizes an autonomous reboot
// attempt.
func (s *SBRAgent) recordStorageOutcome(ok bool) {
	s.journalIOMutex.Lock()
	defer s.journalIOMutex.Unlock()
	if ok {
		if s.storageHealthySince.IsZero() {
			s.storageHealthySince = time.Now()
		}
		return
	}
	s.storageHealthySince = time.Time{}
}

// storageHealthyFor reports how long this node's storage has been healthy in
// this process, and whether it has been observed healthy at all.
func (s *SBRAgent) storageHealthyFor() (time.Duration, bool) {
	s.journalIOMutex.Lock()
	defer s.journalIOMutex.Unlock()
	if s.storageHealthySince.IsZero() {
		return 0, false
	}
	return time.Since(s.storageHealthySince), true
}

// rejoinReleaseLoop periodically looks for the boot unit's keyed cordon and,
// when no CR ever claimed this node and storage has stayed healthy, releases
// it. The loop is deliberately self-healing rather than one-shot: any failed
// or interrupted attempt is simply retried on the next tick with fresh
// verification, so a release can never be skipped on a transient error and
// can never proceed on a stale check.
func (s *SBRAgent) rejoinReleaseLoop() {
	ticker := time.NewTicker(releaseCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			released, err := s.maybeReleaseRejoinCordon(s.ctx)
			switch {
			case err != nil:
				// A failed attempt is retried on the next tick; the node
				// stays cordoned, which is the safe direction.
				logger.Error(err, "Cannot evaluate the rejoin-cordon release; retrying next cycle")
			case released:
				logger.Info("Released the boot unit's cordon after a healthy rejoin with no remediation CR",
					"nodeName", s.nodeName)
				return
			}
		}
	}
}

// maybeReleaseRejoinCordon is the agent-side release of the boot unit's
// cordon, for the case where no remediation CR ever claimed this node
// (design §5.12: an autonomous reboot NHC never turned into a CR). When a CR
// does claim the node — now or later — the peer releases at CR cleanup
// instead, and this method must do nothing, ever.
//
// The release fires only when ALL of the following hold, verified live in
// this call:
//
//   - the Node is cordoned AND carries the boot unit's ownership key; a
//     cordon without the key belongs to someone else (an administrator, a
//     peer) and is never touched;
//   - the Node object is still the one this agent's decisions are bound to;
//   - no OutOfService taint is present (a peer is acting for a CR);
//   - no remediation CR names this node or carries its UID;
//   - this node's storage has been healthy for the whole recovery period.
//
// The release itself is two ordered writes, chosen so that every interruption
// leaves a state the next cycle can finish or safely redo:
//
//   - first, the status write that removes SBRRejoinPending — dying here
//     leaves the cordon and the key in place, so the loop still sees work
//     to do and retries;
//   - then, one main-resource write that uncordons AND removes the key —
//     they cannot come apart. Dying here means the goal was already
//     reached; any leftover condition is cosmetic and the next cycle's
//     checks simply pass.
//
// A verification error is returned, never swallowed: the caller must not
// release on the strength of checks it could not complete.
func (s *SBRAgent) maybeReleaseRejoinCordon(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()

	node := &corev1.Node{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: s.nodeName}, node); err != nil {
		return false, fmt.Errorf("read node %s: %w", s.nodeName, err)
	}

	// Observe the held state for the gauge and the held event before any
	// early return: the alert must fire for a node this loop will not
	// release (a CR owns it, storage is not healthy yet) exactly as for one
	// it is about to.
	_, keyed := node.Annotations[bootunit.RejoinCordonAnnotation]
	s.observeRejoinCordon(node.Spec.Unschedulable && keyed)

	// Only the unit's keyed cordon is ours to release. A cordoned node
	// without the key is someone else's restriction; an unkeyed node has
	// nothing to do at all.
	if !node.Spec.Unschedulable {
		return false, nil
	}
	if !keyed {
		return false, nil
	}
	// A replaced Node object is not the one the unit cordoned: its
	// restrictions are not this agent's to change.
	if s.nodeUID != "" && string(node.UID) != s.nodeUID {
		return false, nil
	}
	// An OutOfService taint means a peer is acting for a CR: the peer owns
	// this node's restrictions, including the keyed cordon's release.
	for _, taint := range node.Spec.Taints {
		if taint.Key == outOfServiceTaintKey {
			return false, nil
		}
	}
	// A CR claiming this node — in any namespace — transfers ownership of
	// the release to the peer path. CRs are matched against the enrollment
	// UID and, when the startup read failed and left it empty, against the
	// UID of the live Node this call just read: more detected claims means
	// fewer wrongful releases, never more. An unreadable CR list is an
	// error, not a "no": releasing on unverified checks is the one
	// forbidden outcome.
	claimed, err := s.remediationClaimsThisNode(ctx, string(node.UID))
	if err != nil {
		return false, err
	}
	if claimed {
		return false, nil
	}
	// Sustained healthy storage for the recovery period: the inverse of the
	// evidence that authorizes an autonomous reboot, never a timer since
	// boot.
	healthyFor, ok := s.storageHealthyFor()
	if !ok || healthyFor < s.autonomousRebootRecoveryPeriod {
		return false, nil
	}

	// Release. Status first: see the method comment for why this order
	// makes every interruption self-healing.
	conditions := make([]corev1.NodeCondition, 0, len(node.Status.Conditions))
	removed := false
	for _, cond := range node.Status.Conditions {
		if cond.Type == bootunit.RejoinConditionType {
			removed = true
			continue
		}
		conditions = append(conditions, cond)
	}
	if removed {
		node.Status.Conditions = conditions
		if err := s.k8sClient.Status().Update(ctx, node); err != nil {
			return false, fmt.Errorf("clear the %s condition: %w", bootunit.RejoinConditionType, err)
		}
	}

	// Re-validate health before uncordoning: the status write took time,
	// and a storage failure recorded while it was in flight invalidates
	// the recovery evidence the release rests on. Aborting here leaves the
	// cordon and the key in place — the next cycle re-verifies with fresh
	// health, and the already-cleared condition is cosmetic (it is
	// republished by nothing, but the keyed cordon keeps the node held and
	// the alert active until a healthy release or break-glass).
	if healthyFor, ok := s.storageHealthyFor(); !ok || healthyFor < s.autonomousRebootRecoveryPeriod {
		logger.Info("Storage health was invalidated during the release; holding the cordon for re-evaluation",
			"nodeName", s.nodeName)
		return false, nil
	}

	// Uncordon and remove the ownership key in ONE write, so a released
	// cordon can never keep its marker and a removed marker can never leave
	// an SBR-owned cordon behind.
	node.Spec.Unschedulable = false
	delete(node.Annotations, bootunit.RejoinCordonAnnotation)
	if err := s.k8sClient.Update(ctx, node); err != nil {
		return false, fmt.Errorf("uncordon %s and remove %s: %w",
			s.nodeName, bootunit.RejoinCordonAnnotation, err)
	}
	// The loop exits after a successful release, so the gauge must be
	// cleared here rather than on the next observation.
	s.observeRejoinCordon(false)

	// The protected boot is complete and released: clear the recorded
	// decision so the incident cannot re-arm later, ordinary boots — the
	// ArmingState contract ("the agent clears the key after the protected
	// boot completes"). Best effort: a failed clear means the next boot
	// cordons again and this loop releases again, which self-heals.
	if s.journal != nil {
		clearCtx, clearCancel := context.WithTimeout(context.Background(), journalWriteTimeout)
		defer clearCancel()
		if err := s.journal.ClearArmingState(clearCtx); err != nil {
			logger.Error(err, "Released the cordon but could not clear the recorded reboot decision; the next boot may cordon again and self-heal through release",
				"path", s.journal.Path())
		}
	}

	return true, nil
}

// observeRejoinCordon records whether this node is currently held by the
// boot unit's keyed cordon: the gauge always, and a Warning event exactly
// once per transition into held. A node held for long needs attention — an
// active CR should see it released at cleanup by a peer, and no CR should
// see it released by this agent after sustained healthy storage — so the
// state is the alert, not just a diagnostic.
func (s *SBRAgent) observeRejoinCordon(held bool) {
	if held {
		rejoinCordonHeldGauge.Set(1)
	} else {
		rejoinCordonHeldGauge.Set(0)
	}
	if held == s.rejoinCordonHeld {
		return
	}
	s.rejoinCordonHeld = held
	if held {
		logger.Info("Node is held by the boot unit's cordon after rejoin; awaiting release",
			"nodeName", s.nodeName)
		if s.recorder != nil && s.recorderObject != nil {
			s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonRejoinCordonHeld,
				"Node re-joined and is held by the boot unit's cordon (SBRRejoinPending); awaiting release")
		}
	}
}

// remediationClaimsThisNode reports whether any remediation CR in any
// namespace names this node or carries its UID. CRs created by NHC may live
// in a namespace other than the agent's own, so this lists cluster-wide
// rather than reading one well-known object. A match in a foreign namespace
// still counts: ownership of the release is the CR's wherever the CR lives.
//
// liveUID is the UID of the Node this release attempt just read. It matches
// CRs even when the startup enrollment read failed and left s.nodeUID empty:
// matching against more identities can only find more claims, and finding a
// claim prevents a release — the safe direction. It never substitutes for
// the enrollment UID in the ownership checks above.
func (s *SBRAgent) remediationClaimsThisNode(ctx context.Context, liveUID string) (bool, error) {
	list := &v1alpha1.StorageBasedRemediationList{}
	if err := s.k8sClient.List(ctx, list); err != nil {
		return false, fmt.Errorf("list remediation CRs: %w", err)
	}
	for _, cr := range list.Items {
		if cr.Name == s.nodeName {
			return true, nil
		}
		if s.nodeUID != "" && string(cr.Status.NodeUID) == s.nodeUID {
			return true, nil
		}
		if liveUID != "" && string(cr.Status.NodeUID) == liveUID {
			return true, nil
		}
	}
	return false, nil
}
