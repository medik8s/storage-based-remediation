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
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/api/v1alpha1"
	agent "github.com/medik8s/storage-based-remediation/internal/agent"
	"github.com/medik8s/storage-based-remediation/internal/mocks"
)

const (
	defaultReconcileCount     = 5
	defaultRequeueAfter       = 500000000
	validSharedStorageClass   = "test-ceph-sc"
	invalidSharedStorageClass = "invalid-storage-class"
	testOperatorImage         = "test-sbr-operator:latest"
	testAgentImage            = "test-sbr-agent:latest"
)

func checkForDefaultReconcile(counter int, result reconcile.Result, err error) {
	Expect(err).NotTo(HaveOccurred())
	Expect(result).To(Equal(reconcile.Result{}))
	Expect(counter).To(BeNumerically("==", 6))
}

func defaultStorageBasedRemediationConfig(resourceName, namespace string) *medik8sv1alpha1.StorageBasedRemediationConfig {
	return &medik8sv1alpha1.StorageBasedRemediationConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
			WatchdogPath:       "/dev/watchdog",
			SharedStorageClass: validSharedStorageClass,
		},
	}
}
func runReconcile(
	ctx context.Context, controllerReconciler *StorageBasedRemediationConfigReconciler, typeNamespacedName types.NamespacedName) (
	int, reconcile.Result, error) {
	var result reconcile.Result
	var err error
	counter := 0
	stop := false
	for !stop && counter < 20 {
		counter++
		result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: typeNamespacedName,
		})
		if err != nil {
			stop = true
		} else if result.RequeueAfter == 0 && !result.Requeue {
			stop = true
		}
	}
	By(fmt.Sprintf("Examining the results: counter: %d, result: %+v, err: %v", counter, result, err))
	return counter, result, err
}

func reconcileWithJob(
	ctx context.Context,
	controllerReconciler *StorageBasedRemediationConfigReconciler,
	typeNamespacedName types.NamespacedName,
) (int, reconcile.Result, error) {
	var result reconcile.Result
	var err error

	counter, result, err := runReconcile(ctx, controllerReconciler, typeNamespacedName)
	if err != nil && strings.Contains(err.Error(), "DaemonSet creation will be delayed until job completes") {
		By("looking for the init job")
		jobs := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobs, client.InNamespace(typeNamespacedName.Namespace))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		job := &jobs.Items[0]
		Expect(jobs.Items).To(HaveLen(1))

		By(fmt.Sprintf("updating job %s to be completed", job.Name))
		job.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		By("looking for the init job")
		jobs = &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobs, client.InNamespace(typeNamespacedName.Namespace))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		job = &jobs.Items[0]
		Expect(jobs.Items).To(HaveLen(1))
		Expect(job.Status.Succeeded).To(BeNumerically("==", 1))

		By("Reconciling the StorageBasedRemediationConfig again")
		partial := 0
		partial, result, err = runReconcile(ctx, controllerReconciler, typeNamespacedName)
		counter = counter + partial
	}
	return counter, result, err
}

