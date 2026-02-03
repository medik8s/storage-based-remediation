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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	agent "github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/mocks"
)

const (
	defaultReconcileCount     = 5
	defaultRequeueAfter       = 500000000
	validSharedStorageClass   = "test-ceph-sc"
	invalidSharedStorageClass = "invalid-storage-class"
)

func checkForDefaultReconcile(counter int, result reconcile.Result, err error) {
	Expect(err).NotTo(HaveOccurred())
	Expect(result).To(Equal(reconcile.Result{}))
	Expect(counter).To(BeNumerically("==", 6))
}

func defaultSBDConfig(resourceName, namespace string) *medik8sv1alpha1.SBDConfig {
	return &medik8sv1alpha1.SBDConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Spec: medik8sv1alpha1.SBDConfigSpec{
			SbdWatchdogPath:    "/dev/watchdog",
			SharedStorageClass: validSharedStorageClass,
			Image:              "test-sbd-agent:latest",
		},
	}
}
func runReconcile(
	ctx context.Context, controllerReconciler *SBDConfigReconciler, typeNamespacedName types.NamespacedName) (
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
	controllerReconciler *SBDConfigReconciler,
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

		By("Reconciling the SBDConfig again")
		partial := 0
		partial, result, err = runReconcile(ctx, controllerReconciler, typeNamespacedName)
		counter = counter + partial
	}
	return counter, result, err
}

