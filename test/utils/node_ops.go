/*
Copyright 2025.

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

package utils

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck
	. "github.com/onsi/gomega"    //nolint:staticcheck
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StopKubeletOnNode stops the kubelet service on a node via oc debug.
// Note: stopping the kubelet often kills the oc debug pod's connection back to the
// API server, causing the command to exit with an error even though the kubelet was
// successfully stopped. We handle this "suicide problem" by treating connection-drop
// errors as success — the caller should verify via WaitForNodeNotReady.
func StopKubeletOnNode(nodeName string) error {
	GinkgoWriter.Printf("Stopping kubelet on node %s via oc debug\n", nodeName)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "oc", "debug", fmt.Sprintf("node/%s", nodeName),
		"--namespace=default",
		"--", "chroot", "/host", "systemctl", "stop", "kubelet")

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Stopping kubelet kills the debug pod's connection, causing oc debug to
		// exit with "signal: killed" or "remote command exited" even on success.
		if strings.Contains(err.Error(), "signal: killed") ||
			strings.Contains(outputStr, "error: remote command exited") {
			GinkgoWriter.Printf("oc debug exited with expected error after kubelet stop (connection dropped): %v\n", err)
			return nil
		}
		return fmt.Errorf("failed to stop kubelet on node %s: %w\nOutput: %s", nodeName, err, outputStr)
	}

	GinkgoWriter.Printf("Successfully stopped kubelet on node %s\n", nodeName)
	return nil
}

// StartKubeletOnNode starts the kubelet service on a node via oc debug.
func StartKubeletOnNode(nodeName string) error {
	GinkgoWriter.Printf("Starting kubelet on node %s via oc debug\n", nodeName)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "oc", "debug", fmt.Sprintf("node/%s", nodeName),
		"--namespace=default",
		"--", "chroot", "/host", "systemctl", "start", "kubelet")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start kubelet on node %s: %w\nOutput: %s", nodeName, err, string(output))
	}

	GinkgoWriter.Printf("Successfully started kubelet on node %s\n", nodeName)
	return nil
}

// WaitForNodeNotReady waits for a node to become NotReady after kubelet stop.
func WaitForNodeNotReady(clients *TestClients, nodeName string, timeout time.Duration) {
	GinkgoWriter.Printf("Waiting for node %s to become NotReady (timeout: %v)\n", nodeName, timeout)

	Eventually(func() bool {
		node := &corev1.Node{}
		err := clients.Client.Get(clients.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			return false
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status != corev1.ConditionTrue {
					GinkgoWriter.Printf("Node %s is now NotReady (Ready=%s)\n", nodeName, condition.Status)
					return true
				}
			}
		}
		return false
	}, timeout, 5*time.Second).Should(BeTrue(), "Node %s should become NotReady", nodeName)
}

// WaitForNodeReady waits for a node to become Ready.
func WaitForNodeReady(clients *TestClients, nodeName string, timeout time.Duration) {
	GinkgoWriter.Printf("Waiting for node %s to become Ready (timeout: %v)\n", nodeName, timeout)

	Eventually(func() bool {
		node := &corev1.Node{}
		err := clients.Client.Get(clients.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			return false
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s is now Ready\n", nodeName)
				return true
			}
		}
		return false
	}, timeout, 10*time.Second).Should(BeTrue(), "Node %s should become Ready", nodeName)
}

// WaitForNodeReboot waits for a node to reboot by detecting a change in boot ID.
func WaitForNodeReboot(clients *TestClients, nodeName string, originalBootID string, timeout time.Duration) {
	GinkgoWriter.Printf("Waiting for node %s to reboot (original boot ID: %s)\n", nodeName, originalBootID)

	Eventually(func() bool {
		node := &corev1.Node{}
		err := clients.Client.Get(clients.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			return false
		}

		currentBootID := strings.TrimSpace(node.Status.NodeInfo.BootID)
		if currentBootID != "" && currentBootID != originalBootID {
			GinkgoWriter.Printf("Node %s has rebooted (new boot ID: %s)\n", nodeName, currentBootID)
			return true
		}
		return false
	}, timeout, 15*time.Second).Should(BeTrue(), "Node %s should reboot within %v", nodeName, timeout)
}
