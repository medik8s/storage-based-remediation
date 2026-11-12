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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Install installs the boot unit binary and systemd service on the host.
// It is called by the agent at startup (and at every agent restart) to
// reconcile the wiring: binary present and hash-correct, unit file present
// and enabled.
//
// The agent runs in a privileged container with the host filesystem mounted.
// The binary is embedded in the agent image and copied to the host state
// root; the unit file is written to the host systemd directory.
func Install(hostSystemdDir string) error {
	if hostSystemdDir == "" {
		hostSystemdDir = "/etc/systemd/system"
	}

	// 1. Ensure the state root exists.
	if err := os.MkdirAll(HostStateRoot, 0700); err != nil {
		return fmt.Errorf("create state root %s: %w", HostStateRoot, err)
	}

	// 2. Copy the binary from the container to the host.
	binChanged, err := copyIfChanged(ContainerBinaryPath, HostBinaryPath, 0755)
	if err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if binChanged {
		_ = logf("boot unit binary installed at %s", HostBinaryPath)
	}

	// 3. Write the systemd unit file.
	unitPath := filepath.Join(hostSystemdDir, UnitName)
	unitChanged, err := copyIfChanged(ContainerUnitPath, unitPath, 0644)
	if err != nil {
		return fmt.Errorf("install unit file: %w", err)
	}

	// 4. Reload systemd and enable the unit.
	if binChanged || unitChanged {
		if err := reloadAndEnable(hostSystemdDir); err != nil {
			return fmt.Errorf("enable %s: %w", UnitName, err)
		}
		_ = logf("boot unit %s installed and enabled", UnitName)
	}

	return nil
}

// Verify checks that the boot unit wiring is present and correct.
// Returns nil when everything is in place, or an error describing what is
// wrong. The agent reports degraded rejoin protection; it never silently
// disables fencing.
func Verify(hostSystemdDir string) error {
	if hostSystemdDir == "" {
		hostSystemdDir = "/etc/systemd/system"
	}

	// Binary exists.
	if _, err := os.Stat(HostBinaryPath); os.IsNotExist(err) {
		return fmt.Errorf("boot unit binary not found at %s", HostBinaryPath)
	}

	// Unit file exists.
	unitPath := filepath.Join(hostSystemdDir, UnitName)
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return fmt.Errorf("systemd unit not found at %s", unitPath)
	}

	// Binary hash matches the container image.
	containerHash, err := fileHash(ContainerBinaryPath)
	if err != nil {
		return fmt.Errorf("read container binary: %w", err)
	}
	hostHash, err := fileHash(HostBinaryPath)
	if err != nil {
		return fmt.Errorf("read host binary: %w", err)
	}
	if containerHash != hostHash {
		return fmt.Errorf("boot unit binary hash mismatch (host %s != container %s)",
			hostHash[:8], containerHash[:8])
	}

	return nil
}

// copyIfChanged copies a file only when the destination is missing or its
// hash differs from the source. Returns true if a copy was made.
func copyIfChanged(src, dst string, mode os.FileMode) (bool, error) {
	srcHash, err := fileHash(src)
	if err != nil {
		return false, fmt.Errorf("read source %s: %w", src, err)
	}

	dstHash, err := fileHash(dst)
	if err == nil && srcHash == dstHash {
		return false, nil // already correct
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

	return true, os.Rename(tmp, dst)
}

// fileHash returns the SHA-256 hex digest of a file, or an error if the
// file cannot be read.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
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
