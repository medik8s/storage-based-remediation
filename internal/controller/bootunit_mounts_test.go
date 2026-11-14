/*
Copyright 2026.

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
	"strings"
	"testing"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

// The boot unit is installed by the agent into host paths, so the DaemonSet
// must expose exactly those directories. These assertions are plain unit tests:
// they need no API server, and they fail loudly if a refactor drops a mount and
// silently turns the install into a no-op in production.
func TestDaemonSetExposesBootUnitHostPaths(t *testing.T) {
	r := &StorageBasedRemediationConfigReconciler{}
	cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{}

	volumes := r.buildVolumes(cfg)
	mounts := r.buildVolumeMounts(cfg)

	wantVolumes := map[string]string{
		"host-systemd":        "/etc/systemd/system",
		"host-usr-local":      "/usr/local",
		"host-etc-kubernetes": "/etc/kubernetes",
		"host-state":          bootunit.HostStateRoot,
	}
	for name, hostPath := range wantVolumes {
		found := false
		for _, v := range volumes {
			if v.Name != name {
				continue
			}
			found = true
			if v.HostPath == nil {
				t.Errorf("volume %q must be a hostPath volume", name)
			} else if v.HostPath.Path != hostPath {
				t.Errorf("volume %q hostPath = %q, want %q", name, v.HostPath.Path, hostPath)
			}
		}
		if !found {
			t.Errorf("volume %q is missing; the agent cannot install the boot unit without it", name)
		}
	}

	wantMounts := map[string]string{
		"host-systemd":        bootunit.ContainerSystemdDir,
		"host-usr-local":      bootunit.ContainerHostUsrLocal,
		"host-etc-kubernetes": "/host/etc/kubernetes",
		"host-state":          bootunit.HostStateRoot,
	}
	for name, mountPath := range wantMounts {
		found := false
		for _, m := range mounts {
			if m.Name != name {
				continue
			}
			found = true
			if m.MountPath != mountPath {
				t.Errorf("mount %q path = %q, want %q", name, m.MountPath, mountPath)
			}
		}
		if !found {
			t.Errorf("mount %q is missing", name)
		}
	}
}

// The source binary lives in the image at ContainerBinaryPath. Mounting the
// host's /usr/local at /usr/local would shadow it, and copyIfChanged would then
// copy a file onto itself — a silent no-op that looks like success.
func TestDaemonSetDoesNotShadowTheSourceBinary(t *testing.T) {
	r := &StorageBasedRemediationConfigReconciler{}
	cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{}

	for _, m := range r.buildVolumeMounts(cfg) {
		if strings.HasPrefix(bootunit.ContainerBinaryPath, m.MountPath+"/") ||
			bootunit.ContainerBinaryPath == m.MountPath {
			t.Fatalf("mount %q at %q shadows the source binary at %q",
				m.Name, m.MountPath, bootunit.ContainerBinaryPath)
		}
	}
}

// The block device is delivered as a volumeDevice at a fixed path; any hostPath
// mount over it would hide the device from the container.
func TestDaemonSetHostMountsDoNotShadowTheBlockDevice(t *testing.T) {
	blockMode := medik8sv1alpha1.SharedStorageVolumeModeBlock
	cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{
		Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
			SharedStorageVolumeMode: &blockMode,
		},
	}
	r := &StorageBasedRemediationConfigReconciler{}

	for _, m := range r.buildVolumeMounts(cfg) {
		if strings.HasPrefix(agent.SharedStorageBlockDevicePath, m.MountPath+"/") ||
			agent.SharedStorageBlockDevicePath == m.MountPath {
			t.Fatalf("mount %q at %q shadows the block device at %q",
				m.Name, m.MountPath, agent.SharedStorageBlockDevicePath)
		}
	}
}

// The state root must keep its real path inside the container: the boot unit,
// the journal library and the arming flag all refer to the same absolute path,
// so a side-path mount would give the agent and the boot unit two different
// views of the same state.
func TestStateRootKeepsItsRealPath(t *testing.T) {
	r := &StorageBasedRemediationConfigReconciler{}
	cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{}

	found := false
	for _, m := range r.buildVolumeMounts(cfg) {
		if m.Name != "host-state" {
			continue
		}
		found = true
		if m.MountPath != bootunit.HostStateRoot {
			t.Fatalf("state root mounted at %q, want %q", m.MountPath, bootunit.HostStateRoot)
		}
		if m.ReadOnly {
			t.Fatal("the state root must be writable: the agent writes the journal there")
		}
	}
	if !found {
		t.Fatal("the state root is not mounted at all")
	}
}
