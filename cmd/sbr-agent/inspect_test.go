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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/inspection"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

func inspectFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sbr-device")
	for _, p := range []string{path, path + agent.SharedStorageFenceDeviceSuffix} {
		if err := os.WriteFile(p, make([]byte, 256*sbdprotocol.SBD_SLOT_SIZE), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestInspectDeviceFromCmdline(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"/usr/local/bin/sbr-agent", "--sbr-device=/sbr-block"}, "/sbr-block"},
		{[]string{"sbr-agent", "--node-name=worker", "--sbr-device", "/custom storage/sbr-device"},
			"/custom storage/sbr-device"},
		{[]string{"sbr-agent", "-sbr-device=/first", "--sbr-device=/last"}, "/last"},
		{[]string{"sh", "--sbr-device=/wrong"}, ""},
		{[]string{"sbr-agent", "--sbr-device"}, ""},
		{[]string{"sbr-agent", "--sbr-device="}, ""},
	} {
		path, err := inspectDeviceFromCmdline([]byte(strings.Join(tc.args, "\x00") + "\x00"))
		if path != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("%v: got %q, %v", tc.args, path, err)
		}
	}
}

func TestInspectCLI(t *testing.T) {
	path := inspectFixture(t)
	var out, stderr bytes.Buffer
	if err := runInspect([]string{"-o", "json", "--sbr-device", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var report inspection.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v: %s", err, &out)
	}
	if report.Mode != "fs" || report.SchemaVersion != 1 || stderr.Len() != 0 {
		t.Fatalf("bad output: %s; stderr %s", &out, &stderr)
	}
	for _, args := range [][]string{{"-o", "yaml"}, {"--init"}, {"extra"}, {"--timeout=0"}, {"--timeout=-1s"}} {
		out.Reset()
		if err := runInspect(args, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	out.Reset()
	if err := runInspect([]string{"--help"}, &out, &stderr); err != nil || out.Len() != 0 {
		t.Fatal("help failed")
	}
	// Missing region produces a diagnostic JSON document AND a failure exit.
	if err := os.Remove(path + agent.SharedStorageFenceDeviceSuffix); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runInspect([]string{"--sbr-device", path}, &out, &stderr); err == nil {
		t.Fatal("incomplete inspection succeeded")
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Errors) == 0 {
		t.Fatalf("diagnostics missing: %s", &out)
	}
}

type inspectBrokenWriter struct{}

func (inspectBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestInspectOutputFailure(t *testing.T) {
	err := runInspect([]string{"--sbr-device", inspectFixture(t)}, inspectBrokenWriter{}, io.Discard)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write failure hidden: %v", err)
	}
}

// Run the real main dispatch in a separate process so os.Exit paths and the
// deadline are covered without starting Kubernetes, watchdog or metrics.
func TestInspectMainProcess(t *testing.T) {
	if os.Getenv("SBR_INSPECT_TEST_CHILD") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"sbr-agent"}, os.Args[i+1:]...)
				main()
				return
			}
		}
		t.Fatal("missing child args")
	}
	path := inspectFixture(t)
	for _, tc := range []struct {
		name    string
		args    []string
		success bool
	}{
		{"inspect", []string{"inspect", "--sbr-device", path, "-o", "json"}, true},
		{"typo", []string{"inspekt"}, false},
		{"misplaced", []string{"--sbr-device", path, "inspect"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestInspectMainProcess$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "SBR_INSPECT_TEST_CHILD=1")
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			err := cmd.Run()
			if (err == nil) != tc.success {
				t.Fatalf("unexpected exit: %v; %s", err, &stderr)
			}
			if tc.success {
				// The testing runner emits PASS after main returns; the first JSON value
				// must still be a valid report, with no agent startup log before it.
				var r inspection.Report
				if err := json.NewDecoder(&out).Decode(&r); err != nil || r.Mode != "fs" {
					t.Fatalf("main did not inspect: %v %s", err, &out)
				}
			}
		})
	}
	// A stalled node map read is bounded by the command deadline. Use the
	// child process so timeout termination also releases blocked goroutines.
	if err := syscall.Mkfifo(path+agent.SharedStorageNodeMappingSuffix, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInspectMainProcess$", "--",
		"inspect", "--sbr-device", path, "--timeout=100ms")
	cmd.Env = append(os.Environ(), "SBR_INSPECT_TEST_CHILD=1")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("deadline failed: %v %s", err, out)
	}
}
