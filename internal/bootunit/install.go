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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/medik8s/common/pkg/nodestate"
)

// Install installs the boot unit binary and systemd service on the host.
// It is called by the agent at startup (and at every agent restart) to
// reconcile the wiring: binary present and hash-correct, unit file present,
// unit enabled.
//
// The agent runs in a privileged container with the host filesystem mounted.
// The binary is embedded in the agent image and copied to /usr/local/bin —
// the writable, SELinux-executable path on OSTree systems (see
// HostBinaryPath); the unit file is written to the host systemd directory.
func Install(hostSystemdDir string) error {
	if hostSystemdDir == "" {
		hostSystemdDir = "/etc/systemd/system"
	}

	// 1. Ensure the state root exists, and make the creation durable: without
	// a directory sync, a power loss can leave the journal and the boot unit
	// binary with nowhere to live.
	if err := os.MkdirAll(HostStateRoot, 0700); err != nil {
		return fmt.Errorf("create state root %s: %w", HostStateRoot, err)
	}
	if err := syncDir(filepath.Dir(HostStateRoot)); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(HostStateRoot), err)
	}

	// The binary's directory must exist too; on OSTree systems /usr/local is
	// a bind mount of /var/usrlocal and is present, but do not assume it.
	if err := os.MkdirAll(filepath.Dir(ContainerHostBinPath), 0755); err != nil {
		return fmt.Errorf("create binary directory %s: %w", filepath.Dir(ContainerHostBinPath), err)
	}

	// 2. Copy the binary from the container image to the host (through the
	// /host/usr/local mount; see ContainerHostBinPath).
	binChanged, err := copyIfChanged(ContainerBinaryPath, ContainerHostBinPath, binaryMode)
	if err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if binChanged {
		_ = logf("boot unit binary installed at %s (host path %s)", ContainerHostBinPath, HostBinaryPath)
	}

	// 3. Write the systemd unit file.
	unitPath := filepath.Join(hostSystemdDir, UnitName)
	unitChanged, err := copyIfChanged(ContainerUnitPath, unitPath, 0644)
	if err != nil {
		return fmt.Errorf("install unit file: %w", err)
	}
	if unitChanged {
		_ = logf("boot unit unit file installed at %s", unitPath)
	}

	// 4. Reload systemd and enable the unit. This runs on every install
	// attempt, not only when a file changed: enablement is part of the
	// reconciled state, and a unit disabled out-of-band must be repaired
	// even when the binary and unit file are already correct. Both steps
	// are idempotent.
	if err := reloadAndEnable(hostSystemdDir); err != nil {
		return fmt.Errorf("enable %s: %w", UnitName, err)
	}

	return nil
}

// WriteIdentityEnv persists the node identity descriptor the boot unit loads
// through its EnvironmentFile directive: the node name, the Node object's
// UID, and a host-routable API server endpoint. The agent calls this at
// enrollment and whenever the identity changes — before it ever arms the
// boot. The unit declares the file required, so a node without a provisioned
// identity refuses to run the armed path rather than running it with empty
// fields.
func WriteIdentityEnv(nodeName, nodeUID, apiServer string) error {
	return writeIdentityEnv(HostIdentityEnvPath, nodeName, nodeUID, apiServer)
}

// writeIdentityEnv is WriteIdentityEnv with the destination path injected,
// so tests can exercise it without touching host paths.
func writeIdentityEnv(path, nodeName, nodeUID, apiServer string) error {
	if nodeName == "" || nodeUID == "" || apiServer == "" {
		return fmt.Errorf("identity descriptor requires node name, UID and API server, got %q, %q, %q",
			nodeName, nodeUID, apiServer)
	}
	content := fmt.Sprintf("NODE_NAME=%s\nNODE_UID=%s\nAPI_SERVER=%s\n", nodeName, nodeUID, apiServer)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create directory %s: %w", filepath.Dir(path), err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// The descriptor must survive the reboot it exists for.
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(path), err)
	}
	return nil
}

