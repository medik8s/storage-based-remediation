package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
	"github.com/medik8s/storage-based-remediation/v5/internal/bootunit"
)

type perNodeDaemonSetSettings struct {
	bootEnabled bool
	bootTimeout string
	recovery    string
	interval    string
	ioTimeout   string
	failures    string
}

func checkPerNodeDaemonSet(t *testing.T, daemon *appsv1.DaemonSet, configUID types.UID, want perNodeDaemonSetSettings) {
	t.Helper()
	if len(daemon.Spec.Template.Spec.Containers) != 1 || daemon.Spec.Template.Spec.Containers[0].Name != "sbr-agent" {
		t.Fatalf("agent containers = %+v", daemon.Spec.Template.Spec.Containers)
	}
	container := daemon.Spec.Template.Spec.Containers[0]
	args := map[string]string{}
	for _, arg := range container.Args {
		name, value, _ := strings.Cut(arg, "=")
		if _, exists := args[name]; exists {
			t.Errorf("duplicate agent argument %q", name)
		}
		args[name] = value
	}
	for name, value := range map[string]string{
		"--block-per-node":                              "true",
		"--" + agent.FlagJournalEnabled:                 "true",
		"--" + agent.FlagAutonomousRebootRecoveryPeriod: want.recovery,
	} {
		if args[name] != value {
			t.Errorf("agent argument %s = %q, want %q", name, args[name], value)
		}
	}
	for _, flag := range []string{agent.FlagIOTimeout, agent.FlagSBRUpdateInterval, agent.FlagMaxConsecutiveFailures, agent.FlagSBRDevice, agent.FlagSBRFileLocking} {
		if value, exists := args["--"+flag]; exists {
			t.Errorf("per-node agent received shared argument --%s=%s", flag, value)
		}
	}
	for name, value := range map[string]string{
		"--" + agent.FlagBootUnitEnabled: "true",
		"--" + agent.FlagBootUnitTimeout: want.bootTimeout,
	} {
		got, exists := args[name]
		if want.bootEnabled && (!exists || got != value) {
			t.Errorf("boot argument %s = %q, want %q", name, got, value)
		}
		if !want.bootEnabled && exists {
			t.Errorf("disabled boot unit received %s=%s", name, got)
		}
	}
	wantEnv := map[string]string{
		"SBR_CONFIGURATION_UID":       string(configUID),
		blockpernode.ProbeIntervalEnv: want.interval,
		blockpernode.IOTimeoutEnv:     want.ioTimeout,
		blockpernode.FailuresEnv:      want.failures,
	}
	for name, value := range wantEnv {
		count := 0
		for _, env := range container.Env {
			if env.Name == name {
				count++
				if env.ValueFrom != nil || env.Value != value {
					t.Errorf("environment %s = %+v, want literal %q", name, env, value)
				}
			}
		}
		if count != 1 {
			t.Errorf("environment %s entries = %d, want 1", name, count)
		}
	}
	type hostPathMount struct {
		hostPath  string
		mountPath string
		readOnly  bool
	}
	wantPaths := map[string]hostPathMount{
		"sys":        {"/sys", "/sys", true},
		"proc":       {"/proc", "/proc", true},
		"host-dev":   {"/custom/dev", BlockModeHostDevMountPath, false},
		"host-state": {bootunit.HostStateRoot, bootunit.HostStateRoot, false},
	}
	if want.bootEnabled {
		wantPaths["host-systemd"] = hostPathMount{"/etc/systemd/system", bootunit.ContainerSystemdDir, false}
		wantPaths["host-usr-local"] = hostPathMount{"/usr/local", bootunit.ContainerHostUsrLocal, false}
		wantPaths["host-etc-kubernetes"] = hostPathMount{"/etc/kubernetes", "/host/etc/kubernetes", true}
	}
	volumes := map[string]corev1.Volume{}
	for _, volume := range daemon.Spec.Template.Spec.Volumes {
		if _, exists := volumes[volume.Name]; exists {
			t.Errorf("duplicate volume %q", volume.Name)
		}
		volumes[volume.Name] = volume
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, mount := range container.VolumeMounts {
		if _, exists := mounts[mount.Name]; exists {
			t.Errorf("duplicate mount %q", mount.Name)
		}
		mounts[mount.Name] = mount
		for _, path := range []string{blockpernode.ProbeDevice, bootunit.ContainerBinaryPath} {
			if mount.MountPath == path || strings.HasPrefix(path, mount.MountPath+"/") {
				t.Errorf("host mount %q at %q hides %q", mount.Name, mount.MountPath, path)
			}
		}
	}
	if len(volumes) != len(wantPaths)+1 || len(mounts) != len(wantPaths) {
		t.Errorf("host volumes and probe = %v, mounts = %v, want host paths %v", volumes, mounts, wantPaths)
	}
	for name, path := range wantPaths {
		volume := volumes[name]
		if volume.HostPath == nil || volume.HostPath.Path != path.hostPath {
			t.Errorf("host volume %s = %+v, want path %s", name, volume.HostPath, path.hostPath)
		}
		mount := mounts[name]
		if mount.MountPath != path.mountPath || mount.ReadOnly != path.readOnly {
			t.Errorf("host mount %s = %+v, want path %s readOnly=%t", name, mount, path.mountPath, path.readOnly)
		}
	}
	state := volumes["host-state"].HostPath
	if state != nil && (state.Type == nil || *state.Type != corev1.HostPathDirectoryOrCreate) {
		t.Errorf("host state directory type = %v, want DirectoryOrCreate", state.Type)
	}
	if !reflect.DeepEqual(container.VolumeDevices, []corev1.VolumeDevice{{Name: blockpernode.ProbeVolume, DevicePath: blockpernode.ProbeDevice}}) {
		t.Errorf("probe devices = %+v", container.VolumeDevices)
	}
	probe := volumes[blockpernode.ProbeVolume]
	if probe.Ephemeral == nil || probe.Ephemeral.VolumeClaimTemplate == nil {
		t.Fatal("per-node probe has no ephemeral claim template")
	}
	claim := probe.Ephemeral.VolumeClaimTemplate.Spec
	if claim.VolumeMode == nil || *claim.VolumeMode != corev1.PersistentVolumeBlock ||
		!reflect.DeepEqual(claim.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}) ||
		claim.StorageClassName == nil || *claim.StorageClassName != "rwx" {
		t.Errorf("probe claim = %+v, want RWX Block on rwx", claim)
	}
	size := claim.Resources.Requests[corev1.ResourceStorage]
	if size.Cmp(resource.MustParse("10Mi")) != 0 {
		t.Errorf("probe claim storage = %s, want 10Mi", size.String())
	}
	if daemon.Spec.Template.Spec.HostPID || container.ReadinessProbe != nil {
		t.Error("per-node probe received shared watchdog process settings")
	}
}

