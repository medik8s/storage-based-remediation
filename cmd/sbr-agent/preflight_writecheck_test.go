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

// Tests for the block-mode write check (performSBRBlockWriteTest) and the pre-flight sentinel
// (createPreflightSentinelAt)

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockformat"
)

// formatTestBlockDevice creates a temp file of blockformat.BlockMinDeviceSize and runs the
// same init path the sbr-agent --init command uses, returning the path and parsed superblock.
func formatTestBlockDevice(t *testing.T) (string, *blockformat.Superblock) {
	t.Helper()
	path := createInitTestDevice(t, blockformat.BlockMinDeviceSize)
	if err := runInit(path, 30*time.Second, logr.Discard()); err != nil {
		t.Fatalf("failed to format test block device: %v", err)
	}

	isBlock, sb, err := probeBlockModeAt(path, 5*time.Second)
	if err != nil {
		t.Fatalf("probeBlockModeAt failed on freshly formatted device: %v", err)
	}
	if !isBlock || sb == nil {
		t.Fatalf("expected freshly formatted device to probe as block mode, got isBlock=%v sb=%v", isBlock, sb)
	}
	return path, sb
}

// TestPerformSBRBlockWriteTest_Success verifies the happy path: a node writes its own
// heartbeat slot and reads it back successfully.
func TestPerformSBRBlockWriteTest_Success(t *testing.T) {
	path, sb := formatTestBlockDevice(t)

	if err := performSBRBlockWriteTest(path, sb, 1, 5*time.Second); err != nil {
		t.Fatalf("expected block-mode write/read-back test to succeed, got: %v", err)
	}
}

// TestPerformSBRBlockWriteTest_DifferentNodeIDsUseDifferentSlots verifies two different
// nodeIDs write to distinct slots and don't clobber each other's data.
func TestPerformSBRBlockWriteTest_DifferentNodeIDsUseDistinctSlots(t *testing.T) {
	path, sb := formatTestBlockDevice(t)

	if err := performSBRBlockWriteTest(path, sb, 1, 5*time.Second); err != nil {
		t.Fatalf("node 1 write test failed: %v", err)
	}
	if err := performSBRBlockWriteTest(path, sb, 2, 5*time.Second); err != nil {
		t.Fatalf("node 2 write test failed: %v", err)
	}
	// Re-running node 1's test after node 2 wrote its own slot proves node 2's write did not
	// disturb node 1's slot (each nodeID maps to its own offset).
	if err := performSBRBlockWriteTest(path, sb, 1, 5*time.Second); err != nil {
		t.Fatalf("node 1 write test failed after node 2 wrote its slot: %v", err)
	}
}

// TestPerformSBRBlockWriteTest_DeviceGone verifies the write check fails loudly (does not
// hang, does not silently pass) when the device disappears.
func TestPerformSBRBlockWriteTest_DeviceGone(t *testing.T) {
	path, sb := formatTestBlockDevice(t)
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove test device: %v", err)
	}

	if err := performSBRBlockWriteTest(path, sb, 1, 5*time.Second); err == nil {
		t.Fatal("expected block-mode write test to fail when the device is gone, but it succeeded")
	}
}

// TestPerformSBRBlockWriteTest_ReadOnlyDevice verifies the write check fails loudly (does not
// hang, does not silently pass) when the device cannot be opened for writing, which is the
// same failure shape a coordinator-exclusive backend produces for every non-owning node.
func TestPerformSBRBlockWriteTest_ReadOnlyDevice(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: read-only file permissions don't block writes")
	}

	path, sb := formatTestBlockDevice(t)
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("failed to make test device read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if err := performSBRBlockWriteTest(path, sb, 1, 5*time.Second); err == nil {
		t.Fatal("expected block-mode write test to fail against a read-only device, but it succeeded")
	}
}

