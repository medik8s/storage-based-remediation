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

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/yaml"

	sbr "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

const (
	testStorageClass    = "test-sc"
	ioTimeoutField      = "ioTimeout"
	outageGraceField    = "totalOutageGracePeriod"
	recoveryPeriodField = "autonomousRebootRecoveryPeriod"
	storageClassField   = "sharedStorageClass"
	volumeModeField     = "sharedStorageVolumeMode"
	blockMode           = string(sbr.SharedStorageVolumeModeBlock)
	filesystemMode      = string(sbr.SharedStorageVolumeModeFilesystem)
	blockPerNodeMode    = string(sbr.SharedStorageVolumeModeBlockPerNode)
	unknownValue        = "Unknown"
)

func TestBundleSchemas(t *testing.T) {
	for _, resource := range []string{"storagebasedremediationconfigs", "storagebasedremediations"} {
		filename := "storage-based-remediation.medik8s.io_" + resource + ".yaml"
		var schemas [2][]apiextensionsv1.CustomResourceDefinitionVersion
		for i, dir := range []string{"config/crd/bases", "bundle/manifests"} {
			data, err := os.ReadFile(filepath.Join("../..", dir, filename))
			if err != nil {
				t.Fatal(err)
			}
			var crd apiextensionsv1.CustomResourceDefinition
			if err := yaml.Unmarshal(data, &crd); err != nil {
				t.Fatal(err)
			}
			schemas[i] = crd.Spec.Versions
		}
		if len(schemas[0]) == 0 || len(schemas[0]) != len(schemas[1]) {
			t.Fatalf("%s has inconsistent versions", resource)
		}
		for i, version := range schemas[0] {
			if version.Schema == nil || !reflect.DeepEqual(version.Schema, schemas[1][i].Schema) {
				t.Errorf("%s %s has a stale bundle schema", resource, version.Name)
			}
		}
	}
}

func TestAPI(t *testing.T) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("../..", "config/crd/bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("../..", "config/webhook/manifests.yaml")},
		},
	}
	config, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest (KUBEBUILDER_ASSETS must contain the required binaries): %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	if err := sbr.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	opts := env.WebhookInstallOptions
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		WebhookServer: webhook.NewServer(webhook.Options{
			Host: opts.LocalServingHost, Port: opts.LocalServingPort, CertDir: opts.LocalServingCertDir,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	validator := &sbr.StorageBasedRemediationConfigValidator{}
	if err := validator.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("webhook manager: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("webhook manager did not stop")
		}
	})
	check := mgr.GetWebhookServer().StartedChecker()
	if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true,
		func(context.Context) (bool, error) { return check(&http.Request{}) == nil, nil }); err != nil {
		t.Fatalf("webhook did not start: %v", err)
	}
	kubeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("config schema", func(t *testing.T) { testConfigSchema(t, kubeClient) })
	t.Run("config webhook", func(t *testing.T) { testConfigWebhook(t, kubeClient) })
	t.Run("status identities", func(t *testing.T) { testStatusIdentities(t, kubeClient) })
	t.Run("cleanup deadline", func(t *testing.T) { testCleanupDeadline(t, kubeClient) })
	t.Run("status phases", func(t *testing.T) { testStatusPhases(t, kubeClient) })
}

func newConfig() *sbr.StorageBasedRemediationConfig {
	return &sbr.StorageBasedRemediationConfig{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "api-", Namespace: metav1.NamespaceDefault,
	}}
}

