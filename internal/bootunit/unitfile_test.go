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

package bootunit

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestUnitFileMatchesConstants pins the shipped unit file to the paths and
// budgets the code installs and enforces. The unit file is data, not code:
// when a constant is renamed or a budget drifts past TimeoutStartSec, no
// other test fails — the failure mode waits for a real boot, where it is a
// unit that cannot exec its binary, cannot find its identity, never runs
// because its gate moved, or is killed mid-retry by systemd.
func TestUnitFileMatchesConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", UnitName))
	if err != nil {
		t.Fatalf("read the shipped unit file: %v", err)
	}

	// Collect every directive; ConditionPathExists legitimately appears
	// several times, so values are gathered per key rather than overwritten.
	directives := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		directives[key] = append(directives[key], strings.TrimSpace(val))
	}
	has := func(key, want string) bool {
		for _, v := range directives[key] {
			if v == want {
				return true
			}
		}
		return false
	}

	if execs := directives["ExecStart"]; len(execs) != 1 || !strings.HasPrefix(execs[0], HostBinaryPath+" ") {
		t.Errorf("ExecStart %v must run the binary the agent installs at %s", execs, HostBinaryPath)
	}
	if !has("EnvironmentFile", HostIdentityEnvPath) {
		t.Errorf("EnvironmentFile must load %s, got %v", HostIdentityEnvPath, directives["EnvironmentFile"])
	}
	if !has("ConditionPathExists", ArmingFlagPath) {
		t.Errorf("the unit must be gated on the arming flag at %s, got %v",
			ArmingFlagPath, directives["ConditionPathExists"])
	}
	// The negated conditions are what keeps the unit off in a container (where
	// the host is not the node) and off on a host that has no state root at
	// all. Losing either silently turns the gate into "run nowhere" or "run
	// everywhere"; both are worth a CI failure instead of a real boot.
	if !has("ConditionPathExists", "!/.dockerenv") {
		t.Errorf("the unit must refuse to run inside a container, got %v",
			directives["ConditionPathExists"])
	}
	if !has("ConditionPathExists", HostStateRoot) {
		t.Errorf("the unit must require the state root %s, got %v",
			HostStateRoot, directives["ConditionPathExists"])
	}
	if !has("Before", "kubelet.service") {
		t.Errorf("the unit must be ordered Before=kubelet.service, got %v", directives["Before"])
	}
	// Enablement is delivered by `systemctl enable`, which writes the
	// WantedBy symlink. Without an [Install] section, activation fails and the
	// agent reports degraded — loud, but only at run time.
	if !has("WantedBy", "multi-user.target") {
		t.Errorf("the unit must be enableable into multi-user.target, got %v", directives["WantedBy"])
	}
	// Detail rather than a safety property: it makes `systemctl is-active`
	// report the oneshot as active after it exits, which is what an operator
	// checking the boot unit's last outcome expects to see.
	if !has("RemainAfterExit", "yes") {
		t.Errorf("the unit should remain active after exit, got %v", directives["RemainAfterExit"])
	}

	timeouts := directives["TimeoutStartSec"]
	if len(timeouts) != 1 {
		t.Fatalf("TimeoutStartSec must appear exactly once, got %v", timeouts)
	}
	sec, err := strconv.Atoi(strings.TrimSuffix(timeouts[0], "s"))
	if err != nil {
		t.Fatalf("TimeoutStartSec %q is not a second count: %v", timeouts[0], err)
	}
	if budget := int(CordonTotalBudget / time.Second); sec <= budget {
		t.Errorf("TimeoutStartSec=%ds must exceed the cordon budget of %ds, or systemd kills the unit mid-retry",
			sec, budget)
	}
}
