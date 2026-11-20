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
	"os"
	"path/filepath"
	"testing"
)

// writeKubeconfig stages a kubelet-style kubeconfig in a temp dir.
func writeKubeconfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubelet.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const kubeletKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: kubernetes
  cluster:
    server: https://api.example.test:6443
contexts:
- name: kubelet
  context:
    cluster: kubernetes
    user: system:node:worker-0
current-context: kubelet
users: []
`

func TestResolveAPIServerFromKubeconfig(t *testing.T) {
	got, err := ResolveAPIServerFromKubeconfig(writeKubeconfig(t, kubeletKubeconfig))
	if err != nil {
		t.Fatalf("ResolveAPIServerFromKubeconfig: %v", err)
	}
	if want := "https://api.example.test:6443"; got != want {
		t.Errorf("server = %q, want %q", got, want)
	}
}

func TestResolveAPIServerFallsBackToTheOnlyCluster(t *testing.T) {
	// A kubelet kubeconfig whose current-context is missing or empty is still
	// usable: there is exactly one cluster, and it names the endpoint.
	body := `apiVersion: v1
kind: Config
clusters:
- name: kubernetes
  cluster:
    server: https://api.example.test:6443
contexts: []
users: []
`
	got, err := ResolveAPIServerFromKubeconfig(writeKubeconfig(t, body))
	if err != nil {
		t.Fatalf("ResolveAPIServerFromKubeconfig: %v", err)
	}
	if want := "https://api.example.test:6443"; got != want {
		t.Errorf("server = %q, want %q", got, want)
	}
}

func TestResolveAPIServerMissingFile(t *testing.T) {
	if _, err := ResolveAPIServerFromKubeconfig(filepath.Join(t.TempDir(), "absent.conf")); err == nil {
		t.Fatal("want an error for a missing kubeconfig")
	}
}

func TestResolveAPIServerWithoutServer(t *testing.T) {
	body := `apiVersion: v1
kind: Config
clusters:
- name: empty
  cluster: {}
contexts: []
users: []
`
	if _, err := ResolveAPIServerFromKubeconfig(writeKubeconfig(t, body)); err == nil {
		t.Fatal("want an error when the kubeconfig names no API server")
	}
}
