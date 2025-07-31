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

package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2" // nolint:revive,staticcheck
)

const (
	prometheusOperatorVersion = "v0.77.1"
	prometheusOperatorURL     = "https://github.com/prometheus-operator/prometheus-operator/" +
		"releases/download/%s/bundle.yaml"

	certmanagerVersion = "v1.16.3"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) (string, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %q\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %q\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%q failed with error %q: %w", command, string(output), err)
	}

	return string(output), nil
}

// InstallPrometheusOperator installs the prometheus Operator to be used to export the enabled metrics.
func InstallPrometheusOperator() error {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "create", "-f", url)
	_, err := Run(cmd)
	return err
}

// UninstallPrometheusOperator uninstalls the prometheus
func UninstallPrometheusOperator() {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// IsPrometheusCRDsInstalled checks if any Prometheus CRDs are installed
// by verifying the existence of key CRDs related to Prometheus.
func IsPrometheusCRDsInstalled() bool {
	// List of common Prometheus CRDs
	prometheusCRDs := []string{
		"prometheuses.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
		"prometheusagents.monitoring.coreos.com",
	}

	cmd := exec.Command("kubectl", "get", "crds", "-o", "custom-columns=NAME:.metadata.name")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	crdList := GetNonEmptyLines(output)
	for _, crd := range prometheusCRDs {
		for _, line := range crdList {
			if strings.Contains(line, crd) {
				return true
			}
		}
	}

	return false
}

// UninstallCertManager uninstalls the cert manager
func UninstallCertManager() {
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
		cmd := exec.Command("kubectl", "delete", "-f", url)
		if _, err := Run(cmd); err != nil {
			warnError(err)
		}
	}
}

// InstallCertManager installs the cert manager bundle.
func InstallCertManager() error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.Command("kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)

	_, err := Run(cmd)
	return err
}

// IsCertManagerCRDsInstalled checks if any Cert Manager CRDs are installed
// by verifying the existence of key CRDs related to Cert Manager.
func IsCertManagerCRDsInstalled() bool {
	// List of common Cert Manager CRDs
	certManagerCRDs := []string{
		"certificates.cert-manager.io",
		"issuers.cert-manager.io",
		"clusterissuers.cert-manager.io",
		"certificaterequests.cert-manager.io",
		"orders.acme.cert-manager.io",
		"challenges.acme.cert-manager.io",
	}

	// Execute the kubectl command to get all CRDs
	cmd := exec.Command("kubectl", "get", "crds")
	output, err := Run(cmd)
	if err != nil {
		return false
	}

	// Check if any of the Cert Manager CRDs are present
	crdList := GetNonEmptyLines(output)
	for _, crd := range certManagerCRDs {
		for _, line := range crdList {
			if strings.Contains(line, crd) {
				return true
			}
		}
	}

	return false
}

// LoadImageToKindClusterWithName loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

// LoadImageToCRCCluster loads a local docker image to the CRC OpenShift cluster
func LoadImageToCRCCluster(name string) error {
	// For CRC, we need to build and push the image to the internal registry
	// or load it directly to CRC's docker daemon

	// First, check if CRC is running
	cmd := exec.Command("crc", "status")
	output, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to check CRC status: %w", err)
	}

	if !strings.Contains(output, "Running") {
		return fmt.Errorf("CRC is not running. Please start CRC first")
	}

	// Set up CRC environment
	cmd = exec.Command("bash", "-c", "eval $(crc oc-env) && oc whoami")
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("failed to set up CRC environment: %w", err)
	}

	// For e2e tests, we have several options:
	// 1. Use podman to load the image directly (CRC uses podman internally)
	// 2. Push to CRC's internal registry
	// 3. Use docker save/load with CRC's docker daemon

	// Option 1: Try to use podman directly
	if err := loadImageToCRCPodman(name); err == nil {
		return nil
	}

	// Option 2: Fallback to docker context switching
	return loadImageToCRCDocker(name)
}

// loadImageToCRCPodman attempts to load image using podman
func loadImageToCRCPodman(name string) error {
	// Save the image using docker to a temporary file
	tempFile := "/tmp/crc-image-" + extractImageName(name) + ".tar"
	cmd := exec.Command("docker", "save", "-o", tempFile, name)
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("failed to save image with docker: %w", err)
	}
	defer func() { _ = os.Remove(tempFile) }()

	// Load the image using CRC's podman with proper environment
	cmd = exec.Command("bash", "-c", fmt.Sprintf("eval $(crc podman-env) && podman load -i %s", tempFile))
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("failed to load image with CRC podman: %w", err)
	}

	return nil
}