func createConfig(t *testing.T, c client.Client, obj *sbr.StorageBasedRemediationConfig) {
	t.Helper()
	if err := c.Create(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
}

func testConfigSchema(t *testing.T, c client.Client) {
	for _, mode := range []string{"", filesystemMode, blockMode, blockPerNodeMode} {
		t.Run("mode "+mode, func(t *testing.T) {
			obj := newConfig()
			if mode != "" {
				value := sbr.SharedStorageVolumeModeType(mode)
				obj.Spec.SharedStorageVolumeMode = &value
			}
			if mode == blockMode || mode == blockPerNodeMode {
				obj.Spec.SharedStorageClass = testStorageClass
			}
			createConfig(t, c, obj)
			stored := &sbr.StorageBasedRemediationConfig{}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), stored); err != nil {
				t.Fatal(err)
			}
			if stored.Spec.IOTimeout != nil || stored.Spec.TotalOutageGracePeriod != nil ||
				stored.Spec.AutonomousRebootRecoveryPeriod != nil || (mode == "" && stored.Spec.SharedStorageVolumeMode != nil) {
				t.Fatal("the API server persisted effective defaults")
			}
		})
	}
	for _, tc := range []struct {
		name, field string
		spec        map[string]interface{}
	}{
		{"unknown mode", volumeModeField, map[string]interface{}{volumeModeField: unknownValue}},
		{"empty mode", volumeModeField, map[string]interface{}{volumeModeField: ""}},
		{"Block without class", storageClassField, map[string]interface{}{volumeModeField: blockMode}},
		{
			"BlockPerNode without class", storageClassField,
			map[string]interface{}{volumeModeField: blockPerNodeMode},
		},
		{
			"BlockPerNode empty class", storageClassField,
			map[string]interface{}{volumeModeField: blockPerNodeMode, storageClassField: ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) { rejectConfigSchema(t, c, tc.spec, tc.field) })
	}
	testDurationSchema(t, c)
}

func rejectConfigSchema(t *testing.T, c client.Client, spec map[string]interface{}, field string) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": sbr.GroupVersion.String(), "kind": "StorageBasedRemediationConfig",
		"metadata": map[string]interface{}{"generateName": "invalid-", "namespace": metav1.NamespaceDefault}, "spec": spec,
	}}
	err := c.Create(t.Context(), obj)
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), field) {
		t.Fatalf("expected schema error for %s, got %v", field, err)
	}
}

func testDurationSchema(t *testing.T, c client.Client) {
	for _, field := range []string{ioTimeoutField, outageGraceField, recoveryPeriodField} {
		for _, value := range []string{
			"-1ns", "0s", "1ns", "999999999ns", "1s", "15s", "10m", "600000000001ns", "1h", "3600000000001ns", "invalid",
		} {
			t.Run(field+"/"+value, func(t *testing.T) {
				duration, parseErr := time.ParseDuration(value)
				valid := parseErr == nil
				switch field {
				case ioTimeoutField:
					valid = valid && duration >= time.Second && duration <= 10*time.Minute
				case outageGraceField:
					valid = valid && duration >= 0 && duration <= time.Hour
				case recoveryPeriodField:
					valid = valid && duration > 0
				}
				if !valid {
					rejectConfigSchema(t, c, map[string]interface{}{
						volumeModeField: blockPerNodeMode, storageClassField: testStorageClass, field: value,
					}, field)
					return
				}
				obj := newConfig()
				mode := sbr.SharedStorageVolumeModeBlockPerNode
				obj.Spec.SharedStorageVolumeMode, obj.Spec.SharedStorageClass = &mode, testStorageClass
				if err := json.Unmarshal([]byte(fmt.Sprintf(`{"%s":%q}`, field, value)), &obj.Spec); err != nil {
					t.Fatal(err)
				}
				createConfig(t, c, obj)
				stored := &unstructured.Unstructured{}
				stored.SetGroupVersionKind(sbr.GroupVersion.WithKind("StorageBasedRemediationConfig"))
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), stored); err != nil {
					t.Fatal(err)
				}
				actual, found, err := unstructured.NestedString(stored.Object, "spec", field)
				if err != nil || !found || actual != duration.String() {
					t.Fatalf("stored duration = %q, found %v, error %v", actual, found, err)
				}
			})
		}
		if field == ioTimeoutField {
			continue
		}
		for _, mode := range []string{"", filesystemMode, blockMode} {
			spec := map[string]interface{}{storageClassField: testStorageClass, field: "1m"}
			if mode != "" {
				spec[volumeModeField] = mode
			}
			rejectConfigSchema(t, c, spec, field)
		}
	}
}

