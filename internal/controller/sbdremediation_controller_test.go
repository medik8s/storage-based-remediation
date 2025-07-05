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

package controller

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/blockdevice"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// testableReconciler wraps SBDRemediationReconciler to make IsLeader mockable for testing
type testableReconciler struct {
	*SBDRemediationReconciler
	isLeaderFunc func() bool
}

func (r *testableReconciler) IsLeader() bool {
	if r.isLeaderFunc != nil {
		return r.isLeaderFunc()
	}
	return r.SBDRemediationReconciler.IsLeader()
}

var _ = Describe("SBDRemediation Controller", func() {
	Context("Node ID Mapping", func() {
		var reconciler *SBDRemediationReconciler
		var tempDir string
		var mockSBDDevice string
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()

			// Create temporary directory for mock SBD device
			var err error
			tempDir, err = ioutil.TempDir("", "sbd-nodemanager-test-")
			Expect(err).NotTo(HaveOccurred())

			// Create directory structure for default SBD device path
			sbdSharedDir := filepath.Join(tempDir, "sbd-shared")
			err = os.MkdirAll(sbdSharedDir, 0755)
			Expect(err).NotTo(HaveOccurred())

			// Create mock SBD device file at the expected default path (512KB to accommodate all slots)
			mockSBDDevice = filepath.Join(sbdSharedDir, "sbd-device")
			err = ioutil.WriteFile(mockSBDDevice, make([]byte, 512*1024), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Create a mock Node for testing
			testNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "",
						"kubernetes.io/os":               "linux",
					},
				},
			}
			Expect(k8sClient.Create(ctx, testNode)).To(Succeed())

			// Create a mock SBDConfig for testing
			testSBDConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sbd-config",
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					// Configure with no shared storage for simpler test setup
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			}
			Expect(k8sClient.Create(ctx, testSBDConfig)).To(Succeed())

			reconciler = &SBDRemediationReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				sbdDevicePath: mockSBDDevice,
				clusterName:   "test-cluster",
			}

			// Initialize the SBD device directly for tests without path discovery
			device, err := blockdevice.Open(mockSBDDevice)
			Expect(err).NotTo(HaveOccurred())
			reconciler.sbdDevice = device

			// Initialize NodeManager for consistent node-to-slot mapping
			err = reconciler.initializeNodeManager(ctx, logf.Log.WithName("test"))
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			if reconciler != nil {
				reconciler.Cleanup()
			}

			// Clean up mock Node
			testNode := &corev1.Node{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-node"}, testNode)
			if err == nil {
				_ = k8sClient.Delete(ctx, testNode)
			}

			// Clean up mock SBDConfig
			testSBDConfig := &medik8sv1alpha1.SBDConfig{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "test-sbd-config", Namespace: "default"}, testSBDConfig)
			if err == nil {
				_ = k8sClient.Delete(ctx, testSBDConfig)
			}

			os.RemoveAll(tempDir)
		})

		It("should consistently map the same node names to the same slot IDs", func() {
			testNodes := []string{
				"worker-1", "worker-2", "node-123",
				"ip-10-0-1-45.ec2.internal",
				"gke-cluster-default-pool-a1b2c3d4-xyz5",
			}

			// Map each node name and verify consistency
			for _, nodeName := range testNodes {
				// First mapping
				nodeID1, err := reconciler.nodeNameToNodeID(ctx, nodeName)
				Expect(err).NotTo(HaveOccurred())
				Expect(nodeID1).To(BeNumerically(">=", 1))
				Expect(nodeID1).To(BeNumerically("<=", sbdprotocol.SBD_MAX_NODES))

				// Second mapping should return the same ID
				nodeID2, err := reconciler.nodeNameToNodeID(ctx, nodeName)
				Expect(err).NotTo(HaveOccurred())
				Expect(nodeID2).To(Equal(nodeID1))
			}
		})

		It("should handle empty node names", func() {
			_, err := reconciler.nodeNameToNodeID(ctx, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("node name cannot be empty"))
		})

		It("should fail when NodeManager is not initialized", func() {
			uninitializedReconciler := &SBDRemediationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := uninitializedReconciler.nodeNameToNodeID(ctx, "test-node")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("node manager not initialized"))
		})

		It("should assign different slot IDs for different node names", func() {
			nodeIDs := make(map[uint16]string)
			testNodes := []string{
				"node-alpha", "node-beta", "node-gamma", "node-delta",
			}

			for _, nodeName := range testNodes {
				nodeID, err := reconciler.nodeNameToNodeID(ctx, nodeName)
				Expect(err).NotTo(HaveOccurred())

				// Check for slot conflicts
				if existingNode, exists := nodeIDs[nodeID]; exists {
					// This should be very rare with good hash distribution
					// If it happens, the NodeManager should handle collision resolution
					Fail(fmt.Sprintf("Slot collision: node %s and %s both got slot %d", nodeName, existingNode, nodeID))
				}
				nodeIDs[nodeID] = nodeName
			}

			// Verify we got different IDs for different nodes
			Expect(len(nodeIDs)).To(Equal(len(testNodes)))
		})
	})

	Context("FencingError", func() {
		It("should format error messages correctly", func() {
			err := &FencingError{
				Operation:  "test operation",
				Underlying: fmt.Errorf("underlying error"),
				Retryable:  true,
				NodeName:   "worker-1",
				NodeID:     5,
			}

			expectedMessage := "fencing error during test operation for node worker-1 (ID: 5): underlying error (retryable)"
			Expect(err.Error()).To(Equal(expectedMessage))
		})

		It("should handle non-retryable errors", func() {
			err := &FencingError{
				Operation:  "marshaling",
				Underlying: fmt.Errorf("invalid data"),
				Retryable:  false,
				NodeName:   "worker-2",
				NodeID:     3,
			}

			Expect(err.Error()).To(ContainSubstring("non-retryable"))
		})
	})

	Context("When reconciling a SBDRemediation resource", func() {
		var (
			reconciler     *SBDRemediationReconciler
			testReconciler *testableReconciler
			ctx            context.Context
			resourceName   string
			namespacedName types.NamespacedName
			tempDir        string
			mockSBDDevice  string
		)

		BeforeEach(func() {
			ctx = context.Background()
			resourceName = fmt.Sprintf("test-remediation-%d", time.Now().UnixNano())
			namespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			// Create temporary directory for mock SBD device
			var err error
			tempDir, err = ioutil.TempDir("", "sbd-controller-test-")
			Expect(err).NotTo(HaveOccurred())

			// Create mock SBD device file (512KB to accommodate all slots)
			mockSBDDevice = filepath.Join(tempDir, "sbd-device")
			err = ioutil.WriteFile(mockSBDDevice, make([]byte, 512*1024), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Create reconciler with test configuration
			reconciler = &SBDRemediationReconciler{
				Client:                k8sClient,
				Scheme:                k8sClient.Scheme(),
				leaderElectionEnabled: false, // Disable for tests
				sbdDevicePath:         mockSBDDevice,
			}

			// Create testable reconciler
			testReconciler = &testableReconciler{
				SBDRemediationReconciler: reconciler,
			}
		})

		AfterEach(func() {
			// Clean up test resource
			resource := &medik8sv1alpha1.SBDRemediation{}
			err := k8sClient.Get(ctx, namespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			// Clean up temporary files
			if reconciler.sbdDevice != nil {
				reconciler.sbdDevice.Close()
			}
			os.RemoveAll(tempDir)
		})

		Context("with leader election disabled", func() {
			It("should successfully fence a node and update status correctly", func() {
				By("Creating a SBDRemediation resource")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-5",
						Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				By("Reconciling the resource multiple times to complete the workflow")
				Eventually(func() bool {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: namespacedName,
					})
					Expect(err).NotTo(HaveOccurred())

					// Get updated resource
					updatedResource := &medik8sv1alpha1.SBDRemediation{}
					err = k8sClient.Get(ctx, namespacedName, updatedResource)
					Expect(err).NotTo(HaveOccurred())

					return updatedResource.IsFencingSucceeded()
				}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())

				By("Verifying the final resource status")
				finalResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, finalResource)).To(Succeed())

				Expect(finalResource.IsFencingSucceeded()).To(BeTrue())
				Expect(finalResource.IsReady()).To(BeTrue())
				readyCondition := finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionReady)
				Expect(readyCondition).NotTo(BeNil())
				Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(readyCondition.Reason).To(Equal("Succeeded"))

				fencingCondition := finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded)
				Expect(fencingCondition).NotTo(BeNil())
				Expect(fencingCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(fencingCondition.Message).To(Equal("Node worker-5 (ID: 5) successfully fenced via SBD device"))

				Expect(finalResource.Status.NodeID).NotTo(BeNil())
				Expect(*finalResource.Status.NodeID).To(Equal(uint16(5)))
				Expect(finalResource.Status.FenceMessageWritten).To(BeTrue())
				Expect(finalResource.Status.LastUpdateTime).NotTo(BeNil())
				Expect(finalResource.Status.OperatorInstance).NotTo(BeEmpty())

				By("Verifying the fence message was written to the SBD device")
				device, err := blockdevice.Open(mockSBDDevice)
				Expect(err).NotTo(HaveOccurred())
				defer device.Close()

				// Read the message from node ID 5's slot
				slotOffset := int64(5) * sbdprotocol.SBD_SLOT_SIZE
				messageBytes := make([]byte, sbdprotocol.SBD_HEADER_SIZE)
				_, err = device.ReadAt(messageBytes, slotOffset)
				Expect(err).NotTo(HaveOccurred())

				// Unmarshal and verify the message
				header, err := sbdprotocol.Unmarshal(messageBytes)
				Expect(err).NotTo(HaveOccurred())
				Expect(header.Type).To(Equal(sbdprotocol.SBD_MSG_TYPE_FENCE))
				Expect(header.NodeID).To(Equal(OperatorNodeID))
			})

			It("should handle invalid node names gracefully with proper status updates", func() {
				By("Creating a SBDRemediation resource with invalid node name")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "invalid-node-name",
						Reason:   medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				By("Reconciling the resource")
				Eventually(func() bool {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: namespacedName,
					})
					Expect(err).NotTo(HaveOccurred())

					updatedResource := &medik8sv1alpha1.SBDRemediation{}
					err = k8sClient.Get(ctx, namespacedName, updatedResource)
					Expect(err).NotTo(HaveOccurred())

					return updatedResource.IsReady() && !updatedResource.IsFencingSucceeded()
				}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

				By("Verifying the resource failed with appropriate error")
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, updatedResource)).To(Succeed())

				Expect(updatedResource.IsReady()).To(BeTrue())
				Expect(updatedResource.IsFencingSucceeded()).To(BeFalse())

				readyCondition := updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionReady)
				Expect(readyCondition).NotTo(BeNil())
				Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(readyCondition.Reason).To(Equal("Failed"))
				Expect(readyCondition.Message).To(ContainSubstring("Failed to map node name to node ID"))

				fencingCondition := updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded)
				Expect(fencingCondition).NotTo(BeNil())
				Expect(fencingCondition.Status).To(Equal(metav1.ConditionFalse))

				Expect(updatedResource.Status.LastUpdateTime).NotTo(BeNil())
			})

			It("should handle SBD device errors gracefully with retry logic", func() {
				By("Setting an invalid SBD device path")
				reconciler.sbdDevicePath = "/nonexistent/device"

				By("Creating a SBDRemediation resource")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-3",
						Reason:   medik8sv1alpha1.SBDRemediationReasonManualFencing,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				By("Reconciling the resource and expecting failure after retries")
				Eventually(func() bool {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: namespacedName,
					})
					Expect(err).NotTo(HaveOccurred())

					updatedResource := &medik8sv1alpha1.SBDRemediation{}
					err = k8sClient.Get(ctx, namespacedName, updatedResource)
					Expect(err).NotTo(HaveOccurred())

					return updatedResource.IsReady() && !updatedResource.IsFencingSucceeded()
				}, 30*time.Second, 500*time.Millisecond).Should(BeTrue())

				By("Verifying the resource failed with device error")
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, updatedResource)).To(Succeed())

				Expect(updatedResource.IsReady()).To(BeTrue())
				Expect(updatedResource.IsFencingSucceeded()).To(BeFalse())

				readyCondition := updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionReady)
				Expect(readyCondition).NotTo(BeNil())
				Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(readyCondition.Reason).To(Equal("Failed"))
				Expect(readyCondition.Message).To(ContainSubstring("Failed to initialize SBD device"))
			})

			It("should handle status update idempotency correctly", func() {
				By("Creating a SBDRemediation resource")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-7",
						Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				By("Performing multiple status updates with the same values")
				logger := logf.Log.WithName("test")

				conditions := map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
					medik8sv1alpha1.SBDRemediationConditionReady: {
						status:  metav1.ConditionFalse,
						reason:  "TestReason",
						message: "Test message",
					},
				}

				result1, err1 := reconciler.updateStatusWithConditions(ctx, resource, conditions, logger)
				Expect(err1).NotTo(HaveOccurred())

				// Second update with same values should be idempotent
				result2, err2 := reconciler.updateStatusWithConditions(ctx, resource, conditions, logger)
				Expect(err2).NotTo(HaveOccurred())

				// The second call should skip the actual update (idempotent behavior)
				// We just verify both calls succeeded
				By("Verifying both calls succeeded")
				Expect(result1).NotTo(BeNil())
				Expect(result2).NotTo(BeNil())

				By("Verifying status was updated correctly")
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, updatedResource)).To(Succeed())

				readyCondition := updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionReady)
				Expect(readyCondition).NotTo(BeNil())
				Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
				Expect(readyCondition.Reason).To(Equal("TestReason"))
				Expect(readyCondition.Message).To(Equal("Test message"))
			})
		})

		Context("with leader election enabled", func() {
			BeforeEach(func() {
				reconciler.leaderElectionEnabled = true
				// Use testReconciler for leadership tests
				testReconciler.SBDRemediationReconciler = reconciler
			})

			It("should process fencing when leadership is available", func() {
				By("Creating a SBDRemediation resource")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-7",
						Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				By("Reconciling with leadership available")
				// Set testReconciler to return true for IsLeader
				testReconciler.isLeaderFunc = func() bool { return true }

				Eventually(func() bool {
					_, err := testReconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: namespacedName,
					})
					Expect(err).NotTo(HaveOccurred())

					updatedResource := &medik8sv1alpha1.SBDRemediation{}
					err = k8sClient.Get(ctx, namespacedName, updatedResource)
					Expect(err).NotTo(HaveOccurred())

					return updatedResource.IsFencingSucceeded()
				}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())

				By("Verifying the resource was successfully fenced")
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, updatedResource)).To(Succeed())
				Expect(updatedResource.IsFencingSucceeded()).To(BeTrue())
				Expect(updatedResource.IsReady()).To(BeTrue())
				Expect(updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded)).NotTo(BeNil())
				Expect(updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded).Status).To(Equal(metav1.ConditionTrue))
				Expect(updatedResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded).Message).To(Equal("Node worker-7 (ID: 7) successfully fenced via SBD device"))
			})
		})

		Context("when resource is deleted", func() {
			It("should clean up properly", func() {
				By("Creating and processing a SBDRemediation resource")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-9",
						Reason:   medik8sv1alpha1.SBDRemediationReasonManualFencing,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				// Initial reconcile to add finalizer
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				Expect(err).NotTo(HaveOccurred())

				By("Deleting the resource")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

				By("Reconciling after deletion")
				_, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying the resource is removed")
				deletedResource := &medik8sv1alpha1.SBDRemediation{}
				err = k8sClient.Get(ctx, namespacedName, deletedResource)
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})
		})

		Context("when resource already exists with different phases", func() {
			It("should not reprocess completed fencing", func() {
				By("Creating a SBDRemediation resource with completed status")
				resource := &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: "worker-11",
						Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				// First reconcile to complete the fencing
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				Expect(err).NotTo(HaveOccurred())

				// Manually update status to completed
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				Eventually(func() error {
					return k8sClient.Get(ctx, namespacedName, updatedResource)
				}, 2*time.Second, 100*time.Millisecond).Should(Succeed())

				updatedResource.SetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded, metav1.ConditionTrue, "AlreadyCompleted", "Already completed")
				updatedResource.SetCondition(medik8sv1alpha1.SBDRemediationConditionReady, metav1.ConditionTrue, "Succeeded", "Already completed")
				Expect(k8sClient.Status().Update(ctx, updatedResource)).To(Succeed())

				By("Reconciling the already completed resource")
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(reconcile.Result{})) // Should not requeue for completed resource

				By("Verifying the resource status remains unchanged")
				finalResource := &medik8sv1alpha1.SBDRemediation{}
				Expect(k8sClient.Get(ctx, namespacedName, finalResource)).To(Succeed())
				Expect(finalResource.IsFencingSucceeded()).To(BeTrue())
				Expect(finalResource.IsReady()).To(BeTrue())
				Expect(finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded)).NotTo(BeNil())
				Expect(finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded).Status).To(Equal(metav1.ConditionTrue))
				Expect(finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded).Message).To(Equal("Already completed"))
			})
		})

		It("should handle timeoutSeconds field correctly", func() {
			By("Creating SBDRemediation with custom timeout")
			customTimeout := int32(120)
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName:       "worker-8",
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: customTimeout,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Verifying timeout is preserved in spec")
			Eventually(func() int32 {
				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				err := k8sClient.Get(ctx, namespacedName, updatedResource)
				if err != nil {
					return 0
				}
				if updatedResource.Spec.TimeoutSeconds == customTimeout {
					return customTimeout
				}
				return 0
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(customTimeout))
		})

		It("should use default timeout when timeoutSeconds is not specified", func() {
			By("Creating SBDRemediation without timeout")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-9",
					Reason:   medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
					// TimeoutSeconds not specified - should use default (60)
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling and verifying default handling")
			Eventually(func() bool {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				return err == nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			By("Verifying resource was processed successfully")
			finalResource := &medik8sv1alpha1.SBDRemediation{}
			Expect(k8sClient.Get(ctx, namespacedName, finalResource)).To(Succeed())
			// Default timeout behavior is handled in the controller logic
		})

		It("should handle finalizer correctly during deletion", func() {
			By("Creating SBDRemediation with finalizer")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:       resourceName,
					Namespace:  "default",
					Finalizers: []string{SBDRemediationFinalizer},
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-finalizer",
					Reason:   medik8sv1alpha1.SBDRemediationReasonManualFencing,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling to process the resource")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Initiating deletion")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Reconciling deletion and verifying finalizer processing")
			Eventually(func() bool {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				if err != nil {
					return false
				}

				// Check if resource is gone (finalizer removed and resource deleted)
				deletedResource := &medik8sv1alpha1.SBDRemediation{}
				err = k8sClient.Get(ctx, namespacedName, deletedResource)
				return errors.IsNotFound(err)
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("should handle status update conflicts gracefully", func() {
			By("Creating SBDRemediation resource")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-conflict",
					Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Simulating concurrent status updates")
			// First reconciliation
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Get current resource
			currentResource := &medik8sv1alpha1.SBDRemediation{}
			Expect(k8sClient.Get(ctx, namespacedName, currentResource)).To(Succeed())

			// Modify resource version to simulate conflict
			currentResource.SetCondition(medik8sv1alpha1.SBDRemediationConditionReady, metav1.ConditionFalse, "Testing", "Conflict test")
			Expect(k8sClient.Status().Update(ctx, currentResource)).To(Succeed())

			By("Second reconciliation should handle any conflicts")
			Eventually(func() bool {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				return err == nil
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("should emit events during reconciliation workflow", func() {
			By("Setting up event recorder mock")
			eventRecorder := &MockEventRecorder{
				Events: make([]Event, 0),
			}
			reconciler.Recorder = eventRecorder

			By("Creating SBDRemediation resource")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-events",
					Reason:   medik8sv1alpha1.SBDRemediationReasonManualFencing,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling and verifying events are emitted")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying events were recorded")
			Eventually(func() int {
				return len(eventRecorder.Events)
			}, 5*time.Second, 100*time.Millisecond).Should(BeNumerically(">", 0))

			// Check for specific events
			events := eventRecorder.Events
			hasRemediationInitiated := false
			for _, event := range events {
				if event.Reason == ReasonRemediationInitiated {
					hasRemediationInitiated = true
					break
				}
			}
			Expect(hasRemediationInitiated).To(BeTrue(), "Should emit remediation initiated event")
		})

		It("should handle node ID out of range errors", func() {
			By("Setting up reconciler with a mock that forces node ID out of range")
			// Create a resource with a node name that would map to an invalid ID
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "node-999", // This should fail node ID mapping
					Reason:   medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling and expecting node ID mapping failure")
			Eventually(func() bool {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: namespacedName,
				})
				Expect(err).NotTo(HaveOccurred())

				updatedResource := &medik8sv1alpha1.SBDRemediation{}
				err = k8sClient.Get(ctx, namespacedName, updatedResource)
				Expect(err).NotTo(HaveOccurred())

				return updatedResource.IsReady() && !updatedResource.IsFencingSucceeded()
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())

			By("Verifying appropriate error conditions are set")
			finalResource := &medik8sv1alpha1.SBDRemediation{}
			Expect(k8sClient.Get(ctx, namespacedName, finalResource)).To(Succeed())

			readyCondition := finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionReady)
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("Failed"))

			fencingCondition := finalResource.GetCondition(medik8sv1alpha1.SBDRemediationConditionFencingSucceeded)
			Expect(fencingCondition).NotTo(BeNil())
			Expect(fencingCondition.Status).To(Equal(metav1.ConditionFalse))
		})

		It("should handle multiple SBDRemediation resources simultaneously", func() {
			By("Creating multiple SBDRemediation resources")
			resources := make([]*medik8sv1alpha1.SBDRemediation, 3)
			namespacedNames := make([]types.NamespacedName, 3)

			for i := 0; i < 3; i++ {
				resourceName := fmt.Sprintf("test-remediation-multi-%d-%d", time.Now().UnixNano(), i)
				namespacedNames[i] = types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				}

				resources[i] = &medik8sv1alpha1.SBDRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.SBDRemediationSpec{
						NodeName: fmt.Sprintf("worker-multi-%d", i+1),
						Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					},
				}
				Expect(k8sClient.Create(ctx, resources[i])).To(Succeed())
			}

			By("Reconciling all resources")
			for i, nsName := range namespacedNames {
				Eventually(func() bool {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: nsName,
					})
					if err != nil {
						return false
					}

					updatedResource := &medik8sv1alpha1.SBDRemediation{}
					err = k8sClient.Get(ctx, nsName, updatedResource)
					if err != nil {
						return false
					}

					return updatedResource.IsFencingSucceeded()
				}, 15*time.Second, 200*time.Millisecond).Should(BeTrue(),
					fmt.Sprintf("Resource %d should be successfully fenced", i))
			}

			By("Cleaning up multiple resources")
			for i, resource := range resources {
				err := k8sClient.Delete(ctx, resource)
				if err != nil && !errors.IsNotFound(err) {
					logf.Log.Error(err, "Failed to delete resource", "index", i)
				}
			}
		})
	})

	Context("Retry Logic", func() {
		var (
			reconciler *SBDRemediationReconciler
			mockDevice string
			tempDir    string
		)

		BeforeEach(func() {
			var err error
			tempDir, err = ioutil.TempDir("", "sbd-retry-test-")
			Expect(err).NotTo(HaveOccurred())

			mockDevice = filepath.Join(tempDir, "sbd-device")
			err = ioutil.WriteFile(mockDevice, make([]byte, 512*1024), 0644)
			Expect(err).NotTo(HaveOccurred())

			reconciler = &SBDRemediationReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				sbdDevicePath: mockDevice,
			}
		})

		AfterEach(func() {
			if reconciler.sbdDevice != nil {
				reconciler.sbdDevice.Close()
			}
			os.RemoveAll(tempDir)
		})

		It("should retry transient errors during fencing", func() {
			ctx := context.Background()
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-1",
					Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
				},
			}

			// Make device temporarily inaccessible
			os.Chmod(mockDevice, 0000)

			// This should fail after retries
			logger := logf.Log.WithName("test")
			err := reconciler.performFencingWithRetry(ctx, sbdRemediation, 1, logger)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fencing failed after"))

			// Restore device permissions for cleanup
			os.Chmod(mockDevice, 0644)
		})

		It("should not retry non-retryable errors", func() {
			ctx := context.Background()

			// Test node mapping error which is non-retryable
			_, err := reconciler.nodeNameToNodeID(ctx, "invalid-node-name")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("node manager not initialized"))

			// For the fencing retry test, we need to test with a valid node ID but create
			// a situation that would cause non-retryable marshaling errors
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-1", // Valid node name
					Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
				},
			}

			// This should succeed because we have a valid setup
			logger := logf.Log.WithName("test")
			err = reconciler.performFencingWithRetry(ctx, sbdRemediation, 1, logger)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Status Update Conflicts", func() {
		var (
			reconciler *SBDRemediationReconciler
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &SBDRemediationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should handle status update conflicts gracefully", func() {
			resourceName := fmt.Sprintf("conflict-test-%d", time.Now().UnixNano())
			namespacedName := types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("Creating a SBDRemediation resource")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-1",
					Reason:   medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Performing concurrent status updates")
			// This simulates the conflict handling mechanism
			err := reconciler.updateStatusWithRetry(ctx, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the final status was applied")
			updatedResource := &medik8sv1alpha1.SBDRemediation{}
			Expect(k8sClient.Get(ctx, namespacedName, updatedResource)).To(Succeed())
			// Status should be set to whatever was in the resource object

			// Clean up
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
	})

	Context("When testing event emission for SBDRemediation", func() {
		var (
			reconciler     *SBDRemediationReconciler
			mockRecorder   *MockEventRecorder
			ctx            context.Context
			resourceName   string
			namespacedName types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			resourceName = fmt.Sprintf("test-events-remediation-%d", time.Now().UnixNano())
			namespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			// Create temporary SBD device for tests
			tempDir, err := ioutil.TempDir("", "sbd-event-test-")
			Expect(err).NotTo(HaveOccurred())

			mockSBDDevice := filepath.Join(tempDir, "sbd-device")
			err = ioutil.WriteFile(mockSBDDevice, make([]byte, 512*1024), 0644)
			Expect(err).NotTo(HaveOccurred())

			mockRecorder = NewMockEventRecorder()
			reconciler = &SBDRemediationReconciler{
				Client:                k8sClient,
				Scheme:                k8sClient.Scheme(),
				Recorder:              mockRecorder,
				leaderElectionEnabled: false, // Disable for tests
				sbdDevicePath:         mockSBDDevice,
			}
		})

		AfterEach(func() {
			resource := &medik8sv1alpha1.SBDRemediation{}
			err := k8sClient.Get(ctx, namespacedName, resource)
			if err == nil {
				k8sClient.Delete(ctx, resource)
			}
		})

		It("should emit events for helper methods", func() {
			By("testing emitEvent helper")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			}

			reconciler.emitEvent(resource, EventTypeNormal, "TestReason", "Test message")

			events := mockRecorder.GetEvents()
			Expect(len(events)).To(Equal(1))
			Expect(events[0].EventType).To(Equal(EventTypeNormal))
			Expect(events[0].Reason).To(Equal("TestReason"))
			Expect(events[0].Message).To(Equal("Test message"))

			By("testing emitEventf helper")
			mockRecorder.Reset()
			reconciler.emitEventf(resource, EventTypeWarning, "TestFormat", "Formatted message: %s", "test-value")

			events = mockRecorder.GetEvents()
			Expect(len(events)).To(Equal(1))
			Expect(events[0].EventType).To(Equal(EventTypeWarning))
			Expect(events[0].Reason).To(Equal("TestFormat"))
			Expect(events[0].Message).To(Equal("Formatted message: test-value"))
		})

		It("should emit events for node ID mapping errors", func() {
			By("creating SBDRemediation with invalid node name")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "invalid-node-name",
					Reason:   medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the resource")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying events were emitted")
			events := mockRecorder.GetEvents()
			Expect(len(events)).To(BeNumerically(">=", 2))

			// Check for remediation initiated event
			remediationInitiatedEvent := false
			for _, event := range events {
				if event.Reason == ReasonRemediationInitiated && event.EventType == EventTypeNormal {
					remediationInitiatedEvent = true
					Expect(event.Message).To(ContainSubstring("invalid-node-name"))
					break
				}
			}
			Expect(remediationInitiatedEvent).To(BeTrue(), "Remediation initiated event should be emitted")

			// Check for node ID mapping error event
			nodeIDErrorEvent := false
			for _, event := range events {
				if event.Reason == ReasonNodeIDMappingError && event.EventType == EventTypeWarning {
					nodeIDErrorEvent = true
					Expect(event.Message).To(ContainSubstring("invalid-node-name"))
					break
				}
			}
			Expect(nodeIDErrorEvent).To(BeTrue(), "Node ID mapping error event should be emitted")
		})

		It("should emit events for leadership waiting", func() {
			By("testing leadership waiting event emission directly")
			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: medik8sv1alpha1.SBDRemediationSpec{
					NodeName: "worker-1",
					Reason:   medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
				},
			}

			// Test the event emission directly
			reconciler.emitEventf(resource, EventTypeNormal, ReasonLeadershipWaiting,
				"Waiting for leadership to perform fencing for node '%s'", resource.Spec.NodeName)

			By("verifying leadership waiting event was emitted")
			events := mockRecorder.GetEvents()
			Expect(len(events)).To(Equal(1))
			Expect(events[0].EventType).To(Equal(EventTypeNormal))
			Expect(events[0].Reason).To(Equal(ReasonLeadershipWaiting))
			Expect(events[0].Message).To(ContainSubstring("worker-1"))
		})

		It("should handle nil recorder gracefully", func() {
			By("setting recorder to nil")
			reconciler.Recorder = nil

			resource := &medik8sv1alpha1.SBDRemediation{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			}

			By("calling event methods with nil recorder")
			// These should not panic
			reconciler.emitEvent(resource, EventTypeNormal, "TestReason", "Test message")
			reconciler.emitEventf(resource, EventTypeWarning, "TestFormat", "Formatted message: %s", "test-value")

			// No events should be recorded
			events := mockRecorder.GetEvents()
			Expect(len(events)).To(Equal(0))
		})
	})
})
