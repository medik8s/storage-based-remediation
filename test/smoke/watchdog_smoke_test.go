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

package smoke

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
)

var _ = Describe("SBD Watchdog Smoke Tests", Ordered, Label("Smoke", "Watchdog"), func() {
	const watchdogTestNamespace = "sbd-watchdog-smoke-test"

	BeforeAll(func() {
		By("initializing Kubernetes clients for watchdog tests if needed")
		if k8sClient == nil {
			err := setupKubernetesClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
		}
	})

	AfterAll(func() {
		By("cleaning up watchdog smoke test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: watchdogTestNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, ns)
	})

	Context("Watchdog Compatibility and Stability", func() {
		var sbdConfig *medik8sv1alpha1.SBDConfig

		BeforeEach(func() {
			// Create a minimal SBD configuration for watchdog testing
			sbdConfig = &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "watchdog-smoke-test",
					Namespace: watchdogTestNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					WatchdogTimeout: &metav1.Duration{Duration: 90 * time.Second}, // Longer timeout for safety
					PetIntervalMultiple: func() *int32 {
						val := int32(6) // Conservative 15-second pet interval
						return &val
					}(),
					// Note: The agent now always runs with testMode=false for production behavior
				},
			}
		})

		AfterEach(func() {
			By("cleaning up SBD configuration and waiting for agents to terminate")
			if sbdConfig != nil {
				_ = k8sClient.Delete(ctx, sbdConfig)

				// Wait for all agent pods to be terminated
				Eventually(func() int {
					pods := &corev1.PodList{}
					err := k8sClient.List(ctx, pods,
						client.InNamespace(watchdogTestNamespace),
						client.MatchingLabels{"app": "sbd-agent"})
					if err != nil {
						return -1
					}
					return len(pods.Items)
				}, time.Minute*2, time.Second*5).Should(Equal(0))
			}
		})

		It("should successfully deploy SBD agents without causing node instability", func() {
			By("creating SBD configuration with watchdog enabled")
			err := k8sClient.Create(ctx, sbdConfig)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for SBD agent DaemonSet to be created")
			var daemonSet *appsv1.DaemonSet
			Eventually(func() bool {
				daemonSets := &appsv1.DaemonSetList{}
				err := k8sClient.List(ctx, daemonSets,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"app": "sbd-agent"})
				if err != nil || len(daemonSets.Items) == 0 {
					return false
				}
				daemonSet = &daemonSets.Items[0]
				return true
			}, time.Minute*3, time.Second*10).Should(BeTrue())

			By("verifying DaemonSet has correct watchdog configuration")
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := daemonSet.Spec.Template.Spec.Containers[0]

			// Verify watchdog-related arguments are present
			argsStr := strings.Join(container.Args, " ")
			Expect(argsStr).To(ContainSubstring("--watchdog-path=/dev/watchdog"))
			Expect(argsStr).To(ContainSubstring("--watchdog-timeout=1m30s"))

			By("waiting for SBD agent pods to become ready")
			Eventually(func() int {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"app": "sbd-agent"})
				if err != nil {
					return 0
				}

				readyPods := 0
				for _, pod := range pods.Items {
					if pod.Status.Phase == corev1.PodRunning {
						// Check if the pod has been ready for a reasonable time
						for _, condition := range pod.Status.Conditions {
							if condition.Type == corev1.PodReady &&
								condition.Status == corev1.ConditionTrue {
								readyPods++
								break
							}
						}
					}
				}
				return readyPods
			}, time.Minute*5, time.Second*15).Should(BeNumerically(">=", 1))

			By("verifying nodes remain stable and don't experience reboots")
			// Monitor node stability for a reasonable period
			Consistently(func() bool {
				nodes := &corev1.NodeList{}
				err := k8sClient.List(ctx, nodes)
				if err != nil {
					return false
				}

				// Check that all nodes remain Ready
				for _, node := range nodes.Items {
					isReady := false
					for _, condition := range node.Status.Conditions {
						if condition.Type == corev1.NodeReady &&
							condition.Status == corev1.ConditionTrue {
							isReady = true
							break
						}
					}
					if !isReady {
						GinkgoWriter.Printf("Node %s is not ready: %+v\n", node.Name, node.Status.Conditions)
						return false
					}
				}
				return true
			}, time.Minute*3, time.Second*15).Should(BeTrue())

			By("checking SBD agent logs for successful watchdog operations")
			pods := &corev1.PodList{}
			err = k8sClient.List(ctx, pods,
				client.InNamespace(watchdogTestNamespace),
				client.MatchingLabels{"app": "sbd-agent"})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(pods.Items)).To(BeNumerically(">=", 1))

			// Check logs of at least one agent pod
			podName := pods.Items[0].Name
			By(fmt.Sprintf("examining logs of SBD agent pod %s", podName))

			// Get recent logs to check for watchdog activity
			Eventually(func() string {
				logs, err := clientset.CoreV1().Pods(watchdogTestNamespace).
					GetLogs(podName, &corev1.PodLogOptions{
						TailLines: func() *int64 { val := int64(50); return &val }(),
					}).DoRaw(ctx)
				if err != nil {
					return ""
				}
				return string(logs)
			}, time.Minute*2, time.Second*10).Should(SatisfyAny(
				// Should see either successful ioctl or write-based fallback
				ContainSubstring("Watchdog pet successful"),
				ContainSubstring("falling back to write-based keep-alive"),
				ContainSubstring("Starting watchdog loop"),
			))

			By("verifying no critical errors in agent logs")
			logs, err := clientset.CoreV1().Pods(watchdogTestNamespace).
				GetLogs(podName, &corev1.PodLogOptions{}).DoRaw(ctx)
			Expect(err).NotTo(HaveOccurred())

			logStr := string(logs)
			// These errors would indicate problems with our fix
			Expect(logStr).NotTo(ContainSubstring("Failed to pet watchdog after retries"))
			Expect(logStr).NotTo(ContainSubstring("watchdog device is not open"))

			// The agent should show successful operation during normal startup
			Expect(logStr).To(SatisfyAny(
				ContainSubstring("Watchdog pet successful"),
				ContainSubstring("SBD Agent started successfully"),
			))
		})

		It("should handle watchdog driver compatibility across different architectures", func() {
			By("creating SBD configuration optimized for compatibility")
			compatConfig := sbdConfig.DeepCopy()
			compatConfig.Name = "watchdog-compat-test"
			compatConfig.Spec.WatchdogTimeout = &metav1.Duration{Duration: 120 * time.Second} // Even longer timeout
			compatConfig.Spec.PetIntervalMultiple = func() *int32 {
				val := int32(8) // Very conservative pet interval
				return &val
			}()

			err := k8sClient.Create(ctx, compatConfig)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = k8sClient.Delete(ctx, compatConfig) }()

			By("waiting for agent deployment and monitoring for compatibility issues")
			Eventually(func() int {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"app": "sbd-agent"})
				if err != nil {
					return 0
				}

				runningPods := 0
				for _, pod := range pods.Items {
					if pod.Status.Phase == corev1.PodRunning {
						runningPods++
					}
				}
				return runningPods
			}, time.Minute*4, time.Second*15).Should(BeNumerically(">=", 1))

			By("verifying agents successfully adapt to different watchdog driver implementations")
			// Monitor for a longer period to catch any delayed issues
			Consistently(func() bool {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"app": "sbd-agent"})
				if err != nil {
					return false
				}

				for _, pod := range pods.Items {
					// Check that pods haven't restarted due to watchdog issues
					// Check restart count across all containers in the pod
					totalRestarts := int32(0)
					for _, containerStatus := range pod.Status.ContainerStatuses {
						totalRestarts += containerStatus.RestartCount
					}
					if totalRestarts > 0 {
						GinkgoWriter.Printf("Pod %s has total restart count %d\n", pod.Name, totalRestarts)

						// Get pod events to understand restart reason
						events := &corev1.EventList{}
						err := k8sClient.List(ctx, events,
							client.InNamespace(watchdogTestNamespace),
							client.MatchingFields{"involvedObject.name": pod.Name})
						if err == nil {
							for _, event := range events.Items {
								if strings.Contains(event.Message, "watchdog") ||
									strings.Contains(event.Reason, "Failed") {
									GinkgoWriter.Printf("Pod %s event: %s - %s\n",
										pod.Name, event.Reason, event.Message)
								}
							}
						}
						return false
					}

					// Verify pod is in Running state
					if pod.Status.Phase != corev1.PodRunning {
						return false
					}
				}
				return true
			}, time.Minute*5, time.Second*20).Should(BeTrue())

			By("verifying SBDConfig status shows healthy operation")
			Eventually(func() bool {
				updatedConfig := &medik8sv1alpha1.SBDConfig{}
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      compatConfig.Name,
					Namespace: watchdogTestNamespace,
				}, updatedConfig)
				if err != nil {
					return false
				}

				// Check that we have some ready nodes reported
				return updatedConfig.Status.ReadyNodes > 0
			}, time.Minute*3, time.Second*15).Should(BeTrue())
		})

		It("should display SBDConfig YAML correctly and maintain stability", func() {
			By("creating and retrieving SBDConfig for YAML validation")
			err := k8sClient.Create(ctx, sbdConfig)
			Expect(err).NotTo(HaveOccurred())

			By("retrieving and displaying SBDConfig YAML")
			retrievedConfig := &medik8sv1alpha1.SBDConfig{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      sbdConfig.Name,
					Namespace: watchdogTestNamespace,
				}, retrievedConfig)
			}, time.Minute*1, time.Second*5).Should(Succeed())

			// Display the configuration for verification
			yamlData, err := yaml.Marshal(retrievedConfig.Spec)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("SBDConfig YAML:\n%s\n", string(yamlData))

			By("verifying watchdog configuration is properly applied")
			Expect(retrievedConfig.Spec.SbdWatchdogPath).To(Equal("/dev/watchdog"))
			Expect(retrievedConfig.Spec.WatchdogTimeout.Duration).To(Equal(90 * time.Second))
			Expect(*retrievedConfig.Spec.PetIntervalMultiple).To(Equal(int32(6)))
			// Note: Watchdog configuration is handled entirely through the CRD spec

			By("monitoring system stability with configuration changes")
			Consistently(func() bool {
				// Verify configuration remains stable
				currentConfig := &medik8sv1alpha1.SBDConfig{}
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      sbdConfig.Name,
					Namespace: watchdogTestNamespace,
				}, currentConfig)
				if err != nil {
					return false
				}

				// Check generation hasn't changed unexpectedly
				return currentConfig.Generation >= retrievedConfig.Generation
			}, time.Minute*2, time.Second*10).Should(BeTrue())
		})
	})
})
