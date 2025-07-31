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
	"bytes"
	"fmt"
	"io"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBD Operator Smoke Tests", Ordered, Label("Smoke", "Operator"), func() {
	var controllerPodName string

	// Verify the environment is set up correctly (setup handled by Makefile)
	BeforeAll(func() {
		By("initializing Kubernetes clients")
		var err error
		testClients, err = utils.SetupKubernetesClients()
		Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
	})

	// Clean up test-specific resources (overall cleanup handled by Makefile)
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		pod := &corev1.Pod{}
		err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: "curl-metrics", Namespace: namespace}, pod)
		if err == nil {
			_ = testClients.Client.Delete(testClients.Context, pod)
		}

		By("cleaning up metrics ClusterRoleBinding")
		clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
		err = testClients.Client.Get(testClients.Context, client.ObjectKey{Name: metricsRoleBindingName}, clusterRoleBinding)
		if err == nil {
			_ = testClients.Client.Delete(testClients.Context, clusterRoleBinding)
		}

	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			systemNamespace := &utils.TestNamespace{
				Name:         "sbd-operator-system",
				ArtifactsDir: "testrun/sbd-operator-system",
				Clients:      testClients,
			}
			utils.DescribeEnvironment(testClients, systemNamespace)
			utils.DescribeEnvironment(testClients, testNamespace)
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
				err := testClients.Client.List(testClients.Context, pods,
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
			existingCRB := &rbacv1.ClusterRoleBinding{}
			err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: metricsRoleBindingName}, existingCRB)
			if err == nil {
				_ = testClients.Client.Delete(testClients.Context, existingCRB)
			}

			// Create ClusterRoleBinding
			clusterRoleBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: metricsRoleBindingName,
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "sbd-operator-metrics-reader",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      serviceAccountName,
						Namespace: namespace,
					},
				},
			}
			err = testClients.Client.Create(testClients.Context, clusterRoleBinding)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			service := &corev1.Service{}
			err = testClients.Client.Get(testClients.Context, client.ObjectKey{Name: metricsServiceName, Namespace: namespace}, service)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			tokenGenerator := testClients.NewServiceAccountTokenGenerator()
			token, err := tokenGenerator.GenerateToken(namespace, serviceAccountName)
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				endpoints := &corev1.Endpoints{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: metricsServiceName, Namespace: namespace}, endpoints)
				g.Expect(err).NotTo(HaveOccurred())

				hasPort8443 := false
				for _, subset := range endpoints.Subsets {
					for _, port := range subset.Ports {
						if port.Port == 8443 {
							hasPort8443 = true
							break
						}
					}
				}
				g.Expect(hasPort8443).To(BeTrue(), "Metrics endpoint is not ready - port 8443 not found")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				req := testClients.Clientset.CoreV1().Pods(namespace).GetLogs(controllerPodName, &corev1.PodLogOptions{})
				podLogs, err := req.Stream(testClients.Context)
				g.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = podLogs.Close() }()

				buf := new(bytes.Buffer)
				_, _ = io.Copy(buf, podLogs)
				output := buf.String()
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			curlPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "curl-metrics",
					Namespace: namespace,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:    "curl",
							Image:   "curlimages/curl:latest",
							Command: []string{"/bin/sh", "-c"},
							Args: []string{
								fmt.Sprintf("curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics",
									token, metricsServiceName, namespace),
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &[]bool{false}[0],
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								RunAsNonRoot: &[]bool{true}[0],
								RunAsUser:    &[]int64{1000}[0],
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
						},
					},
				},
			}
			err = testClients.Client.Create(testClients.Context, curlPod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				pod := &corev1.Pod{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: "curl-metrics", Namespace: namespace}, pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pod.Status.Phase).To(Equal(corev1.PodSucceeded), "curl pod in wrong status")
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
})

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	req := testClients.Clientset.CoreV1().Pods(namespace).GetLogs("curl-metrics", &corev1.PodLogOptions{})
	podLogs, err := req.Stream(testClients.Context)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	defer func() { _ = podLogs.Close() }()

	buf := new(bytes.Buffer)
	_, _ = io.Copy(buf, podLogs)
	metricsOutput := buf.String()

	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}
