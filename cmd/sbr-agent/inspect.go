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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/inspection"
)

func runInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("sbr-agent inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var output, path string
	flags.StringVar(&output, "o", "json", "Output format (json)")
	flags.StringVar(&output, "output", "json", "Output format (json)")
	flags.StringVar(&path, agent.FlagSBRDevice, "",
		"Storage path; defaults to the agent's --sbr-device in /proc/1/cmdline")
	timeout := flags.Duration("timeout", 30*time.Second, "Deadline for the entire inspection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || output != "json" || *timeout <= 0 {
		return errors.New("expected inspect [-o json] [--sbr-device PATH] [--timeout 30s], with a positive timeout")
	}
	// Bound path discovery, open, stat and reads, including filesystem I/O.
	// Only this goroutine owns storage buffers; timeout never reuses them.
	type result struct {
		report *inspection.Report
		err    error
	}
	results := make(chan result, 1)
	go func() {
		if path == "" {
			cmdline, err := os.ReadFile("/proc/1/cmdline")
			if err != nil {
				results <- result{err: fmt.Errorf("read agent configuration: %w; specify --sbr-device", err)}
				return
			}
			var errPath error
			path, errPath = inspectDeviceFromCmdline(cmdline)
			if errPath != nil {
				results <- result{err: errPath}
				return
			}
		}
		r, err := inspection.Read(path)
		results <- result{r, err}
	}()
	timer := time.NewTimer(*timeout)
	defer timer.Stop()
	select {
	case res := <-results:
		if res.err != nil {
			return res.err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(res.report); err != nil {
			return fmt.Errorf("write JSON: %w", err)
		}
		if len(res.report.Errors) != 0 {
			return errors.New("inspection incomplete; see errors in JSON output")
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("inspection timed out after %s", *timeout)
	}
}

// kubectl exec does not inherit PID 1's CLI flags. Read just the storage path
// from the running container's agent, without a Kubernetes client or API call.
func inspectDeviceFromCmdline(data []byte) (string, error) {
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(args) == 0 || filepath.Base(args[0]) != "sbr-agent" {
		return "", errors.New("PID 1 is not sbr-agent; specify --sbr-device")
	}
	var path string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		for _, prefix := range []string{"--sbr-device=", "-sbr-device="} {
			if strings.HasPrefix(arg, prefix) {
				path = strings.TrimPrefix(arg, prefix)
			}
		}
		if arg == "--sbr-device" || arg == "-sbr-device" {
			if i+1 < len(args) {
				i++
				path = args[i]
			}
		}
	}
	if path == "" {
		return "", errors.New("agent storage path not found; specify --sbr-device")
	}
	return path, nil
}
