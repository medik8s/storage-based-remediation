package main

import (
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
	"github.com/medik8s/storage-based-remediation/v5/internal/watcher"
)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=patch

func run() error {
	var assignment blockpernode.WatcherTargets
	if err := json.Unmarshal([]byte(os.Getenv(blockpernode.TargetsEnv)), &assignment); err != nil {
		return err
	}
	host := os.Getenv(blockpernode.NodeNameEnv)
	if host == "" {
		return fmt.Errorf("%s is missing", blockpernode.NodeNameEnv)
	}
	if err := assignment.Validate(host); err != nil {
		return err
	}
	readers := map[types.UID]watcher.Reader{}
	for _, target := range assignment.Targets {
		if target.Probe == nil || target.Timing == nil {
			continue
		}
		device, err := blockpernode.OpenDevice(target.Probe.DevicePath, false)
		if err != nil {
			return err
		}
		defer device.Close()
		readers[target.UID] = device
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	ctx := ctrl.SetupSignalHandler()
	nodes := watcher.NewNodeState()
	go nodes.Synchronize(ctx, kube)
	r := watcher.Runtime{Assignment: assignment, Nodes: nodes, Client: c, Readers: readers, Log: ctrl.Log.WithName("watcher")}
	r.Run(ctx)
	<-ctx.Done()
	return nil
}

func main() {
	ctrl.SetLogger(zap.New(zap.WriteTo(os.Stdout)))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
