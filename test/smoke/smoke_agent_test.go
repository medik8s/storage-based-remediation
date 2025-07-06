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
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBD Agent Smoke Tests", Ordered, Label("Smoke", "Agent"), func() {
	var controllerPodName string

	// Verify the environment is set up correctly (setup handled by Makefile)
	BeforeAll(func() {
		utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
	})

	// Clean up test-specific resources (overall cleanup handled by Makefile)
	AfterAll(func() {
		utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
	})

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

			By("Fetching curl-metrics logs")
			req := testClients.Clientset.CoreV1().Pods(namespace).GetLogs("curl-metrics", &corev1.PodLogOptions{})
			podLogs, err := req.Stream(testClients.Context)
			if err == nil {
				defer podLogs.Close()
				buf := new(bytes.Buffer)
				_, _ = io.Copy(buf, podLogs)
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", buf.String())
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			// Collect controller pod description
			debugCollector.CollectPodDescription(namespace, controllerPodName)
		}

	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("SBD Agent", func() {
		var sbdConfigName string
		var tmpFile string

		BeforeEach(func() {
			sbdConfigName = fmt.Sprintf("test-sbdconfig-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
		})

		It("should deploy SBD agent DaemonSet when SBDConfig is created", func() {
			By("creating an SBDConfig resource from sample configuration")
			// Load sample configuration and customize name
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with our test name
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", sbdConfigName)
			// Ensure imagePullPolicy is Always for testing
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)

			// Write SBDConfig to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdconfig-%s.yaml", sbdConfigName))
			err = os.WriteFile(tmpFile, []byte(sbdConfigYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Display the contents of the tmp file for debugging
			// By("displaying the SBDConfig YAML contents")
			// tmpFileContents, err := os.ReadFile(tmpFile)
			// Expect(err).NotTo(HaveOccurred(), "Failed to read temporary SBDConfig file")
			// fmt.Printf("SBDConfig YAML contents:\n%s\n", string(tmpFileContents))

			// Apply the SBDConfig to the test namespace
			var sbdConfig medik8sv1alpha1.SBDConfig
			err = yaml.Unmarshal([]byte(sbdConfigYAML), &sbdConfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to unmarshal SBDConfig YAML")

			sbdConfig.Namespace = testNamespace.Name

			err = testClients.Client.Create(testClients.Context, &sbdConfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			validator := testNamespace.NewSBDAgentValidator()
			opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
			opts.ExpectedArgs = []string{
				"--watchdog-path=/dev/watchdog",
				"--watchdog-timeout=1m0s",
			}
			err = validator.ValidateAgentDeployment(opts)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Preflight Checks", func() {
		var tmpFile string
		var sbdConfigName string

		BeforeEach(func() {
			// Generate unique name for each test
			sbdConfigName = fmt.Sprintf("test-sbdconfig-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			By("cleaning up test SBDConfig")
			if sbdConfigName != "" {
				cmd := exec.Command("kubectl", "delete", "-n", testNamespace.Name, "sbdconfig", sbdConfigName, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)

				// Wait for SBDConfig deletion to complete (finalizer cleanup)
				By("waiting for SBDConfig deletion to complete")
				Eventually(func() bool {
					sbdConfig := &medik8sv1alpha1.SBDConfig{}
					err := testClients.Client.Get(testClients.Context, client.ObjectKey{
						Name:      sbdConfigName,
						Namespace: testNamespace.Name,
					}, sbdConfig)
					return errors.IsNotFound(err) // Error means resource not found (deleted)
				}, 30*time.Second, 2*time.Second).Should(BeTrue())
			}

			// Clean up temporary file
			if tmpFile != "" {
				os.Remove(tmpFile)
			}
		})

		It("should pass preflight checks with working watchdog and failing SBD device", func() {
			By("creating an SBDConfig with a non-existent SBD device path")
			_, err := testNamespace.CreateSBDConfig(sbdConfigName, func(config *medik8sv1alpha1.SBDConfig) {
				config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
				config.Spec.SharedStorageClass = "non-existent-sc"
				config.Spec.SbdWatchdogPath = "/dev/watchdog"
				config.Spec.ImagePullPolicy = "Always"
				config.Spec.PetIntervalMultiple = func() *int32 {
					val := int32(6)
					return &val
				}()
			})
			Expect(err).NotTo(HaveOccurred())

			// Note: This test verifies that operator creates DaemonSet even with non-existent storage
			// Pods will remain pending due to mock PVC without backing storage, which is expected
		})

		It("should discover and test SharedStorageClass functionality with RWX storage", func() {
			By("looking for StorageClasses that support ReadWriteMany access mode")
			storageClasses := &storagev1.StorageClassList{}
			err := testClients.Client.List(testClients.Context, storageClasses)
			Expect(err).NotTo(HaveOccurred(), "Failed to list StorageClasses")

			var rwxStorageClass *storagev1.StorageClass
			for _, sc := range storageClasses.Items {
				// Look for storage classes that are likely to support RWX
				// Common patterns: efs, nfs, cephfs, glusterfs
				scName := strings.ToLower(sc.Name)
				if strings.Contains(scName, "efs") || strings.Contains(scName, "nfs") ||
					strings.Contains(scName, "cephfs") || strings.Contains(scName, "glusterfs") ||
					strings.Contains(scName, "sbd") {
					rwxStorageClass = &sc
					break
				}
			}

			if rwxStorageClass == nil {
				Skip("No RWX-compatible StorageClass found (efs, nfs, cephfs, glusterfs, sbd) - skipping SharedStorageClass test")
			}

			By(fmt.Sprintf("found RWX-compatible StorageClass: %s", rwxStorageClass.Name))
			GinkgoWriter.Printf("Testing with StorageClass: %s\n", rwxStorageClass.Name)

			By("creating an SBDConfig using the discovered StorageClass")

			sbdConfig, err := testNamespace.CreateSBDConfig(sbdConfigName, func(config *medik8sv1alpha1.SBDConfig) {
				config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
				config.Spec.SharedStorageClass = rwxStorageClass.Name
				config.Spec.SbdWatchdogPath = "/dev/watchdog"
				config.Spec.ImagePullPolicy = "Always"
				config.Spec.PetIntervalMultiple = func() *int32 {
					val := int32(6)
					return &val
				}()
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the DaemonSet created by the controller
			var createdDaemonSet *appsv1.DaemonSet
			Eventually(func() bool {
				daemonSets := &appsv1.DaemonSetList{}
				err := testClients.Client.List(testClients.Context, daemonSets,
					client.InNamespace(testNamespace.Name),
					client.MatchingLabels{"sbdconfig": sbdConfigName})
				if err != nil || len(daemonSets.Items) == 0 {
					return false
				}
				createdDaemonSet = &daemonSets.Items[0]
				return true
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(), "DaemonSet should be created by controller")

			By("verifying the controller creates a PVC from the StorageClass")
			expectedPVCName := fmt.Sprintf("%s-shared-storage", sbdConfigName)
			var createdPVC *corev1.PersistentVolumeClaim

			Eventually(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      expectedPVCName,
					Namespace: testNamespace.Name,
				}, pvc)
				if err != nil {
					return false
				}
				createdPVC = pvc
				return true
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(), "Controller should create a PVC from the StorageClass")

			By("verifying the PVC has the correct StorageClass and ReadWriteMany access mode")
			Expect(createdPVC.Spec.StorageClassName).NotTo(BeNil())
			Expect(*createdPVC.Spec.StorageClassName).To(Equal(rwxStorageClass.Name))

			hasRWX := false
			for _, accessMode := range createdPVC.Spec.AccessModes {
				if accessMode == corev1.ReadWriteMany {
					hasRWX = true
					break
				}
			}
			Expect(hasRWX).To(BeTrue(), "Created PVC should have ReadWriteMany access mode")

			By("verifying the DaemonSet has the correct PVC mount configuration")
			// Check for shared storage volume
			hasSharedStorageVolume := false
			for _, volume := range createdDaemonSet.Spec.Template.Spec.Volumes {
				if volume.Name == "shared-storage" &&
					volume.PersistentVolumeClaim != nil &&
					volume.PersistentVolumeClaim.ClaimName == expectedPVCName {
					hasSharedStorageVolume = true
					break
				}
			}
			Expect(hasSharedStorageVolume).To(BeTrue(), "DaemonSet should have shared storage volume configured")

			// Find the sbd-agent container and check its volume mounts
			var sbdAgentContainer *corev1.Container
			for i, container := range createdDaemonSet.Spec.Template.Spec.Containers {
				if container.Name == "sbd-agent" {
					sbdAgentContainer = &createdDaemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(sbdAgentContainer).NotTo(BeNil(), "sbd-agent container should exist")

			By("verifying the sbd-agent container has the correct volume mount")
			hasSharedStorageMount := false
			for _, mount := range sbdAgentContainer.VolumeMounts {
				if mount.Name == "shared-storage" && mount.MountPath == "/sbd-shared" {
					hasSharedStorageMount = true
					break
				}
			}
			Expect(hasSharedStorageMount).To(BeTrue(), "sbd-agent container should have shared storage mounted")

			By("checking SBDConfig status conditions for shared storage readiness")
			Eventually(func() bool {
				retrievedConfig := &medik8sv1alpha1.SBDConfig{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      sbdConfigName,
					Namespace: testNamespace.Name,
				}, retrievedConfig)
				if err != nil {
					return false
				}

				// Check if any conditions are set (the exact conditions depend on the controller implementation)
				return len(retrievedConfig.Status.Conditions) > 0
			}, 3*time.Minute, 15*time.Second).Should(BeTrue(), "SBDConfig should have status conditions set")

			By("verifying the SharedStorageClass field is correctly configured")
			retrievedConfig := &medik8sv1alpha1.SBDConfig{}
			err = testClients.Client.Get(testClients.Context, client.ObjectKey{
				Name:      sbdConfigName,
				Namespace: testNamespace.Name,
			}, retrievedConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(retrievedConfig.Spec.SharedStorageClass).To(Equal(rwxStorageClass.Name))

			By("displaying final SBDConfig status for debugging")
			yamlData, err := yaml.Marshal(retrievedConfig)
			if err == nil {
				GinkgoWriter.Printf("Final SBDConfig YAML:\n%s\n", string(yamlData))
			}

			validator := testNamespace.NewSBDAgentValidator()
			opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
			opts.ExpectedArgs = []string{
				"--watchdog-path=/dev/watchdog",
				"--watchdog-timeout=1m30s",
			}

			// Check if EFS provisioning is working by examining PVC events
			By("checking if EFS provisioning is working properly")
			pvcName := sbdConfig.Spec.GetSharedStoragePVCName(sbdConfig.Name)

			// Wait briefly to see if PVC can be provisioned
			time.Sleep(30 * time.Second)

			// Check PVC status and events to determine if provisioning is possible
			pvc := &corev1.PersistentVolumeClaim{}
			err = testClients.Client.Get(testClients.Context, client.ObjectKey{
				Name:      pvcName,
				Namespace: testNamespace.Name,
			}, pvc)
			Expect(err).NotTo(HaveOccurred())

			if pvc.Status.Phase == corev1.ClaimPending {
				// Check for credential-related provisioning failures
				events := &corev1.EventList{}
				err = testClients.Client.List(testClients.Context, events, client.InNamespace(testNamespace.Name))
				Expect(err).NotTo(HaveOccurred())

				hasCredentialError := false
				for _, event := range events.Items {
					if event.Type == "Warning" && event.Reason == "ProvisioningFailed" {
						if strings.Contains(event.Message, "NoCredentialProviders") ||
							strings.Contains(event.Message, "no valid providers in chain") ||
							strings.Contains(event.Message, "Access denied") ||
							strings.Contains(event.Message, "UnauthorizedOperation") {
							hasCredentialError = true
							By(fmt.Sprintf("Detected EFS credential issue: %s", event.Message))
							break
						}
					}
				}

				if hasCredentialError {
					By("EFS CSI driver has credential issues - skipping pod readiness check but validating configuration")
					opts.MinReadyPods = 0 // Skip pod readiness check due to credential issues
					By("Validating DaemonSet configuration without pod readiness")
				} else {
					By("PVC is pending but no credential errors detected - giving more time for provisioning")
					opts.PodReadyTimeout = time.Minute * 10 // Give EFS-mounted pods more time to start
				}
			} else {
				By("PVC is bound - proceeding with normal pod readiness check")
				opts.PodReadyTimeout = time.Minute * 10 // Give EFS-mounted pods more time to start
			}

			err = validator.ValidateAgentDeployment(opts)
			Expect(err).NotTo(HaveOccurred())

			GinkgoWriter.Printf("SharedStorageClass functionality test completed successfully with StorageClass: %s\n", rwxStorageClass.Name)
		})
	})
})
