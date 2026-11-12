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
	"os"
	"path/filepath"
	"testing"
)

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

func TestVerifyMissingBinary(t *testing.T) {
	dir := t.TempDir()
	// Install a valid binary but no unit file.
	os.MkdirAll(HostStateRoot, 0700)
	defer os.RemoveAll(HostStateRoot)
	os.WriteFile(HostBinaryPath, []byte("binary"), 0755)
	defer os.Remove(HostBinaryPath)

	err := Verify(dir)
	if err == nil {
		t.Fatal("expected error for missing unit file")
	}
}
