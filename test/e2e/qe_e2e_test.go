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

package e2e

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBR QE E2E Tests", Ordered, Serial, Label("qe", "e2e"), func() {

	var rwxStorageClassName string

	BeforeAll(func() {
		By("Discovering RWX-compatible storage class for shared SBD device")
		storageClasses := &storagev1.StorageClassList{}
		err := testClients.Client.List(testClients.Context, storageClasses)
		Expect(err).NotTo(HaveOccurred(), "Failed to list storage classes")

		var provisioner string
		for _, sc := range storageClasses.Items {
			if isRWXCompatibleProvisioner(sc.Provisioner) {
				rwxStorageClassName = sc.Name
				provisioner = sc.Provisioner
				By(fmt.Sprintf("Found RWX-compatible storage class: %s (provisioner: %s)", sc.Name, sc.Provisioner))
				break
			}
		}
		Expect(rwxStorageClassName).NotTo(BeEmpty(),
			"At least one RWX-compatible storage class is required (e.g., sbd-cephfs from ODF)")

		// Verify CSI driver is registered on all worker nodes before running tests.
		// ODF/CephFS CSI driver registration can be delayed on ephemeral Prow CI clusters,
		// causing agent pods to get stuck in ContainerCreating.
		testClients.WaitForCSIDriverOnAllNodes(provisioner, 5*time.Minute)
	})

	Context("Operator Installation Validation", func() {

		It("should verify SBR controller manager is running", func() {
			By("Checking for SBR controller-manager pod in operator namespace")

			operatorNS := testNamespace.OperatorNamespace()

			podList := &corev1.PodList{}
			err := testClients.Client.List(
				testClients.Context,
				podList,
				client.InNamespace(operatorNS.Name),
				client.MatchingLabels{"control-plane": "controller-manager"},
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to list pods in operator namespace")

			By(fmt.Sprintf("Found %d controller-manager pod(s)", len(podList.Items)))
			Expect(podList.Items).NotTo(BeEmpty(),
				"At least one controller-manager pod should exist")

			for _, pod := range podList.Items {
				Expect(pod.Status.Phase).To(Equal(corev1.PodRunning),
					fmt.Sprintf("Pod %s should be Running, got %s", pod.Name, pod.Status.Phase))

				for _, cs := range pod.Status.ContainerStatuses {
					Expect(cs.Ready).To(BeTrue(),
						fmt.Sprintf("Container %s in pod %s should be ready", cs.Name, pod.Name))
				}
			}

			By("Verified: SBR controller manager is running")
		})

		It("should verify all SBR CRDs are installed", func() {
			By("Checking for SBR custom resource definitions")

			expectedCRDs := []string{
				"sbdconfigs.storage-based-remediation.medik8s.io",
				"storagebasedremediations.storage-based-remediation.medik8s.io",
				"storagebasedremediationtemplates.storage-based-remediation.medik8s.io",
			}

			for _, crdName := range expectedCRDs {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{Name: crdName}, crd)
				Expect(err).NotTo(HaveOccurred(),
					fmt.Sprintf("CRD %s should exist", crdName))

				By(fmt.Sprintf("Found CRD: %s (Kind: %s)", crdName, crd.Spec.Names.Kind))

				var established bool
				for _, condition := range crd.Status.Conditions {
					if condition.Type == apiextensionsv1.Established &&
						condition.Status == apiextensionsv1.ConditionTrue {
						established = true
						break
					}
				}
				Expect(established).To(BeTrue(),
					fmt.Sprintf("CRD %s should be Established", crdName))
			}

			By("Verified: All SBR CRDs are installed and established")
		})
	})

	Context("Agent Deployment", func() {

		It("should deploy SBD agent DaemonSet on all worker nodes", func() {
			By("Creating SBDConfig to trigger agent deployment")

			sbdConfigName := "qe-daemonset-test"
			sbdConfig, err := testNamespace.CreateSBDConfig(sbdConfigName,
				func(config *medik8sv1alpha1.SBDConfig) {
					config.Spec.SbdWatchdogPath = "/dev/watchdog"
					config.Spec.SharedStorageClass = rwxStorageClassName
				})
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")
			DeferCleanup(func() {
				if err := testNamespace.CleanupSBDConfig(sbdConfig); err != nil {
					GinkgoWriter.Printf("Warning: failed to cleanup SBDConfig %s: %v\n", sbdConfig.Name, err)
				}
			})
			By(fmt.Sprintf("Created SBDConfig: %s", sbdConfig.Name))

			dsName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)

			By("Waiting for agent DaemonSet to be created")
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: dsName, Namespace: testNamespace.Name},
					daemonSet)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(),
				"DaemonSet %s should be created", dsName)

			// Count worker nodes
			workerCount := int32(len(clusterInfo.WorkerNodes))
			By(fmt.Sprintf("Cluster has %d worker nodes", workerCount))
			Expect(workerCount).To(BeNumerically(">=", int32(1)))

			By("Waiting for DaemonSet to reach desired replicas")
			Eventually(func() int32 {
				_ = testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: dsName, Namespace: testNamespace.Name},
					daemonSet)
				return daemonSet.Status.NumberReady
			}, 3*time.Minute, 10*time.Second).Should(Equal(workerCount),
				"All worker nodes should have ready agent replicas")

			By(fmt.Sprintf("DaemonSet has %d/%d ready replicas", daemonSet.Status.NumberReady, workerCount))

			// Verify agent pods are Running
			agentPods := &corev1.PodList{}
			err = testClients.Client.List(testClients.Context,
				agentPods,
				client.InNamespace(testNamespace.Name),
				client.MatchingLabels{"app": "sbd-agent"})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(agentPods.Items)).To(Equal(int(workerCount)),
				"Should have one agent pod per worker node")

			for _, pod := range agentPods.Items {
				Expect(pod.Status.Phase).To(Equal(corev1.PodRunning),
					fmt.Sprintf("Agent pod %s should be Running", pod.Name))
			}

			By("Verified: All agent pods are running on eligible worker nodes")
		})
	})

	Context("Node Remediation via NHC", func() {

		It("should remediate a node when kubelet is stopped", func() {
			By("Ensuring SBDConfig exists for remediation test")
			sbdConfigName := "qe-remediation-test"
			sbdConfig, err := testNamespace.CreateSBDConfig(sbdConfigName,
				func(config *medik8sv1alpha1.SBDConfig) {
					config.Spec.SbdWatchdogPath = "/dev/watchdog"
					config.Spec.SharedStorageClass = rwxStorageClassName
				})
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")
			DeferCleanup(func() {
				if err := testNamespace.CleanupSBDConfig(sbdConfig); err != nil {
					GinkgoWriter.Printf("Warning: failed to cleanup SBDConfig %s: %v\n", sbdConfig.Name, err)
				}
			})

			dsName := fmt.Sprintf("sbd-agent-%s", sbdConfigName)

			By("Waiting for agent DaemonSet to be fully ready on all nodes")
			Eventually(func() bool {
				ds := &appsv1.DaemonSet{}
				if err := testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: dsName, Namespace: testNamespace.Name}, ds); err != nil {
					return false
				}
				return ds.Status.NumberReady > 0 && ds.Status.NumberReady == ds.Status.DesiredNumberScheduled
			}, 3*time.Minute, 10*time.Second).Should(BeTrue(),
				"All agent pods should be ready on all scheduled nodes")

			By("Selecting a ready worker node")
			var targetNode *corev1.Node
			nodeList := &corev1.NodeList{}
			Expect(testClients.Client.List(testClients.Context, nodeList)).To(Succeed())

			for i := range nodeList.Items {
				node := &nodeList.Items[i]
				_, isCP := node.Labels["node-role.kubernetes.io/control-plane"]
				_, isMaster := node.Labels["node-role.kubernetes.io/master"]
				if isCP || isMaster {
					continue
				}
				for _, c := range node.Status.Conditions {
					if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
						targetNode = node
						break
					}
				}
				if targetNode != nil {
					break
				}
			}
			Expect(targetNode).NotTo(BeNil(), "Need at least one Ready worker node")
			By(fmt.Sprintf("Target node: %s", targetNode.Name))

			By("Recording boot IDs for all nodes before disruption")
			originalBootIDs := getNodeBootIDs(clusterInfo)
			originalBootID := targetNode.Status.NodeInfo.BootID
			Expect(originalBootID).NotTo(BeEmpty())
			By(fmt.Sprintf("Original boot ID for target node: %s", originalBootID))

			// Deploy a pinned workload pod on the target node
			By("Deploying pinned workload pod on target node")
			workloadPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("qe-workload-%d", time.Now().Unix()),
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "qe-workload"},
				},
				Spec: corev1.PodSpec{
					NodeName:      targetNode.Name,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "workload",
						Image:   "registry.access.redhat.com/ubi9/ubi-minimal:latest",
						Command: []string{"/bin/bash", "-c", "sleep 3600"},
					}},
				},
			}
			Expect(testClients.Client.Create(testClients.Context, workloadPod)).To(Succeed())
			DeferCleanup(func() {
				if err := testClients.Client.Delete(testClients.Context, workloadPod); err != nil {
					GinkgoWriter.Printf("Warning: failed to delete workload pod %s: %v\n", workloadPod.Name, err)
				}
			})

			Eventually(func() corev1.PodPhase {
				p := &corev1.Pod{}
				_ = testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: workloadPod.Name, Namespace: testNamespace.Name}, p)
				return p.Status.Phase
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))

			By("Stopping kubelet on target node via oc debug")
			Expect(utils.StopKubeletOnNode(targetNode.Name)).To(Succeed())
			DeferCleanup(func() {
				By("DeferCleanup: ensuring kubelet is restarted on target node")
				if err := utils.StartKubeletOnNode(targetNode.Name); err != nil {
					GinkgoWriter.Printf("Warning: failed to restart kubelet on %s: %v\n", targetNode.Name, err)
				}
			})

			By("Waiting for node to become NotReady")
			utils.WaitForNodeNotReady(testClients, targetNode.Name, 3*time.Minute)

			// Simulate NHC creating a StorageBasedRemediation CR
			// The CR name must equal the target node name (the operator derives node from CR name)
			By("Simulating NHC: creating StorageBasedRemediation CR")
			sbdRemediation := &medik8sv1alpha1.StorageBasedRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      targetNode.Name,
					Namespace: testNamespace.Name,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationSpec{
					Reason:         medik8sv1alpha1.SBDRemediationReasonHeartbeatTimeout,
					TimeoutSeconds: 300,
				},
			}
			Expect(testClients.Client.Create(testClients.Context, sbdRemediation)).To(Succeed())
			DeferCleanup(func() {
				// Strip finalizers to prevent stuck resources (matches CleanupSBDConfigs pattern)
				latest := &medik8sv1alpha1.StorageBasedRemediation{}
				if err := testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: sbdRemediation.Name, Namespace: testNamespace.Name}, latest); err == nil {
					if len(latest.Finalizers) > 0 {
						latest.Finalizers = nil
						if err := testClients.Client.Update(testClients.Context, latest); err != nil {
							GinkgoWriter.Printf("Warning: failed to strip finalizers from StorageBasedRemediation %s: %v\n", sbdRemediation.Name, err)
						}
					}
				}
				if err := client.IgnoreNotFound(testClients.Client.Delete(testClients.Context, sbdRemediation)); err != nil {
					GinkgoWriter.Printf("Warning: failed to delete StorageBasedRemediation %s: %v\n", sbdRemediation.Name, err)
				}
			})
			By(fmt.Sprintf("Created StorageBasedRemediation for node %s", targetNode.Name))

			By("Waiting for node to reboot (boot ID change)")
			utils.WaitForNodeReboot(testClients, targetNode.Name, originalBootID, 10*time.Minute)

			By("Waiting for node to become Ready again")
			utils.WaitForNodeReady(testClients, targetNode.Name, 10*time.Minute)

			// Verify workload pod was deleted (out-of-service taint evicts pods)
			By("Verifying pinned workload pod was evicted")
			Eventually(func() bool {
				p := &corev1.Pod{}
				err := testClients.Client.Get(testClients.Context,
					client.ObjectKey{Name: workloadPod.Name, Namespace: testNamespace.Name}, p)
				if err != nil {
					return apierrors.IsNotFound(err)
				}
				// Pod might be in a terminal state on the old node
				return p.Spec.NodeName == targetNode.Name && p.Status.Phase != corev1.PodRunning
			}, 3*time.Minute, 10*time.Second).Should(BeTrue())

			// Verify other worker nodes were NOT rebooted
			By("Verifying other worker nodes remained stable")
			for _, wn := range clusterInfo.WorkerNodes {
				if strings.EqualFold(wn.Metadata.Name, targetNode.Name) {
					continue
				}
				currentBootID := getNodeBootID(wn.Metadata.Name)
				Expect(currentBootID).NotTo(BeEmpty(),
					fmt.Sprintf("Should get boot ID for node %s", wn.Metadata.Name))
				Expect(currentBootID).To(Equal(originalBootIDs[wn.Metadata.Name]),
					fmt.Sprintf("Node %s should NOT have rebooted during remediation of %s",
						wn.Metadata.Name, targetNode.Name))
			}

			By("Verified: Node remediation completed successfully")
		})
	})
})
