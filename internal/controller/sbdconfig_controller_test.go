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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
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

			By("reconciling the SBDConfig")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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

			By("reconciling the SBDConfig")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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

			By("reconciling the SBDConfig")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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

			By("reconciling the SBDConfig")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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

			By("reconciling the resource")
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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
		It("should create a controller reconciler successfully", func() {
			reconciler := &SBDConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			Expect(reconciler.Client).NotTo(BeNil())
			Expect(reconciler.Scheme).NotTo(BeNil())
		})
	})
})