func TestReconcilePersistsPerNodeDaemonSetHostSettings(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled *bool
		want    perNodeDaemonSetSettings
	}{
		{"enabled with explicit settings", ptr.To(true), perNodeDaemonSetSettings{true, "2m0s", "7m0s", "10s", "24s", "11"}},
		{"default disabled", nil, perNodeDaemonSetSettings{false, "", "5m0s", "5s", "15s", "7"}},
		{"disabled with unused timeout", ptr.To(false), perNodeDaemonSetSettings{false, "", "5m0s", "5s", "15s", "7"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			config := watcherConfig()
			config.Spec.WatchdogPath = "/custom/dev/watchdog4"
			config.Spec.BootUnitEnabled = test.enabled
			if test.enabled != nil {
				config.Spec.BootUnitTimeout = &metav1.Duration{Duration: 2 * time.Minute}
			}
			if test.want.bootEnabled {
				config.Spec.AutonomousRebootRecoveryPeriod = &metav1.Duration{Duration: 7 * time.Minute}
				config.Spec.IOTimeout = &metav1.Duration{Duration: 24 * time.Second}
				config.Spec.SBRTimeoutSeconds = ptr.To[int32](60)
				config.Spec.MaxConsecutiveFailures = ptr.To[int32](11)
			}
			r := watcherReconciler(config)
			reconcile := func() *appsv1.DaemonSet {
				t.Helper()
				if err := r.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
					t.Fatalf("read stored configuration: %v", err)
				}
				if _, err := r.reconcileBlockPerNode(ctx, config, "example.test/sbr:watcher"); err != nil {
					t.Fatalf("reconcile agent DaemonSet: %v", err)
				}
				daemon := &appsv1.DaemonSet{}
				key := types.NamespacedName{Name: "sbr-agent-" + config.Name, Namespace: config.Namespace}
				if err := r.Get(ctx, key, daemon); err != nil {
					t.Fatalf("read stored agent DaemonSet: %v", err)
				}
				return daemon
			}
			daemon := reconcile()
			checkPerNodeDaemonSet(t, daemon, config.UID, test.want)
			repeated := reconcile()
			if !reflect.DeepEqual(daemon.Spec.Template, repeated.Spec.Template) {
				t.Error("repeated reconciliation changed the agent template")
			}
			if test.want.bootEnabled {
				config.Spec.BootUnitEnabled = ptr.To(false)
				if err := r.Update(ctx, config); err != nil {
					t.Fatalf("disable the boot unit in the stored configuration: %v", err)
				}
				want := test.want
				want.bootEnabled, want.bootTimeout = false, ""
				checkPerNodeDaemonSet(t, reconcile(), config.UID, want)
			}
		})
	}
}

