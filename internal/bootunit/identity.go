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

	"k8s.io/client-go/tools/clientcmd"
)

// ResolveAPIServerFromKubeconfig returns the API server endpoint recorded in a
// kubeconfig file — in the agent, the node's own /etc/kubernetes/kubelet.conf.
//
// The boot unit runs before kubelet, so it cannot use the in-cluster service
// address: that address is only routable once kube-proxy is running. The
// node's kubelet kubeconfig already names the endpoint kubelet itself uses,
// which is by definition reachable from the node.
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