// CreateArmingFlag arms the boot unit's ConditionPathExists gate. The agent
// calls this from its arming path ONLY after the journal write the gate
// depends on has succeeded — the decision and arming state for a committed
// reboot, or the first durable journal write when the flag follows watchdog
// arming — so the flag's presence always implies a journal existed. The
// creation is durable, and idempotent: an existing flag is left as it is.
func CreateArmingFlag() error {
	return createArmingFlag(ArmingFlagPath, syncDir)
}

// createArmingFlag is CreateArmingFlag with the path and the directory-sync
// operation injected, so tests can exercise both without touching host paths
// and without depending on filesystem permissions to make a sync fail.
func createArmingFlag(path string, sync func(string) error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Already armed — but a previous creation attempt may have
			// failed to confirm the directory entry. Confirm it now, before
			// reporting success: one transient sync failure must not leave
			// the gate unconfirmed for the life of the process.
			if serr := sync(filepath.Dir(path)); serr != nil {
				return fmt.Errorf("sync %s: %w", filepath.Dir(path), serr)
			}
			return nil
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("close %s: %w", path, err)
	}
	// The gate must survive the reboot it exists for: flush the directory so
	// the new entry is durable. The file itself is empty; it carries no data.
	if err := sync(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(path), err)
	}
	return nil
}

// ClearArmingFlag removes the gate. The agent calls this at startup, once it
// has taken over protection duties for the boot: while the flag exists the
// boot unit runs on every boot and the journal tiers decide, and the agent
// replaces that with its own live monitoring. Idempotent.
func ClearArmingFlag() error {
	return clearArmingFlag(ArmingFlagPath)
}

// clearArmingFlag is ClearArmingFlag with the path injected, so tests can
// exercise it without touching host paths.
func clearArmingFlag(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	// The removal must survive a reboot too: without the directory sync, a
	// power loss could resurrect the flag and re-arm the next boot.
	return syncDir(filepath.Dir(path))
}

// Verify checks that the boot unit wiring is present and correct.
// Returns nil when everything is in place, or an error describing what is
// wrong. The agent reports degraded rejoin protection; it never silently
// disables fencing.
func Verify(hostSystemdDir string) error {
	return VerifyFS(hostSystemdDir, nodestate.OSFileSystem())
}

// VerifyFS is Verify with the filesystem injected, using the same
// abstraction as the nodestate store, so tests can verify the wiring
// without touching host paths.
func VerifyFS(hostSystemdDir string, fsys nodestate.FS) error {
	if hostSystemdDir == "" {
		hostSystemdDir = "/etc/systemd/system"
	}

	// Binary exists (as this process sees the host: through the
	// /host/usr/local mount), and is executable: a correct binary with a
	// drifted mode fails at exec exactly like a missing one.
	fi, err := fsys.Stat(ContainerHostBinPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("boot unit binary not found at %s (host path %s)", ContainerHostBinPath, HostBinaryPath)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", ContainerHostBinPath, err)
	}
	if fi.Mode().Perm() != binaryMode {
		return fmt.Errorf("boot unit binary mode is %#o at %s, want %#o — it would fail to execute",
			fi.Mode().Perm(), ContainerHostBinPath, binaryMode)
	}

	// Unit file exists.
	unitPath := filepath.Join(hostSystemdDir, UnitName)
	if _, err := fsys.Stat(unitPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("systemd unit not found at %s", unitPath)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", unitPath, err)
	}

	// Binary hash matches the container image.
	containerHash, err := fileHashFS(fsys, ContainerBinaryPath)
	if err != nil {
		return fmt.Errorf("read container binary: %w", err)
	}
	hostHash, err := fileHashFS(fsys, ContainerHostBinPath)
	if err != nil {
		return fmt.Errorf("read host binary: %w", err)
	}
	if containerHash != hostHash {
		return fmt.Errorf("boot unit binary hash mismatch (host %s != container %s)",
			hostHash[:8], containerHash[:8])
	}

	return nil
}