// loadImageToCRCDocker attempts to load image using docker with CRC context
func loadImageToCRCDocker(name string) error {
	// Get CRC machine IP for docker context
	cmd := exec.Command("crc", "ip")
	crcIP, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to get CRC IP: %w", err)
	}
	crcIP = strings.TrimSpace(crcIP)

	// Try to use docker with CRC's docker daemon
	dockerHost := fmt.Sprintf("tcp://%s:2376", crcIP)

	// Save image locally first
	tempFile := "/tmp/crc-image-" + extractImageName(name) + ".tar"
	cmd = exec.Command("docker", "save", "-o", tempFile, name)
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("failed to save image to file: %w", err)
	}
	defer func() { _ = os.Remove(tempFile) }()

	// Load image to CRC's docker daemon
	cmd = exec.Command("docker", "load", "-i", tempFile)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	if _, err := Run(cmd); err != nil {
		// If this fails, the image might already be available
		// or we're in a different setup. Log and continue.
		fmt.Printf("Warning: failed to load image to CRC docker daemon: %v\n", err)
	}

	return nil
}

// extractImageName extracts the image name from a full image tag
func extractImageName(fullImage string) string {
	parts := strings.Split(fullImage, "/")
	imagePart := parts[len(parts)-1]
	return strings.Split(imagePart, ":")[0]
}

// LoadImageToCluster loads an image to the appropriate cluster (Kind or CRC)
func LoadImageToCluster(name string) error {
	// Check if we're running in CRC mode (environment variable or detection)
	if os.Getenv("USE_CRC") == "true" || IsCRCEnvironment() {
		return LoadImageToCRCCluster(name)
	}
	return LoadImageToKindClusterWithName(name)
}

// IsCRCEnvironment detects if we're in a CRC environment
func IsCRCEnvironment() bool {
	// Check if crc command is available and running
	cmd := exec.Command("crc", "status")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	return strings.Contains(output, "Running")
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, fmt.Errorf("failed to get current working directory: %w", err)
	}
	wd = strings.ReplaceAll(wd, "/test/e2e", "")
	return wd, nil
}

// UncommentCode searches for target in the file and remove the comment prefix
// of the target content. The target content may span multiple lines.
func UncommentCode(filename, target, prefix string) error {
	// false positive
	// nolint:gosec
	content, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file %q: %w", filename, err)
	}
	strContent := string(content)

	idx := strings.Index(strContent, target)
	if idx < 0 {
		return fmt.Errorf("unable to find the code %q to be uncomment", target)
	}

	out := new(bytes.Buffer)
	_, err = out.Write(content[:idx])
	if err != nil {
		return fmt.Errorf("failed to write to output: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(target))
	if !scanner.Scan() {
		return nil
	}
	for {
		if _, err = out.WriteString(strings.TrimPrefix(scanner.Text(), prefix)); err != nil {
			return fmt.Errorf("failed to write to output: %w", err)
		}
		// Avoid writing a newline in case the previous line was the last in target.
		if !scanner.Scan() {
			break
		}
		if _, err = out.WriteString("\n"); err != nil {
			return fmt.Errorf("failed to write to output: %w", err)
		}
	}

	if _, err = out.Write(content[idx+len(target):]); err != nil {
		return fmt.Errorf("failed to write to output: %w", err)
	}

	// false positive
	// nolint:gosec
	if err = os.WriteFile(filename, out.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file %q: %w", filename, err)
	}

	return nil
}

// getProjectImage returns the project image name based on environment variables.
// It uses the same pattern as the Makefile QUAY_* variables, with sensible defaults for local testing.
func GetProjectImage() string {
	// Allow complete override via OPERATOR_IMG environment variable
	if testImg := os.Getenv("OPERATOR_IMG"); testImg != "" {
		return testImg
	}

	registry := os.Getenv("QUAY_REGISTRY")
	if registry == "" {
		registry = "localhost:5000" // Local registry for testing
	}

	org := os.Getenv("QUAY_ORG")
	if org == "" {
		org = "sbd-operator"
	}

	version := os.Getenv("TAG")
	if version == "" {
		version = "smoke-test"
	}

	return fmt.Sprintf("%s/%s/sbd-operator:%s", registry, org, version)
}

// getAgentImage returns the agent image name based on environment variables.
// It uses the same pattern as the Makefile QUAY_* variables, with sensible defaults for local testing.
func GetAgentImage() string {
	// Allow complete override via AGENT_IMG environment variable
	if agentImg := os.Getenv("AGENT_IMG"); agentImg != "" {
		return agentImg
	}

	registry := os.Getenv("QUAY_REGISTRY")
	if registry == "" {
		registry = "localhost:5000" // Local registry for testing
	}

	org := os.Getenv("QUAY_ORG")
	if org == "" {
		org = "sbd-operator"
	}

	version := os.Getenv("TAG")
	if version == "" {
		version = "smoke-test"
	}

	return fmt.Sprintf("%s/%s/sbd-agent:%s", registry, org, version)
}
