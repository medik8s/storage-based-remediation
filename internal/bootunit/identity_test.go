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
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestResolveAPIServerFromNodeTriesEveryLayout pins the distribution
// compatibility: OpenShift/RHCOS keeps the node kubeconfig at
// /etc/kubernetes/kubeconfig (kubelet.conf there is a KubeletConfiguration),
// kubeadm-style clusters keep the kubelet's client kubeconfig at
// /etc/kubernetes/kubelet.conf. Every layout must resolve.
func TestResolveAPIServerFromNodeTriesEveryLayout(t *testing.T) {
	writeKubeconfig := func(t *testing.T, server string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "kubeconfig")
		content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
  name: cluster
contexts:
- context:
    cluster: cluster
    user: user
  name: default
current-context: default
users:
- name: user
`, server)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// A file that exists but is a KubeletConfiguration, not a kubeconfig —
	// what /etc/kubernetes/kubelet.conf is on OpenShift/RHCOS.
	writeKubeletConfiguration := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "kubelet.conf")
		if err := os.WriteFile(path, []byte("apiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	orig := kubeconfigCandidates
	t.Cleanup(func() { kubeconfigCandidates = orig })

	t.Run("openshift layout: kubeconfig wins over the kubelet configuration", func(t *testing.T) {
		kubeconfigCandidates = []string{
			writeKubeconfig(t, "https://openshift-api:6443"),
			writeKubeletConfiguration(t),
		}
		server, err := ResolveAPIServerFromNode()
		if err != nil || server != "https://openshift-api:6443" {
			t.Fatalf("ResolveAPIServerFromNode = %q, %v; want the openshift-api endpoint", server, err)
		}
	})

	t.Run("kubeadm layout: kubelet.conf is the kubeconfig", func(t *testing.T) {
		kubeconfigCandidates = []string{
			filepath.Join(t.TempDir(), "kubeconfig-does-not-exist"),
			writeKubeconfig(t, "https://kubeadm-api:6443"),
		}
		server, err := ResolveAPIServerFromNode()
		if err != nil || server != "https://kubeadm-api:6443" {
			t.Fatalf("ResolveAPIServerFromNode = %q, %v; want the kubeadm-api endpoint", server, err)
		}
	})

	t.Run("an unparseable first candidate falls through to the next", func(t *testing.T) {
		kubeconfigCandidates = []string{
			writeKubeletConfiguration(t),
			writeKubeconfig(t, "https://fallback-api:6443"),
		}
		server, err := ResolveAPIServerFromNode()
		if err != nil || server != "https://fallback-api:6443" {
			t.Fatalf("ResolveAPIServerFromNode = %q, %v; want the fallback endpoint", server, err)
		}
	})

	t.Run("no candidate usable names every attempt", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		kubeconfigCandidates = []string{missing, writeKubeletConfiguration(t)}
		_, err := ResolveAPIServerFromNode()
		if err == nil {
			t.Fatal("want an error when no candidate is usable")
		}
		for _, want := range []string{missing, "KubeletConfiguration"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name the attempt on %q", err, want)
			}
		}
	})
}
