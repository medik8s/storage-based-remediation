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
// service Before=kubelet.service, gated by an arming flag file the agent
// creates when it commits a reboot decision. When armed, it cordons the node
// and publishes an SBRRejoinPending condition using the node identity.
//
// The binary is installed and maintained by the sbr-agent (see
// internal/bootunit/install.go). It is not a runtime component of any
// operator — it runs once at boot, before Kubernetes starts.
//
// Exit codes: 0 = completed (armed and cordoned, or not armed), 1 = was
// armed but could not cordon (the node boots normally — the cordon is a
// scheduler signal, not a fence). The exit code is for the systemd journal,
// not for any caller.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

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
		keyFile   = fs.String("key", "/var/lib/kubelet/pki/kubelet-current.pem", "kubelet client key")
		timeout   = fs.Duration("timeout", bootunit.APITimeout, "timeout for the cordon attempt")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 1
	}

	// Read the journal to decide whether this boot needs protection.
	journal := bootunit.NewHostJournal("")
	decision := bootunit.EvaluateArming(journal)

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

	// Cordon and announce with bounded retries.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout*time.Duration(bootunit.CordonRetryAttempts))
	defer cancel()

	svc := bootunit.NewService(client)
	if err := svc.CordonAndAnnounce(ctx, decision); err != nil {
		fmt.Fprintf(os.Stderr, "sbr-self-cordon: %v\n", err)
		return 1
	}

	fmt.Printf("sbr-self-cordon: node cordoned, %s published\n", bootunit.RejoinConditionType)
	return 0
}
