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

// Command sbr-self-cordon is the pre-kubelet boot unit. It runs as a systemd
// service Before=kubelet.service, gated by an arming flag the agent creates
// when it arms the watchdog — after the first durable journal write — and
// removes at startup when it takes over protection duties. When armed, it
// cordons the node and publishes an SBRRejoinPending condition using the
// node identity.
//
// The binary is installed and maintained by the sbr-agent (see
// internal/bootunit/install.go). It is not a runtime component of any
// operator — it runs once at boot, before Kubernetes starts.
//
// Exit codes: 0 = completed (armed, cordoned and announced, or not armed),
// 1 = was armed but could not cordon or announce within the retry budget
// (the node boots normally — the cordon is a scheduler signal, not a fence).
// The exit code is for the systemd journal, not for any caller.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/medik8s/common/pkg/nodeidentity"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("sbr-self-cordon", flag.ContinueOnError)
	var (
		nodeName  = fs.String("node-name", "", "name of this Kubernetes node")
		uid       = fs.String("uid", "", "expected Node UID, recorded while the node was healthy")
		apiServer = fs.String("api-server", "", "direct host-routable API endpoint")
		caFile    = fs.String("ca", "/etc/kubernetes/kubelet-ca.crt", "CA bundle path")
		certFile  = fs.String("cert", "/var/lib/kubelet/pki/kubelet-client-current.pem", "kubelet client certificate")
		keyFile   = fs.String("key", "/var/lib/kubelet/pki/kubelet-client-current.pem", "kubelet client key; the kubelet client PEM combines certificate and key, so this defaults to the same file as -cert")
		stateDir  = fs.String("state-dir", "", "journal state directory (default "+bootunit.HostStateRoot+")")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 1
	}

	// Read the journal to decide whether this boot needs protection. The
	// unit only runs when the agent's arming flag exists, so a journal that
	// cannot be opened or read fails closed: the zero snapshot is unusable
	// and EvaluateArming arms on it. The expected Node UID binds the read:
	// an armed decision recorded for a different Node object is ignored, so
	// a replaced Node is neither cordoned for a stale incident nor handed
	// its incident ID.
	journal, err := bootunit.NewJournal(*stateDir)
	snapshot := &bootunit.ArmingSnapshot{}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbr-self-cordon: cannot open journal: %v\n", err)
	} else {
		snapshot = journal.ReadForArming(context.Background(), *uid)
		if p := snapshot.Problem(); p != "" {
			fmt.Fprintf(os.Stderr, "sbr-self-cordon: journal problem: %s\n", p)
		}
		if snapshot.ForeignDecision() {
			fmt.Fprintf(os.Stderr, "sbr-self-cordon: journal carries an armed decision for a different Node UID; ignoring it (the Node object was replaced)\n")
		}
	}
	decision := bootunit.EvaluateArming(snapshot)

	fmt.Printf("sbr-self-cordon: node=%s decision=%s\n", *nodeName, decision)

	if !decision.Armed {
		return 0 // normal boot; kubelet starts normally
	}

	// Build the node identity client from the pre-recorded descriptor.
	cfg := nodeidentity.Config{
		NodeName:    *nodeName,
		ExpectedUID: *uid,
		APIServer:   *apiServer,
		CAFile:      *caFile,
		CertFile:    *certFile,
		KeyFile:     *keyFile,
	}
	client, err := nodeidentity.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbr-self-cordon: invalid config: %v\n", err)
		return 1
	}

	// Cordon and announce with bounded retries. The incident ID, when the
	// boot was armed by a recorded decision, travels in the condition
	// message for the release protocol to match.
	var incidentID string
	if a := snapshot.Arming(); a != nil {
		incidentID = a.IncidentID
	}

	// The total budget covers every retry delay plus one in-flight call, and
	// stays below the unit's TimeoutStartSec: a budget above it would let
	// systemd kill the unit mid-retry, and a cordon still being attempted
	// would read as a cordon failure. See CordonTotalBudget.
	ctx, cancel := context.WithTimeout(context.Background(), bootunit.CordonTotalBudget)
	defer cancel()

	svc := bootunit.NewService(client)
	if err := svc.CordonAndAnnounce(ctx, decision, incidentID); err != nil {
		fmt.Fprintf(os.Stderr, "sbr-self-cordon: %v\n", err)
		return 1
	}

	fmt.Printf("sbr-self-cordon: node cordoned, %s published\n", bootunit.RejoinConditionType)
	return 0
}
