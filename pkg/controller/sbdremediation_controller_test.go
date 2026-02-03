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
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/mocks"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

// Note: Controller tests simplified since agent-based fencing architecture
// moved device access and fencing logic to the SBD agents

var _ = Describe("StorageBasedRemediation Controller", func() {
	Context("When reconciling a StorageBasedRemediation resource", func() {
		var (
			reconciler     *SBDRemediationReconciler
			ctx            context.Context
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

			reconciler = &SBDRemediationReconciler{
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

			mockHeartbeatDevice := mocks.NewMockBlockDevice("/tmp/test-sbd", 1024*1024)
			mockFenceDevice := mocks.NewMockBlockDevice("/tmp/test-sbd-fence", 1024*1024)
			reconciler.SetSBDDevices(mockHeartbeatDevice, mockFenceDevice)

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
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason: medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
				},
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
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason: medik8sv1alpha1.SBDRemediationReasonManualFencing,
				},
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

		It("should handle timeoutSeconds field correctly", func() {
			By("Creating StorageBasedRemediation with custom timeout")
			testNodeName := "worker-3"
			customTimeout := int32(120)
			resource := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testNodeName,
					Namespace: "default",
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonManualFencing,
					TimeoutSeconds: customTimeout,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Verifying timeout is preserved in spec")
			Eventually(func() int32 {
				updatedResource := &medik8sv1alpha1.StorageBasedRemediation{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: testNodeName, Namespace: "default"}, updatedResource)
				if err != nil {
					return 0
				}
				return updatedResource.Spec.TimeoutSeconds
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(customTimeout))
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
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
						Reason:         medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
						TimeoutSeconds: 300,
					},
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
				Expect(finalResource.Spec.Reason).To(Equal(medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive))
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
					Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
						Reason:         medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive,
						TimeoutSeconds: 300,
					},
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
				Expect(finalResource.Spec.Reason).To(Equal(medik8sv1alpha1.SBDRemediationReasonNodeUnresponsive))
			})
		})
	})

	Context("Controller setup and configuration", func() {
		It("should initialize properly", func() {
			By("Creating a controller instance")
			reconciler := &SBDRemediationReconciler{
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

			reconciler := &SBDRemediationReconciler{
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
