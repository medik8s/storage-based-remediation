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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/medik8s/common/pkg/nodestate/nodestatetest"

	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// runWithArgs runs the boot unit binary's main with the given command line.
func runWithArgs(t *testing.T, args ...string) int {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"sbr-self-cordon"}, args...)
	t.Cleanup(func() { os.Args = orig })
	return run()
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns its
// exit code together with everything it printed. The redirect is restored on
// every path, including a panic, so a failing test cannot leave the rest of
// the package writing into a closed pipe.
func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	os.Stdout = w
	defer func() { os.Stdout = orig }()

	code := fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

func TestRunNotArmedExitsZero(t *testing.T) {
	// A journal whose last recorded state is an orderly stop, with no reboot
	// decision and no failed I/O: the boot is not armed, and the unit must
	// exit 0 without ever building an API client.
	dir := nodestatetest.TempStateDir(t)
	j, err := bootunit.NewJournal(dir)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	if err := j.RecordCleanShutdown(context.Background()); err != nil {
		t.Fatalf("RecordCleanShutdown: %v", err)
	}

	code, out := captureStdout(t, func() int {
		return runWithArgs(t, "-state-dir", dir,
			"-node-name", "worker-0", "-uid", "uid-1", "-api-server", "https://api.example:6443")
	})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 for an unarmed boot", code)
	}
	if !strings.Contains(out, string(bootunit.BootStateNotArmed)) {
		t.Errorf("output %q does not report the %q state", out, bootunit.BootStateNotArmed)
	}
}

func TestRunArmedWithInvalidIdentityFails(t *testing.T) {
	// An armed decision puts the unit on the cordon path; an identity the
	// node identity client rejects (a non-https endpoint) fails it before
	// any network I/O, which is what makes this testable without a cluster.
	dir := nodestatetest.TempStateDir(t)
	j, err := bootunit.NewJournal(dir)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	if err := j.WriteArmingState(context.Background(), &bootunit.ArmingState{
		Version:      1,
		Phase:        bootunit.PhaseArmed,
		NodeUID:      "uid-1",
		IncidentID:   "incident-1",
		BeforeBootID: "an-earlier-boot",
	}); err != nil {
		t.Fatalf("WriteArmingState: %v", err)
	}

	code, out := captureStdout(t, func() int {
		return runWithArgs(t, "-state-dir", dir,
			"-node-name", "worker-0", "-uid", "uid-1", "-api-server", "http://not-https")
	})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for an armed boot that cannot cordon", code)
	}
	// An armed boot that never cordons must still report a state: this is the
	// path where the identity client cannot even be built, and leaving it
	// silent made the "every outcome is greppable" contract untrue.
	if !strings.Contains(out, string(bootunit.BootStateCordonFailed)) {
		t.Errorf("output %q does not report the %q state", out, bootunit.BootStateCordonFailed)
	}
}

func TestRunFailsClosedOnMissingJournal(t *testing.T) {
	// No journal at all: the zero snapshot is unusable and the decision
	// arms. The same invalid endpoint keeps the failure offline.
	dir := nodestatetest.TempStateDir(t)

	code, _ := captureStdout(t, func() int {
		return runWithArgs(t, "-state-dir", dir,
			"-node-name", "worker-0", "-uid", "uid-1", "-api-server", "http://not-https")
	})
	if code != 1 {
		t.Errorf("exit code = %d, want 1: a missing journal must fail closed into the armed path", code)
	}
}

func TestRunRejectsUnknownFlags(t *testing.T) {
	if code := runWithArgs(t, "-bogus"); code != 1 {
		t.Errorf("exit code = %d, want 1 for a flag parse failure", code)
	}
}
