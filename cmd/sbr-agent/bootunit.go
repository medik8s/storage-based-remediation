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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// setupBootUnit reconciles the pre-kubelet self-cordon unit on this node.
//
// It runs once at agent start and at every agent restart. It is deliberately
// best effort: a failure here degrades rejoin protection, which must be
// reported loudly, but it must never stop the agent from fencing. The fence is
// the safety property; the cordon is not.
//
// The order is load-bearing:
//
//  1. Persist the identity descriptor first. The installed unit declares it a
//     required EnvironmentFile, so a node without one refuses to start the unit
//     rather than running the armed path with empty identity fields.
//  2. Install the binary and unit, and activate them. Activation runs on every
//     attempt, so a unit disabled out of band is repaired here.
//  3. Verify, so a hash or mode drift is reported now rather than at the next
//     boot, when nobody is watching.
//  4. Establish the journal, then open the gate. The arming flag only ever
//     appears after a durable journal write, so its presence implies a journal
//     this agent could write — the property the boot unit's fail-closed tier
//     depends on. Opening the gate is what makes the unit run at the NEXT boot
//     and let the journal tiers decide; the agent does not need it for the
//     current boot, where it is already running.
//
// A detect-only agent never removes or unlocks anything on the host, and it
// must never leave a gate that would cordon a node it is not remediating, so it
// clears the flag instead of creating it.
//
// Returning an error means the wiring is degraded, not that the node is unsafe.
//
// It also returns this Node object's UID as soon as it has been read, even when
// a later step fails. The agent caches that value to bind a future reboot
// decision to this Node object; an empty result only costs that binding, which
// the journal reader tolerates, and never the decision itself.
func setupBootUnit(
	ctx context.Context,
	k8sClient client.Client,
	nodeName string,
	journal *bootunit.Journal,
	detectOnly bool,
) (string, error) {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return "", fmt.Errorf("read node %s for its identity: %w", nodeName, err)
	}
	nodeUID := string(node.UID)
	if node.UID == "" {
		return "", fmt.Errorf("node %s has no UID", nodeName)
	}

	// The boot unit runs before kubelet, so the in-cluster service address is
	// not routable yet. The node's own kubelet kubeconfig names the endpoint
	// kubelet uses, which is reachable from the node by definition.
	apiServer, err := bootunit.ResolveAPIServerFromKubeconfig(bootunit.ContainerKubeletConf)
	if err != nil {
		return nodeUID, fmt.Errorf("resolve the API server for the boot unit: %w", err)
	}

	if err := bootunit.WriteIdentityEnv(nodeName, nodeUID, apiServer); err != nil {
		return nodeUID, fmt.Errorf("write the boot unit identity descriptor: %w", err)
	}

	if err := bootunit.Install(bootunit.ContainerSystemdDir); err != nil {
		return nodeUID, fmt.Errorf("install the boot unit: %w", err)
	}

	if err := bootunit.Verify(bootunit.ContainerSystemdDir); err != nil {
		return nodeUID, fmt.Errorf("verify the boot unit wiring: %w", err)
	}

	if detectOnly {
		// Detect-only mode remediates nothing, so a boot unit that cordons
		// would be acting on behalf of a decision this agent never makes.
		if err := bootunit.ClearArmingFlag(); err != nil {
			return nodeUID, fmt.Errorf("clear the boot unit gate in detect-only mode: %w", err)
		}
		logger.Info("Detect-only mode: boot unit installed but not gated on", "nodeName", nodeName)
		return nodeUID, nil
	}

	if journal == nil {
		return nodeUID, fmt.Errorf("no journal available under %s; not opening the boot unit gate",
			bootunit.HostStateRoot)
	}
	if err := journal.RecordRunning(ctx); err != nil {
		// Without a durable journal the gate must stay shut: a flag next to a
		// journal nobody can write would make the next boot fail closed on
		// evidence this agent never produced.
		return nodeUID, fmt.Errorf("record this agent as running before opening the boot unit gate: %w", err)
	}

	if err := bootunit.CreateArmingFlag(); err != nil {
		return nodeUID, fmt.Errorf("open the boot unit gate: %w", err)
	}

	return nodeUID, nil
}
