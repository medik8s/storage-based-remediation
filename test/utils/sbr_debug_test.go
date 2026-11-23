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

package utils

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestExecInPodIsBounded pins the fix for the e2e hang: a kubectl exec that
// never returns on its own — an exec blocked on uninterruptible storage I/O
// from a node whose storage the test itself disrupted — must be cut off by
// the deadline, with an error that names the timeout, instead of hanging the
// test binary until the CI job's step timeout.
func TestExecInPodIsBounded(t *testing.T) {
	origTimeout, origRun := execInPodTimeout, runKubectlExec
	t.Cleanup(func() { execInPodTimeout, runKubectlExec = origTimeout, origRun })

	execInPodTimeout = 100 * time.Millisecond
	runKubectlExec = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		// An exec blocked on storage I/O returns only when the context
		// kills it, if at all.
		<-ctx.Done()
		return ctx.Err()
	}

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, _, err = (&TestClients{}).execInPod("pod-a", "ns",
			[]string{"dd", "if=/dev/sbr/sbr-device-fence", "bs=512", "count=255", "status=none"})
	}()

	select {
	case <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("want a timeout error naming the deadline, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execInPod did not return; the deadline must bound a blocked exec")
	}
}

// TestExecInPodPropagatesNonTimeoutErrors keeps the two failure modes
// distinguishable: an exec that fails for an ordinary reason must surface
// kubectl's error, not be reported as a timeout.
func TestExecInPodPropagatesNonTimeoutErrors(t *testing.T) {
	origRun := runKubectlExec
	t.Cleanup(func() { runKubectlExec = origRun })

	runKubectlExec = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return io.ErrClosedPipe
	}

	_, _, err := (&TestClients{}).execInPod("pod-a", "ns", []string{"cat", "/dev/sbr/sbr-device-nodemap"})
	if err == nil || !strings.Contains(err.Error(), "kubectl exec failed") {
		t.Fatalf("want the underlying kubectl error, got %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("an ordinary failure must not be reported as a timeout: %v", err)
	}
}
