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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/medik8s/common/pkg/nodestate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// LabelControlPlaneRole is the well-known control-plane role label.
	LabelControlPlaneRole = "node-role.kubernetes.io/control-plane"
	// LabelMasterRole is the deprecated master role label, still set on
	// clusters upgraded from releases that used it.
	LabelMasterRole = "node-role.kubernetes.io/master"
)

// IsControlPlaneNode reports whether the Node carries a control-plane role.
//
// The boot unit must never be installed or gated on a control-plane node: it
// cordons the node before kubelet starts, and taking a control-plane member out
// of scheduling from a host service is outside SBR's remediation policy. The
// agent DaemonSet targets worker nodes, so this check is defence in depth for a
// Pod that ends up on a control-plane node anyway.
func IsControlPlaneNode(node *corev1.Node) bool {
	if node == nil {
		return false
	}
	if _, ok := node.Labels[LabelControlPlaneRole]; ok {
		return true
	}
	_, ok := node.Labels[LabelMasterRole]
	return ok
}

// Install installs the boot unit binary and systemd service on the host.
// It is called by the agent at startup (and at every agent restart) to
// reconcile the wiring: binary present and hash-correct, unit file present,
// unit enabled.
//
// ensureSecureDir brings a state directory in line with the medik8s state
// contract: present, and usable by its owner alone (mode 0700).
//
// The DaemonSet mounts the state root as a hostPath with DirectoryOrCreate,
// and kubelet creates missing hostPath directories with mode 0755 — always
// before the agent's first line of code runs. os.MkdirAll cannot tighten an
// existing directory, so without this step the nodestate security policy
// (correctly: it reports violations and never repairs them, by design)
// rejects every journal operation on a fresh node.
//
// The agent is the component that deployed that mount, so adopting the
// directory is its job. Adoption is strictly tightening: only the permission
// bits of an existing directory are changed. A symlink, a regular file, or
// any other surprise at the path is left exactly as found, for nodestate to
// fail closed on — and the library's file-level checks (mode 0600, owner)
// still apply to everything inside the directory.
func ensureSecureDir(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			// Not ours to repair: a symlink or a file at the state path is
			// exactly the tampering the policy exists to catch.
			return nil
		}
		if fi.Mode().Perm()&0o077 == 0 {
			return nil
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("tighten %s to 0700: %w", dir, err)
		}
		// Best effort: even if the tightened mode is lost to a power loss,
		// the next agent start tightens it again.
		if err := syncDir(dir); err != nil {
			return fmt.Errorf("sync %s after tightening its mode: %w", dir, err)
		}
		klog.Infof("bootunit: tightened state directory %s from %#o to 0700 (kubelet creates hostPath directories 0755)",
			dir, fi.Mode().Perm())
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create state directory %s: %w", dir, err)
		}
		// Durable creation: without the parent sync, a power loss can leave
		// the journal and the boot unit binary with nowhere to live.
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return fmt.Errorf("sync %s: %w", filepath.Dir(dir), err)
		}
		return nil
	default:
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
}

// EnsureStateRoot adopts the state root for the agent: it creates the
// directory with the contract mode when missing, and tightens the mode of a
// directory kubelet created 0755. The agent calls it before it opens the
// journal for writing; read-only consumers (the boot unit) do not, so they
// never write to the host.
func EnsureStateRoot() error {
	return ensureSecureDir(HostStateRoot)
}

