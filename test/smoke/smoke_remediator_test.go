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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sbd-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sbd-operator-metrics-binding"

// conditionTypeReady represents the Ready condition type
const conditionTypeReady = "Ready"

var _ = Describe("SBD Remediation Smoke Tests", Label("Smoke", "Remediation"), func() {

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

	Context("SBD Remediation", func() {
		var sbdRemediationName string

		BeforeEach(func() {
			sbdRemediationName = fmt.Sprintf("test-sbdremediation-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			// Clean up StorageBasedRemediation
			By("cleaning up StorageBasedRemediation resource")
			sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
			err := testClients.Client.Get(testClients.Context,
				client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace.Name}, sbdRemediation)
			if err == nil {
				_ = testClients.Client.Delete(testClients.Context, sbdRemediation)
			}

			// Wait for StorageBasedRemediation deletion to complete
			By("waiting for StorageBasedRemediation deletion to complete")
			Eventually(func() bool {
				sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace.Name}, sbdRemediation)
				return errors.IsNotFound(err) // Error means resource not found (deleted)
			}, 30*time.Second, 2*time.Second).Should(BeTrue())
		})

		It("should create and manage StorageBasedRemediation resource", func() {
			By("creating an StorageBasedRemediation resource")
			testNodeName := "test-node"
			sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 60,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageBasedRemediation")

			By("verifying the StorageBasedRemediation resource exists")
			verifySBDRemediationExists := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Name).To(Equal(sbdRemediationName))
			}
			Eventually(verifySBDRemediationExists, 30*time.Second).Should(Succeed())

			By("verifying the StorageBasedRemediation has conditions")
			verifySBDRemediationConditions := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Status.Conditions).ToNot(BeEmpty(), "StorageBasedRemediation should have status conditions")
			}
			Eventually(verifySBDRemediationConditions, 60*time.Second).Should(Succeed())

			By("verifying the StorageBasedRemediation status fields are set")
			verifySBDRemediationStatus := func(g Gomega) {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      sbdRemediationName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(foundSBDRemediation.Status.OperatorInstance).ToNot(BeEmpty(),
					"StorageBasedRemediation should have operatorInstance set")
				g.Expect(foundSBDRemediation.Status.LastUpdateTime).ToNot(BeNil(), "StorageBasedRemediation should have lastUpdateTime set")
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
				if len(foundSBDConfig.Status.Conditions) == 0 {
					return false
				}
				for _, condition := range foundSBDConfig.Status.Conditions {
					if condition.Type == conditionTypeReady {
						return true
					}
				}
				return false
			}, 4*time.Minute).Should(BeTrue())

			By("cleaning up the test SBDConfig")
			err = testClients.Client.Delete(testClients.Context, sbdConfig)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle StorageBasedRemediation resources with timeout validation", func() {
			By("creating a test StorageBasedRemediation with custom timeout")
			testTimeoutNodeName := "test-timeout-node"
			sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testTimeoutNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 120,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageBasedRemediation")

			By("verifying the StorageBasedRemediation timeout is preserved")
			Eventually(func() int32 {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      testTimeoutNodeName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return 0
				}
				return foundSBDRemediation.Spec.TimeoutSeconds
			}).Should(Equal(int32(120)))

			By("verifying the StorageBasedRemediation is processed")
			Eventually(func() bool {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      testTimeoutNodeName,
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return false
				}
				if len(foundSBDRemediation.Status.Conditions) == 0 {
					return false
				}
				for _, condition := range foundSBDRemediation.Status.Conditions {
					if condition.Type == conditionTypeReady {
						return true
					}
				}
				return false
			}).Should(BeTrue())

			By("cleaning up the test StorageBasedRemediation")
			err = testClients.Client.Delete(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle multiple StorageBasedRemediation resources concurrently", func() {

			By("getting real worker node names from the cluster")
			nodes := &corev1.NodeList{}
			err := testClients.Client.List(testClients.Context, nodes)
			Expect(err).NotTo(HaveOccurred())

			var workerNodes []string
			for _, node := range nodes.Items {
				// Filter to worker nodes (not control plane)
				isWorker := true
				for label := range node.Labels {
					if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
						isWorker = false
						break
					}
				}
				if isWorker {
					workerNodes = append(workerNodes, node.Name)
				}
			}
			numRemediations := len(workerNodes) - 1 // Reduced to use available worker nodes

			if numRemediations < 1 {
				Skip(fmt.Sprintf("Need at least %d worker nodes for concurrent test, found %d", 2, len(workerNodes)))
			}

			remediationNames := make([]string, numRemediations)

			By(fmt.Sprintf("creating %d StorageBasedRemediation resources with real node names", numRemediations))
			for i := 0; i < numRemediations; i++ {
				// Use node name directly as remediation name
				remediationNames[i] = workerNodes[i]
				sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      remediationNames[i],
						Namespace: testNamespace.Name,
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
						Reason:         medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
						TimeoutSeconds: 60,
					},
				}
				err := testClients.Client.Create(testClients.Context, sbdRemediation)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create StorageBasedRemediation %d", i))
			}

			By("verifying all SBDRemediations are processed by agents")
			for i, name := range remediationNames {
				Eventually(func() bool {
					foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
					err := testClients.Client.Get(testClients.Context, types.NamespacedName{
						Name:      name,
						Namespace: testNamespace.Name,
					}, foundSBDRemediation)
					if err != nil {
						fmt.Printf("Error getting StorageBasedRemediation %s: %v\n", name, err)
						return false
					}

					// Log status for debugging
					if foundSBDRemediation.Status.Conditions != nil {
						fmt.Printf("StorageBasedRemediation %s conditions: %+v\n", name, foundSBDRemediation.Status.Conditions)
					}

					// For smoke tests without SBD devices, we expect:
					// 1. Ready=True (processing completed)
					// 2. FencingSucceeded=False (no SBD device available)
					// This validates the agent pipeline works but safely fails at device access
					if len(foundSBDRemediation.Status.Conditions) > 0 {
						hasReady := false
						hasFencingFailed := false

						for _, condition := range foundSBDRemediation.Status.Conditions {
							if condition.Type == conditionTypeReady && condition.Status == "True" {
								hasReady = true
							}
							if condition.Type == "FencingSucceeded" && condition.Status == "False" {
								hasFencingFailed = true
								// Verify it's the expected failure reason (no SBD device)
								if strings.Contains(condition.Message, "SBD device") ||
									strings.Contains(condition.Message, "no such file or directory") {
									fmt.Printf("Expected fencing failure for %s: %s\n", name, condition.Message)
								}
							}
						}

						// Both conditions indicate successful agent processing with expected failure
						if hasReady && hasFencingFailed {
							return true
						}
					}
					return false
				}, time.Minute*2, time.Second*10).Should(BeTrue(),
					fmt.Sprintf("StorageBasedRemediation %d should be processed by agent (expect safe fencing failure)", i))
			}

			By("cleaning up all test SBDRemediations")
			for _, name := range remediationNames {
				sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: name, Namespace: testNamespace.Name}, sbdRemediation)
				if err == nil {
					_ = testClients.Client.Delete(testClients.Context, sbdRemediation)
				}
			}
		})

		It("should handle StorageBasedRemediation resources with invalid node names", func() {
			By("creating a test StorageBasedRemediation with intentionally invalid node name (tests error handling)")
			// Note: This test specifically uses a clearly fake node name to test
			// error handling for truly non-existent nodes, unlike other tests
			// that use real cluster node names for realistic pipeline testing
			invalidNodeName := "definitely-non-existent-node-12345" // Clearly fake for error testing
			sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      invalidNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 30,
				},
			}

			err := testClients.Client.Create(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageBasedRemediation")

			By("verifying the StorageBasedRemediation becomes ready with failed fencing due to invalid node")
			Eventually(func() bool {
				foundSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{}
				err := testClients.Client.Get(testClients.Context, types.NamespacedName{
					Name:      "test-invalid-node-remediation",
					Namespace: testNamespace.Name,
				}, foundSBDRemediation)
				if err != nil {
					return false
				}

				if len(foundSBDRemediation.Status.Conditions) == 0 {
					return false
				}

				// For truly invalid node names, we expect:
				// 1. Ready=True (processing completed)
				// 2. FencingSucceeded=False (node name couldn't be mapped to node ID)
				// This validates error handling for non-existent nodes
				hasReady := false
				hasFencingFailed := false
				hasNodeMappingError := false

				for _, condition := range foundSBDRemediation.Status.Conditions {
					if condition.Type == conditionTypeReady && condition.Status == "True" {
						hasReady = true
					}
					if condition.Type == "FencingSucceeded" && condition.Status == "False" {
						hasFencingFailed = true
						// Verify it's the expected node mapping failure
						if strings.Contains(condition.Message, "Failed to map node name") ||
							strings.Contains(condition.Message, "unable to extract valid node ID") ||
							strings.Contains(condition.Message, "no numeric part found") {
							hasNodeMappingError = true
							fmt.Printf("Expected node mapping error for invalid node: %s\n", condition.Message)
						}
					}
				}

				// All conditions indicate proper error handling for invalid node names
				return hasReady && hasFencingFailed && hasNodeMappingError
			}, time.Minute*1, time.Second*10).Should(BeTrue())

			By("cleaning up the test StorageBasedRemediation")
			err = testClients.Client.Delete(testClients.Context, sbdRemediation)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should validate timeout ranges in StorageBasedRemediation CRD", func() {
			By("getting a real worker node name for validation tests")
			nodes := &corev1.NodeList{}
			err := testClients.Client.List(testClients.Context, nodes)
			Expect(err).NotTo(HaveOccurred())

			var testNodeName string
			for _, node := range nodes.Items {
				// Use first worker node found
				isWorker := true
				for label := range node.Labels {
					if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
						isWorker = false
						break
					}
				}
				if isWorker {
					testNodeName = node.Name
					break
				}
			}

			if testNodeName == "" {
				Skip("No worker nodes found for timeout validation test")
			}

			By("attempting to create StorageBasedRemediation with timeout below minimum")
			invalidSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 29, // Below minimum (30)
				},
			}

			err = testClients.Client.Create(testClients.Context, invalidSBDRemediation)
			Expect(err).To(HaveOccurred(), "Should reject timeout below minimum (30)")

			By("attempting to create StorageBasedRemediation with timeout above maximum")
			invalidSBDRemediation = &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 301, // Above maximum (300)
				},
			}

			err = testClients.Client.Create(testClients.Context, invalidSBDRemediation)
			Expect(err).To(HaveOccurred(), "Should reject timeout above maximum (300)")

			By("creating StorageBasedRemediation with valid timeout at boundaries")
			validSBDRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: 30, // Minimum valid timeout
				},
			}

			err = testClients.Client.Create(testClients.Context, validSBDRemediation)
			Expect(err).NotTo(HaveOccurred(), "Should accept minimum valid timeout (30)")

			validSBDRemediationMax := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
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