func testConfigWebhook(t *testing.T, c client.Client) {
	for _, mode := range []string{"", filesystemMode, blockMode, blockPerNodeMode} {
		obj := newConfig()
		obj.Spec.SharedStorageClass = testStorageClass
		if mode != "" {
			value := sbr.SharedStorageVolumeModeType(mode)
			obj.Spec.SharedStorageVolumeMode = &value
		}
		createConfig(t, c, obj)
		for _, next := range []sbr.SharedStorageVolumeModeType{
			sbr.SharedStorageVolumeModeFilesystem, sbr.SharedStorageVolumeModeBlock, sbr.SharedStorageVolumeModeBlockPerNode,
		} {
			updated := obj.DeepCopy()
			updated.Spec.SharedStorageVolumeMode = &next
			err := c.Update(t.Context(), updated)
			if next == obj.Spec.GetSharedStorageVolumeMode() {
				if err != nil {
					t.Fatal(err)
				}
				obj = updated
			} else {
				requireWebhookRejection(t, err, volumeModeField)
			}
		}
		obj.Spec.IOTimeout = &metav1.Duration{Duration: 22 * time.Second}
		if err := c.Update(t.Context(), obj); err != nil {
			t.Fatal(err)
		}
		if mode == "" || mode == filesystemMode {
			obj.Spec.SharedStorageVolumeMode = nil
			if err := c.Update(t.Context(), obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, field := range []string{outageGraceField, recoveryPeriodField} {
		for _, initial := range []string{"", "30s"} {
			obj := newConfig()
			mode := sbr.SharedStorageVolumeModeBlockPerNode
			obj.Spec.SharedStorageVolumeMode, obj.Spec.SharedStorageClass = &mode, testStorageClass
			if initial != "" {
				setConfigDuration(t, obj, field, initial)
			}
			createConfig(t, c, obj)
			equivalent := "30s"
			if initial == "" {
				equivalent = map[string]string{outageGraceField: "60s", recoveryPeriodField: "300s"}[field]
			}
			setConfigDuration(t, obj, field, equivalent)
			if err := c.Update(t.Context(), obj); err != nil {
				t.Fatal(err)
			}
			if field == outageGraceField && initial == "" {
				patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"totalOutageGracePeriod":"1m"}}`))
				if err := c.Patch(t.Context(), obj, patch); err != nil {
					t.Fatal(err)
				}
			}
			changed := obj.DeepCopy()
			setConfigDuration(t, changed, field, "31s")
			requireWebhookRejection(t, c.Update(t.Context(), changed), field)
			patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"%s":null}}`, field)))
			err := c.Patch(t.Context(), obj, patch)
			if initial == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireWebhookRejection(t, err, field)
			}
		}
	}
}

func setConfigDuration(t *testing.T, obj *sbr.StorageBasedRemediationConfig, field, value string) {
	t.Helper()
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"%s":%q}`, field, value)), &obj.Spec); err != nil {
		t.Fatal(err)
	}
}

func requireWebhookRejection(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "denied the request") || !strings.Contains(err.Error(), field) {
		t.Fatalf("expected webhook rejection for %s, got %v", field, err)
	}
}

func newRemediation(t *testing.T, c client.Client) *sbr.StorageBasedRemediation {
	t.Helper()
	obj := &sbr.StorageBasedRemediation{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "api-", Namespace: metav1.NamespaceDefault,
	}}
	if err := c.Create(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	stored := &sbr.StorageBasedRemediation{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Status, sbr.StorageBasedRemediationStatus{}) {
		t.Fatal("legacy remediation status changed")
	}
	return stored
}

func requireStatusRejection(t *testing.T, err error, field string) {
	t.Helper()
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), field) {
		t.Fatalf("expected status schema error for %s, got %v", field, err)
	}
}

func testStatusIdentities(t *testing.T, c client.Client) {
	for _, tc := range []struct {
		field string
		set   func(*sbr.StorageBasedRemediationStatus, string)
	}{
		{"nodeUID", func(s *sbr.StorageBasedRemediationStatus, v string) { s.NodeUID = types.UID(v) }},
		{"configUID", func(s *sbr.StorageBasedRemediationStatus, v string) { s.ConfigUID = types.UID(v) }},
		{"volumeMode", func(s *sbr.StorageBasedRemediationStatus, v string) { s.VolumeMode = v }},
		{"beforeBootID", func(s *sbr.StorageBasedRemediationStatus, v string) { s.BeforeBootID = v }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			obj := newRemediation(t, c)
			patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"status":{"%s":""}}`, tc.field)))
			requireStatusRejection(t, c.Status().Patch(t.Context(), obj, patch), tc.field)
			tc.set(&obj.Status, "initial")
			if err := c.Status().Update(t.Context(), obj); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"changed", ""} {
				changed := obj.DeepCopy()
				tc.set(&changed.Status, value)
				requireStatusRejection(t, c.Status().Update(t.Context(), changed), tc.field)
			}
			for _, path := range []string{"/status/" + tc.field, "/status"} {
				patch := client.RawPatch(types.JSONPatchType, []byte(fmt.Sprintf(`[{"op":"remove","path":%q}]`, path)))
				requireStatusRejection(t, c.Status().Patch(t.Context(), obj, patch), tc.field)
			}
			for _, body := range []string{
				fmt.Sprintf(`{"status":{"%s":null}}`, tc.field), `{"status":null}`,
				fmt.Sprintf(`{"status":{"%s":"changed"}}`, tc.field),
			} {
				patch := client.RawPatch(types.MergePatchType, []byte(body))
				requireStatusRejection(t, c.Status().Patch(t.Context(), obj, patch), tc.field)
			}
			allowOtherUpdates(t, c, obj)
		})
	}
}