// Install places the boot unit's binary and unit file on the host and
// activates them.
//
// The agent runs in a privileged container with the host filesystem mounted.
// The binary is embedded in the agent image and copied to /usr/local/bin —
// the writable, SELinux-executable path on OSTree systems (see
// HostBinaryPath); the unit file is written to the host systemd directory.
//
// hostSystemdDir is where THIS PROCESS sees the host's /etc/systemd/system
// (ContainerSystemdDir in the agent). Activation is separate: systemctl runs
// in the host namespace, where the same file appears at its real path.
func Install(hostSystemdDir string, timeout ...time.Duration) error {
	if hostSystemdDir == "" {
		hostSystemdDir = ContainerSystemdDir
	}

	// 1. Ensure the state root exists with the mode the medik8s state
	// contract requires. ensureSecureDir, not a bare MkdirAll: kubelet wins
	// the race and creates the hostPath directory 0755 first.
	if err := ensureSecureDir(HostStateRoot); err != nil {
		return fmt.Errorf("secure state root %s: %w", HostStateRoot, err)
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
	template, err := os.ReadFile(ContainerUnitPath)
	if err != nil {
		return fmt.Errorf("read container unit file: %w", err)
	}
	rendered, err := RenderUnit(template, timeout...)
	if err != nil {
		return err
	}
	unitHash := sha256.Sum256(rendered)
	unitChanged, err := writeIfChanged(bytes.NewReader(rendered), hex.EncodeToString(unitHash[:]), unitPath, 0644)
	if err != nil {
		return fmt.Errorf("install unit file: %w", err)
	}
	if unitChanged {
		_ = logf("boot unit unit file installed at %s", unitPath)
	}

	// 4. Enable the unit. This runs on every install attempt, not only
	// when a file changed: enablement is part of the reconciled state, and
	// a unit disabled out-of-band must be repaired even when the binary
	// and unit file are already correct. Idempotent.
	if err := enableUnit(hostSystemdDir); err != nil {
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
	for _, v := range []struct{ field, value string }{
		{"node name", nodeName},
		{"node UID", nodeUID},
		{"API server", apiServer},
	} {
		if err := validateIdentityValue(v.value); err != nil {
			return fmt.Errorf("identity descriptor %s: %w", v.field, err)
		}
	}
	content := fmt.Sprintf("NODE_NAME=%s\nNODE_UID=%s\nAPI_SERVER=%s\n", nodeName, nodeUID, apiServer)

	if err := ensureSecureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("secure directory %s: %w", filepath.Dir(path), err)
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

// validateIdentityValue rejects values the systemd EnvironmentFile format
// cannot carry safely: control characters, whitespace and quotes could inject
// additional assignments or break the unit's variable expansion. Legitimate
// values — DNS-1123 node names, UID strings and https URLs — are printable
// and contain none of those.
func validateIdentityValue(v string) error {
	for _, r := range v {
		if r < 0x21 || r == 0x7f || r == '"' || r == '\'' || r == '\\' {
			return fmt.Errorf("value %q contains %q, which the EnvironmentFile format cannot carry safely", v, r)
		}
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

// ClearArmingFlag removes the gate. Today the agent calls this only in
// detect-only mode: an agent that remediates nothing must not leave a gate
// that cordons a node on its behalf. In remediation mode the flag is never
// cleared once created — the unit is meant to run on every boot and let the
// journal tiers decide. Idempotent.
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
func Verify(hostSystemdDir string, timeout ...time.Duration) error {
	return VerifyFS(hostSystemdDir, nodestate.OSFileSystem(), timeout...)
}

// VerifyFS is Verify with the filesystem injected, using the same
// abstraction as the nodestate store, so tests can verify the wiring
// without touching host paths.
func VerifyFS(hostSystemdDir string, fsys nodestate.FS, timeout ...time.Duration) error {
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

	// Compare the installed unit with the template rendered for this timeout.
	containerUnit, err := fsys.Open(ContainerUnitPath, nodestate.OpenOptions{})
	if err != nil {
		return fmt.Errorf("read container unit file: %w", err)
	}
	defer containerUnit.Close()
	template, err := io.ReadAll(containerUnit)
	if err != nil {
		return fmt.Errorf("read container unit file: %w", err)
	}
	rendered, err := RenderUnit(template, timeout...)
	if err != nil {
		return err
	}
	unitHash := sha256.Sum256(rendered)
	containerUnitHash := hex.EncodeToString(unitHash[:])
	hostUnitHash, err := fileHashFS(fsys, unitPath)
	if err != nil {
		return fmt.Errorf("read unit file: %w", err)
	}
	if containerUnitHash != hostUnitHash {
		return fmt.Errorf("systemd unit file hash mismatch (host %s != container %s)",
			hostUnitHash[:8], containerUnitHash[:8])
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
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	return writeIfChanged(in, srcHash, dst, mode)
}

func writeIfChanged(in io.Reader, srcHash, dst string, mode os.FileMode) (bool, error) {
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

// RenderUnit applies the configured timeout to the command and service ceiling.
// Callers without a timeout retain the previous 90-second and 120-second limits.
func RenderUnit(template []byte, configured ...time.Duration) ([]byte, error) {
	timeout := CordonTotalBudget
	ceiling := int64(120)
	if len(configured) > 0 {
		timeout = configured[0]
		seconds := timeout / time.Second
		if timeout%time.Second != 0 {
			seconds++
		}
		ceiling = int64(seconds) + 5
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("boot-unit-timeout must be positive")
	}
	rendered := strings.ReplaceAll(string(template), "@BOOT_UNIT_TIMEOUT@", timeout.String())
	rendered = strings.ReplaceAll(rendered, "@BOOT_UNIT_CEILING@", fmt.Sprint(ceiling))
	return []byte(rendered), nil
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

// enableUnit makes systemd start the unit at boot, without talking to the
// running manager: enablement is a symlink to the unit inside the Wants
// directory of the unit's WantedBy target — exactly what "systemctl enable"
// creates — and systemd scans /etc/systemd/system afresh at every boot.
//
// Reaching the host's manager from the agent container does not work
// (live-verified on OCP 4.22 / RHCOS): nsenter into the host mount namespace
// exposes /run/systemd/private, but connecting from a different PID namespace
// fails with "Failed to connect to bus: No data available" — the peer
// credentials cannot translate — and joining the host PID namespace is not
// permitted for a process that is not a child of one inside it. Nothing is
// lost by skipping the manager: daemon-reload only teaches the RUNNING
// manager about the unit, and the unit's whole purpose is the NEXT boot.
//
// The symlink is reconciled on every call: a unit disabled out of band is
// re-enabled, and a wants entry that is not the right symlink is replaced.
func enableUnit(systemdDir string) error {
	wantsDir := filepath.Join(systemdDir, UnitWantsDir)
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", wantsDir, err)
	}

	link := filepath.Join(wantsDir, UnitName)
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if existing, err := os.Readlink(link); err == nil && existing == HostUnitPath {
				// Already enabled — but a previous enable may have created
				// the symlink without confirming its directory entry. Confirm
				// it now before reporting success: one transient sync failure
				// must not leave the enablement unconfirmed for the life of
				// the process (the same rule createArmingFlag applies to the
				// gate). The symlink itself is left intact.
				if serr := syncDir(wantsDir); serr != nil {
					return fmt.Errorf("sync %s: %w", wantsDir, serr)
				}
				return nil
			}
		}
		// A wrong target, or a wants entry that is not a symlink at all:
		// replace it.
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("replace %s: %w", link, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", link, err)
	}

	if err := os.Symlink(HostUnitPath, link); err != nil {
		return fmt.Errorf("link %s: %w", link, err)
	}
	// Durable enablement: without the directory sync, a power loss can
	// leave the unit installed but not enabled.
	if err := syncDir(wantsDir); err != nil {
		return fmt.Errorf("sync %s: %w", wantsDir, err)
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