var _ = Describe("StorageBasedRemediationConfig Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-sbrconfig"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

		var namespace string

		ctx := context.Background()

		// typeNamespacedName will be set dynamically in tests since namespace is set in BeforeEach
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *StorageBasedRemediationConfigReconciler

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			namespace = fmt.Sprintf("test-sbr-system-%d", time.Now().UnixNano())

			// Set the typeNamespacedName now that we have the namespace
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}

			By("creating the test namespace")
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			By("initializing controller reconciler")
			controllerReconciler = &StorageBasedRemediationConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Populate k8s with a fake Ceph shared storage class
			reclaimPolicy := corev1.PersistentVolumeReclaimRetain
			storageClass := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: validSharedStorageClass,
				},
				Provisioner:   "openshift-storage.cephfs.csi.ceph.com",
				ReclaimPolicy: &reclaimPolicy,
				Parameters: map[string]string{
					"clusterID": "openshift-storage",
					"csi.storage.k8s.io/controller-expand-secret-name":      "rook-csi-cephfs-provisioner",
					"csi.storage.k8s.io/controller-expand-secret-namespace": "openshift-storage",
					"csi.storage.k8s.io/node-stage-secret-name":             "rook-csi-cephfs-node",
					"csi.storage.k8s.io/node-stage-secret-namespace":        "openshift-storage",
					"csi.storage.k8s.io/provisioner-secret-name":            "rook-csi-cephfs-provisioner",
					"csi.storage.k8s.io/provisioner-secret-namespace":       "openshift-storage",
					"fsName": "ocs-storagecluster-cephfilesystem",
					"pool":   "ocs-storagecluster-cephfilesystem-data0",
				},
			}
			err := k8sClient.Create(ctx, storageClass)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			// set environment variable for the operator image
			err = os.Setenv("OPERATOR_IMAGE", testOperatorImage)
			Expect(err).NotTo(HaveOccurred())
			err = os.Setenv("POD_NAMESPACE", namespace)
			Expect(err).NotTo(HaveOccurred())

			err = os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
			})
		})

		AfterEach(func() {
			By("cleaning up the specific resource instance StorageBasedRemediationConfig")
			resource := &medik8sv1alpha1.StorageBasedRemediationConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			By("cleaning up the test namespace")
			testNamespace := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, testNamespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("should successfully reconcile an existing resource", func() {
			By("creating the custom resource for the Kind StorageBasedRemediationConfig")
			resource := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the created resource")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the resource still exists")
			sbrconfig := &medik8sv1alpha1.StorageBasedRemediationConfig{}
			err = k8sClient.Get(ctx, typeNamespacedName, sbrconfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(sbrconfig.Name).To(Equal(resourceName))
		})

		It("should handle reconciling a non-existent resource", func() {
			nonExistentName := types.NamespacedName{
				Name:      "non-existent-sbrconfig",
				Namespace: namespace,
			}

			By("reconciling a non-existent resource")
			_, result, err := runReconcile(ctx, controllerReconciler, nonExistentName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should configure sbr-device flag correctly for shared storage", func() {
			By("creating an StorageBasedRemediationConfig with shared storage")
			resource := defaultStorageBasedRemediationConfig(resourceName, namespace)

			By("testing the buildSBRAgentArgs method")
			args := controllerReconciler.buildSBRAgentArgs(resource)

			By("verifying the sbr-device flag is set correctly")
			expectedSBRDevice := fmt.Sprintf("--%s=%s/%s",
				agent.FlagSBRDevice, agent.SharedStorageSBRDeviceDirectory, agent.SharedStorageSBRDeviceFile)
			Expect(args).To(ContainElement(expectedSBRDevice))

			By("verifying file locking is enabled for shared storage")
			expectedFileLocking := fmt.Sprintf("--%s=true", agent.FlagSBRFileLocking)
			Expect(args).To(ContainElement(expectedFileLocking))

			expectedMaxFailures := fmt.Sprintf("--%s=%d", agent.FlagMaxConsecutiveFailures, medik8sv1alpha1.DefaultMaxConsecutiveFailures)
			Expect(args).To(ContainElement(expectedMaxFailures))

			expectedTimeout := fmt.Sprintf("--%s=%d", agent.FlagSBRTimeoutSeconds, medik8sv1alpha1.DefaultSBRTimeoutSeconds)
			Expect(args).To(ContainElement(expectedTimeout))

			expectedUpdateInterval := fmt.Sprintf("--%s=%s", agent.FlagSBRUpdateInterval, resource.Spec.GetSBRUpdateInterval().String())
			Expect(args).To(ContainElement(expectedUpdateInterval))
		})

		It("should successfully reconcile after resource deletion", func() {
			By("creating the custom resource for the Kind StorageBasedRemediationConfig")
			resource := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("deleting the resource first")
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling the deleted resource")
			counter, result, err := runReconcile(ctx, controllerReconciler, typeNamespacedName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(counter).To(BeNumerically("==", 1))
		})
	})

	Context("When testing DaemonSet management", func() {
		const (
			resourceName = "test-daemonset-sbrconfig"
			timeout      = time.Second * 30
			interval     = time.Millisecond * 250
		)

		var namespace string
		ctx := context.Background()

		// typeNamespacedName will be set dynamically in BeforeEach
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *StorageBasedRemediationConfigReconciler

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			namespace = fmt.Sprintf("test-daemonset-system-%d", time.Now().UnixNano())

			// Set the typeNamespacedName now that we have the namespace
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}

			By("creating the test namespace")
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			By("initializing controller reconciler")
			controllerReconciler = &StorageBasedRemediationConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			err := os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
			})
		})

		AfterEach(func() {
			By("cleaning up the specific resource instance StorageBasedRemediationConfig")
			resource := &medik8sv1alpha1.StorageBasedRemediationConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			By("cleaning up the test namespace")
			testNamespace := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, testNamespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("should create a DaemonSet when StorageBasedRemediationConfig is applied", func() {
			By("creating the StorageBasedRemediationConfig resource")
			sbrConfig := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created")
			expectedDaemonSetName := fmt.Sprintf("sbr-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
			}, timeout, interval).Should(Succeed())

			By("verifying the DaemonSet has correct configuration")
			Expect(daemonSet.Name).To(Equal(expectedDaemonSetName))
			Expect(daemonSet.Namespace).To(Equal(namespace))
			Expect(daemonSet.Labels["app"]).To(Equal("sbr-agent"))
			Expect(daemonSet.Labels["sbrconfig"]).To(Equal(resourceName))
			Expect(daemonSet.Labels["managed-by"]).To(Equal("sbr-operator"))

			By("verifying the DaemonSet has the correct owner reference")
			Expect(daemonSet.OwnerReferences).To(HaveLen(1))
			Expect(daemonSet.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(daemonSet.OwnerReferences[0].Kind).To(Equal("StorageBasedRemediationConfig"))
			Expect(*daemonSet.OwnerReferences[0].Controller).To(BeTrue())

			By("verifying the DaemonSet pod template has correct configuration")
			container := daemonSet.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("sbr-agent"))
			Expect(container.Image).To(Equal(testAgentImage))
			Expect(container.Args).To(ContainElement("--watchdog-path=/dev/watchdog"))

			By("verifying the DaemonSet has correct volume mounts")
			Expect(container.VolumeMounts).To(HaveLen(4))
			volumeMountNames := make([]string, len(container.VolumeMounts))
			for i, vm := range container.VolumeMounts {
				volumeMountNames[i] = vm.Name
			}
			Expect(volumeMountNames).To(ContainElements("dev", "sys", "proc", "shared-storage"))

			By("verifying the DaemonSet has correct volumes")
			Expect(daemonSet.Spec.Template.Spec.Volumes).To(HaveLen(4))
			volumeNames := make([]string, len(daemonSet.Spec.Template.Spec.Volumes))
			for i, v := range daemonSet.Spec.Template.Spec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ContainElements("dev", "sys", "proc", "shared-storage"))

			By("verifying the DaemonSet has correct security context")
			Expect(*container.SecurityContext.Privileged).To(BeTrue())
			Expect(*container.SecurityContext.RunAsUser).To(BeEquivalentTo(0))

			By("verifying the DaemonSet does not use host networking")
			Expect(daemonSet.Spec.Template.Spec.HostNetwork).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))

			By("verifying the DaemonSet declares container ports")
			Expect(container.Ports).To(HaveLen(2))
			Expect(container.Ports[0].Name).To(Equal("runtime-metrics"))
			Expect(container.Ports[0].ContainerPort).To(BeEquivalentTo(8080))
			Expect(container.Ports[1].Name).To(Equal("agent-metrics"))
			Expect(container.Ports[1].ContainerPort).To(BeEquivalentTo(agent.DefaultMetricsPort))
		})

		It("should update DaemonSet when StorageBasedRemediationConfig is modified", func() {
			By("creating the StorageBasedRemediationConfig resource")
			sbrConfig := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created")
			expectedDaemonSetName := fmt.Sprintf("sbr-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
			}, timeout, interval).Should(Succeed())

			By("updating the StorageBasedRemediationConfig watchdog path")
			// Fetch the latest version to avoid conflicts
			err = k8sClient.Get(ctx, typeNamespacedName, sbrConfig)
			Expect(err).NotTo(HaveOccurred())
			sbrConfig.Spec.WatchdogPath = "/dev/watchdog1"
			Expect(k8sClient.Update(ctx, sbrConfig)).To(Succeed())

			By("reconciling the updated StorageBasedRemediationConfig")
			counter, result, err = runReconcile(ctx, controllerReconciler, typeNamespacedName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(counter).To(BeNumerically("==", 1))

			By("verifying the DaemonSet was updated with new watchdog path")
			Eventually(func() []string {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
				if err != nil {
					return nil
				}
				return daemonSet.Spec.Template.Spec.Containers[0].Args
			}, timeout, interval).Should(ContainElement("--watchdog-path=/dev/watchdog1"))
		})

		It("should set correct owner reference for garbage collection", func() {
			By("creating the StorageBasedRemediationConfig resource")
			sbrConfig := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created")
			expectedDaemonSetName := fmt.Sprintf("sbr-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
			}, timeout, interval).Should(Succeed())

			By("verifying the DaemonSet has correct owner reference before deletion")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      expectedDaemonSetName,
				Namespace: namespace,
			}, daemonSet)
			Expect(err).NotTo(HaveOccurred())
			Expect(daemonSet.OwnerReferences).To(HaveLen(1))
			Expect(daemonSet.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(daemonSet.OwnerReferences[0].Kind).To(Equal("StorageBasedRemediationConfig"))
			Expect(*daemonSet.OwnerReferences[0].Controller).To(BeTrue())

			By("deleting the StorageBasedRemediationConfig")
			Expect(k8sClient.Delete(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig deletion to handle finalizer cleanup")
			counter, result, err = reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			Expect(counter).To(BeNumerically("==", 1))
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the StorageBasedRemediationConfig is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, sbrConfig)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())

			// Note: In test environments, garbage collection may not run automatically
			// The important verification is that the owner reference was correctly set above
		})

		It("should handle default values correctly", func() {
			By("creating the StorageBasedRemediationConfig resource with minimal spec")
			sbrConfig := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created with default values")
			expectedDaemonSetName := fmt.Sprintf("sbr-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace, // deployed to same namespace as StorageBasedRemediationConfig
				}, daemonSet)
			}, timeout, interval).Should(Succeed())

			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal(testAgentImage))
			Expect(daemonSet.Namespace).To(Equal(namespace))
		})
	})

	Context("When testing event emission", func() {
		const (
			resourceName = "test-events-sbrconfig"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

		var namespace string
		ctx := context.Background()
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *StorageBasedRemediationConfigReconciler
		var mockRecorder *mocks.MockEventRecorder

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			namespace = fmt.Sprintf("test-events-system-%d", time.Now().UnixNano())

			// Set the typeNamespacedName now that we have the namespace
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}

			By("creating the test namespace")
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			mockRecorder = mocks.NewMockEventRecorder()
			controllerReconciler = &StorageBasedRemediationConfigReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: mockRecorder,
			}

			err := os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
			})
		})

		AfterEach(func() {
			// Clean up resources
			resource := &medik8sv1alpha1.StorageBasedRemediationConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				_ = k8sClient.Delete(ctx, resource)
			}

			testNamespace := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, testNamespace)
			if err == nil {
				_ = k8sClient.Delete(ctx, testNamespace)
			}
		})

		It("should emit events during successful reconciliation", func() {
			By("creating the StorageBasedRemediationConfig resource")
			resource := defaultStorageBasedRemediationConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the resource multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying events were emitted")
			events := mockRecorder.GetEvents()
			Expect(len(events)).To(BeNumerically(">=", 2))

			// Check for DaemonSet management event
			daemonSetEvent := false
			for _, event := range events {
				if event.Reason == ReasonDaemonSetManaged && event.EventType == EventTypeNormal {
					daemonSetEvent = true
					Expect(event.Message).To(ContainSubstring("DaemonSet"))
					break
				}
			}
			Expect(daemonSetEvent).To(BeTrue(), "DaemonSet management event should be emitted")

			// Check for reconciliation success event
			reconcileEvent := false
			for _, event := range events {
				if event.Reason == ReasonStorageBasedRemediationConfigReconciled && event.EventType == EventTypeNormal {
					reconcileEvent = true
					Expect(event.Message).To(ContainSubstring(resourceName))
					break
				}
			}
			Expect(reconcileEvent).To(BeTrue(), "StorageBasedRemediationConfig reconciled event should be emitted")
		})

		It("should emit events for helper methods", func() {
			By("testing emitEvent helper")
			resource := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
			}

			controllerReconciler.emitEvent(resource, EventTypeNormal, "TestReason", "Test message")

			events := mockRecorder.GetEvents()
			Expect(events).To(HaveLen(1))
			Expect(events[0].EventType).To(Equal(EventTypeNormal))
			Expect(events[0].Reason).To(Equal("TestReason"))
			Expect(events[0].Message).To(Equal("Test message"))

			By("testing emitEventf helper")
			mockRecorder.Reset()
			controllerReconciler.emitEventf(resource, EventTypeWarning, "TestFormat", "Formatted message: %s", "test-value")

			events = mockRecorder.GetEvents()
			Expect(events).To(HaveLen(1))
			Expect(events[0].EventType).To(Equal(EventTypeWarning))
			Expect(events[0].Reason).To(Equal("TestFormat"))
			Expect(events[0].Message).To(Equal("Formatted message: test-value"))
		})

		It("should handle nil recorder gracefully", func() {
			By("setting recorder to nil")
			controllerReconciler.Recorder = nil

			resource := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			}

			By("calling event methods with nil recorder")
			// These should not panic
			controllerReconciler.emitEvent(resource, EventTypeNormal, "TestReason", "Test message")
			controllerReconciler.emitEventf(resource, EventTypeWarning, "TestFormat", "Formatted message: %s", "test-value")

			// No events should be recorded
			events := mockRecorder.GetEvents()
			Expect(events).To(BeEmpty())
		})
	})

	Context("When testing controller initialization", func() {
		var namespace string

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			namespace = fmt.Sprintf("test-events-system-%d", time.Now().UnixNano())

			// Create the test namespace
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			err := os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
			})
		})

		AfterEach(func() {
			// Clean up the test namespace
			testNamespace := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, testNamespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("should create a controller reconciler successfully", func() {
			reconciler := &StorageBasedRemediationConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			Expect(reconciler.Client).NotTo(BeNil())
			Expect(reconciler.Scheme).NotTo(BeNil())
		})
	})

	Context("When validating storage classes", func() {
		const (
			timeout  = time.Second * 10
			interval = time.Millisecond * 250
		)

		var validationReconciler *StorageBasedRemediationConfigReconciler
		var mockEventRecorder *mocks.MockEventRecorder
		var validationNamespace string

		BeforeEach(func() {
			validationNamespace = fmt.Sprintf("validation-test-%d", time.Now().UnixNano())

			// Create the test namespace
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: validationNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			// Create reconciler with mock event recorder
			mockEventRecorder = mocks.NewMockEventRecorder()
			validationReconciler = &StorageBasedRemediationConfigReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: mockEventRecorder,
			}

			err := os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
			})
		})

		AfterEach(func() {
			// Clean up test namespace
			testNamespace := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: validationNamespace}, testNamespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("should reject gp3-csi storage class (ReadWriteOnce only)", func() {
			By("creating a gp3-csi storage class")
			gp3StorageClass := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gp3-csi",
				},
				Provisioner: "ebs.csi.aws.com",
				Parameters: map[string]string{
					"type": "gp3",
				},
				AllowVolumeExpansion: &[]bool{true}[0],
			}
			Expect(k8sClient.Create(ctx, gp3StorageClass)).To(Succeed())

			By("creating StorageBasedRemediationConfig with gp3-csi storage class")
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gp3-validation",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass: "gp3-csi",
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("performing first reconciliation (adds finalizer)")
			counter, result, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
				Name:      sbrConfig.Name,
				Namespace: sbrConfig.Namespace,
			})
			By("expecting reconciliation to fail due to storage class incompatibility")
			Expect(err).To(HaveOccurred())
			Expect(counter).To(BeNumerically("==", 3))
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(err.Error()).To(ContainSubstring("does not support ReadWriteMany"))

			By("verifying warning event was emitted")
			Eventually(func() bool {
				events := mockEventRecorder.GetEvents()
				for _, event := range events {
					if event.EventType == EventTypeWarning && event.Reason == ReasonPVCError {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("cleaning up test storage class")
			Expect(k8sClient.Delete(ctx, gp3StorageClass)).To(Succeed())
		})

		It("should reject non-existent storage class", func() {
			By("creating StorageBasedRemediationConfig with non-existent storage class")
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-missing-sc",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass: "non-existent-storage-class",
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("performing reconciliation")
			_, _, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
				Name:      sbrConfig.Name,
				Namespace: sbrConfig.Namespace,
			})

			By("expecting reconciliation to fail due to missing storage class")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))

			By("verifying warning event was emitted")
			Eventually(func() bool {
				events := mockEventRecorder.GetEvents()
				for _, event := range events {
					if event.EventType == EventTypeWarning && event.Reason == ReasonPVCError {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("should skip validation when no shared storage is configured", func() {
			By("creating StorageBasedRemediationConfig without shared storage")
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-shared-storage",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					// No SharedStorageClass specified
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("reconciling the StorageBasedRemediationConfig")
			counter, result, err := reconcileWithJob(ctx, validationReconciler, types.NamespacedName{
				Name:      sbrConfig.Name,
				Namespace: sbrConfig.Namespace,
			})
			Expect(counter).To(BeNumerically("==", 3))
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(err.Error()).To(ContainSubstring("no shared storage configured"))
		})

		It("should recognize known RWX-compatible provisioners", func() {
			testCases := []struct {
				name        string
				provisioner string
				compatible  bool
			}{
				{"AWS EFS", "efs.csi.aws.com", true},
				{"Azure Files", "file.csi.azure.com", true},
				{"GCP Filestore", "filestore.csi.storage.gke.io", true},
				{"NFS CSI", "nfs.csi.k8s.io", true},
				{"CephFS", "cephfs.csi.ceph.com", true},
				{"AWS EBS", "ebs.csi.aws.com", false},
				{"Azure Disk", "disk.csi.azure.com", false},
				{"GCP Persistent Disk", "pd.csi.storage.gke.io", false},
			}

			for _, tc := range testCases {
				By(fmt.Sprintf("testing %s provisioner (%s)", tc.name, tc.provisioner))

				compatible := validationReconciler.isRWXCompatibleProvisioner(tc.provisioner)
				Expect(compatible).To(Equal(tc.compatible),
					fmt.Sprintf("Expected %s (%s) to be compatible=%t", tc.name, tc.provisioner, tc.compatible))
			}
		})

		It("should recognize known RWX-incompatible provisioners", func() {
			testCases := []struct {
				name         string
				provisioner  string
				incompatible bool
			}{
				{"AWS EBS", "ebs.csi.aws.com", true},
				{"Azure Disk", "disk.csi.azure.com", true},
				{"GCP Persistent Disk", "pd.csi.storage.gke.io", true},
				{"VMware vSphere", "csi.vsphere.vmware.com", true},
				{"OpenStack Cinder", "cinder.csi.openstack.org", true},
				{"AWS EFS", "efs.csi.aws.com", false},
				{"Azure Files", "file.csi.azure.com", false},
				{"Unknown provisioner", "unknown.provisioner.example.com", false},
			}

			for _, tc := range testCases {
				By(fmt.Sprintf("testing %s provisioner (%s)", tc.name, tc.provisioner))

				incompatible := validationReconciler.isRWXIncompatibleProvisioner(tc.provisioner)
				Expect(incompatible).To(Equal(tc.incompatible),
					fmt.Sprintf("Expected %s (%s) to be incompatible=%t", tc.name, tc.provisioner, tc.incompatible))
			}
		})

		Context("When testing unknown provisioners", func() {
			It("should test unknown provisioner", func() {
				By("creating a custom storage class with unknown provisioner")
				customStorageClass := &storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "custom-unknown-provisioner",
					},
					Provisioner: "custom.example.com/unknown-provisioner",
					Parameters: map[string]string{
						"type": "custom",
					},
				}
				Expect(k8sClient.Create(ctx, customStorageClass)).To(Succeed())

				By("creating StorageBasedRemediationConfig with unknown provisioner")
				sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-unknown-provisioner",
						Namespace: validationNamespace,
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
						SharedStorageClass: "custom-unknown-provisioner",
					},
				}
				Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

				By("reconciling the StorageBasedRemediationConfig")
				counter, result, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
					Name:      sbrConfig.Name,
					Namespace: sbrConfig.Namespace,
				})
				Expect(counter).To(BeNumerically("==", 4))
				Expect(result).To(Equal(reconcile.Result{}))

				By("expecting reconciliation to complete (may succeed or fail depending on actual provisioner capability)")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ReadWriteMany"))
			})
		})

		Context("When a stale test PVC exists from a previous interrupted run (RHWA-1017)", func() {
			var (
				customStorageClass *storagev1.StorageClass
				stalePVC           *corev1.PersistentVolumeClaim
				sbrConfig          *medik8sv1alpha1.StorageBasedRemediationConfig
			)

			BeforeEach(func() {
				customStorageClass = &storagev1.StorageClass{
					ObjectMeta:  metav1.ObjectMeta{Name: "stale-pvc-test-sc"},
					Provisioner: "custom.example.com/stale-pvc-test",
				}
				Expect(k8sClient.Create(ctx, customStorageClass)).To(Succeed())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, customStorageClass)).To(Succeed())
				})

				sbrConfigName := "test-stale-pvc-cleanup"

				stalePVC = &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-rwx-test", sbrConfigName),
						Namespace: validationNamespace,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
						StorageClassName: &customStorageClass.Name,
					},
				}
				Expect(k8sClient.Create(ctx, stalePVC)).To(Succeed())

				// envtest has no pvc-protection controller to clear the kubernetes.io/pvc-protection
				// finalizer, so deletion would stall. Strip it manually; a real cluster handles this automatically.
				patch := client.MergeFrom(stalePVC.DeepCopy())
				stalePVC.Finalizers = nil
				Expect(k8sClient.Patch(ctx, stalePVC, patch)).To(Succeed())

				sbrConfig = &medik8sv1alpha1.StorageBasedRemediationConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      sbrConfigName,
						Namespace: validationNamespace,
					},
					Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
						SharedStorageClass: customStorageClass.Name,
					},
				}
				Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, sbrConfig)).To(Succeed())
				})
			})

			It("should replace a stale test PVC with a fresh one on reconcile", func() {
				_, _, _ = runReconcile(ctx, validationReconciler, types.NamespacedName{
					Name:      sbrConfig.Name,
					Namespace: sbrConfig.Namespace,
				})

				finalPVC := &corev1.PersistentVolumeClaim{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: stalePVC.Name, Namespace: validationNamespace}, finalPVC)
				Expect(err).NotTo(HaveOccurred())
				Expect(finalPVC.UID).NotTo(Equal(stalePVC.UID),
					"PVC should be a newly created one, not the original stale PVC")
				// In envtest, pvc-protection finalizer is never cleared so the PVC stays
				// in Terminating after the controller's defer delete; in a real cluster it
				// would be fully removed.
				Expect(finalPVC.DeletionTimestamp).NotTo(BeNil(),
					"New PVC should be in Terminating state (deleted by controller defer cleanup)")
			})
		})
	})

	Context("When verifying PV reclaim-policy cleanup (RHWA-1017)", func() {
		const (
			pvPatchRetainSCName    = "pv-patch-retain-sc"
			pvPatchUnknownProv     = "custom.example.com/pv-patch-test"
			pvPatchCephSCName      = "pv-patch-ceph-sc"
			pvPatchCephProvisioner = "openshift-storage.cephfs.csi.ceph.com"
		)

		var (
			pvPatchNamespace  string
			pvPatchReconciler *StorageBasedRemediationConfigReconciler
		)

		ctx := context.Background()

		BeforeEach(func() {
			pvPatchNamespace = fmt.Sprintf("pv-patch-test-%d", time.Now().UnixNano())
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: pvPatchNamespace},
			})).To(Succeed())

			pvPatchReconciler = &StorageBasedRemediationConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Unknown-provisioner SC with Retain policy — triggers testRWXSupport
			reclaimPolicy := corev1.PersistentVolumeReclaimRetain
			retainSC := &storagev1.StorageClass{
				ObjectMeta:    metav1.ObjectMeta{Name: pvPatchRetainSCName},
				Provisioner:   pvPatchUnknownProv,
				ReclaimPolicy: &reclaimPolicy,
			}
			err := k8sClient.Create(ctx, retainSC)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			// Known-RWX Ceph SC — skips testRWXSupport (used by the handleDeletion test)
			cephSC := &storagev1.StorageClass{
				ObjectMeta:    metav1.ObjectMeta{Name: pvPatchCephSCName},
				Provisioner:   pvPatchCephProvisioner,
				ReclaimPolicy: &reclaimPolicy,
				Parameters: map[string]string{
					"clusterID": "openshift-storage",
					"fsName":    "ocs-storagecluster-cephfilesystem",
				},
			}
			err = k8sClient.Create(ctx, cephSC)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)).To(Succeed())
			DeferCleanup(func() {
				_ = os.Unsetenv("RELATED_IMAGE_AGENT")
				ns := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvPatchNamespace}, ns); err == nil {
					_ = k8sClient.Delete(ctx, ns)
				}
			})
		})

		// Verifies that the test PVC has no OwnerReference (preventing a runaway reconcile loop)
		// and that a bound test PV with reclaimPolicy Retain is patched to Delete on cleanup.
		// Both checks share the same goroutine observation window during testRWXSupport's 5s sleep.
		It("should not set OwnerReference on test PVC and should patch the bound test PV reclaimPolicy to Delete", func() {
			const sbrConfigName = "test-rwxsupport-cleanup"
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: sbrConfigName, Namespace: pvPatchNamespace},
				Spec:       medik8sv1alpha1.StorageBasedRemediationConfigSpec{SharedStorageClass: pvPatchRetainSCName},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, sbrConfig) })

			// Pre-create a PV with Retain policy to simulate a provisioner bound volume.
			// PersistentVolume is cluster-scoped, so derive the name from the unique namespace.
			pvName := fmt.Sprintf("test-pv-%s", pvPatchNamespace)
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: pvName},
				Spec: corev1.PersistentVolumeSpec{
					Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						NFS: &corev1.NFSVolumeSource{Server: "fake-nfs", Path: "/data"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pv)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pv) })

			// testRWXSupport creates the test PVC and then sleeps 5s before re-fetching it.
			// Run the reconcile in the background so we can inspect the PVC during that window.
			reconcileDone := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(reconcileDone)
				runReconcile(ctx, pvPatchReconciler, types.NamespacedName{
					Name: sbrConfigName, Namespace: pvPatchNamespace,
				})
			}()

			testPVCName := fmt.Sprintf("%s-rwx-test", sbrConfigName)
			testPVC := &corev1.PersistentVolumeClaim{}
			Eventually(func() error {
				return k8sClient.Get(ctx,
					types.NamespacedName{Name: testPVCName, Namespace: pvPatchNamespace}, testPVC)
			}, 8*time.Second, 200*time.Millisecond).Should(Succeed())

			By("verifying the test PVC carries no OwnerReference")
			Expect(testPVC.OwnerReferences).To(BeEmpty(),
				"the test PVC must not carry an OwnerReference to the SBRConfig")

			By("simulating a provisioner binding the test PVC to the Retain PV")
			patch := client.MergeFrom(testPVC.DeepCopy())
			testPVC.Spec.VolumeName = pvName
			Expect(k8sClient.Patch(ctx, testPVC, patch)).To(Succeed())

			<-reconcileDone

			By("verifying the test PV reclaim policy was patched to Delete")
			fetchedPV := &corev1.PersistentVolume{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvName}, fetchedPV)).To(Succeed())
			Expect(fetchedPV.Spec.PersistentVolumeReclaimPolicy).To(
				Equal(corev1.PersistentVolumeReclaimDelete),
				"test PV reclaim policy must be patched to Delete before the test PVC is deleted",
			)
		})

		// Covers the handleDeletion fix: shared-storage PV with Retain policy is patched to
		// Delete before the SBRConfig finalizer is removed, preventing Released PV accumulation.
		It("should patch the shared-storage PV reclaimPolicy to Delete during SBRConfig deletion", func() {
			const sbrConfigName = "test-deletion-pv-patch"
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: sbrConfigName, Namespace: pvPatchNamespace},
				// Ceph is known-RWX-compatible so validateStorageClass short-circuits
				// and testRWXSupport is never called.
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{SharedStorageClass: pvPatchCephSCName},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			By("running reconcile until the init-job waiting error; the shared-storage PVC is created by then")
			_, _, _ = runReconcile(ctx, pvPatchReconciler, types.NamespacedName{
				Name: sbrConfigName, Namespace: pvPatchNamespace,
			})

			sharedPVCName := sbrConfig.Spec.GetSharedStoragePVCName(sbrConfigName)
			sharedPVC := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: sharedPVCName, Namespace: pvPatchNamespace,
			}, sharedPVC)).To(Succeed())

			By("creating a Retain PV and binding the shared-storage PVC to it")
			// PersistentVolume is cluster-scoped, so derive the name from the unique namespace.
			sharedPVName := fmt.Sprintf("test-shared-pv-%s", pvPatchNamespace)
			sharedPV := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: sharedPVName},
				Spec: corev1.PersistentVolumeSpec{
					Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Mi")},
					AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						NFS: &corev1.NFSVolumeSource{Server: "fake-nfs", Path: "/shared"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sharedPV)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, sharedPV) })

			pvcPatch := client.MergeFrom(sharedPVC.DeepCopy())
			sharedPVC.Spec.VolumeName = sharedPVName
			Expect(k8sClient.Patch(ctx, sharedPVC, pvcPatch)).To(Succeed())

			By("deleting the SBRConfig and running the finalizer-removal reconcile")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: sbrConfigName, Namespace: pvPatchNamespace,
			}, sbrConfig)).To(Succeed())
			Expect(k8sClient.Delete(ctx, sbrConfig)).To(Succeed())

			_, result, err := runReconcile(ctx, pvPatchReconciler, types.NamespacedName{
				Name: sbrConfigName, Namespace: pvPatchNamespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			By("verifying the shared-storage PV reclaim policy was patched to Delete")
			fetchedPV := &corev1.PersistentVolume{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sharedPVName}, fetchedPV)).To(Succeed())
			Expect(fetchedPV.Spec.PersistentVolumeReclaimPolicy).To(
				Equal(corev1.PersistentVolumeReclaimDelete),
				"shared-storage PV reclaim policy must be patched to Delete during SBRConfig deletion",
			)
		})
	})

	Context("When testing block volume mode support", func() {
		var blockReconciler *StorageBasedRemediationConfigReconciler
		var blockNamespace string
		const blockStorageClass = "test-rbd-sc"

		ctx := context.Background()

		BeforeEach(func() {
			blockNamespace = fmt.Sprintf("block-test-%d", time.Now().UnixNano())

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: blockNamespace},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			blockReconciler = &StorageBasedRemediationConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create a Ceph RBD storage class for block mode tests
			reclaimPolicy := corev1.PersistentVolumeReclaimDelete
			sc := &storagev1.StorageClass{
				ObjectMeta:    metav1.ObjectMeta{Name: blockStorageClass},
				Provisioner:   "openshift-storage.rbd.csi.ceph.com",
				ReclaimPolicy: &reclaimPolicy,
			}
			err := k8sClient.Create(ctx, sc)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(os.Setenv("RELATED_IMAGE_AGENT", testAgentImage)).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("RELATED_IMAGE_AGENT") })
		})

		AfterEach(func() {
			ns := &corev1.Namespace{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: blockNamespace}, ns); err == nil {
				Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			}
		})

		It("should configure agent args for block mode", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}

			args := blockReconciler.buildSBRAgentArgs(sbrConfig)

			By("verifying block device path is used instead of filesystem path")
			expectedDevice := fmt.Sprintf("--%s=%s", agent.FlagSBRDevice, agent.SharedStorageBlockDevicePath)
			Expect(args).To(ContainElement(expectedDevice))

			By("verifying file locking is disabled for block mode")
			expectedLocking := fmt.Sprintf("--%s=false", agent.FlagSBRFileLocking)
			Expect(args).To(ContainElement(expectedLocking))
		})

		It("should configure agent args for filesystem mode", func() {
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "fs-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass: validSharedStorageClass,
				},
			}

			args := blockReconciler.buildSBRAgentArgs(sbrConfig)

			By("verifying filesystem path is used")
			expectedDevice := fmt.Sprintf("--%s=%s/%s",
				agent.FlagSBRDevice, agent.SharedStorageSBRDeviceDirectory, agent.SharedStorageSBRDeviceFile)
			Expect(args).To(ContainElement(expectedDevice))

			By("verifying file locking is enabled for filesystem mode")
			expectedLocking := fmt.Sprintf("--%s=true", agent.FlagSBRFileLocking)
			Expect(args).To(ContainElement(expectedLocking))
		})

		It("should use volumeDevices for block mode DaemonSet", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-ds-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}

			By("verifying volumeDevices contains block device path")
			devices := blockReconciler.buildVolumeDevices(sbrConfig)
			Expect(devices).To(HaveLen(1))
			Expect(devices[0].Name).To(Equal("shared-storage"))
			Expect(devices[0].DevicePath).To(Equal(agent.SharedStorageBlockDevicePath))

			By("verifying volumeMounts does NOT contain shared-storage")
			mounts := blockReconciler.buildVolumeMounts(sbrConfig)
			for _, m := range mounts {
				Expect(m.Name).NotTo(Equal("shared-storage"))
			}
		})

		It("should not place block device path under any hostPath mount", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-overlap-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}

			By("verifying block device path does not overlap with any volume mount path")
			mounts := blockReconciler.buildVolumeMounts(sbrConfig)
			devices := blockReconciler.buildVolumeDevices(sbrConfig)
			Expect(devices).To(HaveLen(1))
			devicePath := devices[0].DevicePath
			for _, m := range mounts {
				Expect(strings.HasPrefix(devicePath, m.MountPath)).To(BeFalse(),
					"block device path %q must not be under mount path %q — hostPath mount would shadow the volumeDevice",
					devicePath, m.MountPath)
			}
		})

		It("should mount host /dev at /host-dev and resolve the watchdog there in block mode", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-hostdev-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}

			By("verifying host /dev is mounted at /host-dev, never over /dev")
			mounts := blockReconciler.buildVolumeMounts(sbrConfig)
			var hostDev *corev1.VolumeMount
			for i := range mounts {
				Expect(mounts[i].MountPath).NotTo(Equal("/dev"),
					"a bind mount at /dev makes the OCI runtime skip creating the CSI block device node")
				if mounts[i].Name == "host-dev" {
					hostDev = &mounts[i]
				}
			}
			Expect(hostDev).NotTo(BeNil(), "block mode must mount host /dev at a side path")
			Expect(hostDev.MountPath).To(Equal(BlockModeHostDevMountPath))

			By("verifying the agent resolves the watchdog under /host-dev")
			args := blockReconciler.buildSBRAgentArgs(sbrConfig)
			Expect(args).To(ContainElement(
				fmt.Sprintf("--%s=%s/watchdog", agent.FlagWatchdogPath, BlockModeHostDevMountPath)))
		})

		It("should use volumeMounts for filesystem mode DaemonSet", func() {
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "fs-ds-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass: validSharedStorageClass,
				},
			}

			By("verifying volumeDevices is nil for filesystem mode")
			devices := blockReconciler.buildVolumeDevices(sbrConfig)
			Expect(devices).To(BeNil())

			By("verifying volumeMounts contains shared-storage")
			mounts := blockReconciler.buildVolumeMounts(sbrConfig)
			found := false
			for _, m := range mounts {
				if m.Name == "shared-storage" {
					found = true
					Expect(m.MountPath).To(Equal(agent.SharedStorageSBRDeviceDirectory))
				}
			}
			Expect(found).To(BeTrue(), "shared-storage mount not found in filesystem mode")
		})

		It("should build block init job with sbr-agent --init", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-init-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}

			job := blockReconciler.buildBlockInitJob(sbrConfig, "test-init-job", "test-pvc")

			By("verifying the init job uses the agent image")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal(testAgentImage))

			By("verifying --init flag is set")
			expectedInit := fmt.Sprintf("--%s=true", agent.FlagInit)
			Expect(container.Args).To(ContainElement(expectedInit))

			By("verifying --sbr-device points to block device path")
			expectedDevice := fmt.Sprintf("--%s=%s", agent.FlagSBRDevice, agent.SharedStorageBlockDevicePath)
			Expect(container.Args).To(ContainElement(expectedDevice))

			By("verifying volumeDevices is used instead of volumeMounts")
			Expect(container.VolumeMounts).To(BeNil())
			Expect(container.VolumeDevices).To(HaveLen(1))
			Expect(container.VolumeDevices[0].DevicePath).To(Equal(agent.SharedStorageBlockDevicePath))
		})

		It("should validate RBD provisioner as block-compatible", func() {
			Expect(blockReconciler.isRWXBlockCompatibleProvisioner("rbd.csi.ceph.com")).To(BeTrue())
			Expect(blockReconciler.isRWXBlockCompatibleProvisioner("openshift-storage.rbd.csi.ceph.com")).To(BeTrue())
		})

		It("should reject NFS provisioner for block mode", func() {
			Expect(blockReconciler.isRWXBlockCompatibleProvisioner("nfs.csi.k8s.io")).To(BeFalse())
			Expect(blockReconciler.isRWXBlockCompatibleProvisioner("efs.csi.aws.com")).To(BeFalse())
		})

		It("should reject filesystem-only provisioner for block mode via validateStorageClass", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-validation-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      validSharedStorageClass, // CephFS — filesystem only
					SharedStorageVolumeMode: &blockMode,
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			err := blockReconciler.validateStorageClass(ctx, sbrConfig, logr.Discard())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not RWX block volumes"))
		})

		It("should accept RBD provisioner for block mode via validateStorageClass", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-rbd-validation", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass, // RBD — block capable
					SharedStorageVolumeMode: &blockMode,
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			err := blockReconciler.validateStorageClass(ctx, sbrConfig, logr.Discard())
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set volumeMode Block on PVC for block mode", func() {
			blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "block-pvc-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass:      blockStorageClass,
					SharedStorageVolumeMode: &blockMode,
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			_, err := blockReconciler.ensurePVC(ctx, sbrConfig, logr.Discard())
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: blockNamespace}, pvc)).To(Succeed())
			Expect(pvc.Spec.VolumeMode).NotTo(BeNil())
			Expect(*pvc.Spec.VolumeMode).To(Equal(corev1.PersistentVolumeBlock))
		})

		It("should not set volumeMode on PVC for filesystem mode", func() {
			sbrConfig := &medik8sv1alpha1.StorageBasedRemediationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "fs-pvc-test", Namespace: blockNamespace},
				Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
					SharedStorageClass: validSharedStorageClass,
				},
			}
			Expect(k8sClient.Create(ctx, sbrConfig)).To(Succeed())

			_, err := blockReconciler.ensurePVC(ctx, sbrConfig, logr.Discard())
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			pvcName := sbrConfig.Spec.GetSharedStoragePVCName(sbrConfig.Name)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: blockNamespace}, pvc)).To(Succeed())
			// API server defaults VolumeMode to Filesystem when not explicitly set
			Expect(pvc.Spec.VolumeMode).NotTo(BeNil())
			Expect(*pvc.Spec.VolumeMode).To(Equal(corev1.PersistentVolumeFilesystem))
		})
	})
})
