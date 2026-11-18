/*
Copyright 2025.

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

// Tests for RHWA-2065 (AGT-01): exactly one host process may control the watchdog
// and write the journal, enforced by a non-blocking exclusive flock on a lock file
// that is separate from the atomically replaced journal and is never unlinked while
// a process may hold it.
//
// These tests describe the required behaviour of the not-yet-written hostlock
// package. They are expected to fail until RHWA-2065 is implemented.
package hostlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// helperEnvVar makes the test binary re-execute itself as a stand-in for a second
// agent process on the same host.
const helperEnvVar = "SBR_HOSTLOCK_TEST_HELPER_PATH"

// TestDefaultLockPathMatchesDesign pins the lock file location. The lock file must be
// separate from the journal so that the journal's atomic replace (write temp, rename)
// never destroys the lock.
func TestDefaultLockPathMatchesDesign(t *testing.T) {
	const wantDir = "/var/lib/medik8s/state"
	const wantPath = wantDir + "/sbr-local-agent.lock"

	if DefaultLockPath != wantPath {
		t.Errorf("DefaultLockPath = %q, want %q", DefaultLockPath, wantPath)
	}
	if DefaultStateDir != wantDir {
		t.Errorf("DefaultStateDir = %q, want %q", DefaultStateDir, wantDir)
	}
	if filepath.Base(DefaultLockPath) == "sbr.json" {
		t.Error("lock file must not be the journal file")
	}
}

// TestAcquireSucceedsOnFreeHost covers the normal case: no other process holds the
// lock, so the local agent takes it and may go on to open the watchdog.
func TestAcquireSucceedsOnFreeHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() on a free host returned error: %v", err)
	}
	if lock == nil {
		t.Fatal("Acquire() returned a nil lock and no error")
	}
	t.Cleanup(func() { _ = lock.Release() })

	if lock.Path() != path {
		t.Errorf("lock.Path() = %q, want %q", lock.Path(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file was not created: %v", err)
	}
}

// TestAcquireCreatesMissingStateDir covers first start on a node where the state
// directory does not exist yet. The directory must be private to root.
func TestAcquireCreatesMissingStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "sbr-local-agent.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() with a missing state directory returned error: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("state directory was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state directory mode = %#o, want 0700", got)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("lock file mode = %#o, want 0600", got)
	}
}

// TestSecondAcquireInSameProcessReportsConflict is the single-process form of the
// acceptance criterion: a second attempt must not quietly succeed.
func TestSecondAcquireInSameProcessReportsConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire() returned error: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	second, err := Acquire(path)
	if err == nil {
		_ = second.Release()
		t.Fatal("second Acquire() succeeded while the lock was held; it must report a conflict")
	}
	if second != nil {
		t.Error("second Acquire() returned a non-nil lock together with an error")
	}
	if !errors.Is(err, ErrLocalAgentConflict) {
		t.Errorf("second Acquire() error = %v, want ErrLocalAgentConflict", err)
	}
	if !IsConflict(err) {
		t.Errorf("IsConflict(%v) = false, want true", err)
	}
}

// TestSecondProcessReportsConflict is the acceptance criterion as written: two agent
// processes on one host, the second writes nothing and reports LocalAgentConflict.
func TestSecondProcessReportsConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	held, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() in the first process returned error: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	out, err := runHelper(t, path)
	if err == nil {
		t.Fatalf("second process acquired the lock while it was held; output: %s", out)
	}
	if !strings.Contains(out, ReasonLocalAgentConflict) {
		t.Errorf("second process output = %q, want it to report %q", out, ReasonLocalAgentConflict)
	}
}

// TestReasonConstantMatchesTicket pins the reason string the agent reports through
// status and events when it cannot take the lock.
func TestReasonConstantMatchesTicket(t *testing.T) {
	if ReasonLocalAgentConflict != "LocalAgentConflict" {
		t.Errorf("ReasonLocalAgentConflict = %q, want %q", ReasonLocalAgentConflict, "LocalAgentConflict")
	}
}

// TestReleaseDoesNotUnlinkLockFile covers "never unlinked during operation". Removing
// the file would let a later process take a lock on a different inode and arm a second
// watchdog.
func TestReleaseDoesNotUnlinkLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() returned error: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() returned error: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file was removed by Release(): %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("lock file was replaced by Release(); it must keep the same inode")
	}
}

// TestReleaseAllowsReacquire covers planned agent replacement: once the holder is gone
// the successor may take the lock, on the same file.
func TestReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire() returned error: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() returned error: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() after Release() returned error: %v", err)
	}
	t.Cleanup(func() { _ = second.Release() })
}

// TestLockIsReleasedWhenHolderProcessExits covers unplanned agent death: the kernel
// drops the flock, so the replacement agent can take over without anyone breaking
// the lock by hand.
func TestLockIsReleasedWhenHolderProcessExits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	if out, err := runHelper(t, path); err != nil {
		t.Fatalf("helper process failed to acquire a free lock: %v, output: %s", err, out)
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() after the holder exited returned error: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
}

// TestReleaseIsIdempotent keeps shutdown paths simple: the agent may release on more
// than one path without turning that into an error.
func TestReleaseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbr-local-agent.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() returned error: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release() returned error: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second Release() returned error: %v, want nil", err)
	}
}

// TestUnusableLockPathReportsConflict covers the acceptance criterion about a
// filesystem that cannot lock: any failure to establish the lock is a conflict, never
// a silent success.
func TestUnusableLockPathReportsConflict(t *testing.T) {
	base := t.TempDir()
	readOnlyDir := filepath.Join(base, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("failed to create the read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("running as root ignores directory permissions")
	}

	lock, err := Acquire(filepath.Join(readOnlyDir, "sbr-local-agent.lock"))
	if err == nil {
		_ = lock.Release()
		t.Fatal("Acquire() succeeded on a lock path it cannot write; it must report a conflict")
	}
	if !errors.Is(err, ErrLocalAgentConflict) {
		t.Errorf("Acquire() error = %v, want ErrLocalAgentConflict", err)
	}
}

// TestAcquireRejectsADirectory guards against pointing the lock at the state directory
// itself, which would make the conflict check meaningless.
func TestAcquireRejectsADirectory(t *testing.T) {
	dir := t.TempDir()

	lock, err := Acquire(dir)
	if err == nil {
		_ = lock.Release()
		t.Fatal("Acquire() succeeded on a directory; it must report a conflict")
	}
	if !errors.Is(err, ErrLocalAgentConflict) {
		t.Errorf("Acquire() error = %v, want ErrLocalAgentConflict", err)
	}
}

// runHelper re-executes this test binary as a second process that tries to take the
// lock at path, prints the outcome, and exits. It returns the combined output.
func runHelper(t *testing.T, path string) (string, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperAcquiresLock", "-test.v")
	cmd.Env = append(os.Environ(), helperEnvVar+"="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestHelperAcquiresLock is not a test. It is the body of the helper process started
// by runHelper, and it does nothing unless that environment variable is set.
func TestHelperAcquiresLock(t *testing.T) {
	path := os.Getenv(helperEnvVar)
	if path == "" {
		t.Skip("not running as the helper process")
	}

	lock, err := Acquire(path)
	if err != nil {
		if IsConflict(err) {
			t.Fatalf("%s: %v", ReasonLocalAgentConflict, err)
		}
		t.Fatalf("Acquire() returned an unexpected error: %v", err)
	}
	_ = lock.Release()
}