var _ = Describe("SBDConfig Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-sbdconfig"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

		var namespace string

		ctx := context.Background()

		// typeNamespacedName will be set dynamically in tests since namespace is set in BeforeEach
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *SBDConfigReconciler

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			namespace = fmt.Sprintf("test-sbd-system-%d", time.Now().UnixNano())

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
			controllerReconciler = &SBDConfigReconciler{
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
			err = os.Setenv("OPERATOR_IMAGE", "test-sbd-agent:latest")
			Expect(err).NotTo(HaveOccurred())
			err = os.Setenv("POD_NAMESPACE", namespace)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			By("cleaning up the specific resource instance SBDConfig")
			resource := &medik8sv1alpha1.SBDConfig{}
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
			By("creating the custom resource for the Kind SBDConfig")
			resource := defaultSBDConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the created resource")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the resource still exists")
			sbdconfig := &medik8sv1alpha1.SBDConfig{}
			err = k8sClient.Get(ctx, typeNamespacedName, sbdconfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(sbdconfig.Name).To(Equal(resourceName))
		})

		It("should handle reconciling a non-existent resource", func() {
			nonExistentName := types.NamespacedName{
				Name:      "non-existent-sbdconfig",
				Namespace: namespace,
			}

			By("reconciling a non-existent resource")
			_, result, err := runReconcile(ctx, controllerReconciler, nonExistentName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should configure sbd-device flag correctly for shared storage", func() {
			By("creating an SBDConfig with shared storage")
			resource := defaultSBDConfig(resourceName, namespace)

			By("testing the buildSBDAgentArgs method")
			args := controllerReconciler.buildSBDAgentArgs(resource)

			By("verifying the sbd-device flag is set correctly")
			expectedSBDDevice := fmt.Sprintf("--%s=%s/%s",
				agent.FlagSBDDevice, agent.SharedStorageSBDDeviceDirectory, agent.SharedStorageSBDDeviceFile)
			Expect(args).To(ContainElement(expectedSBDDevice))

			By("verifying file locking is enabled for shared storage")
			expectedFileLocking := fmt.Sprintf("--%s=true", agent.FlagSBDFileLocking)
			Expect(args).To(ContainElement(expectedFileLocking))
		})

		It("should successfully reconcile after resource deletion", func() {
			By("creating the custom resource for the Kind SBDConfig")
			resource := defaultSBDConfig(resourceName, namespace)
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
			resourceName = "test-daemonset-sbdconfig"
			timeout      = time.Second * 30
			interval     = time.Millisecond * 250
		)

		var namespace string
		ctx := context.Background()

		// typeNamespacedName will be set dynamically in BeforeEach
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *SBDConfigReconciler

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
			controllerReconciler = &SBDConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		AfterEach(func() {
			By("cleaning up the specific resource instance SBDConfig")
			resource := &medik8sv1alpha1.SBDConfig{}
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

		It("should create a DaemonSet when SBDConfig is applied", func() {
			By("creating the SBDConfig resource")
			sbdConfig := defaultSBDConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", resourceName)
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
			Expect(daemonSet.Labels["app"]).To(Equal("sbd-agent"))
			Expect(daemonSet.Labels["sbdconfig"]).To(Equal(resourceName))
			Expect(daemonSet.Labels["managed-by"]).To(Equal("sbd-operator"))

			By("verifying the DaemonSet has the correct owner reference")
			Expect(daemonSet.OwnerReferences).To(HaveLen(1))
			Expect(daemonSet.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(daemonSet.OwnerReferences[0].Kind).To(Equal("SBDConfig"))
			Expect(*daemonSet.OwnerReferences[0].Controller).To(BeTrue())

			By("verifying the DaemonSet pod template has correct configuration")
			container := daemonSet.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("sbd-agent"))
			Expect(container.Image).To(Equal("test-sbd-agent:latest"))
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
		})

		It("should update DaemonSet when SBDConfig is modified", func() {
			By("creating the SBDConfig resource")
			sbdConfig := defaultSBDConfig(resourceName, namespace)
			sbdConfig.Spec.Image = "test-sbd-agent:v1.0.0"
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created with initial image")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
			}, timeout, interval).Should(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("test-sbd-agent:v1.0.0"))

			By("updating the SBDConfig image")
			// Fetch the latest version to avoid conflicts
			err = k8sClient.Get(ctx, typeNamespacedName, sbdConfig)
			Expect(err).NotTo(HaveOccurred())
			sbdConfig.Spec.Image = "test-sbd-agent:v2.0.0"
			sbdConfig.Spec.SbdWatchdogPath = "/dev/watchdog1"
			Expect(k8sClient.Update(ctx, sbdConfig)).To(Succeed())

			By("reconciling the updated SBDConfig")
			counter, result, err = runReconcile(ctx, controllerReconciler, typeNamespacedName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(counter).To(BeNumerically("==", 1))

			By("verifying the DaemonSet was updated with new image and watchdog path")
			Eventually(func() string {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace,
				}, daemonSet)
				if err != nil {
					return ""
				}
				return daemonSet.Spec.Template.Spec.Containers[0].Image
			}, timeout, interval).Should(Equal("test-sbd-agent:v2.0.0"))

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
			By("creating the SBDConfig resource")
			sbdConfig := defaultSBDConfig(resourceName, namespace)
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", resourceName)
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
			Expect(daemonSet.OwnerReferences[0].Kind).To(Equal("SBDConfig"))
			Expect(*daemonSet.OwnerReferences[0].Controller).To(BeTrue())

			By("deleting the SBDConfig")
			Expect(k8sClient.Delete(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig deletion to handle finalizer cleanup")
			counter, result, err = reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			Expect(counter).To(BeNumerically("==", 1))
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the SBDConfig is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, sbdConfig)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())

			// Note: In test environments, garbage collection may not run automatically
			// The important verification is that the owner reference was correctly set above
		})

		It("should handle default values correctly", func() {
			By("creating the SBDConfig resource with minimal spec")
			sbdConfig := defaultSBDConfig(resourceName, namespace)
			sbdConfig.Spec.Image = "" // no image specified - should use defaults
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			counter, result, err := reconcileWithJob(ctx, controllerReconciler, typeNamespacedName)
			checkForDefaultReconcile(counter, result, err)

			By("verifying the DaemonSet was created with default values")
			expectedDaemonSetName := fmt.Sprintf("sbd-agent-%s", resourceName)
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedDaemonSetName,
					Namespace: namespace, // deployed to same namespace as SBDConfig
				}, daemonSet)
			}, timeout, interval).Should(Succeed())

			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("sbd-agent:latest"))
			Expect(daemonSet.Namespace).To(Equal(namespace))
		})
	})

	Context("When testing event emission", func() {
		const (
			resourceName = "test-events-sbdconfig"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

		var namespace string
		ctx := context.Background()
		var typeNamespacedName types.NamespacedName

		var controllerReconciler *SBDConfigReconciler
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
			controllerReconciler = &SBDConfigReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: mockRecorder,
			}
		})

		AfterEach(func() {
			// Clean up resources
			resource := &medik8sv1alpha1.SBDConfig{}
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
			By("creating the SBDConfig resource")
			resource := defaultSBDConfig(resourceName, namespace)
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
				if event.Reason == ReasonSBDConfigReconciled && event.EventType == EventTypeNormal {
					reconcileEvent = true
					Expect(event.Message).To(ContainSubstring(resourceName))
					break
				}
			}
			Expect(reconcileEvent).To(BeTrue(), "SBDConfig reconciled event should be emitted")
		})

		It("should emit events for helper methods", func() {
			By("testing emitEvent helper")
			resource := &medik8sv1alpha1.SBDConfig{
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

			resource := &medik8sv1alpha1.SBDConfig{
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
			reconciler := &SBDConfigReconciler{
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

		var validationReconciler *SBDConfigReconciler
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
			validationReconciler = &SBDConfigReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: mockEventRecorder,
			}
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

			By("creating SBDConfig with gp3-csi storage class")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gp3-validation",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SharedStorageClass: "gp3-csi",
					Image:              "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("performing first reconciliation (adds finalizer)")
			counter, result, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
				Name:      sbdConfig.Name,
				Namespace: sbdConfig.Namespace,
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
			By("creating SBDConfig with non-existent storage class")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-missing-sc",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SharedStorageClass: "non-existent-storage-class",
					Image:              "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("performing reconciliation")
			_, _, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
				Name:      sbdConfig.Name,
				Namespace: sbdConfig.Namespace,
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
			By("creating SBDConfig without shared storage")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-shared-storage",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					Image: "test-sbd-agent:latest",
					// No SharedStorageClass specified
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig")
			counter, result, err := reconcileWithJob(ctx, validationReconciler, types.NamespacedName{
				Name:      sbdConfig.Name,
				Namespace: sbdConfig.Namespace,
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

				By("creating SBDConfig with unknown provisioner")
				sbdConfig := &medik8sv1alpha1.SBDConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-unknown-provisioner",
						Namespace: validationNamespace,
					},
					Spec: medik8sv1alpha1.SBDConfigSpec{
						SharedStorageClass: "custom-unknown-provisioner",
						Image:              "test-sbd-agent:latest",
					},
				}
				Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

				By("reconciling the SBDConfig")
				counter, result, err := runReconcile(ctx, validationReconciler, types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				})
				Expect(counter).To(BeNumerically("==", 4))
				Expect(result).To(Equal(reconcile.Result{}))

				By("expecting reconciliation to complete (may succeed or fail depending on actual provisioner capability)")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ReadWriteMany"))
			})
		})
	})
})
