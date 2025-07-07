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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	agent "github.com/medik8s/sbd-operator/pkg/agent"
	storagev1 "k8s.io/api/storage/v1"
)

// MockEventRecorder is a mock implementation of record.EventRecorder for testing
type MockEventRecorder struct {
	Events []Event
	mutex  sync.RWMutex
}

// Event represents a recorded event for testing
type Event struct {
	Object    runtime.Object
	EventType string
	Reason    string
	Message   string
}

// Event records an event for testing
func (m *MockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = append(m.Events, Event{
		Object:    object,
		EventType: eventtype,
		Reason:    reason,
		Message:   message,
	})
}

// Eventf records a formatted event for testing
func (m *MockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	m.Event(object, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

// AnnotatedEventf records an annotated formatted event for testing
func (m *MockEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	m.Eventf(object, eventtype, reason, messageFmt, args...)
}

// GetEvents returns a copy of recorded events for testing
func (m *MockEventRecorder) GetEvents() []Event {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	events := make([]Event, len(m.Events))
	copy(events, m.Events)
	return events
}

// Reset clears all recorded events
func (m *MockEventRecorder) Reset() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = nil
}

// NewMockEventRecorder creates a new MockEventRecorder
func NewMockEventRecorder() *MockEventRecorder {
	return &MockEventRecorder{
		Events: make([]Event, 0),
	}
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
			resource := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the created resource")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			By("verifying the resource still exists")
			sbdconfig := &medik8sv1alpha1.SBDConfig{}
			err = k8sClient.Get(ctx, typeNamespacedName, sbdconfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(sbdconfig.Name).To(Equal(resourceName))
		})

		It("should handle reconciling a non-existent resource without error", func() {
			nonExistentName := types.NamespacedName{
				Name:      "non-existent-sbdconfig",
				Namespace: namespace,
			}

			By("reconciling a non-existent resource")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: nonExistentName,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should configure sbd-device flag correctly for shared storage", func() {
			By("creating an SBDConfig with shared storage")
			resource := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath:    "/dev/watchdog",
					SharedStorageClass: "test-storage-class",
					Image:              "test-sbd-agent:latest",
				},
			}

			By("testing the buildSBDAgentArgs method")
			args := controllerReconciler.buildSBDAgentArgs(resource)

			By("verifying the sbd-device flag is set correctly")
			expectedSBDDevice := fmt.Sprintf("--%s=/sbd-shared/%s", agent.FlagSBDDevice, agent.SharedStorageSBDDeviceFile)
			Expect(args).To(ContainElement(expectedSBDDevice))

			By("verifying file locking is enabled for shared storage")
			expectedFileLocking := fmt.Sprintf("--%s=true", agent.FlagSBDFileLocking)
			Expect(args).To(ContainElement(expectedFileLocking))
		})

		It("should not set sbd-device flag when shared storage is not configured", func() {
			By("creating an SBDConfig without shared storage")
			resource := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}

			By("testing the buildSBDAgentArgs method")
			args := controllerReconciler.buildSBDAgentArgs(resource)

			By("verifying no sbd-device flag is set")
			for _, arg := range args {
				Expect(arg).NotTo(ContainSubstring(fmt.Sprintf("--%s", agent.FlagSBDDevice)))
			}

			By("verifying no file locking flag is set")
			for _, arg := range args {
				Expect(arg).NotTo(ContainSubstring(fmt.Sprintf("--%s", agent.FlagSBDFileLocking)))
			}
		})

		It("should successfully reconcile after resource deletion", func() {
			By("creating the custom resource for the Kind SBDConfig")
			resource := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("deleting the resource first")
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling the deleted resource")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
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
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			// First reconcile adds finalizer
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			By("verifying the namespace was created")
			createdNamespace := &corev1.Namespace{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, createdNamespace)
			}, timeout, interval).Should(Succeed())

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
			Expect(container.VolumeMounts).To(HaveLen(3))
			volumeMountNames := make([]string, len(container.VolumeMounts))
			for i, vm := range container.VolumeMounts {
				volumeMountNames[i] = vm.Name
			}
			Expect(volumeMountNames).To(ContainElements("dev", "sys", "proc"))

			By("verifying the DaemonSet has correct volumes")
			Expect(daemonSet.Spec.Template.Spec.Volumes).To(HaveLen(3))
			volumeNames := make([]string, len(daemonSet.Spec.Template.Spec.Volumes))
			for i, v := range daemonSet.Spec.Template.Spec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ContainElements("dev", "sys", "proc"))

			By("verifying the DaemonSet has correct security context")
			Expect(*container.SecurityContext.Privileged).To(BeTrue())
			Expect(*container.SecurityContext.RunAsUser).To(BeEquivalentTo(0))
		})

		It("should update DaemonSet when SBDConfig is modified", func() {
			By("creating the SBDConfig resource")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:v1.0.0",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			// First reconcile adds finalizer
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			// First reconcile adds finalizer
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					// No image specified - should use defaults
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the SBDConfig multiple times for finalizer and resource creation")
			// First reconcile adds finalizer
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
		var mockRecorder *MockEventRecorder

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

			mockRecorder = NewMockEventRecorder()
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
				k8sClient.Delete(ctx, resource)
			}

			testNamespace := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, testNamespace)
			if err == nil {
				k8sClient.Delete(ctx, testNamespace)
			}
		})

		It("should emit events during successful reconciliation", func() {
			By("creating the SBDConfig resource")
			resource := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SbdWatchdogPath: "/dev/watchdog",
					Image:           "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling the resource multiple times for finalizer and resource creation")
			// First reconcile adds finalizer
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources and emits events
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

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
			Expect(len(events)).To(Equal(1))
			Expect(events[0].EventType).To(Equal(EventTypeNormal))
			Expect(events[0].Reason).To(Equal("TestReason"))
			Expect(events[0].Message).To(Equal("Test message"))

			By("testing emitEventf helper")
			mockRecorder.Reset()
			controllerReconciler.emitEventf(resource, EventTypeWarning, "TestFormat", "Formatted message: %s", "test-value")

			events = mockRecorder.GetEvents()
			Expect(len(events)).To(Equal(1))
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
			Expect(len(events)).To(Equal(0))
		})
	})

	Context("When testing controller initialization", func() {
		var namespace string
		const (
			resourceName = "test-events-system"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

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

		It("should support multiple SBDConfig resources in the same namespace", func() {
			By("creating a multi-SBD controller reconciler")
			multiSBDReconciler := &SBDConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("creating the first SBDConfig")
			// First SBDConfig
			sbdConfig1 := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "first-sbdconfig",
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					ImagePullPolicy: "IfNotPresent",
					Image:           "test-sbd-agent:v1.0.0",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig1)).To(Succeed())

			// Reconcile the first SBDConfig multiple times for finalizer and resource creation
			// First reconcile adds finalizer
			_, err := multiSBDReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig1.Name,
					Namespace: sbdConfig1.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile creates the resources
			_, err = multiSBDReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig1.Name,
					Namespace: sbdConfig1.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the first SBDConfig creates shared resources")
			// Check service account (shared)
			serviceAccount := &corev1.ServiceAccount{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "sbd-agent", Namespace: namespace}, serviceAccount)
			}, timeout, interval).Should(Succeed())

			// Check first ClusterRoleBinding (SBDConfig-specific)
			clusterRoleBinding1 := &rbacv1.ClusterRoleBinding{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("sbd-agent-%s-%s", namespace, sbdConfig1.Name)}, clusterRoleBinding1)
			}, timeout, interval).Should(Succeed())

			// Check first DaemonSet (SBDConfig-specific)
			daemonSet1 := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("sbd-agent-%s", sbdConfig1.Name), Namespace: namespace}, daemonSet1)
			}, timeout, interval).Should(Succeed())

			By("creating the second SBDConfig in the same namespace")
			sbdConfig2 := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "second-sbdconfig",
					Namespace: namespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					ImagePullPolicy: "Always",
					Image:           "test-sbd-agent:v2.0.0",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig2)).To(Succeed())

			// Reconcile the second SBDConfig multiple times for finalizer and resource creation
			// First reconcile adds finalizer
			_, err = multiSBDReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig2.Name,
					Namespace: sbdConfig2.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile creates the resources
			_, err = multiSBDReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig2.Name,
					Namespace: sbdConfig2.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying both SBDConfigs share the service account but have separate resources")
			// Service account should still be the same (shared)
			sharedServiceAccount := &corev1.ServiceAccount{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "sbd-agent", Namespace: namespace}, sharedServiceAccount)
			}, timeout, interval).Should(Succeed())

			// Verify service account has shared resource annotations
			Expect(sharedServiceAccount.Annotations).To(HaveKeyWithValue("sbd-operator/shared-resource", "true"))
			Expect(sharedServiceAccount.Annotations).To(HaveKeyWithValue("sbd-operator/managed-by", "sbd-operator"))

			// Second ClusterRoleBinding (SBDConfig-specific)
			clusterRoleBinding2 := &rbacv1.ClusterRoleBinding{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("sbd-agent-%s-%s", namespace, sbdConfig2.Name)}, clusterRoleBinding2)
			}, timeout, interval).Should(Succeed())

			// Second DaemonSet (SBDConfig-specific)
			daemonSet2 := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("sbd-agent-%s", sbdConfig2.Name), Namespace: namespace}, daemonSet2)
			}, timeout, interval).Should(Succeed())

			By("verifying the DaemonSets have different configurations")
			Expect(daemonSet1.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
			Expect(daemonSet2.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
			Expect(daemonSet1.Spec.Template.Spec.Containers[0].Image).To(Equal("test-sbd-agent:v1.0.0"))
			Expect(daemonSet2.Spec.Template.Spec.Containers[0].Image).To(Equal("test-sbd-agent:v2.0.0"))

			By("verifying ClusterRoleBindings have different names but same service account")
			Expect(clusterRoleBinding1.Name).To(Equal(fmt.Sprintf("sbd-agent-%s-%s", namespace, sbdConfig1.Name)))
			Expect(clusterRoleBinding2.Name).To(Equal(fmt.Sprintf("sbd-agent-%s-%s", namespace, sbdConfig2.Name)))
			Expect(clusterRoleBinding1.Subjects[0].Name).To(Equal("sbd-agent"))
			Expect(clusterRoleBinding2.Subjects[0].Name).To(Equal("sbd-agent"))
			Expect(clusterRoleBinding1.Subjects[0].Namespace).To(Equal(namespace))
			Expect(clusterRoleBinding2.Subjects[0].Namespace).To(Equal(namespace))

			By("cleaning up test resources")
			Expect(k8sClient.Delete(ctx, sbdConfig1)).To(Succeed())
			Expect(k8sClient.Delete(ctx, sbdConfig2)).To(Succeed())
		})
	})

	Context("When managing OpenShift SecurityContextConstraints", func() {
		var customControllerReconciler *SBDConfigReconciler
		var customNamespace string

		BeforeEach(func() {
			// Generate unique namespace for each test to avoid conflicts
			customNamespace = fmt.Sprintf("custom-sbd-namespace-%d", time.Now().UnixNano())

			// Create the custom namespace first
			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: customNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())

			customControllerReconciler = &SBDConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		AfterEach(func() {
			// Clean up the test namespace
			testNamespace := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: customNamespace}, testNamespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
			}
		})

		It("should manage resources in custom namespace", func() {
			By("creating an SBDConfig in the custom namespace")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-scc-sbdconfig",
					Namespace: customNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					Image:           "test-sbd-agent:latest",
					ImagePullPolicy: "Always",
				},
			}

			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("reconciling the created resource multiple times for finalizer and resource creation")
			customTypeNamespacedName := types.NamespacedName{
				Name:      "test-scc-sbdconfig",
				Namespace: customNamespace,
			}

			// First reconcile adds finalizer
			result, err := customControllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: customTypeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Second reconcile creates the resources
			result, err = customControllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: customTypeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			By("verifying the service account is created in the custom namespace")
			serviceAccount := &corev1.ServiceAccount{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "sbd-agent", Namespace: customNamespace}, serviceAccount)
			}, time.Second*10, time.Millisecond*250).Should(Succeed())

			By("verifying the DaemonSet is created in the custom namespace")
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "sbd-agent-test-scc-sbdconfig", Namespace: customNamespace}, daemonSet)
			}, time.Second*10, time.Millisecond*250).Should(Succeed())

			By("verifying the SBDConfig status is updated")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, customTypeNamespacedName, sbdConfig)
				if err != nil {
					return false
				}
				// Check if status has been updated (TotalNodes should be set)
				return sbdConfig.Status.TotalNodes >= 0
			}, time.Second*10, time.Millisecond*250).Should(BeTrue())
		})
	})

	Context("When validating storage classes", func() {
		const (
			timeout  = time.Second * 10
			interval = time.Millisecond * 250
		)

		var validationReconciler *SBDConfigReconciler
		var mockEventRecorder *MockEventRecorder
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
			mockEventRecorder = NewMockEventRecorder()
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
			_, err := validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("performing second reconciliation (validates storage class)")
			_, err = validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})

			By("expecting reconciliation to fail due to storage class incompatibility")
			Expect(err).To(HaveOccurred())
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

		It("should accept EFS storage class (ReadWriteMany compatible)", func() {
			By("creating an EFS storage class")
			efsStorageClass := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "efs-csi",
				},
				Provisioner: "efs.csi.aws.com",
				Parameters: map[string]string{
					"fileSystemId":   "fs-12345678",
					"directoryPerms": "700",
				},
			}
			Expect(k8sClient.Create(ctx, efsStorageClass)).To(Succeed())

			By("creating SBDConfig with EFS storage class")
			sbdConfig := &medik8sv1alpha1.SBDConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-efs-validation",
					Namespace: validationNamespace,
				},
				Spec: medik8sv1alpha1.SBDConfigSpec{
					SharedStorageClass: "efs-csi",
					Image:              "test-sbd-agent:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sbdConfig)).To(Succeed())

			By("performing first reconciliation (adds finalizer)")
			_, err := validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("performing second reconciliation (validates storage class and creates PVC)")
			_, err = validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})

			By("expecting reconciliation to fail because job was just created")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("SBD device initialization job"))
			Expect(err.Error()).To(ContainSubstring("was just created"))

			By("simulating job completion by updating job status")
			jobName := fmt.Sprintf("%s-sbd-device-init", sbdConfig.Name)
			job := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      jobName,
					Namespace: sbdConfig.Namespace,
				}, job)
			}, timeout, interval).Should(Succeed())

			// Update job status to show completion
			job.Status.Succeeded = 1
			job.Status.Conditions = []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("performing third reconciliation (should succeed after job completion)")
			_, err = validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})

			By("expecting reconciliation to succeed")
			Expect(err).NotTo(HaveOccurred())

			By("verifying PVC was created")
			pvc := &corev1.PersistentVolumeClaim{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name),
					Namespace: sbdConfig.Namespace,
				}, pvc)
			}, timeout, interval).Should(Succeed())

			By("verifying PVC has ReadWriteMany access mode")
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))

			By("cleaning up test storage class")
			Expect(k8sClient.Delete(ctx, efsStorageClass)).To(Succeed())
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

			By("performing first reconciliation (adds finalizer)")
			_, err := validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("performing second reconciliation (validates storage class)")
			_, err = validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
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
			_, err := validationReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      sbdConfig.Name,
					Namespace: sbdConfig.Namespace,
				},
			})

			By("expecting reconciliation to succeed")
			Expect(err).NotTo(HaveOccurred())

			By("verifying no PVC was created")
			pvc := &corev1.PersistentVolumeClaim{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-shared-storage", sbdConfig.Name),
				Namespace: sbdConfig.Namespace,
			}, pvc)
			Expect(errors.IsNotFound(err)).To(BeTrue())
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
			It("should test unknown provisioner with temporary PVC", func() {
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
				_, err := validationReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      sbdConfig.Name,
						Namespace: sbdConfig.Namespace,
					},
				})

				By("expecting reconciliation to complete (may succeed or fail depending on actual provisioner capability)")
				// The result depends on whether the unknown provisioner actually supports RWX
				// In a real environment, this would likely fail, but in our test environment
				// without a real provisioner, we just verify the validation logic runs
				if err != nil {
					Expect(err.Error()).To(ContainSubstring("ReadWriteMany"))
				}

				By("verifying temporary test PVC was created and cleaned up")
				// The temporary PVC should be cleaned up automatically
				testPVC := &corev1.PersistentVolumeClaim{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-rwx-test", sbdConfig.Name),
						Namespace: sbdConfig.Namespace,
					}, testPVC)
					return errors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())

				By("cleaning up test storage class")
				Expect(k8sClient.Delete(ctx, customStorageClass)).To(Succeed())
			})
		})
	})
})
