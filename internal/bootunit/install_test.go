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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/medik8s/common/pkg/nodestate"
)

// remapFS routes filesystem calls for the mapped paths into a test
// directory, so Verify can be exercised against the fixed host paths
// without touching the host.
type remapFS struct {
	nodestate.FS
	mapping map[string]string
}

func (r remapFS) mapPath(name string) string {
	if dst, ok := r.mapping[name]; ok {
		return dst
	}
	return name
}

func (r remapFS) Stat(name string) (os.FileInfo, error) { return r.FS.Stat(r.mapPath(name)) }
func (r remapFS) Lstat(name string) (os.FileInfo, error) {
	return r.FS.Lstat(r.mapPath(name))
}
func (r remapFS) Open(name string, opts nodestate.OpenOptions) (nodestate.File, error) {
	return r.FS.Open(r.mapPath(name), opts)
}

// verifyFixture stages a fake host in a temporary directory and returns an
// FS that remaps the boot unit's fixed paths into it. hostBinary and
// containerBinary are the file contents at those paths (empty string means
// the file is absent); unitFile likewise for the systemd unit.
func verifyFixture(t *testing.T, hostBinary, containerBinary, unitFile string) (nodestate.FS, string) {
	t.Helper()
	dir := t.TempDir()

	mapping := map[string]string{
		ContainerHostBinPath: filepath.Join(dir, "host-bin"),
		ContainerBinaryPath:  filepath.Join(dir, "container-bin"),
	}
	for _, f := range []struct{ content, path string }{
		{hostBinary, mapping[ContainerHostBinPath]},
		{containerBinary, mapping[ContainerBinaryPath]},
		{unitFile, filepath.Join(dir, UnitName)},
	} {
		if f.content == "" {
			continue // absent
		}
		if err := os.WriteFile(f.path, []byte(f.content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return remapFS{FS: nodestate.OSFileSystem(), mapping: mapping}, dir
}

func TestCopyIfChanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	content := []byte("test binary content")
	if err := os.WriteFile(src, content, 0755); err != nil {
		t.Fatal(err)
	}

	// First copy: file doesn't exist, should copy.
	changed, err := copyIfChanged(src, dst, 0755)
	if err != nil || !changed {
		t.Fatalf("first copy: changed=%v err=%v", changed, err)
	}

	// Second copy: same content, should skip.
	changed, err = copyIfChanged(src, dst, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second copy should be a no-op when hash matches")
	}

	// Third copy: changed source, should copy again.
	if err := os.WriteFile(src, []byte("changed content"), 0755); err != nil {
		t.Fatal(err)
	}
	changed, err = copyIfChanged(src, dst, 0755)
	if err != nil || !changed {
		t.Fatalf("third copy: changed=%v err=%v", changed, err)
	}
}

func TestCopyIfChangedSourceMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := copyIfChanged(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "dst"), 0755)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestFileHash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file")
	os.WriteFile(p, []byte("hello"), 0644)

	h1, err := fileHash(p)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := fileHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("same file must produce same hash")
	}

	os.WriteFile(p, []byte("world"), 0644)
	h3, err := fileHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h3 {
		t.Fatal("different content must produce different hash")
	}
}

func TestVerifyFS(t *testing.T) {
	tests := []struct {
		name       string
		hostBinary string
		contBinary string
		unitFile   string
		wantErr    bool
		wantErrIn  string
	}{
		{
			name:       "wiring complete",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n",
		},
		{
			name:       "host binary missing",
			hostBinary: "",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "boot unit binary not found",
		},
		{
			name:       "unit file missing",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "",
			wantErr:    true,
			wantErrIn:  "systemd unit not found",
		},
		{
			name:       "binary hash mismatch",
			hostBinary: "binary-v1",
			contBinary: "binary-v2",
			unitFile:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "hash mismatch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, unitDir := verifyFixture(t, tt.hostBinary, tt.contBinary, tt.unitFile)
			err := VerifyFS(unitDir, fsys)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrIn) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestWriteIdentityEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbr-identity.env")

	if err := writeIdentityEnv(path, "", "uid", "https://api:6443"); err == nil {
		t.Fatal("empty node name must be rejected")
	}
	if err := writeIdentityEnv(path, "node", "", "https://api:6443"); err == nil {
		t.Fatal("empty UID must be rejected")
	}
	if err := writeIdentityEnv(path, "node", "uid", ""); err == nil {
		t.Fatal("empty API server must be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected input must not write the file")
	}

	if err := writeIdentityEnv(path, "worker-0", "1111-2222", "https://api.example:6443"); err != nil {
		t.Fatalf("writeIdentityEnv: %v", err)
	}

	// The systemd EnvironmentFile format, with all three values.
	want := "NODE_NAME=worker-0\nNODE_UID=1111-2222\nAPI_SERVER=https://api.example:6443\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	// Root-only: the boot unit trusts this file for its identity.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %#o, want 0600", perm)
	}

	// No temp file left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind after write")
	}
}

func TestArmingFlagLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbr-armed")

	// Create is durable and 0600, like the rest of the state root.
	if err := createArmingFlag(path, syncDir); err != nil {
		t.Fatalf("createArmingFlag: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %#o, want 0600", perm)
	}

	// Create is idempotent: an existing flag is left alone.
	if err := createArmingFlag(path, syncDir); err != nil {
		t.Fatalf("createArmingFlag on an existing flag: %v", err)
	}

	// Clear removes it, and is idempotent too.
	if err := clearArmingFlag(path); err != nil {
		t.Fatalf("clearArmingFlag: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("flag survived the clear")
	}
	if err := clearArmingFlag(path); err != nil {
		t.Fatalf("clearArmingFlag on an absent flag: %v", err)
	}
}

func TestCopyIfChangedReconcilesModeOnMatchingHash(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	// Destination with correct content but a drifted, non-executable mode.
	if err := os.WriteFile(dst, []byte("binary"), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := copyIfChanged(src, dst, 0755)
	if err != nil {
		t.Fatalf("copyIfChanged: %v", err)
	}
	if changed {
		t.Error("no copy should be made when the hash matches")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0755 {
		t.Errorf("mode = %#o, want 0755 reconciled", perm)
	}
}

func TestVerifyFSDetectsNonExecutableMode(t *testing.T) {
	fsys, unitDir := verifyFixture(t, "binary-v1", "binary-v1", "[Unit]\n")
	// Drift the host binary's mode after the fixture: content still matches,
	// but the unit would fail 203/EXEC at exec.
	hostBin := filepath.Join(unitDir, "host-bin")
	if err := os.Chmod(hostBin, 0644); err != nil {
		t.Fatal(err)
	}
	err := VerifyFS(unitDir, fsys)
	if err == nil {
		t.Fatal("want error for a non-executable host binary")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("error %q does not mention the mode", err)
	}
}

func TestCreateArmingFlagConfirmsExistingEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbr-armed")
	if err := createArmingFlag(path, syncDir); err != nil {
		t.Fatal(err)
	}

	// The existing-flag branch must confirm the directory entry before
	// reporting success. Inject a failing sync and assert it is invoked
	// and its failure surfaced — no dependence on filesystem permissions,
	// which do not stop a sync running as root.
	synced := 0
	failSync := func(string) error {
		synced++
		return errors.New("directory sync failed")
	}
	if err := createArmingFlag(path, failSync); err == nil {
		t.Fatal("existing-flag branch must propagate the directory sync failure")
	}
	if synced != 1 {
		t.Errorf("injected sync called %d times, want 1", synced)
	}
}