func allowOtherUpdates(t *testing.T, c client.Client, obj *sbr.StorageBasedRemediation) {
	t.Helper()
	obj.SetCondition(sbr.SBRRemediationConditionLeadershipAcquired, metav1.ConditionTrue, "Test", "Test")
	if err := c.Status().Update(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	obj.Labels = map[string]string{"test": "updated"}
	if err := c.Update(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	stored := &sbr.StorageBasedRemediation{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatal(err)
	}
	if !stored.HasLeadership() || !equality.Semantic.DeepEqual(obj.Status, stored.Status) ||
		stored.Labels["test"] != "updated" {
		t.Fatal("unrelated updates changed status")
	}
}

func testCleanupDeadline(t *testing.T, c client.Client) {
	obj := newRemediation(t, c)
	deadline := metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(time.Minute))
	obj.Status.Cleanup = &sbr.CleanupStatus{Deadline: &deadline}
	if err := c.Status().Update(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*sbr.StorageBasedRemediation){
		func(o *sbr.StorageBasedRemediation) { o.Status.Cleanup.Deadline.Time = deadline.Add(time.Second) },
		func(o *sbr.StorageBasedRemediation) { o.Status.Cleanup.Deadline = nil },
		func(o *sbr.StorageBasedRemediation) { o.Status.Cleanup = nil },
		func(o *sbr.StorageBasedRemediation) { o.Status = sbr.StorageBasedRemediationStatus{} },
	} {
		changed := obj.DeepCopy()
		mutate(changed)
		requireStatusRejection(t, c.Status().Update(t.Context(), changed), "cleanup.deadline")
	}
	for _, path := range []string{"/status/cleanup/deadline", "/status/cleanup", "/status"} {
		patch := client.RawPatch(types.JSONPatchType, []byte(fmt.Sprintf(`[{"op":"remove","path":%q}]`, path)))
		requireStatusRejection(t, c.Status().Patch(t.Context(), obj, patch), "cleanup.deadline")
	}
	for _, body := range []string{
		`{"status":{"cleanup":{"deadline":null}}}`, `{"status":{"cleanup":null}}`, `{"status":null}`,
		`{"status":{"cleanup":{"deadline":"2030-01-01T00:00:00Z"}}}`,
	} {
		patch := client.RawPatch(types.MergePatchType, []byte(body))
		requireStatusRejection(t, c.Status().Patch(t.Context(), obj, patch), "cleanup.deadline")
	}
	obj.Status.Cleanup.Phase = "Completed"
	obj.Status.Cleanup.OutOfServiceApplied = true
	allowOtherUpdates(t, c, obj)
}

func testStatusPhases(t *testing.T, c client.Client) {
	obj := newRemediation(t, c)
	for _, phase := range []string{"BootEnded", "Offered", "Cancelled", "Accepted"} {
		obj.Status.ExecutionPhase = phase
		if err := c.Status().Update(t.Context(), obj); err != nil {
			t.Fatal(err)
		}
	}
	changed := obj.DeepCopy()
	changed.Status.ExecutionPhase = unknownValue
	requireStatusRejection(t, c.Status().Update(t.Context(), changed), "executionPhase")
	for _, phase := range []string{"Skipped", "Pending", "Completed", "Observing", unknownValue, "Incomplete"} {
		obj.Status.Cleanup = &sbr.CleanupStatus{Phase: phase}
		if err := c.Status().Update(t.Context(), obj); err != nil {
			t.Fatal(err)
		}
	}
	changed = obj.DeepCopy()
	changed.Status.Cleanup.Phase = "invalid"
	requireStatusRejection(t, c.Status().Update(t.Context(), changed), "phase")
}
