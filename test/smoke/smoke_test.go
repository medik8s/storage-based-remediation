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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "sbd-operator-system"

// testNamespace where SBDConfig resources and their DaemonSets are deployed
const testNamespace = "sbd-test"

// serviceAccountName created for the project
const serviceAccountName = "sbd-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sbd-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sbd-operator-metrics-binding"

var (
	// Kubernetes clients
	k8sClient client.Client
	clientset *kubernetes.Clientset
	ctx       = context.Background()
)

// setupKubernetesClients initializes the Kubernetes clients
func setupKubernetesClients() error {
	// Load kubeconfig - try environment variable first, then default location
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(homeDir, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Create scheme with core Kubernetes types and add our CRDs
	clientScheme := runtime.NewScheme()
	err = scheme.AddToScheme(clientScheme)
	if err != nil {
		return fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	err = medik8sv1alpha1.AddToScheme(clientScheme)
	if err != nil {
		return fmt.Errorf("failed to add SBD types to scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err = client.New(config, client.Options{Scheme: clientScheme})
	if err != nil {
		return fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create standard clientset
	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return nil
}

var _ = Describe("SBD Operator Smoke Tests", Ordered, Label("Smoke"), func() {
	var controllerPodName string

	// Verify the environment is set up correctly (setup handled by Makefile)
	BeforeAll(func() {
		By("initializing Kubernetes clients")
		err := setupKubernetesClients()
		Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")

		By("verifying the controller-manager namespace exists")
		ns := &corev1.Namespace{}
		err = k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, ns)
		Expect(err).NotTo(HaveOccurred(), "Expected namespace to exist (should be created by Makefile setup)")

		By("verifying CRDs are installed")
		cmd := exec.Command("kubectl", "get", "crd", "sbdconfigs.medik8s.medik8s.io")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Expected CRDs to be installed (should be done by Makefile setup)")

		By("verifying the controller-manager is deployed")
		deployment := &appsv1.Deployment{}
		err = k8sClient.Get(ctx, client.ObjectKey{
			Name:      "sbd-operator-controller-manager",
			Namespace: namespace,
		}, deployment)
		Expect(err).NotTo(HaveOccurred(), "Expected controller-manager to be deployed (should be done by Makefile setup)")
	})

	// Clean up test-specific resources (overall cleanup handled by Makefile)
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		pod := &corev1.Pod{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "curl-metrics", Namespace: namespace}, pod)
		if err == nil {
			_ = k8sClient.Delete(ctx, pod)
		}

		By("cleaning up metrics ClusterRoleBinding")
		cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("cleaning up any test SBDConfigs")
		sbdConfigs := &medik8sv1alpha1.SBDConfigList{}
		err = k8sClient.List(ctx, sbdConfigs, client.InNamespace(testNamespace))
		if err == nil {
			for _, sbdConfig := range sbdConfigs.Items {
				_ = k8sClient.Delete(ctx, &sbdConfig)
			}
		}
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get controller-manager pods
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(namespace),
					client.MatchingLabels{"control-plane": "controller-manager"})
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")

				// Filter out pods that are being deleted
				var activePods []corev1.Pod
				for _, pod := range pods.Items {
					if pod.DeletionTimestamp == nil {
						activePods = append(activePods, pod)
					}
				}
				g.Expect(activePods).To(HaveLen(1), "expected 1 controller pod running")

				controllerPodName = activePods[0].Name
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				g.Expect(activePods[0].Status.Phase).To(Equal(corev1.PodRunning), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			// Clean up any existing clusterrolebinding first
			cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=sbd-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("SBD Agent", func() {
		var sbdConfigName string
		var tmpFile string

		BeforeEach(func() {
			sbdConfigName = fmt.Sprintf("test-sbdconfig-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			// Clean up SBDConfig and wait for finalizer cleanup

			By("cleaning up SBDConfig resource")
			cmd := exec.Command("kubectl", "delete", "sbdconfig", sbdConfigName, "-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			// Wait for SBDConfig deletion to complete (finalizer cleanup)
			By("waiting for SBDConfig deletion to complete")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace)
				_, err := utils.Run(cmd)
				return err != nil // Error means resource not found (deleted)
			}, 5*time.Minute, 2*time.Second).Should(BeTrue())

			// Clean up any remaining DaemonSets (should be auto-deleted by owner references)
			By("cleaning up any remaining SBD agent DaemonSets")
			cmd = exec.Command("kubectl", "delete", "daemonset", "-l", "app.kubernetes.io/name=sbd-agent", "-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			// Clean up SCC (note: SCC is managed by operator installation, not by individual tests)
			By("SCC cleanup not needed - managed by operator installation")
			// The SCC sbd-operator-sbd-agent-privileged is deployed via the OpenShift installer
			// and should not be deleted by individual tests

			// Clean up temporary file
			if tmpFile != "" {
				os.Remove(tmpFile)
			}
		})

		It("should deploy SBD agent DaemonSet when SBDConfig is created", func() {
			By("creating an SBDConfig resource from sample configuration")
			// Load sample configuration and customize name
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with our test name
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", sbdConfigName)
			// Ensure imagePullPolicy is Always for testing
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)

			// Write SBDConfig to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdconfig-%s.yaml", sbdConfigName))
			err = os.WriteFile(tmpFile, []byte(sbdConfigYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Display the contents of the tmp file for debugging
			// By("displaying the SBDConfig YAML contents")
			// tmpFileContents, err := os.ReadFile(tmpFile)
			// Expect(err).NotTo(HaveOccurred(), "Failed to read temporary SBDConfig file")
			// fmt.Printf("SBDConfig YAML contents:\n%s\n", string(tmpFileContents))

			// Apply the SBDConfig to the test namespace
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig resource exists")
			verifySBDConfigExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(sbdConfigName))
			}
			Eventually(verifySBDConfigExists, 30*time.Second).Should(Succeed())

			By("verifying the SBD agent DaemonSet is created")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)
			verifyDaemonSetCreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)

				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(expectedDaemonSetName))
			}
			Eventually(verifyDaemonSetCreated, 8*time.Minute).Should(Succeed())

			By("verifying the DaemonSet has correct image and configuration")
			verifyDaemonSetConfig := func(g Gomega) {
				// Check image - should be automatically derived from operator image
				// (same registry/org/tag as operator, but with sbd-agent as image name)
				cmd := exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.spec.template.spec.containers[0].image}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(agentImage))

				// Check image pull policy
				cmd = exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.spec.template.spec.containers[0].imagePullPolicy}")
				output, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Always"))

				// Check watchdog path argument
				cmd = exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.spec.template.spec.containers[0].args}")
				output, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("--watchdog-path=/dev/watchdog"))
			}
			Eventually(verifyDaemonSetConfig, 30*time.Second).Should(Succeed())

			By("verifying the DaemonSet has expected number of pods scheduled")
			verifyDaemonSetScheduled := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.status.desiredNumberScheduled}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should have at least 1 pod scheduled (for the single CRC node)
				g.Expect(output).ToNot(BeEmpty())
				g.Expect(output).ToNot(Equal("0"))
			}
			Eventually(verifyDaemonSetScheduled, 60*time.Second).Should(Succeed())

			By("verifying the SBDConfig status reflects DaemonSet creation")
			verifySBDConfigStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace, "-o", "jsonpath={.status.totalNodes}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should have at least 1 node targeted
				g.Expect(output).ToNot(BeEmpty())
				g.Expect(output).ToNot(Equal("0"))
			}
			Eventually(verifySBDConfigStatus, 60*time.Second).Should(Succeed())
		})

		It("should have SBD agent pods running and ready", func() {
			By("creating an SBDConfig resource from sample configuration")
			// Load sample configuration and customize name
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with our test name
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", sbdConfigName)
			// Ensure imagePullPolicy is Always for testing
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)

			// Write SBDConfig to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdconfig-%s.yaml", sbdConfigName))
			err = os.WriteFile(tmpFile, []byte(sbdConfigYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Apply the SBDConfig
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying SecurityContextConstraints is deployed")
			verifySCC := func(g Gomega) {
				// Verify the SCC exists (deployed via OpenShift installer)
				cmd := exec.Command("kubectl", "get", "scc", "sbd-operator-sbd-agent-privileged", "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "SCC should be deployed via OpenShift installer")
				g.Expect(output).To(Equal("sbd-operator-sbd-agent-privileged"))
			}
			Eventually(verifySCC, 30*time.Second).Should(Succeed())

			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)

			By("verifying the DaemonSet is created")
			verifyDaemonSetExists := func(g Gomega) {
				daemonSet := &appsv1.DaemonSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      expectedDaemonSetName,
					Namespace: testNamespace,
				}, daemonSet)
				if err != nil {
					fmt.Printf("DaemonSet lookup failed: %v\n", err)
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(daemonSet.Name).To(Equal(expectedDaemonSetName))
			}
			Eventually(verifyDaemonSetExists, 60*time.Second).Should(Succeed())

			By("verifying SBD agent pods are created")
			verifySBDPodsExist := func(g Gomega) {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(testNamespace),
					client.MatchingLabels{"sbdconfig": sbdConfigName})
				if err != nil {
					fmt.Printf("Pod lookup failed: %v\n", err)
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(pods.Items)).To(BeNumerically(">", 0), "No SBD agent pods found")
			}
			Eventually(verifySBDPodsExist, 90*time.Second).Should(Succeed())

			By("verifying SBD agent pods are running")
			verifySBDPodsRunning := func(g Gomega) {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(testNamespace),
					client.MatchingLabels{"sbdconfig": sbdConfigName})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(pods.Items)).To(BeNumerically(">", 0), "No pods found")

				// All pods should be Running
				for _, pod := range pods.Items {
					fmt.Printf("Pod phase is %s, expected Running\n", pod.Status.Phase)
					g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), fmt.Sprintf("Pod %s phase is %s, expected Running", pod.Name, pod.Status.Phase))
				}
			}
			Eventually(verifySBDPodsRunning, 2*time.Minute).Should(Succeed())

			By("verifying SBD agent pods are ready")
			verifySBDPodsReady := func(g Gomega) {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					client.InNamespace(testNamespace),
					client.MatchingLabels{"sbdconfig": sbdConfigName})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(pods.Items)).To(BeNumerically(">", 0), "No pods found")

				// All pods should have Ready=True
				for _, pod := range pods.Items {
					var readyCondition *corev1.PodCondition
					for i := range pod.Status.Conditions {
						if pod.Status.Conditions[i].Type == corev1.PodReady {
							readyCondition = &pod.Status.Conditions[i]
							break
						}
					}
					g.Expect(readyCondition).ToNot(BeNil(), fmt.Sprintf("Pod %s has no Ready condition", pod.Name))
					g.Expect(readyCondition.Status).To(Equal(corev1.ConditionTrue), fmt.Sprintf("Pod %s ready status is %s, expected True", pod.Name, readyCondition.Status))
				}
			}
			Eventually(verifySBDPodsReady, 6*time.Minute).Should(Succeed())

			By("verifying SBDConfig status shows pods are ready")
			verifySBDConfigReady := func(g Gomega) {
				sbdConfig := &medik8sv1alpha1.SBDConfig{}
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      sbdConfigName,
					Namespace: testNamespace,
				}, sbdConfig)
				g.Expect(err).NotTo(HaveOccurred())

				// Check for DaemonSetReady condition
				var daemonSetReadyCondition *metav1.Condition
				for i := range sbdConfig.Status.Conditions {
					if sbdConfig.Status.Conditions[i].Type == "DaemonSetReady" {
						daemonSetReadyCondition = &sbdConfig.Status.Conditions[i]
						break
					}
				}
				g.Expect(daemonSetReadyCondition).ToNot(BeNil(), "SBDConfig should have DaemonSetReady condition")
				g.Expect(daemonSetReadyCondition.Status).To(Equal(metav1.ConditionTrue), "SBDConfig status should show DaemonSetReady condition as True")
			}
			Eventually(verifySBDConfigReady, 3*time.Minute).Should(Succeed())

			By("verifying SBD agent container logs show successful startup")
			verifySBDAgentLogs := func(g Gomega) {
				// Get the first pod name
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l", fmt.Sprintf("sbdconfig=%s", sbdConfigName), "-o", "jsonpath={.items[0].metadata.name}")
				podName, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(podName).ToNot(BeEmpty())

				// Check the logs
				cmd = exec.Command("kubectl", "logs", podName, "-n", testNamespace, "-c", "sbd-agent", "--tail=50")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Look for signs that the agent started successfully
				g.Expect(output).To(Or(
					ContainSubstring("Starting SBD Agent"),
					ContainSubstring("SBD Agent started successfully"),
					ContainSubstring("Watchdog initialized"),
					ContainSubstring("Agent running"),
				), fmt.Sprintf("SBD agent logs don't show successful startup: %s", output))
			}
			Eventually(verifySBDAgentLogs, 2*time.Minute).Should(Succeed())
		})
	})

	Context("SBD Remediation", func() {
		var sbdRemediationName string
		var tmpFile string

		BeforeEach(func() {
			sbdRemediationName = fmt.Sprintf("test-sbdremediation-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			// Clean up SBDRemediation
			By("cleaning up SBDRemediation resource")
			cmd := exec.Command("kubectl", "delete", "sbdremediation", sbdRemediationName, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			// Wait for SBDRemediation deletion to complete
			By("waiting for SBDRemediation deletion to complete")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "sbdremediation", sbdRemediationName)
				_, err := utils.Run(cmd)
				return err != nil // Error means resource not found (deleted)
			}, 30*time.Second, 2*time.Second).Should(BeTrue())

			// Clean up temporary file
			if tmpFile != "" {
				os.Remove(tmpFile)
			}
		})

		It("should create and manage SBDRemediation resource", func() {
			By("creating an SBDRemediation resource")
			sbdRemediationYAML := fmt.Sprintf(`
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: %s
spec:
  nodeName: "test-node"
  reason: "ManualFencing"
  timeoutSeconds: 60
`, sbdRemediationName)

			// Write SBDRemediation to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdremediation-%s.yaml", sbdRemediationName))
			err := os.WriteFile(tmpFile, []byte(sbdRemediationYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Apply the SBDRemediation
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation resource exists")
			verifySBDRemediationExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(sbdRemediationName))
			}
			Eventually(verifySBDRemediationExists, 30*time.Second).Should(Succeed())

			By("verifying the SBDRemediation has conditions")
			verifySBDRemediationConditions := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have status conditions")
			}
			Eventually(verifySBDRemediationConditions, 60*time.Second).Should(Succeed())

			By("verifying the SBDRemediation status fields are set")
			verifySBDRemediationStatus := func(g Gomega) {
				// Check if operatorInstance is set
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.operatorInstance}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have operatorInstance set")

				// Check if lastUpdateTime is set
				cmd = exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.lastUpdateTime}")
				output, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have lastUpdateTime set")
			}
			Eventually(verifySBDRemediationStatus, 60*time.Second).Should(Succeed())
		})
	})

	Context("SBD Remediation Enhancements", func() {
		It("should be able to create and reconcile SBDConfig resources", func() {
			By("creating a test SBDConfig from sample configuration")
			// Load sample configuration
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with test name and ensure Always pull policy
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", "test-sbdconfig")
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)

			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			cmd.Stdin = strings.NewReader(sbdConfigYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig is created and processed")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdconfig", "test-sbdconfig", "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(output, "Ready")
			}, 4*time.Minute).Should(BeTrue())

			By("cleaning up the test SBDConfig")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdconfig", "test-sbdconfig", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should handle SBDRemediation resources with timeout validation", func() {
			By("creating a test SBDRemediation with custom timeout")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			sbdRemediationYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-sbdremediation-timeout
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 120
`
			cmd.Stdin = strings.NewReader(sbdRemediationYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation timeout is preserved")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "-o", "jsonpath={.spec.timeoutSeconds}")
				output, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(output)
			}).Should(Equal("120"))

			By("verifying the SBDRemediation is processed")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(output, "Ready")
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should handle multiple SBDRemediation resources concurrently", func() {
			const numRemediations = 5
			remediationNames := make([]string, numRemediations)

			By(fmt.Sprintf("creating %d SBDRemediation resources", numRemediations))
			for i := 0; i < numRemediations; i++ {
				remediationNames[i] = fmt.Sprintf("test-concurrent-remediation-%d", i)
				sbdRemediationYAML := fmt.Sprintf(`
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: %s
spec:
  nodeName: test-worker-node-%d
  reason: NodeUnresponsive
  timeoutSeconds: 60
`, remediationNames[i], i)

				cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
				cmd.Stdin = strings.NewReader(sbdRemediationYAML)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create SBDRemediation %d", i))
			}

			By("verifying all SBDRemediations are processed")
			for i, name := range remediationNames {
				Eventually(func() bool {
					cmd := exec.Command("kubectl", "-n", testNamespace, "get", "sbdremediation", name, "-o", "jsonpath={.status.conditions}")
					output, err := utils.Run(cmd)
					if err != nil {
						return false
					}
					return strings.Contains(output, "Ready")
				}).Should(BeTrue(), fmt.Sprintf("SBDRemediation %d should be ready", i))
			}

			By("cleaning up all test SBDRemediations")
			for _, name := range remediationNames {
				cmd := exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", name, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			}
		})

		It("should handle SBDRemediation resources with invalid node names", func() {
			By("creating a test SBDRemediation with invalid node name")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			sbdRemediationYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-node-remediation
spec:
  nodeName: non-existent-node-12345
  reason: HeartbeatTimeout
  timeoutSeconds: 30
`
			cmd.Stdin = strings.NewReader(sbdRemediationYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation becomes ready with failed fencing")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-invalid-node-remediation", "-o", "json")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}

				var remediation map[string]interface{}
				if err := json.Unmarshal([]byte(output), &remediation); err != nil {
					return false
				}

				status, ok := remediation["status"].(map[string]interface{})
				if !ok {
					return false
				}

				conditions, ok := status["conditions"].([]interface{})
				if !ok {
					return false
				}

				readyFound := false
				fencingFailed := false

				for _, conditionInterface := range conditions {
					condition, ok := conditionInterface.(map[string]interface{})
					if !ok {
						continue
					}

					conditionType, ok := condition["type"].(string)
					if !ok {
						continue
					}

					conditionStatus, ok := condition["status"].(string)
					if !ok {
						continue
					}

					if conditionType == "Ready" && conditionStatus == "True" {
						readyFound = true
						reason, _ := condition["reason"].(string)
						if reason == "Failed" {
							fencingFailed = true
						}
					}
				}

				return readyFound && fencingFailed
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-invalid-node-remediation", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should validate timeout ranges in SBDRemediation CRD", func() {
			By("attempting to create SBDRemediation with timeout below minimum")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			invalidTimeoutYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-timeout-low
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 29
`
			cmd.Stdin = strings.NewReader(invalidTimeoutYAML)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject timeout below minimum (30)")

			By("attempting to create SBDRemediation with timeout above maximum")
			cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			invalidTimeoutYAML = `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-timeout-high
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 301
`
			cmd.Stdin = strings.NewReader(invalidTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject timeout above maximum (300)")

			By("creating SBDRemediation with valid timeout at boundaries")
			validTimeoutYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-valid-timeout-boundary
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 30
`
			cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			cmd.Stdin = strings.NewReader(validTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should accept minimum valid timeout (30)")

			validTimeoutYAML = strings.ReplaceAll(validTimeoutYAML, "timeoutSeconds: 30", "timeoutSeconds: 300")
			validTimeoutYAML = strings.ReplaceAll(validTimeoutYAML, "test-valid-timeout-boundary", "test-valid-timeout-boundary-max")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(validTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should accept maximum valid timeout (300)")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-valid-timeout-boundary", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-valid-timeout-boundary-max", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})
	})

	Context("Preflight Checks", func() {
		var tmpFile string
		var sbdConfigName string

		BeforeEach(func() {
			// Generate unique name for each test
			sbdConfigName = fmt.Sprintf("test-sbdconfig-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			By("cleaning up test SBDConfig")
			if sbdConfigName != "" {
				cmd := exec.Command("kubectl", "delete", "-n", testNamespace, "sbdconfig", sbdConfigName, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)

				// Wait for SBDConfig deletion to complete (finalizer cleanup)
				By("waiting for SBDConfig deletion to complete")
				Eventually(func() bool {
					cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace)
					_, err := utils.Run(cmd)
					return err != nil // Error means resource not found (deleted)
				}, 30*time.Second, 2*time.Second).Should(BeTrue())
			}

			// Clean up temporary file
			if tmpFile != "" {
				os.Remove(tmpFile)
			}
		})

		It("should pass preflight checks with working watchdog and failing SBD device", func() {
			By("creating an SBDConfig with a non-existent SBD device path")
			// Load sample configuration and customize for this test
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with our test name
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", sbdConfigName)
			// Ensure imagePullPolicy is Always for testing
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)
			// Add a non-existent shared storage PVC to force shared storage check to fail
			// This tests the either/or logic: watchdog works, shared storage fails, should still pass
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `sbdWatchdogPath: "/dev/watchdog"`, `sbdWatchdogPath: "/dev/watchdog"
  # Non-existent shared storage PVC to test preflight either/or logic
  sharedStoragePVC: "non-existent-pvc"`)

			// Write SBDConfig to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdconfig-preflight-watchdog-%s.yaml", sbdConfigName))
			err = os.WriteFile(tmpFile, []byte(sbdConfigYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Apply the SBDConfig
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig resource exists")
			verifySBDConfigExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(sbdConfigName))
			}
			Eventually(verifySBDConfigExists, 30*time.Second).Should(Succeed())

			By("verifying the SBD agent DaemonSet is created despite shared storage failure")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)
			verifyDaemonSetCreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(expectedDaemonSetName))
			}
			Eventually(verifyDaemonSetCreated, 60*time.Second).Should(Succeed())

			By("verifying SBD agent pods are created and running (watchdog-only mode)")
			verifySBDPodsRunning := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l", fmt.Sprintf("sbdconfig=%s", sbdConfigName), "-o", "jsonpath={.items[*].status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// All pods should be Running despite shared storage failure
				phases := strings.Fields(output)
				for _, phase := range phases {
					fmt.Printf("Pod phase is %s, expected Running\n", phase)
					g.Expect(phase).To(Equal("Running"), fmt.Sprintf("Pod phase is %s, expected Running", phase))
				}
				g.Expect(len(phases)).To(BeNumerically(">", 0), "No pods found")
			}
			Eventually(verifySBDPodsRunning, 2*time.Minute).Should(Succeed())

			By("verifying agent logs show preflight checks passed with watchdog only")
			verifyPreflightLogs := func(g Gomega) {
				// Get the first pod name
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l", fmt.Sprintf("sbdconfig=%s", sbdConfigName), "-o", "jsonpath={.items[0].metadata.name}")
				podName, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(podName).ToNot(BeEmpty())

				// Check the logs for preflight check messages
				cmd = exec.Command("kubectl", "logs", podName, "-n", testNamespace, "-c", "sbd-agent", "--tail=100")
				logs, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should show that preflight checks passed despite shared storage failure
				g.Expect(logs).To(ContainSubstring("Pre-flight checks passed"), "Should show preflight checks passed")
				g.Expect(logs).To(ContainSubstring("watchdog device available"), "Should show watchdog is available")
			}
			Eventually(verifyPreflightLogs, 2*time.Minute).Should(Succeed())
		})

		It("should pass preflight checks with working shared storage and failing watchdog", func() {
			By("creating an SBDConfig with a non-existent watchdog path")
			// Load sample configuration and customize for this test
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with our test name
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", sbdConfigName)
			// Ensure imagePullPolicy is Always for testing
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)
			// Use a non-existent watchdog path to force watchdog check to fail
			// This tests the either/or logic: shared storage works, watchdog fails, should still pass
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `sbdWatchdogPath: "/dev/watchdog"`, `sbdWatchdogPath: "/dev/non-existent-watchdog"
  # Add a mock shared storage PVC (will be created for testing)
  sharedStoragePVC: "mock-shared-storage"`)

			// Write SBDConfig to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdconfig-preflight-sbd-%s.yaml", sbdConfigName))
			err = os.WriteFile(tmpFile, []byte(sbdConfigYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			By("creating a mock shared storage PVC for testing")
			// Note: In a real scenario, this would be a proper RWX PVC
			// For testing, we'll create a simple PVC that the operator can find
			mockPVCYAML := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mock-shared-storage
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi`

			pvcFile := filepath.Join("/tmp", "mock-pvc.yaml")
			err = os.WriteFile(pvcFile, []byte(mockPVCYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(pvcFile)

			// Create the PVC
			cmd := exec.Command("kubectl", "apply", "-f", pvcFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			defer func() {
				// Clean up PVC
				cleanupCmd := exec.Command("kubectl", "delete", "pvc", "mock-shared-storage", "-n", testNamespace, "--ignore-not-found=true")
				utils.Run(cleanupCmd)
			}()

			// Apply the SBDConfig
			cmd = exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig resource exists")
			verifySBDConfigExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "sbdconfig", sbdConfigName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(sbdConfigName))
			}
			Eventually(verifySBDConfigExists, 30*time.Second).Should(Succeed())

			By("verifying the SBD agent DaemonSet is created despite watchdog failure")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)
			verifyDaemonSetCreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset", expectedDaemonSetName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(expectedDaemonSetName))
			}
			Eventually(verifyDaemonSetCreated, 60*time.Second).Should(Succeed())

			By("verifying SBD agent pods are created and running (shared storage mode)")
			verifySBDPodsRunning := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l", fmt.Sprintf("sbdconfig=%s", sbdConfigName), "-o", "jsonpath={.items[*].status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// All pods should be Running despite watchdog failure
				phases := strings.Fields(output)
				for _, phase := range phases {
					g.Expect(phase).To(Equal("Running"), fmt.Sprintf("Pod phase is %s, expected Running", phase))
				}
				g.Expect(len(phases)).To(BeNumerically(">", 0), "No pods found")
			}
			Eventually(verifySBDPodsRunning, 2*time.Minute).Should(Succeed())

			By("verifying agent logs show preflight checks passed with shared storage")
			verifyPreflightLogs := func(g Gomega) {
				// Get the first pod name
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l", fmt.Sprintf("sbdconfig=%s", sbdConfigName), "-o", "jsonpath={.items[0].metadata.name}")
				podName, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(podName).ToNot(BeEmpty())

				// Check the logs for preflight check messages
				cmd = exec.Command("kubectl", "logs", podName, "-n", testNamespace, "-c", "sbd-agent", "--tail=100")
				logs, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should show that preflight checks passed despite watchdog failure
				g.Expect(logs).To(ContainSubstring("Pre-flight checks passed"), "Should show preflight checks passed")
				g.Expect(logs).To(ContainSubstring("shared storage available"), "Should show shared storage is available")
			}
			Eventually(verifyPreflightLogs, 2*time.Minute).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// createE2EKustomization creates a dynamic kustomization file for e2e testing
func createE2EKustomization(imageName string) error {
	// Parse the image name to extract components
	parts := strings.Split(imageName, "/")
	var newName, newTag string

	if len(parts) >= 2 {
		imageParts := strings.Split(parts[len(parts)-1], ":")
		newName = strings.Join(parts[:len(parts)-1], "/") + "/" + imageParts[0]
		if len(imageParts) > 1 {
			newTag = imageParts[1]
		} else {
			newTag = "latest"
		}
	} else {
		imageParts := strings.Split(imageName, ":")
		newName = imageParts[0]
		if len(imageParts) > 1 {
			newTag = imageParts[1]
		} else {
			newTag = "latest"
		}
	}

	kustomizationContent := fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- ../../config/default

images:
- name: controller:latest
  newName: %s
  newTag: %s

patches:
- path: image-pull-policy-patch.yaml
  target:
    kind: Deployment
    name: controller-manager
`, newName, newTag)

	return os.WriteFile("test/e2e/kustomization.yaml", []byte(kustomizationContent), 0644)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
