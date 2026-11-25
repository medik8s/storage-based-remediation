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

package bootunit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodestate"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIsControlPlaneNode pins the guard that keeps the pre-kubelet cordon off
// control-plane nodes. The unit would cordon before kubelet starts, which must
// never happen outside SBR's worker remediation policy.
func TestIsControlPlaneNode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "control-plane role", labels: map[string]string{LabelControlPlaneRole: ""}, want: true},
		{name: "master role on an upgraded cluster", labels: map[string]string{LabelMasterRole: ""}, want: true},
		{name: "worker role", labels: map[string]string{"node-role.kubernetes.io/worker": ""}, want: false},
		{name: "no role labels", labels: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: tc.labels}}
			if got := IsControlPlaneNode(node); got != tc.want {
				t.Errorf("IsControlPlaneNode(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}

	if IsControlPlaneNode(nil) {
		t.Error("a nil Node must not be reported as a control-plane node")
	}
}

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
// the file is absent); unitFile likewise for the systemd unit, and
// containerUnit for the unit template inside the agent image.
func verifyFixture(t *testing.T, hostBinary, containerBinary, unitFile, containerUnit string) (nodestate.FS, string) {
	t.Helper()
	dir := t.TempDir()

	mapping := map[string]string{
		ContainerHostBinPath: filepath.Join(dir, "host-bin"),
		ContainerBinaryPath:  filepath.Join(dir, "container-bin"),
		ContainerUnitPath:    filepath.Join(dir, "container-unit"),
	}
	for _, f := range []struct{ content, path string }{
		{hostBinary, mapping[ContainerHostBinPath]},
		{containerBinary, mapping[ContainerBinaryPath]},
		{unitFile, filepath.Join(dir, UnitName)},
		{containerUnit, mapping[ContainerUnitPath]},
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
		contUnit   string
		wantErr    bool
		wantErrIn  string
	}{
		{
			name:       "wiring complete",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n",
			contUnit:   "[Unit]\n",
		},
		{
			name:       "host binary missing",
			hostBinary: "",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n",
			contUnit:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "boot unit binary not found",
		},
		{
			name:       "unit file missing",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "",
			contUnit:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "systemd unit not found",
		},
		{
			name:       "binary hash mismatch",
			hostBinary: "binary-v1",
			contBinary: "binary-v2",
			unitFile:   "[Unit]\n",
			contUnit:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "hash mismatch",
		},
		{
			name:       "unit file drift",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n# edited on the host\n",
			contUnit:   "[Unit]\n",
			wantErr:    true,
			wantErrIn:  "unit file hash mismatch",
		},
		{
			name:       "container unit template missing",
			hostBinary: "binary-v1",
			contBinary: "binary-v1",
			unitFile:   "[Unit]\n",
			contUnit:   "",
			wantErr:    true,
			wantErrIn:  "read container unit file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, unitDir := verifyFixture(t, tt.hostBinary, tt.contBinary, tt.unitFile, tt.contUnit)
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

func TestVerifyFSConfiguredTimeout(t *testing.T) {
	template := "ExecStart=/usr/local/bin/sbr-self-cordon -boot-unit-timeout=@BOOT_UNIT_TIMEOUT@\nTimeoutStartSec=@BOOT_UNIT_CEILING@\n"
	rendered, err := RenderUnit([]byte(template), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fsys, unitDir := verifyFixture(t, "binary-v1", "binary-v1", string(rendered), template)
	if err := VerifyFS(unitDir, fsys, 2*time.Minute); err != nil {
		t.Fatalf("configured unit rejected: %v", err)
	}
	if err := VerifyFS(unitDir, fsys, 3*time.Minute); err == nil {
		t.Fatal("unit with a different timeout must fail verification")
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
	// Values the EnvironmentFile format cannot carry safely must be
	// rejected: a newline would inject an extra assignment, quotes and
	// backslashes fight the parser, and whitespace breaks the expansion.
	for _, tt := range []struct{ name, uid, api string }{
		{"worker\nEVIL=1", "uid", "https://api:6443"},
		{"worker 0", "uid", "https://api:6443"},
		{`worker"0`, "uid", "https://api:6443"},
		{`wor\ker`, "uid", "https://api:6443"},
		{"worker-0", "uid\x00", "https://api:6443"},
		{"worker-0", "uid", "https://api\n"},
	} {
		if err := writeIdentityEnv(path, tt.name, tt.uid, tt.api); err == nil {
			t.Errorf("unsafe identity values %q / %q / %q must be rejected", tt.name, tt.uid, tt.api)
		}
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
	fsys, unitDir := verifyFixture(t, "binary-v1", "binary-v1", "[Unit]\n", "[Unit]\n")
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

// TestReloadAndEnableRunsInHostNamespace pins the mechanism: activation must
// reach the host's systemd manager. The agent image has no systemd, and a
// container-local systemctl would only fail — so every command enters the host
func TestEnsureSecureDirCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	if err := ensureSecureDir(dir); err != nil {
		t.Fatalf("ensureSecureDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("created directory mode = %#o, want 0700", fi.Mode().Perm())
	}
}

// TestEnsureSecureDirTightensKubeletStyleDirectory pins the adoption half:
// kubelet creates hostPath directories 0755 before the agent runs, and
// MkdirAll cannot tighten an existing directory, so the agent must. Without
// this, the nodestate security policy rejects every journal operation on a
// fresh node.
func TestEnsureSecureDirTightensKubeletStyleDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod to the kubelet mode: %v", err)
	}

	if err := ensureSecureDir(dir); err != nil {
		t.Fatalf("ensureSecureDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat tightened directory: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700 after tightening", fi.Mode().Perm())
	}
}

// TestEnsureSecureDirLeavesForeignObjectsToThePolicy pins the boundary of the
// repair: a symlink or a regular file at the state path is exactly the
// tampering the security policy exists to catch, so the agent must leave it
// untouched and let the store fail closed on it.
func TestEnsureSecureDirLeavesForeignObjectsToThePolicy(t *testing.T) {
	base := t.TempDir()

	link := filepath.Join(base, "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := ensureSecureDir(link); err != nil {
		t.Fatalf("ensureSecureDir on a symlink: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was modified: %v, %v", fi, err)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := ensureSecureDir(file); err != nil {
		t.Fatalf("ensureSecureDir on a regular file: %v", err)
	}
	if fi, err := os.Lstat(file); err != nil || fi.IsDir() {
		t.Fatalf("file was modified: %v, %v", fi, err)
	}
}

// TestJournalWritesAfterDirectoryAdoption is the regression test for the
// live failure this fixes: a journal opened over a kubelet-created 0755 state
// directory used to fail every write with "insecure permissions". The agent
// adopts the directory before opening the journal, and writes then succeed.
func TestJournalWritesAfterDirectoryAdoption(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod to the kubelet mode: %v", err)
	}

	if err := ensureSecureDir(dir); err != nil {
		t.Fatalf("adopt the directory: %v", err)
	}
	j, err := NewJournal(dir)
	if err != nil {
		t.Fatalf("NewJournal over an adopted directory: %v", err)
	}
	if err := j.RecordRunning(context.Background()); err != nil {
		t.Fatalf("RecordRunning on an adopted directory: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat adopted directory: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700 after adoption", fi.Mode().Perm())
	}
}

// TestEnableUnitCreatesTheWantsSymlink pins the bus-free activation:
// enablement is the Wants symlink systemctl would create, it is idempotent,
// it repairs a wrong target or a stray non-symlink entry, and a second call
// does not rewrite a correct one.
func TestEnableUnitCreatesTheWantsSymlink(t *testing.T) {
	dir := t.TempDir()

	if err := enableUnit(dir); err != nil {
		t.Fatalf("enableUnit: %v", err)
	}
	link := filepath.Join(dir, UnitWantsDir, UnitName)
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the wants symlink was not created: %v", err)
	}
	if target != HostUnitPath {
		t.Fatalf("symlink target = %q, want the host path %q", target, HostUnitPath)
	}

	// Idempotent: the second call leaves the correct symlink untouched.
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	ino1 := sysIno(t, fi)
	if err := enableUnit(dir); err != nil {
		t.Fatalf("second enableUnit: %v", err)
	}
	fi, err = os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if sysIno(t, fi) != ino1 {
		t.Fatal("a correct wants symlink was replaced; enablement must be idempotent")
	}

	// An out-of-band disable (symlink removed) is repaired.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := enableUnit(dir); err != nil {
		t.Fatalf("enableUnit after out-of-band disable: %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != HostUnitPath {
		t.Fatalf("disable was not repaired: %q, %v", target, err)
	}

	// A wrong target is replaced.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/lib/systemd/system/elsewhere.service", link); err != nil {
		t.Fatal(err)
	}
	if err := enableUnit(dir); err != nil {
		t.Fatalf("enableUnit over a wrong target: %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != HostUnitPath {
		t.Fatalf("wrong target was not replaced: %q, %v", target, err)
	}

	// A stray regular file in the wants directory is replaced too.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("not a symlink"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableUnit(dir); err != nil {
		t.Fatalf("enableUnit over a stray file: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("stray file was not replaced: %v, %v", fi, err)
	}
}

// sysIno returns a file's inode identity, to assert a symlink was not
// replaced.
func sysIno(t *testing.T, fi os.FileInfo) uint64 {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode comparison not supported on this platform")
	}
	return st.Ino
}
