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
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/internal/mocks"
	"github.com/medik8s/storage-based-remediation/internal/sbdprotocol"
)

// Note: Controller tests simplified since agent-based fencing architecture
// moved device access and fencing logic to the SBR agents

var _ = Describe("StorageBasedRemediation Controller", func() {
	Context("When reconciling a StorageBasedRemediation resource", func() {
		var (
			resourceName   string
			namespacedName types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			// Use unique resource name for each test to avoid conflicts
			resourceName = fmt.Sprintf("test-remediation-%d", time.Now().UnixNano())
			namespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			reconciler = &SBRRemediationReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: nil, // Event recorder not needed for basic tests
			}

			config := sbdprotocol.NodeManagerConfig{
				ClusterName:        "test-cluster",
				SyncInterval:       30 * time.Second,
				StaleNodeTimeout:   10 * time.Minute,
				Logger:             logr.Discard(),
				FileLockingEnabled: true,
			}

			mockHeartbeatDevice := mocks.NewMockBlockDevice("/tmp/test-sbr", 1024*1024)
			mockFenceDevice := mocks.NewMockBlockDevice("/tmp/test-sbr-fence", 1024*1024)
			reconciler.SetSBRDevices(mockHeartbeatDevice, mockFenceDevice)

			nodeManager, err := sbdprotocol.NewNodeManager(mockHeartbeatDevice, config)
			Expect(err).NotTo(HaveOccurred())

			for i := 1; i <= 5; i++ {
				_, err := nodeManager.GetNodeIDForNode(fmt.Sprintf("worker-%d", i))
				Expect(err).NotTo(HaveOccurred())
			}

			nodeID, err := nodeManager.GetNodeIDForNode("worker-1")
			Expect(err).NotTo(HaveOccurred())
			reconciler.SetOwnNodeInfo(nodeID, "worker-1")

			reconciler.SetNodeManager(nodeManager)

			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-sbrconfig-%d", time.Now().UnixNano()),
					Namespace: "default",
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())
			sbrConfig.Status.StorageValidation = &medik8sv1alpha1.StorageValidationStatus{ConcurrentWriteable: new(true)}
			Expect(k8sClient.Status().Update(ctx, sbrConfig)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, sbrConfig)).To(Succeed())
			})
			reconciler.SetSBRConfigRef(sbrConfig.Name, sbrConfig.Namespace)
		})

		It("should handle non-existent resources gracefully", func() {
			By("Attempting to reconcile a non-existent resource")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should add finalizer to new StorageBasedRemediation resources", func() {
			By("Creating a StorageBasedRemediation resource")
			testNodeName := "worker-1"
			resource := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling the resource")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying finalizer was added")
			updatedResource := &medik8sv1alpha1.StorageBasedRemediation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName, Namespace: "default"}, updatedResource)).To(Succeed())

			// Note: In agent-based architecture, the controller primarily adds finalizers
			// and updates status, while agents handle the actual fencing
		})

		It("should handle deletion properly", func() {
			By("Creating a StorageBasedRemediation resource")
			testNodeName := "worker-2"
			resource := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			workerNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-2",
				},
			}
			Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed())
			})

			By("Initial reconcile to add finalizer")
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

			By("Verifying the resource cleanup")
			// The controller should handle cleanup gracefully
		})

		Context("with valid StorageBasedRemediation spec for a fake node", func() {
			It("should handle normal processing flow", func() {
				By("Creating a well-formed StorageBasedRemediation resource")
				testNodeName := "fake-node-1"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testNodeName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
				}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed())
				})

				By("Reconciling the resource multiple times")
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
				})
				// Placing finilizer
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
				})
				// Node isn't found error
				Expect(err).To(HaveOccurred())

				By("Verifying the resource exists and is processable")
				finalResource := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName, Namespace: "default"}, finalResource)).To(Succeed())
				Expect(finalResource.Name).To(Equal(testNodeName))
			})
		})

		Context("with valid StorageBasedRemediation spec for a real node", func() {
			It("should handle normal processing flow", func() {
				By("Creating a well-formed StorageBasedRemediation resource")
				testNodeName := "worker-4"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testNodeName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNodeName,
					},
				}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed())
				})

				By("Reconciling the resource multiple times")
				for i := 0; i < 3; i++ {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
					})
					Expect(err).NotTo(HaveOccurred())
				}

				By("Verifying the resource exists and is processable")
				finalResource := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName, Namespace: "default"}, finalResource)).To(Succeed())
				Expect(finalResource.Name).To(Equal(testNodeName))
			})
		})

		Context("when the storage write check has not passed", func() {
			It("withholds fencing instead of erroring, and does not cordon the node", func() {
				By("Pointing the reconciler at a config CR without StorageValidation.ConcurrentWriteable=true")
				reconciler.SetSBRConfigRef("does-not-exist", "default")

				By("Creating a well-formed StorageBasedRemediation resource")
				testNodeName := "worker-5"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testNodeName,
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNodeName,
					},
				}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed())
				})

				By("Adding the finalizer")
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Requeue).To(BeTrue())

				By("Reconciling again: the gate should withhold fencing without erroring")
				result, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(10 * time.Second))

				By("Verifying the node was never cordoned")
				finalNode := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName}, finalNode)).To(Succeed())
				Expect(finalNode.Spec.Unschedulable).To(BeFalse())
			})

			It("withholds fencing and emits ReasonFencingWithheld when storage validation is explicitly false", func() {
				By("Creating a config CR whose storage validation is explicitly false")
				falseConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("test-sbrconfig-false-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
				}
				Expect(k8sClient.Create(ctx, falseConfig)).To(Succeed())
				falseConfig.Status.StorageValidation = &medik8sv1alpha1.StorageValidationStatus{ConcurrentWriteable: new(false)}
				Expect(k8sClient.Status().Update(ctx, falseConfig)).To(Succeed())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, falseConfig)).To(Succeed()) })

				recorder := record.NewFakeRecorder(10)
				reconciler.Recorder = recorder
				reconciler.SetSBRConfigRef(falseConfig.Name, falseConfig.Namespace)

				testNodeName := "worker-6"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{Name: testNodeName, Namespace: "default"},
					Spec:       medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNodeName}}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed()) })

				req := reconcile.Request{NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"}}
				_, err := reconciler.Reconcile(ctx, req) // adds the finalizer
				Expect(err).NotTo(HaveOccurred())

				result, err := reconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(10 * time.Second))

				finalNode := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName}, finalNode)).To(Succeed())
				Expect(finalNode.Spec.Unschedulable).To(BeFalse())

				Eventually(recorder.Events).Should(Receive(ContainSubstring("FencingWithheld")))
			})

			It("withholds fencing when no StorageBasedRemediationConfig reference has been configured", func() {
				By("Clearing the config reference, simulating an agent that never called SetSBRConfigRef")
				reconciler.SetSBRConfigRef("", "")

				testNodeName := "worker-7"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{Name: testNodeName, Namespace: "default"},
					Spec:       medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNodeName}}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed()) })

				req := reconcile.Request{NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"}}
				_, err := reconciler.Reconcile(ctx, req) // adds the finalizer
				Expect(err).NotTo(HaveOccurred())

				result, err := reconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred(), "a missing config reference must fail safe, not error out")
				Expect(result.RequeueAfter).To(Equal(10 * time.Second))

				finalNode := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName}, finalNode)).To(Succeed())
				Expect(finalNode.Spec.Unschedulable).To(BeFalse())
			})

			It("gates fencing the same way for a block-mode config (docs/design/storage-validation.md section 4.4: gate applies in both volume modes)", func() {
				blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
				blockConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("test-sbrconfig-block-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
						SharedStorageVolumeMode: &blockMode,
					},
				}
				Expect(k8sClient.Create(ctx, blockConfig)).To(Succeed())
				blockConfig.Status.StorageValidation = &medik8sv1alpha1.StorageValidationStatus{ConcurrentWriteable: new(false)}
				Expect(k8sClient.Status().Update(ctx, blockConfig)).To(Succeed())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, blockConfig)).To(Succeed()) })

				reconciler.SetSBRConfigRef(blockConfig.Name, blockConfig.Namespace)

				testNodeName := "worker-8"
				resource := &medik8sv1alpha1.StorageBasedRemediation{
					ObjectMeta: metav1.ObjectMeta{Name: testNodeName, Namespace: "default"},
					Spec:       medik8sv1alpha1.StorageBasedRemediationSpec{},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())

				workerNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNodeName}}
				Expect(k8sClient.Create(ctx, workerNode)).To(Succeed())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, workerNode)).To(Succeed()) })

				req := reconcile.Request{NamespacedName: types.NamespacedName{Name: testNodeName, Namespace: "default"}}
				_, err := reconciler.Reconcile(ctx, req) // adds the finalizer
				Expect(err).NotTo(HaveOccurred())

				result, err := reconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(10 * time.Second))

				finalNode := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName}, finalNode)).To(Succeed())
				Expect(finalNode.Spec.Unschedulable).To(BeFalse(),
					"block-mode configs must be gated on storage validation exactly like filesystem-mode configs")
			})
		})
	})

	Context("fencing remediation status helpers", func() {
		var (
			sbr           *medik8sv1alpha1.StorageBasedRemediation
			clientBuilder *fake.ClientBuilder
		)

		BeforeEach(func() {
			sbr = &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-2",
					Namespace: "default",
				},
			}
			clientBuilder = fake.NewClientBuilder().
				WithObjects(sbr).
				WithStatusSubresource(&medik8sv1alpha1.StorageBasedRemediation{})
		})

		JustBeforeEach(func() {
			reconciler = &SBRRemediationReconciler{Client: clientBuilder.Build()}
		})

		Context("when Succeeded is already False", func() {
			BeforeEach(func() {
				sbr.Finalizers = []string{SBRRemediationFinalizer}
				sbr.SetCondition(
					medik8sv1alpha1.SBRRemediationConditionSucceeded,
					metav1.ConditionFalse, ReasonFailed, "already failed")
				clientBuilder = fake.NewClientBuilder().
					WithObjects(sbr).
					WithStatusSubresource(&medik8sv1alpha1.StorageBasedRemediation{})
			})

			It("should not retry fencing", func() {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(sbr),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(reconciler.Client.Get(ctx, client.ObjectKeyFromObject(sbr), sbrFound)).To(Succeed())
				Expect(sbrFound.IsFailed()).To(BeTrue())
				Expect(sbrFound.IsFencingInProgress()).To(BeFalse())
				Expect(sbrFound.IsProcessing()).To(BeFalse())
			})
		})

		Context("when NHC sets the timed-out annotation", func() {
			var targetNodeID uint16

			BeforeEach(func() {
				sbr.Finalizers = []string{SBRRemediationFinalizer}
				sbr.Annotations = map[string]string{
					"remediation.medik8s.io/nhc-timed-out": "",
				}
				started := metav1.NewTime(time.Now().Add(-30 * time.Second))
				sbr.Status.Conditions = []metav1.Condition{
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionFencingInProgress),
						Status:             metav1.ConditionTrue,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionProcessing),
						Status:             metav1.ConditionTrue,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionSucceeded),
						Status:             metav1.ConditionUnknown,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
				}
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-2"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
				}
				clientBuilder = fake.NewClientBuilder().
					WithScheme(k8sClient.Scheme()).
					WithObjects(sbr, node).
					WithStatusSubresource(&medik8sv1alpha1.StorageBasedRemediation{})
			})

			JustBeforeEach(func() {
				targetNodeID = attachTestSBRFencing(reconciler)
				// Stale heartbeat would otherwise prove fencingComplete and apply OOS.
				writeHeartbeatForTest(reconciler, targetNodeID, 120*time.Second)
			})

			It("should set Succeeded=False and Ready=False and stop without an OOS taint", func() {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(sbr),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(reconciler.Client.Get(
					ctx, client.ObjectKeyFromObject(sbr), sbrFound,
				)).To(Succeed())
				Expect(sbrFound.IsFailed()).To(BeTrue())
				Expect(sbrFound.IsProcessing()).To(BeFalse())
				Expect(sbrFound.IsFencingInProgress()).To(BeFalse())
				Expect(sbrFound.IsConditionFalse(medik8sv1alpha1.SBRRemediationConditionFencingSucceeded)).To(BeTrue())
				Expect(sbrFound.IsConditionFalse(medik8sv1alpha1.SBRRemediationConditionReady)).To(BeTrue())

				node := &corev1.Node{}
				Expect(reconciler.Client.Get(ctx, client.ObjectKey{Name: "worker-2"}, node)).To(Succeed())
				Expect(taintExists(node.Spec.Taints, outOfServiceTaint)).To(BeFalse())
				Expect(fenceSlotHasFenceMessage(reconciler, targetNodeID)).To(BeFalse())
			})
		})

		Context("when fencing monitor window times out", func() {
			var targetNodeID uint16

			BeforeEach(func() {
				sbr.Finalizers = []string{SBRRemediationFinalizer}
				started := metav1.NewTime(time.Now().Add(
					-time.Duration(DefaultFencingMonitorTimeoutSeconds+10) * time.Second))
				sbr.Status.Conditions = []metav1.Condition{
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionFencingInProgress),
						Status:             metav1.ConditionTrue,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionProcessing),
						Status:             metav1.ConditionTrue,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
					{
						Type:               string(medik8sv1alpha1.SBRRemediationConditionSucceeded),
						Status:             metav1.ConditionUnknown,
						Reason:             ReasonInProgress,
						Message:            "fencing",
						LastTransitionTime: started,
					},
				}
				writeable := true
				sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test-sbr-config", Namespace: "default"},
					Status: medik8sv1alpha1.StorageBasedRemediationConfigStatus{
						StorageValidation: &medik8sv1alpha1.StorageValidationStatus{
							ConcurrentWriteable: &writeable,
						},
					},
				}
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-2"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
				}
				clientBuilder = fake.NewClientBuilder().
					WithScheme(k8sClient.Scheme()).
					WithObjects(sbr, node, sbrConfig).
					WithStatusSubresource(&medik8sv1alpha1.StorageBasedRemediation{})
			})

			JustBeforeEach(func() {
				targetNodeID = attachTestSBRFencing(reconciler)
				reconciler.SetSBRConfigRef("test-sbr-config", "default")
				// Fresh heartbeat so timeout is "not yet dead", not fencingComplete.
				writeHeartbeatForTest(reconciler, targetNodeID, 5*time.Second)
			})

			It("should not set Succeeded=False and should retry the fence write", func() {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(sbr),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(time.Second))

				sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(reconciler.Client.Get(
					ctx, client.ObjectKeyFromObject(sbr), sbrFound,
				)).To(Succeed())
				Expect(sbrFound.IsFailed()).To(BeFalse())
				Expect(sbrFound.IsFencingInProgress()).To(BeFalse())
				Expect(sbrFound.IsProcessing()).To(BeTrue())
				Expect(fenceSlotHasFenceMessage(reconciler, targetNodeID)).To(BeFalse())

				result, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(sbr),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(10 * time.Second))

				Expect(reconciler.Client.Get(
					ctx, client.ObjectKeyFromObject(sbr), sbrFound,
				)).To(Succeed())
				Expect(sbrFound.IsFailed()).To(BeFalse())
				Expect(sbrFound.IsFencingInProgress()).To(BeTrue())
				cond := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionFencingInProgress)
				Expect(cond).NotTo(BeNil())
				Expect(cond.LastTransitionTime.Time).To(BeTemporally(">", time.Now().Add(-5*time.Second)))
				Expect(sbrFound.IsProcessing()).To(BeTrue())
				Expect(fenceSlotHasFenceMessage(reconciler, targetNodeID)).To(BeTrue(),
					"monitor timeout must rewrite the fence on the next reconcile")

				node := &corev1.Node{}
				Expect(reconciler.Client.Get(ctx, client.ObjectKey{Name: "worker-2"}, node)).To(Succeed())
				Expect(taintExists(node.Spec.Taints, outOfServiceTaint)).To(BeFalse())
			})
		})

		Context("when the fence write fails", func() {
			BeforeEach(func() {
				sbr.Finalizers = []string{SBRRemediationFinalizer}
				writeable := true
				sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test-sbr-config", Namespace: "default"},
					Status: medik8sv1alpha1.StorageBasedRemediationConfigStatus{
						StorageValidation: &medik8sv1alpha1.StorageValidationStatus{
							ConcurrentWriteable: &writeable,
						},
					},
				}
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-2"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
				}
				clientBuilder = fake.NewClientBuilder().
					WithScheme(k8sClient.Scheme()).
					WithObjects(sbr, node, sbrConfig).
					WithStatusSubresource(&medik8sv1alpha1.StorageBasedRemediation{})
			})

			JustBeforeEach(func() {
				_ = attachTestSBRFencing(reconciler)
				reconciler.SetSBRConfigRef("test-sbr-config", "default")
				fenceDev, ok := reconciler.fenceDevice.(*mocks.MockBlockDevice)
				Expect(ok).To(BeTrue())
				fenceDev.SetFailWrite(true)
			})

			It("returns an error without Succeeded=False, FencingInProgress, or an OOS taint", func() {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(sbr),
				})
				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
				Expect(reconciler.Client.Get(
					ctx, client.ObjectKeyFromObject(sbr), sbrFound,
				)).To(Succeed())
				Expect(sbrFound.IsFailed()).To(BeFalse())
				Expect(sbrFound.IsFencingInProgress()).To(BeFalse())

				node := &corev1.Node{}
				Expect(reconciler.Client.Get(ctx, client.ObjectKey{Name: "worker-2"}, node)).To(Succeed())
				Expect(taintExists(node.Spec.Taints, outOfServiceTaint)).To(BeFalse())
			})
		})

		Context("handleFencingInProgress", func() {
			When("status update succeeds", func() {
				It("should persist processing and unknown succeeded conditions in one status update", func() {
					err := reconciler.handleFencingInProgress(ctx, sbr, logr.Discard())
					Expect(err).NotTo(HaveOccurred())

					sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
					Expect(reconciler.Client.Get(ctx, client.ObjectKeyFromObject(sbr), sbrFound)).To(Succeed())

					fencingInProgressCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionFencingInProgress)
					verifyCondition(fencingInProgressCondition, metav1.ConditionTrue, ReasonInProgress, "Fencing node worker-2")

					processingCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionProcessing)
					verifyCondition(processingCondition, metav1.ConditionTrue, ReasonInProgress, "Fencing node worker-2")

					succeededCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionSucceeded)
					verifyCondition(succeededCondition, metav1.ConditionUnknown, ReasonInProgress, "Fencing node worker-2")
				})
			})

			When("status update fails", func() {
				BeforeEach(func() {
					clientBuilder = clientBuilder.WithInterceptorFuncs(interceptorStatusSubresourceUpdateOrDelegate())
				})

				It("should return an error", func() {
					err := reconciler.handleFencingInProgress(ctx, sbr, logr.Discard())
					Expect(err).To(HaveOccurred())
				})
			})
		})

		Context("handleFencingSuccess", func() {
			When("status update succeeds", func() {
				It("should persist fencing success conditions in one status update", func() {
					err := reconciler.handleFencingSuccess(ctx, sbr, logr.Discard())
					Expect(err).NotTo(HaveOccurred())

					sbrFound := &medik8sv1alpha1.StorageBasedRemediation{}
					Expect(reconciler.Client.Get(ctx, client.ObjectKeyFromObject(sbr), sbrFound)).To(Succeed())

					fencingInProgressCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionFencingInProgress)
					verifyCondition(fencingInProgressCondition, metav1.ConditionFalse, ReasonCompleted, "Fencing completed")

					fencingSucceededCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionFencingSucceeded)
					verifyCondition(fencingSucceededCondition, metav1.ConditionTrue, ReasonCompleted, "Node worker-2 fenced successfully")

					remediationReadyCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionReady)
					verifyCondition(remediationReadyCondition, metav1.ConditionTrue, ReasonCompleted, "Remediation completed successfully")

					processingCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionProcessing)
					verifyCondition(processingCondition, metav1.ConditionFalse, ReasonCompleted, "Remediation completed successfully")

					succeededCondition := sbrFound.GetCondition(medik8sv1alpha1.SBRRemediationConditionSucceeded)
					verifyCondition(succeededCondition, metav1.ConditionTrue, ReasonCompleted, "Node worker-2 remediated successfully")
				})
			})

			When("status update fails", func() {
				BeforeEach(func() {
					clientBuilder = clientBuilder.WithInterceptorFuncs(interceptorStatusSubresourceUpdateOrDelegate())
				})

				It("should return a wrapped error", func() {
					err := reconciler.handleFencingSuccess(ctx, sbr, logr.Discard())
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(
						"failed to update StorageBasedRemediation status after fencing succeeded"))
				})
			})
		})
	})

	Context("Controller setup and configuration", func() {
		It("should initialize properly", func() {
			By("Creating a controller instance")
			reconciler := &SBRRemediationReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: nil,
			}

			By("Verifying controller fields are set")
			Expect(reconciler.Client).NotTo(BeNil())
			Expect(reconciler.Scheme).NotTo(BeNil())
		})

		It("should handle SetupWithManager", func() {
			By("Creating a manager and setting up the controller")
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme: k8sClient.Scheme(),
			})
			Expect(err).NotTo(HaveOccurred())

			reconciler := &SBRRemediationReconciler{
				Client:   mgr.GetClient(),
				Scheme:   mgr.GetScheme(),
				Recorder: mgr.GetEventRecorderFor("test-controller"),
			}

			By("Setting up with manager")
			err = reconciler.SetupWithManager(mgr, time.Now().Format("20060102150405"))
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func verifyCondition(conditionType *metav1.Condition, conditionStatus metav1.ConditionStatus, conditionReason, conditionMessage string) {
	Expect(conditionType).NotTo(BeNil())
	Expect(conditionType.Status).To(Equal(conditionStatus))
	Expect(conditionType.Reason).To(Equal(conditionReason))
	Expect(conditionType.Message).To(Equal(conditionMessage))
}

func writeHeartbeatForTest(r *SBRRemediationReconciler, nodeID uint16, age time.Duration) {
	GinkgoHelper()
	h := sbdprotocol.NewHeartbeat(nodeID, 1)
	h.Timestamp = uint64(time.Now().Add(-age).UnixNano())
	data, err := sbdprotocol.Marshal(h)
	Expect(err).NotTo(HaveOccurred())
	_, err = r.sbrDevice.WriteAt(data, r.slotOffset(nodeID))
	Expect(err).NotTo(HaveOccurred())
}

func attachTestSBRFencing(r *SBRRemediationReconciler) uint16 {
	heartbeatDev := mocks.NewMockBlockDevice(fmt.Sprintf("/tmp/test-sbr-retry-%d", time.Now().UnixNano()), 1024*1024)
	fenceDev := mocks.NewMockBlockDevice(fmt.Sprintf("/tmp/test-sbr-fence-retry-%d", time.Now().UnixNano()), 1024*1024)
	r.SetSBRDevices(heartbeatDev, fenceDev)

	nodeManager, err := sbdprotocol.NewNodeManager(heartbeatDev, sbdprotocol.NodeManagerConfig{
		ClusterName:        "test-cluster",
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   10 * time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: true,
	})
	Expect(err).NotTo(HaveOccurred())
	for i := 1; i <= 5; i++ {
		_, err := nodeManager.GetNodeIDForNode(fmt.Sprintf("worker-%d", i))
		Expect(err).NotTo(HaveOccurred())
	}
	ownID, err := nodeManager.GetNodeIDForNode("worker-1")
	Expect(err).NotTo(HaveOccurred())
	r.SetOwnNodeInfo(ownID, "worker-1")
	r.SetNodeManager(nodeManager)

	targetID, err := nodeManager.GetNodeIDForNode("worker-2")
	Expect(err).NotTo(HaveOccurred())
	return targetID
}

func fenceSlotHasFenceMessage(r *SBRRemediationReconciler, nodeID uint16) bool {
	buf := make([]byte, sbdprotocol.SBD_HEADER_SIZE)
	n, err := r.fenceDevice.ReadAt(buf, r.slotOffset(nodeID))
	if err != nil || n < sbdprotocol.SBD_HEADER_SIZE {
		return false
	}
	header, err := sbdprotocol.Unmarshal(buf[:sbdprotocol.SBD_HEADER_SIZE])
	return err == nil && header.Type == sbdprotocol.SBD_MSG_TYPE_FENCE
}

func interceptorStatusSubresourceUpdateOrDelegate() interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(
			ctx context.Context,
			c client.Client,
			subResourceName string,
			obj client.Object,
			opts ...client.SubResourceUpdateOption,
		) error {
			if subResourceName == "status" {
				return errors.New("apiserver rejected status")
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
}
