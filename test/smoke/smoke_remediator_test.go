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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
)

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sbd-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sbd-operator-metrics-binding"

var _ = Describe("SBD Remediation Smoke Tests", Label("Smoke", "Remediation"), func() {
	var controllerPodName string

	// Verify the environment is set up correctly (setup handled by Makefile)
	//	BeforeAll(func() {
	//	})

	// Clean up test-specific resources (overall cleanup handled by Makefile)
	//	AfterAll(func() {
	//	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			debugCollector := testClients.NewDebugCollector()

			// Collect controller logs
			debugCollector.CollectControllerLogs(namespace, controllerPodName)

			// Collect Kubernetes events
			debugCollector.CollectKubernetesEvents(namespace)
		}

	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("SBD Remediation", func() {
		var sbdRemediationName string

		BeforeEach(func() {
			sbdRemediationName = fmt.Sprintf("test-sbdremediation-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			// Clean up SBDRemediation
			By("cleaning up SBDRemediation resource")
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{}
			err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace.Name}, sbdRemediation)
			if err == nil {
				_ = testClients.Client.Delete(testClients.Context, sbdRemediation)
			}

			// Wait for SBDRemediation deletion to complete
			By("waiting for SBDRemediation deletion to complete")
			Eventually(func() bool {
				sbdRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace.Name}, sbdRemediation)
				return errors.IsNotFound(err) // Error means resource not found (deleted)
			}, 30*time.Second, 2*time.Second).Should(BeTrue())
		})

		It("should create and manage SBDRemediation resource", func() {
			By("creating an SBDRemediation resource")
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 60,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation resource exists")
			verifySBDRemediationExists := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Name).To(Equal(sbdRemediationName))
			}
			Eventually(verifySBDRemediationExists, 30*time.Second).Should(Succeed())

			By("verifying the SBDRemediation has conditions")
			verifySBDRemediationConditions := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Status.Conditions).ToNot(BeEmpty(), "SBDRemediation should have status conditions")
			}
			Eventually(verifySBDRemediationConditions, 60*time.Second).Should(Succeed())

			By("verifying the SBDRemediation status fields are set")
			verifySBDRemediationStatus := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Status.OperatorInstance).ToNot(BeEmpty(), "SBDRemediation should have operatorInstance set")
				g.Expect(foundSBDRemediation.Status.LastUpdateTime).ToNot(BeNil(), "SBDRemediation should have lastUpdateTime set")
			}
			Eventually(verifySBDRemediationStatus, 60*time.Second).Should(Succeed())
		})
	})

	Context("SBD Remediation Enhancements", func() {
		It("should be able to create and reconcile SBDConfig resources", func() {
			By("creating a test SBDConfig from sample configuration")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sbdconfig",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath:     "/dev/watchdog",
					ImagePullPolicy:     "Always",
					WatchdogTimeout:     &metav1.Duration{Duration: 90 * time.Second},
					StaleNodeTimeout:    &metav1.Duration{Duration: 1 * time.Hour},
					PetIntervalMultiple: func() *int32 { v := int32(6); return &v }(),
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdConfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig is created and processed")
			Eventually(func() bool {
				foundSBDConfig := &medik8sv1alpha1.SBDConfig{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      "test-sbdconfig",
					Namespace: testNamespace.Name,
				}, foundSBDConfig)
				if err != nil {
					return false
				}
				if foundSBDConfig.Status.Conditions == nil || len(foundSBDConfig.Status.Conditions) == 0 {
					return false
				}
				for _, condition := range foundSBDConfig.Status.Conditions {
					if condition.Type == "Ready" {
						return true
					}
				}
				return false
			}, 4*time.Minute).Should(BeTrue())

			By("cleaning up the test SBDConfig")
			err = testClients.Client.Delete(testClients.Context, sbdConfig)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle SBDRemediation resources with timeout validation", func() {
			By("creating a test SBDRemediation with custom timeout")
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sbdremediation-timeout",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-worker-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 120,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation timeout is preserved")
			Eventually(func() int32 {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      "test-sbdremediation-timeout",
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return 0
				}
				return foundSBDRemediation.Spec.TimeoutSeconds
			}).Should(Equal(int32(120)))

			By("verifying the SBDRemediation is processed")
			Eventually(func() bool {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      "test-sbdremediation-timeout",
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return false
				}
				if foundSBDRemediation.Status.Conditions == nil || len(foundSBDRemediation.Status.Conditions) == 0 {
					return false
				}
				for _, condition := range foundSBDRemediation.Status.Conditions {
					if condition.Type == "Ready" {
						return true
					}
				}
				return false
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			err = testClients.Client.Delete(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle multiple SBDRemediation resources concurrently", func() {
			const numRemediations = 5
			remediationNames := make([]string, numRemediations)

			By(fmt.Sprintf("creating %d SBDRemediation resources", numRemediations))
			for i := 0; i < numRemediations; i++ {
				remediationNames[i] = fmt.Sprintf("test-concurrent-remediation-%d", i)
				sbdRemediation := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      remediationNames[i],
						Namespace: testNamespace.Name,
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName:       fmt.Sprintf("test-worker-node-%d", i),
						Reason:         medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
						TimeoutSeconds: 60,
					},
				}
				err := testClients.Client.Create(testClients.Context, sbdRemediation)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create SBDRemediation %d", i))
			}

			By("verifying all SBDRemediations are processed")
			for i, name := range remediationNames {
				Eventually(func() bool {
					foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
					err := testClients.Client.Get(testClients.Context, types.NamespacedName{
						Name:      name,
						Namespace: testNamespace.Name,
					}, foundSBDRemediation)
					if err != nil {
						fmt.Println("Error getting SBDRemediation", err)
						return false
					}
					fmt.Println("SBDRemediation", foundSBDRemediation.Status.Conditions)
					return foundSBDRemediation.Status.Conditions != nil && len(foundSBDRemediation.Status.Conditions) > 0 && foundSBDRemediation.Status.Conditions[0].Type == "Ready" && foundSBDRemediation.Status.Conditions[0].Status == "True"
				}).Should(BeTrue(), fmt.Sprintf("SBDRemediation %d should be ready", i))
			}

			By("cleaning up all test SBDRemediations")
			for _, name := range remediationNames {
				sbdRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: name, Namespace: testNamespace.Name}, sbdRemediation)
				if err == nil {
					_ = testClients.Client.Delete(testClients.Context, sbdRemediation)
				}
			}
		})

		It("should handle SBDRemediation resources with invalid node names", func() {
			By("creating a test SBDRemediation with invalid node name")
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-node-remediation",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "non-existent-node-12345",
					Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 30,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation becomes ready with failed fencing")
			Eventually(func() bool {
				foundSBDRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      "test-invalid-node-remediation",
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return false
				}

				if foundSBDRemediation.Status.Conditions == nil || len(foundSBDRemediation.Status.Conditions) == 0 {
					return false
				}

				readyFound := false
				fencingFailed := false

				for _, condition := range foundSBDRemediation.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == "True" {
						readyFound = true
						if condition.Reason == "Failed" {
							fencingFailed = true
						}
					}
				}

				return readyFound && fencingFailed
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			err = testClients.Client.Delete(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should validate timeout ranges in SBDRemediation CRD", func() {
			By("attempting to create SBDRemediation with timeout below minimum")
			invalidSBDRemediation := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-timeout-low",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-worker-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 29, // Below minimum (30)
				},
			}

			err := testClients.Client.Create(testClients.Context, invalidSBDRemediation)
			Expect(err).To(HaveOccurred(), "Should reject timeout below minimum (30)")

			By("attempting to create SBDRemediation with timeout above maximum")
			invalidSBDRemediation = &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-timeout-high",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-worker-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 301, // Above maximum (300)
				},
			}

			err = testClients.Client.Create(testClients.Context, invalidSBDRemediation)
			Expect(err).To(HaveOccurred(), "Should reject timeout above maximum (300)")

			By("creating SBDRemediation with valid timeout at boundaries")
			validSBDRemediation := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid-timeout-boundary",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-worker-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 30, // Minimum valid timeout
				},
			}

			err = testClients.Client.Create(testClients.Context, validSBDRemediation)
			Expect(err).NotTo(HaveOccurred(), "Should accept minimum valid timeout (30)")

			validSBDRemediationMax := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid-timeout-boundary-max",
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "test-worker-node",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 300, // Maximum valid timeout
				},
			}

			err = testClients.Client.Create(testClients.Context, validSBDRemediationMax)
			Expect(err).NotTo(HaveOccurred(), "Should accept maximum valid timeout (300)")

			By("cleaning up test resources")
			err = testClients.Client.Delete(testClients.Context, validSBDRemediation)
			Expect(err).NotTo(HaveOccurred())
			err = testClients.Client.Delete(testClients.Context, validSBDRemediationMax)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
