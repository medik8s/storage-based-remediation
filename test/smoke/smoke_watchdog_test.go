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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBD Watchdog Smoke Tests", Ordered, Label("Smoke", "Watchdog"), func() {
	BeforeAll(func() {
		By("initializing Kubernetes clients for watchdog tests if needed")
		if testClients == nil {
			var err error
			testClients, err = utils.SetupKubernetesClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
		}

		By("creating watchdog smoke test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace.Name,
			},
		}
		err := testClients.Client.Create(testClients.Context, ns)
		if err != nil && !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred(), "Failed to create watchdog test namespace")
		}

		// Create test namespace wrapper for utilities
		testNamespace = &utils.TestNamespace{
			Name:    testNamespace.Name,
			Clients: testClients,
		}
		utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
	})

	AfterAll(func() {
		utils.CleanupSBDConfigs(testClients.Client, *testNamespace, testClients.Context)
	})

	Context("Watchdog Compatibility and Stability", func() {
		var sbdConfigName string

		BeforeEach(func() {
			sbdConfigName = fmt.Sprintf("test-sbdconfig-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			By("cleaning up SBD configuration and waiting for agents to terminate")

			// Clean up all SBDConfigs in the test namespace
			sbdConfigs := &medik8sv1alpha1.SBDConfigList{}
			err := testClients.Client.List(testClients.Context, sbdConfigs, client.InNamespace(testNamespace.Name))
			if err == nil {
				for _, config := range sbdConfigs.Items {
					err := testNamespace.CleanupSBDConfig(&config)
					if err != nil {
						GinkgoWriter.Printf("Warning: failed to cleanup SBDConfig %s: %v\n", config.Name, err)
					}
				}
			}
		})

		It("should successfully deploy SBD agents without causing node instability", func() {

			// Create a minimal SBD configuration for watchdog testing using utility
			sbdConfig, err := testNamespace.CreateSBDConfig(sbdConfigName, func(config *medik8sv1alpha1.SBDConfig) {
				config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second} // Longer timeout for safety
				config.Spec.PetIntervalMultiple = func() *int32 {
					val := int32(6) // Conservative 15-second pet interval
					return &val
				}()
				// Note: The agent now always runs with testMode=false for production behavior
			})
			Expect(err).NotTo(HaveOccurred())

			By("validating that SBD agents deploy without causing node reboots")
			validator := testNamespace.NewSBDAgentValidator()
			opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
			opts.ExpectedArgs = []string{
				"--watchdog-path=/dev/watchdog",
				"--watchdog-timeout=1m30s",
			}
			opts.NodeStableTime = time.Minute * 5 // Monitor for longer period
			err = validator.ValidateNoNodeReboots(opts)
			Expect(err).NotTo(HaveOccurred())

			By("retrieving and displaying SBDConfig YAML")
			retrievedConfig := &medik8sv1alpha1.SBDConfig{}
			Eventually(func() error {
				return testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      sbdConfig.Name,
					Namespace: testNamespace.Name,
				}, retrievedConfig)
			}, time.Minute*1, time.Second*5).Should(Succeed())

			// Display the configuration for verification
			yamlData, yamlErr := yaml.Marshal(retrievedConfig.Spec)
			Expect(yamlErr).NotTo(HaveOccurred())
			GinkgoWriter.Printf("SBDConfig YAML:\n%s\n", string(yamlData))
		})
	})
})
