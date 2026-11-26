package main

import (
	"flag"
	"testing"
	"time"

	"github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
)

func TestDurationFlagDefaults(t *testing.T) {
	for _, test := range []struct {
		name  string
		want  time.Duration
		value *time.Duration
	}{
		{agent.FlagIOTimeout, agent.IoTimeout, ioTimeout},
		{agent.FlagAutonomousRebootRecoveryPeriod, v1alpha1.DefaultAutonomousRebootRecoveryPeriod, autonomousRebootRecoveryPeriodFlag},
	} {
		t.Run(test.name, func(t *testing.T) {
			registered := flag.Lookup(test.name)
			if registered == nil {
				t.Fatal("agent flag is not registered")
			}
			if registered.DefValue != test.want.String() {
				t.Fatalf("registered default = %q, want %q", registered.DefValue, test.want.String())
			}
			if *test.value != test.want {
				t.Fatalf("agent flag value = %s, want %s", *test.value, test.want)
			}
		})
	}
}