// binaryMode is the mode installed host executables carry. A binary whose
// content is correct but whose mode has drifted to something non-executable
// fails at exactly the same step as a missing one (systemd 203/EXEC), so the
// mode is reconciled and verified alongside the content.
const binaryMode os.FileMode = 0o755

// copyIfChanged copies a file only when the destination is missing or its
// hash differs from the source. A destination with matching content but the
// wrong mode has the mode reconciled instead of being skipped. Returns true
// if a copy was made.
func copyIfChanged(src, dst string, mode os.FileMode) (bool, error) {
	srcHash, err := fileHash(src)
	if err != nil {
		return false, fmt.Errorf("read source %s: %w", src, err)
	}

	dstHash, err := fileHash(dst)
	if err == nil && srcHash == dstHash {
		// Content already correct. Reconcile the mode: a matching hash with
		// a non-executable destination would fail at exec exactly like a
		// missing binary, and skipping here would leave that state both
		// unrepaired and undetected.
		if fi, serr := os.Stat(dst); serr == nil && fi.Mode().Perm() != mode {
			if cerr := os.Chmod(dst, mode); cerr != nil {
				return false, fmt.Errorf("chmod %s to %#o: %w", dst, mode, cerr)
			}
		}
		// A previous copy may have completed the rename but failed to
		// confirm the directory entry; confirm it before reporting an
		// already-correct destination, so one transient sync failure does
		// not downgrade every later install to an unconfirmed success.
		if serr := syncDir(filepath.Dir(dst)); serr != nil {
			return false, fmt.Errorf("sync %s: %w", filepath.Dir(dst), serr)
		}
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read destination %s: %w", dst, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return false, err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return false, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return false, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}

	if err := os.Rename(tmp, dst); err != nil {
		return false, err
	}
	// The replacement is only durable once the directory entry is flushed:
	// a power loss after the file sync but before this would leave the
	// destination missing and the new bytes orphaned under the temp name.
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return true, fmt.Errorf("sync %s after rename: %w", filepath.Dir(dst), err)
	}
	return true, nil
}

// fileHash returns the SHA-256 hex digest of a file, or an error if the
// file cannot be read.
func fileHash(path string) (string, error) {
	return fileHashFS(nodestate.OSFileSystem(), path)
}

// fileHashFS is fileHash over an injected filesystem.
func fileHashFS(fsys nodestate.FS, path string) (string, error) {
	f, err := fsys.Open(path, nodestate.OpenOptions{})
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// syncDir flushes a directory's entries to stable storage, so that a rename
// or creation in it survives a power loss.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// reloadAndEnable runs daemon-reload and enables the unit. These commands
// run on the host filesystem because the agent container has the host's
// systemd directory mounted at the given path.
func reloadAndEnable(hostSystemdDir string) error {
	// daemon-reload
	reload := exec.Command("systemctl", "daemon-reload")
	reload.Env = append(os.Environ(),
		fmt.Sprintf("SYSTEMD_UNIT_PATH=%s", hostSystemdDir),
	)
	if out, err := reload.CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %v: %s", err, string(out))
	}

	// enable (idempotent — safe to run on every agent start)
	enable := exec.Command("systemctl", "enable", UnitName)
	enable.Env = append(os.Environ(),
		fmt.Sprintf("SYSTEMD_UNIT_PATH=%s", hostSystemdDir),
	)
	if out, err := enable.CombinedOutput(); err != nil {
		return fmt.Errorf("enable %s: %v: %s", UnitName, err, string(out))
	}

	return nil
}

// logf is a minimal logger for the install path. The agent provides its own
// structured logging; this is sufficient for the host-side installation,
// which writes to stdout (captured in the systemd journal via the agent pod).
func logf(format string, args ...interface{}) error {
	_, err := fmt.Printf("[sbr-bootunit] "+format+"\n", args...)
	return err
}
