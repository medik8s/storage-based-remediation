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

package utils

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInspectFromPodCLIContract(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$INSPECT_ARGS"
printf '%s' "$INSPECT_STDOUT"
printf '%s' "$INSPECT_STDERR" >&2
exit "$INSPECT_EXIT"
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("INSPECT_ARGS", filepath.Join(dir, "args"))
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	tc := &TestClients{Context: context.Background()}
	for _, test := range []struct {
		name, stdout, stderr, exit string
		success                    bool
		device                     string
	}{
		{
			"success",
			`{"schema_version":1,"mode":"block","heartbeats":[],"fences":[],"errors":[]}`,
			"", "0", true, "/sbr-block",
		},
		{
			"filesystem",
			`{"schema_version":1,"mode":"fs","errors":[]}`,
			"", "0", true, "/dev/sbr/sbr-device",
		},
		{
			"custom-filesystem",
			`{"schema_version":1,"mode":"fs","errors":[]}`,
			"", "0", true, "/custom storage/sbr-device",
		},
		{
			"bad-json",
			`not JSON`,
			"", "0", false, "/sbr-block",
		},
		{
			"unknown-schema",
			`{"schema_version":2,"mode":"block"}`,
			"", "0", false, "/sbr-block",
		},
		{
			"invalid-record",
			`{"schema_version":1,"mode":"fs","errors":["bad CRC"]}`,
			"", "0", false, "/sbr-block",
		},
		{
			"failed-command",
			`{"errors":["bad CRC"]}`,
			"inspection incomplete", "1", false, "/sbr-block",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "namespace"},
				Spec: corev1.PodSpec{
					HostPID: true,
					Containers: []corev1.Container{
						{Name: "sidecar", Args: []string{"--sbr-device=/wrong"}},
						{
							Name: "sbr-agent",
							Args: []string{
								"--node-name=worker",
								"--sbr-device=" + test.device,
							},
						},
					},
				},
			}
			tc.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			t.Setenv("INSPECT_STDOUT", test.stdout)
			t.Setenv("INSPECT_STDERR", test.stderr)
			t.Setenv("INSPECT_EXIT", test.exit)
			_, err := tc.InspectFromPod("pod", "namespace")
			if (err == nil) != test.success {
				t.Fatalf("unexpected result: %v", err)
			}
			if test.exit == "1" &&
				(!strings.Contains(err.Error(), "bad CRC") ||
					!strings.Contains(err.Error(), test.stderr)) {
				t.Fatalf("lost diagnostics: %v", err)
			}
			args, readErr := os.ReadFile(filepath.Join(dir, "args"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			expectedArgs := "exec\n-n\nnamespace\npod\n-c\nsbr-agent\n--\nsbr-agent\n" +
				"inspect\n--sbr-device\n" + test.device + "\n-o\njson\n"
			if string(args) != expectedArgs {
				t.Fatalf("unexpected invocation: %s", args)
			}
		})
	}
}

func TestInspectPodDeviceConfiguration(t *testing.T) {
	for _, test := range []struct {
		name       string
		containers []corev1.Container
		want       string
	}{
		{
			name: "split-flag",
			containers: []corev1.Container{
				{Name: "sbr-agent", Args: []string{"--sbr-device", "/custom/sbr-device"}},
			},
			want: "/custom/sbr-device",
		},
		{
			name:       "missing-container",
			containers: []corev1.Container{{Name: "sidecar", Args: []string{"--sbr-device=/wrong"}}},
			want:       "",
		},
		{
			name:       "missing-flag",
			containers: []corev1.Container{{Name: "sbr-agent"}},
			want:       "",
		},
		{
			name:       "empty-value",
			containers: []corev1.Container{{Name: "sbr-agent", Args: []string{"--sbr-device="}}},
			want:       "",
		},
		{
			name:       "missing-value",
			containers: []corev1.Container{{Name: "sbr-agent", Args: []string{"--sbr-device"}}},
			want:       "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := inspectionDeviceFromPod(&corev1.Pod{
				Spec: corev1.PodSpec{Containers: test.containers},
			})
			if path != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("got %q, %v; want %q", path, err, test.want)
			}
		})
	}
}

func TestInspectFromPodMissingConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "namespace"},
		Spec: corev1.PodSpec{
			HostPID:    true,
			Containers: []corev1.Container{{Name: "sbr-agent"}},
		},
	}
	tc := &TestClients{
		Context: context.Background(),
		Client:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
	}
	// No kubectl is available: configuration errors must be returned before exec.
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct{ name, want string }{
		{"pod", "no configured --sbr-device"},
		{"absent", "get inspection pod"},
	} {
		_, err := tc.InspectFromPod(test.name, "namespace")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
