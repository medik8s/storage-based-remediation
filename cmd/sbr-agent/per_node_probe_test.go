package main

import (
	"flag"
	"testing"
	"time"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

func TestPerNodeStartupUsesLiteralSettings(t *testing.T) {
	values := map[string]string{blockpernode.ProbeIntervalEnv: "5s", blockpernode.IOTimeoutEnv: "15s", blockpernode.FailuresEnv: "7"}
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.Duration(agent.FlagIOTimeout, agent.IoTimeout, "")
	timing, err := perNodeStartupTiming(values, flags)
	if err != nil || timing.IOTimeout.Duration != 15*time.Second {
		t.Fatalf("literal settings: %+v %v", timing, err)
	}
	_ = flags.Parse([]string{"--io-timeout=2s"})
	if _, err := perNodeStartupTiming(values, flags); err == nil {
		t.Fatal("accepted conflicting legacy argument")
	}
	if err := flags.Parse([]string{"--io-timeout=15s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := perNodeStartupTiming(values, flags); err != nil {
		t.Fatalf("rejected matching legacy argument: %v", err)
	}
	delete(values, blockpernode.IOTimeoutEnv)
	if _, err := perNodeStartupTiming(values, flags); err == nil {
		t.Fatal("accepted missing literal setting")
	}
}
