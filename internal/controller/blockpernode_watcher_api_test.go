package controller

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type watcherWorkloadClient struct {
	client.Client
	updates int
}

func (c *watcherWorkloadClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if _, ok := object.(*appsv1.Deployment); ok {
		c.updates++
	}
	return c.Client.Update(ctx, object, options...)
}

func TestWatcherReconcileWithAPIDefaultsKeepsStoredTemplate(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for real API checks")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	apiConfig, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	}()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme, storagev1.AddToScheme, api.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(apiConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config := watcherConfig()
	config.UID, config.Generation = "", 0
	config.Spec.NodeSelector = map[string]string{"selected": "true"}
	config.Spec.WatchdogPath = "/custom/dev/watchdog4"
	config.Spec.BootUnitEnabled = ptr.To(true)
	config.Spec.BootUnitTimeout = &metav1.Duration{Duration: 2 * time.Minute}
	config.Spec.AutonomousRebootRecoveryPeriod = &metav1.Duration{Duration: 7 * time.Minute}
	config.Spec.IOTimeout = &metav1.Duration{Duration: 24 * time.Second}
	config.Spec.SBRTimeoutSeconds = ptr.To[int32](60)
	config.Spec.MaxConsecutiveFailures = ptr.To[int32](11)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: config.Namespace}}); err != nil {
		t.Fatalf("create Namespace: %v", err)
	}
	if err := c.Create(ctx, config); err != nil {
		t.Fatalf("create configuration: %v", err)
	}
	target, host := watcherNode("target"), watcherNode("host")
	target.UID, host.UID = "", ""
	target.Labels["selected"] = "true"
	for _, node := range []*corev1.Node{&target, &host} {
		if err := c.Create(ctx, node); err != nil {
			t.Fatalf("create Node %s: %v", node.Name, err)
		}
	}
	csi := &storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: host.Name, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: host.Name, UID: host.UID}}}, Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{{Name: "driver.example.test", NodeID: host.Name}}}}
	if err := c.Create(ctx, csi); err != nil {
		t.Fatalf("create host CSINode: %v", err)
	}
	counted := &watcherWorkloadClient{Client: c}
	r := &StorageBasedRemediationConfigReconciler{Client: counted, APIReader: c, Scheme: scheme}
	reconcile := func() {
		t.Helper()
		if err := c.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
			t.Fatalf("read configuration: %v", err)
		}
		if _, err := r.reconcileBlockPerNode(ctx, config, "example.test/sbr:watcher"); err != nil {
			t.Fatalf("reconcile workloads: %v", err)
		}
	}
	reconcile()
	wantAgent := perNodeDaemonSetSettings{true, "2m0s", "7m0s", "10s", "24s", "11"}
	checkStoredAgent := func() {
		t.Helper()
		daemon := &appsv1.DaemonSet{}
		key := client.ObjectKey{Name: "sbr-agent-" + config.Name, Namespace: config.Namespace}
		if err := c.Get(ctx, key, daemon); err != nil {
			t.Fatalf("read API-persisted agent DaemonSet: %v", err)
		}
		checkPerNodeDaemonSet(t, daemon, config.UID, wantAgent)
	}
	checkStoredAgent()
	deployments := &appsv1.DeploymentList{}
	if err := c.List(ctx, deployments, client.InNamespace(config.Namespace), client.MatchingLabels{blockpernode.ConfigurationUIDLabel: string(config.UID)}); err != nil {
		t.Fatalf("list watcher Deployments: %v", err)
	}
	if len(deployments.Items) != 1 {
		t.Fatalf("watcher Deployments=%d", len(deployments.Items))
	}
	deployment := deployments.Items[0].DeepCopy()
	if deployment.Spec.RevisionHistoryLimit == nil || deployment.Spec.ProgressDeadlineSeconds == nil || deployment.Spec.Template.Spec.RestartPolicy == "" || deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy == "" {
		t.Fatalf("API defaults are absent: %+v", deployment.Spec)
	}
	if deployment.Spec.Template.Annotations != nil {
		t.Fatalf("API retained an empty annotations map: %+v", deployment.Spec.Template.Annotations)
	}
	seedCurrentPod := func(name string) *corev1.Pod {
		t.Helper()
		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}, Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To[int32](1), Selector: deployment.Spec.Selector.DeepCopy(), Template: *deployment.Spec.Template.DeepCopy()}}
		rs.Spec.Selector.MatchLabels[appsv1.DefaultDeploymentUniqueLabelKey] = name
		rs.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = name
		if err := c.Create(ctx, rs); err != nil {
			t.Fatalf("create current ReplicaSet: %v", err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.Namespace, Labels: rs.Spec.Template.DeepCopy().Labels, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}}, Spec: *rs.Spec.Template.Spec.DeepCopy()}
		pod.Spec.NodeName = host.Name
		if err := c.Create(ctx, pod); err != nil {
			t.Fatalf("create current Pod: %v", err)
		}
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		if err := c.Status().Update(ctx, pod); err != nil {
			t.Fatalf("set current Pod Ready: %v", err)
		}
		return pod
	}
	pod := seedCurrentPod("first")
	if current, err := r.currentWatcherPod(ctx, pod, deployment); err != nil || !current {
		t.Fatalf("API-defaulted Pod recognition: current=%t error=%v", current, err)
	}
	empty := deployment.DeepCopy()
	empty.Spec.Template.Annotations = map[string]string{}
	emptyCurrent, err := r.currentWatcherPod(ctx, pod, empty)
	if err != nil {
		t.Fatalf("compare empty annotations representation: %v", err)
	}
	t.Logf("current-Pod recognition before API normalization: nil=true empty=%t", emptyCurrent)
	if err := c.Update(ctx, empty); err != nil {
		t.Fatalf("round-trip equivalent empty annotations: %v", err)
	}
	if empty.Spec.Template.Annotations != nil {
		t.Fatalf("API did not normalize empty annotations: %+v", empty.Spec.Template.Annotations)
	}
	if current, err := r.currentWatcherPod(ctx, pod, empty); err != nil || !current {
		t.Fatalf("round-trip Pod recognition: current=%t error=%v", current, err)
	}
	checkStable := func(description string, iterations int) {
		t.Helper()
		before := deployment.DeepCopy()
		requests := counted.updates
		for index := 0; index < iterations; index++ {
			reconcile()
			if err := c.Get(ctx, client.ObjectKeyFromObject(deployment), deployment); err != nil {
				t.Fatalf("read %s Deployment: %v", description, err)
			}
			if !reflect.DeepEqual(before.Spec.Template, deployment.Spec.Template) {
				t.Fatalf("%s changed stored Pod template on reconcile %d", description, index+1)
			}
			if deployment.Generation != before.Generation {
				t.Fatalf("%s changed Deployment generation: previous=%d current=%d", description, before.Generation, deployment.Generation)
			}
			if current, err := r.currentWatcherPod(ctx, pod, deployment); err != nil || !current {
				t.Fatalf("%s lost current Pod: current=%t error=%v", description, current, err)
			}
			result := newStructuralResult()
			if ready, err := r.watcherGroupReadiness(ctx, deployment, []string{host.Name}, result); err != nil || !ready {
				t.Fatalf("%s current Pod readiness: ready=%t failures=%+v error=%v", description, ready, result.failures, err)
			}
		}
		t.Logf("%s: reconciles=%d Deployment Update attempts=%d persisted template changes=0", description, iterations, counted.updates-requests)
	}
	checkStable("defaulted workload", 3)
	beforeRepair := deployment.DeepCopy()
	requests := counted.updates
	if _, err := r.watcherGroupReadiness(ctx, deployment, []string{"other-host"}, newStructuralResult()); err != nil {
		t.Fatalf("repair disallowed placement after API read: %v", err)
	}
	if counted.updates != requests+1 {
		t.Fatalf("placement repair requests=%d", counted.updates-requests)
	}
	if deployment.Spec.Template.Annotations[placementRepair] != string(pod.UID) {
		t.Fatalf("placement repair marker=%q expected=%q", deployment.Spec.Template.Annotations[placementRepair], pod.UID)
	}
	if deployment.Generation != beforeRepair.Generation+1 {
		t.Fatalf("placement repair generation: previous=%d current=%d", beforeRepair.Generation, deployment.Generation)
	}
	if _, err := r.watcherGroupReadiness(ctx, deployment, []string{"other-host"}, newStructuralResult()); err != nil {
		t.Fatalf("repeat old Pod evaluation: %v", err)
	}
	if counted.updates != requests+1 {
		t.Fatalf("old current Pod caused repeated repairs=%d", counted.updates-requests)
	}
	repairMarker := deployment.Spec.Template.Annotations[placementRepair]
	pod = seedCurrentPod("replacement")
	checkStable("repaired workload", 3)
	if deployment.Spec.Template.Annotations[placementRepair] != repairMarker {
		t.Fatalf("reconcile changed placement repair marker: previous=%q current=%q", repairMarker, deployment.Spec.Template.Annotations[placementRepair])
	}
	config.Spec.BootUnitEnabled = ptr.To(false)
	if err := c.Update(ctx, config); err != nil {
		t.Fatalf("disable the boot unit in the API configuration: %v", err)
	}
	reconcile()
	wantAgent.bootEnabled, wantAgent.bootTimeout = false, ""
	checkStoredAgent()
}
