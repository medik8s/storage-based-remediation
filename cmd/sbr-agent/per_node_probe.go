package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

func perNodeStartupTiming(values map[string]string, flags *flag.FlagSet) (*blockpernode.ProbeTiming, error) {
	timing, err := blockpernode.ParseTiming(values)
	if err != nil {
		return nil, err
	}
	flags.Visit(func(f *flag.Flag) {
		var expected string
		switch f.Name {
		case agent.FlagSBRUpdateInterval:
			expected = timing.ProbeInterval.Duration.String()
		case agent.FlagIOTimeout:
			expected = timing.IOTimeout.Duration.String()
		case agent.FlagMaxConsecutiveFailures:
			expected = fmt.Sprint(timing.MaxConsecutiveFailures)
		default:
			return
		}
		if f.Value.String() != expected {
			err = fmt.Errorf("legacy argument %s conflicts with fixed probe settings", f.Name)
		}
	})
	return timing, err
}

func runPerNodeAgent() error {
	values := map[string]string{}
	for _, key := range []string{blockpernode.ProbeIntervalEnv, blockpernode.IOTimeoutEnv, blockpernode.FailuresEnv} {
		values[key] = os.Getenv(key)
	}
	timing, err := perNodeStartupTiming(values, flag.CommandLine)
	if err != nil {
		return err
	}
	if err := initializeLogger(*logLevel); err != nil {
		return err
	}
	configUID := types.UID(os.Getenv("SBR_CONFIGURATION_UID"))
	name := os.Getenv("NODE_NAME")
	if name == "" || configUID == "" {
		return fmt.Errorf("Node name and configuration UID are required")
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	c, err := client.New(config, client.Options{})
	if err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), timing.IOTimeout.Duration)
	node := &corev1.Node{}
	err = c.Get(startupCtx, types.NamespacedName{Name: name}, node)
	cancel()
	if err != nil {
		return err
	}
	owner, err := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
	if err != nil || owner.NodeUID != node.UID || owner.ConfigUID != configUID || owner.Phase != blockpernode.Active {
		return fmt.Errorf("Node has no active matching configuration owner")
	}
	device, err := blockpernode.OpenDevice(blockpernode.ProbeDevice, true)
	if err != nil {
		return err
	}
	defer device.Close()
	ctx := ctrl.SetupSignalHandler()
	updates := make(chan blockpernode.ProbeProgress, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case progress := <-updates:
				if time.Since(progress.ObservedAt) > timing.Gap() {
					continue
				}
				status, reason := corev1.ConditionFalse, "Recovered"
				if progress.Failures > 0 {
					if progress.Failures < timing.MaxConsecutiveFailures {
						continue
					}
					status, reason = corev1.ConditionTrue, "StorageProbeFailed"
				}
				attempt, stop := context.WithTimeout(ctx, min(timing.IOTimeout.Duration, timing.ProbeInterval.Duration))
				current := &corev1.Node{}
				if err := c.Get(attempt, types.NamespacedName{Name: name}, current); err == nil {
					active, e := blockpernode.DecodeOwner(current.Annotations[blockpernode.OwnerAnnotation])
					if e == nil && current.UID == node.UID && active == owner {
						err = blockpernode.PatchCondition(attempt, c, current, status, reason, "Local storage probe for "+name, time.Now())
						if err != nil {
							logger.Error(err, "Local storage condition failed", "target", name)
						}
					}
				}
				stop()
			}
		}
	}()
	blockpernode.RunProbe(ctx, *timing, device, func(progress blockpernode.ProbeProgress) {
		select {
		case updates <- progress:
		default:
		}
	})
	return nil
}
