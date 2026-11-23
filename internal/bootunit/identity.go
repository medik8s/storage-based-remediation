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
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// ResolveAPIServerFromKubeconfig returns the API server endpoint recorded in a
// kubeconfig file.
//
// The context's cluster wins when it names one; otherwise the first cluster
// with a server URL is used, because a kubelet kubeconfig is single-cluster in
// practice and the redundant lookup makes the function tolerant of trimming.
func ResolveAPIServerFromKubeconfig(path string) (string, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return "", fmt.Errorf("read kubeconfig %s: %w", path, err)
	}

	if ctx, ok := cfg.Contexts[cfg.CurrentContext]; ok && ctx != nil && ctx.Cluster != "" {
		if cluster, ok := cfg.Clusters[ctx.Cluster]; ok && cluster != nil && cluster.Server != "" {
			return cluster.Server, nil
		}
	}
	for _, cluster := range cfg.Clusters {
		if cluster != nil && cluster.Server != "" {
			return cluster.Server, nil
		}
	}

	return "", fmt.Errorf("kubeconfig %s names no API server", path)
}

// kubeconfigCandidates lists, in order, where the node's own kubeconfig is
// looked for. It is a package variable so tests can substitute their own
// paths; production always uses the constants.
var kubeconfigCandidates = []string{ContainerKubeletKubeconfig, ContainerKubeletConf}

// ResolveAPIServerFromNode returns the API server endpoint from the node's
// own kubeconfig, trying every known location.
//
// The boot unit runs before kubelet, so it cannot use the in-cluster service
// address: that address is only routable once kube-proxy is running. The
// node's own kubeconfig already names the endpoint kubelet itself uses, which
// is by definition reachable from the node — but where that kubeconfig lives
// depends on the distribution:
//
//   - OpenShift / RHCOS keep the node's client kubeconfig at
//     /etc/kubernetes/kubeconfig; /etc/kubernetes/kubelet.conf there is the
//     kubelet's KubeletConfiguration, not a kubeconfig (live-verified on
//     OCP 4.22: parsing it as a kubeconfig fails with "no kind
//     KubeletConfiguration is registered").
//   - kubeadm-style clusters, including kind, keep the kubelet's client
//     kubeconfig at /etc/kubernetes/kubelet.conf.
//
// A candidate that is missing, or that exists but does not parse as a
// kubeconfig, moves the search to the next one; exhausting all candidates is
// an error naming each attempt, so an administrator can see what was tried.
func ResolveAPIServerFromNode() (string, error) {
	var attempts []string
	for _, path := range kubeconfigCandidates {
		server, err := ResolveAPIServerFromKubeconfig(path)
		if err == nil {
			return server, nil
		}
		attempts = append(attempts, fmt.Sprintf("%s: %v", path, err))
	}
	return "", fmt.Errorf("no usable node kubeconfig on this host: %s", strings.Join(attempts, "; "))
}