// TestCreatePreflightSentinelAt_CreatesFileAndParentDir verifies the sentinel file is created
// with the expected content, including creating a parent directory that does not yet exist.
func TestCreatePreflightSentinelAt_CreatesFileAndParentDir(t *testing.T) {
	sentinelPath := filepath.Join(t.TempDir(), "nested", "sbr", "preflight-ok")

	if err := createPreflightSentinelAt(sentinelPath); err != nil {
		t.Fatalf("expected sentinel creation to succeed, got: %v", err)
	}

	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("expected sentinel file to exist and be readable, got: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("unexpected sentinel content: %q", string(data))
	}
}

// TestCreatePreflightSentinelAt_FailsWhenParentIsUnwritable verifies sentinel creation returns
// an error (instead of panicking or silently succeeding) when its directory cannot be created.
// This is the failure path main() checks before deciding whether to exit.
func TestCreatePreflightSentinelAt_FailsWhenParentIsUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks on directories don't apply")
	}

	readOnlyParent := t.TempDir()
	if err := os.Chmod(readOnlyParent, 0o555); err != nil {
		t.Fatalf("failed to make temp dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyParent, 0o755) })

	sentinelPath := filepath.Join(readOnlyParent, "sbr", "preflight-ok")

	if err := createPreflightSentinelAt(sentinelPath); err == nil {
		t.Fatal("expected sentinel creation to fail under a read-only parent directory, but it succeeded")
	}
}

// TestCreatePreflightSentinelAt_NeverCreatedOnPreflightFailure verifies the safety property the
// design relies on: runPreflightChecks failing must mean the sentinel is never created, so the
// readiness probe (which waits on the sentinel) can never see a false "pass".
func TestCreatePreflightSentinelAt_NeverCreatedOnPreflightFailure(t *testing.T) {
	if err := initializeLogger("info"); err != nil {
		t.Fatalf("failed to initialize logger: %v", err)
	}

	tmpDir := t.TempDir()
	watchdogPath := filepath.Join(tmpDir, "watchdog")
	sentinelPath := filepath.Join(tmpDir, "run", "sbr", "preflight-ok")

	// A previous successful execution must not make this failed execution ready.
	if err := createPreflightSentinelAt(sentinelPath); err != nil {
		t.Fatal(err)
	}
	if err := resetPreflightSentinelAt(sentinelPath); err != nil {
		t.Fatal(err)
	}

	// No watchdog file and no SBR device: preflight must fail.
	err := runPreflightChecks(watchdogPath, "", "test-node", 1, false)
	if err == nil {
		t.Fatal("expected runPreflightChecks to fail with no watchdog and no SBR device")
	}

	// Mirror main()'s ordering: only create the sentinel when preflight succeeds.
	if err == nil {
		if sentinelErr := createPreflightSentinelAt(sentinelPath); sentinelErr != nil {
			t.Fatalf("unexpected sentinel creation error: %v", sentinelErr)
		}
	}

	if _, statErr := os.Stat(sentinelPath); statErr == nil {
		t.Fatal("sentinel file must not exist after a failed pre-flight check")
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("unexpected error checking for sentinel file: %v", statErr)
	}
}

func TestResetPreflightSentinelAt_MissingFile(t *testing.T) {
	if err := resetPreflightSentinelAt(filepath.Join(t.TempDir(), "missing", "preflight-ok")); err != nil {
		t.Fatal(err)
	}
}

func TestResetPreflightSentinelAt_PreservesOtherMarkers(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "sbr-agent", "preflight-ok")
	shared := filepath.Join(root, "sbr", "preflight-ok")
	other := filepath.Join(root, "other-agent", "preflight-ok")
	for _, path := range []string{local, shared, other} {
		if err := createPreflightSentinelAt(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := resetPreflightSentinelAt(local); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("expected local marker to be removed, got %v", err)
	}
	for _, path := range []string{shared, other} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated marker removed: %v", err)
		}
	}
}

func TestResetPreflightSentinelAt_ReportsRemovalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preflight-ok")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resetPreflightSentinelAt(path); err == nil {
		t.Fatal("expected failure removing a nonempty directory")
	}
}