func TestSharedDaemonSetKeepsStorageAttachments(t *testing.T) {
	filesystem, block := api.SharedStorageVolumeModeFilesystem, api.SharedStorageVolumeModeBlock
	for name, mode := range map[string]*api.SharedStorageVolumeModeType{"default filesystem": nil, "filesystem": &filesystem, "block": &block} {
		t.Run(name, func(t *testing.T) {
			config := watcherConfig()
			config.Spec.SharedStorageVolumeMode = mode
			config.Spec.WatchdogPath = "/custom/dev/watchdog4"
			config.Spec.IOTimeout = &metav1.Duration{Duration: 45 * time.Second}
			daemon := (&StorageBasedRemediationConfigReconciler{}).buildDaemonSet(config, "example.test/sbr:shared")
			container := daemon.Spec.Template.Spec.Containers[0]
			volumes := map[string]corev1.Volume{}
			for _, volume := range daemon.Spec.Template.Spec.Volumes {
				volumes[volume.Name] = volume
			}
			shared := volumes["shared-storage"]
			if shared.PersistentVolumeClaim == nil || shared.PersistentVolumeClaim.ClaimName != "config-shared-storage" {
				t.Errorf("shared storage volume = %+v", shared)
			}
			for _, name := range []string{"host-state", "host-systemd", "host-usr-local", "host-etc-kubernetes", blockpernode.ProbeVolume} {
				if _, exists := volumes[name]; exists {
					t.Errorf("shared agent received per-node volume %q", name)
				}
			}
			if !slices.Contains(container.Args, "--io-timeout=2s") || slices.Contains(container.Args, "--block-per-node=true") {
				t.Errorf("shared agent arguments = %v", container.Args)
			}
			if !daemon.Spec.Template.Spec.HostPID || container.ReadinessProbe == nil {
				t.Error("shared agent lost watchdog process settings")
			}
			if mode != nil && *mode == block {
				if !reflect.DeepEqual(container.VolumeDevices, []corev1.VolumeDevice{{Name: "shared-storage", DevicePath: agent.SharedStorageBlockDevicePath}}) ||
					!slices.Contains(container.VolumeMounts, corev1.VolumeMount{Name: "host-dev", MountPath: BlockModeHostDevMountPath}) {
					t.Errorf("shared Block attachments = %+v, mounts = %+v", container.VolumeDevices, container.VolumeMounts)
				}
				host := volumes["host-dev"].HostPath
				if host == nil || host.Path != "/custom/dev" {
					t.Errorf("shared Block watchdog directory = %+v", host)
				}
				for _, mount := range container.VolumeMounts {
					if mount.Name == "shared-storage" || mount.MountPath == "/dev" {
						t.Errorf("shared Block received incompatible mount %+v", mount)
					}
				}
			} else if len(container.VolumeDevices) != 0 ||
				!slices.Contains(container.VolumeMounts, corev1.VolumeMount{Name: "dev", MountPath: "/dev"}) ||
				!slices.Contains(container.VolumeMounts, corev1.VolumeMount{Name: "shared-storage", MountPath: agent.SharedStorageSBRDeviceDirectory}) {
				t.Errorf("shared Filesystem attachments = %+v, mounts = %+v", container.VolumeDevices, container.VolumeMounts)
			}
		})
	}
}
