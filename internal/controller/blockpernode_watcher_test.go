package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

func watcherConfig() *api.StorageBasedRemediationConfig {
	return &api.StorageBasedRemediationConfig{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "ns", UID: "config-uid", Generation: 1, Finalizers: []string{StorageBasedRemediationConfigFinalizerName}}, Spec: api.StorageBasedRemediationConfigSpec{SharedStorageClass: "rwx", SharedStorageVolumeMode: ptr.To(api.SharedStorageVolumeModeBlockPerNode)}}
}

func watcherNode(name string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{corev1.LabelOSStable: "linux", "node-role.kubernetes.io/worker": ""}}}
}

func watcherReconciler(objects ...client.Object) *StorageBasedRemediationConfigReconciler {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = storagev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.StorageBasedRemediationConfig{}, &corev1.Node{}, &corev1.Pod{}, &corev1.PersistentVolumeClaim{}).WithObjects(objects...).Build()
	return &StorageBasedRemediationConfigReconciler{Client: c, Scheme: scheme}
}

func ownerFor(config *api.StorageBasedRemediationConfig, node *corev1.Node, group string) {
	owner := blockpernode.ConfigurationOwner{Version: 1, NodeUID: node.UID, Configuration: configIdentity(config), OwnershipGeneration: "generation", Phase: blockpernode.Active}
	value, _ := json.Marshal(owner)
	node.Annotations = map[string]string{blockpernode.OwnerAnnotation: string(value), blockpernode.GroupAnnotation: group}
}

func TestWatcherGroupsAreDeterministicAndKeepLabelPresence(t *testing.T) {
	config := watcherConfig()
	var nodes []corev1.Node
	for i := 0; i < 17; i++ {
		nodes = append(nodes, watcherNode(fmt.Sprintf("node-%02d", i)))
	}
	for _, size := range []int32{1, 16, 17} {
		config.Spec.WatcherGroupSize = &size
		groups, incomplete := watcherGroups(config, nodes)
		if incomplete != 0 || len(groups) != (17+int(size)-1)/int(size) {
			t.Fatalf("size %d groups=%d incomplete=%d", size, len(groups), incomplete)
		}
		reversed := append([]corev1.Node(nil), nodes...)
		for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
			reversed[i], reversed[j] = reversed[j], reversed[i]
		}
		got, _ := watcherGroups(config, reversed)
		if !reflect.DeepEqual(groups, got) {
			t.Fatal("input order changed groups")
		}
	}
	config.Spec.WatcherGroupSize = nil
	groups, _ := watcherGroups(config, nodes)
	if len(groups) != 2 {
		t.Fatal("omitted size did not use 16")
	}
	nodes = nodes[:2]
	nodes[0].Labels[corev1.LabelTopologyRegion] = ""
	groups, _ = watcherGroups(config, nodes)
	if len(groups) != 2 {
		t.Fatal("absent and empty region merged")
	}
	config.Spec.WatcherGroupLabels = []string{blockpernode.GroupAnnotation}
	nodes[0].Labels[blockpernode.GroupAnnotation] = "user-group"
	groups, incomplete := watcherGroups(config, nodes)
	if incomplete != 1 || len(groups) != 1 {
		t.Fatal("custom label handling failed")
	}
	if err := config.Spec.ValidateAll(); err != nil {
		t.Fatalf("group-named user label rejected: %v", err)
	}
}

type assignmentClient struct {
	client.Client
	updates int
	rsError error
}

func (c *assignmentClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	c.updates++
	return c.Client.Update(ctx, object, options...)
}
func (c *assignmentClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, ok := object.(*appsv1.ReplicaSet); ok && c.rsError != nil {
		return c.rsError
	}
	return c.Client.Get(ctx, key, object, options...)
}

func TestNodeAssignmentsPreserveFieldsAndSkipRepeatedWrites(t *testing.T) {
	ctx := context.Background()
	config := watcherConfig()
	node := watcherNode("a")
	node.Annotations = map[string]string{"unrelated": "keep", "sbr.medik8s.io/remediation-owner": "keep"}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	r := watcherReconciler(&node)
	counted := &assignmentClient{Client: r.Client}
	r.Client = counted
	current := &corev1.Node{}
	_ = r.Get(ctx, client.ObjectKey{Name: "a"}, current)
	updated, err := r.updateNodeAssignment(ctx, config, current, blockpernode.Active, "group")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := blockpernode.DecodeOwner(updated.Annotations[blockpernode.OwnerAnnotation])
	generation := owner.OwnershipGeneration
	if updated.Annotations["unrelated"] != "keep" || updated.Annotations["sbr.medik8s.io/remediation-owner"] != "keep" || len(updated.Status.Conditions) != 1 {
		t.Fatal("assignment changed unrelated fields")
	}
	if _, err := r.updateNodeAssignment(ctx, config, updated, blockpernode.Active, "group"); err != nil || counted.updates != 1 {
		t.Fatalf("repeated assignment writes=%d error=%v", counted.updates, err)
	}
	if _, err := r.updateNodeAssignment(ctx, config, current, blockpernode.Active, "other"); !apierrors.IsConflict(err) {
		t.Fatalf("stale version accepted: %v", err)
	}
	updated, err = r.updateNodeAssignment(ctx, config, updated, blockpernode.Conflict, "")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = r.updateNodeAssignment(ctx, config, updated, blockpernode.Active, "group")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ = blockpernode.DecodeOwner(updated.Annotations[blockpernode.OwnerAnnotation])
	if owner.OwnershipGeneration == generation {
		t.Fatal("reactivation retained old generation")
	}
	other := watcherConfig()
	other.UID = "other"
	if _, err := r.updateNodeAssignment(ctx, other, updated, blockpernode.Active, "other"); err == nil {
		t.Fatal("another owner acquired active Node")
	}
}

func probeObjects() (*api.StorageBasedRemediationConfig, *corev1.Node, *appsv1.DaemonSet, *corev1.Pod, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	config := watcherConfig()
	node := watcherNode("a")
	node.CreationTimestamp = metav1.NewTime(time.Unix(10, 0))
	daemon := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "ns", UID: "daemon"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "ns", UID: "pod", CreationTimestamp: metav1.NewTime(time.Unix(20, 0)), OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", UID: daemon.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "a", Volumes: []corev1.Volume{{Name: blockpernode.ProbeVolume, VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{}}}}}, Containers: []corev1.Container{{Name: "sbr-agent", Env: []corev1.EnvVar{{Name: blockpernode.ProbeIntervalEnv, Value: "5s"}, {Name: blockpernode.IOTimeoutEnv, Value: "15s"}, {Name: blockpernode.FailuresEnv, Value: "7"}}}}}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "agent-a-storage-probe", Namespace: "ns", UID: "claim", OwnerReferences: []metav1.OwnerReference{{Kind: "Pod", UID: pod.UID, Controller: ptr.To(true)}}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv", StorageClassName: ptr.To("rwx"), VolumeMode: ptr.To(corev1.PersistentVolumeBlock), AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv", UID: "pv-uid"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "rwx", VolumeMode: ptr.To(corev1.PersistentVolumeBlock), AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, ClaimRef: &corev1.ObjectReference{Name: pvc.Name, Namespace: pvc.Namespace, UID: pvc.UID}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "csi.test"}}}}
	return config, &node, daemon, pod, pvc, pv
}

func TestProbeMappingChecksExactResourceIdentities(t *testing.T) {
	cases := map[string]func(*corev1.Pod, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume){
		"valid": func(*corev1.Pod, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {},
		"old Node Pod": func(p *corev1.Pod, _ *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
			p.CreationTimestamp = metav1.NewTime(time.Unix(1, 0))
		},
		"wrong Pod owner": func(p *corev1.Pod, _ *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
			p.OwnerReferences[0].UID = "old-daemon"
		},
		"wrong claim owner": func(_ *corev1.Pod, p *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
			p.OwnerReferences[0].UID = "other-pod"
		},
		"pending replacement": func(_ *corev1.Pod, p *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
			p.Status.Phase = corev1.ClaimPending
			p.Spec.VolumeName = ""
		},
		"wrong PV claim": func(_ *corev1.Pod, _ *corev1.PersistentVolumeClaim, p *corev1.PersistentVolume) {
			p.Spec.ClaimRef.UID = "old-claim"
		},
		"missing CSI": func(_ *corev1.Pod, _ *corev1.PersistentVolumeClaim, p *corev1.PersistentVolume) { p.Spec.CSI = nil },
		"wrong class": func(_ *corev1.Pod, p *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
			p.Spec.StorageClassName = ptr.To("other")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config, node, daemon, pod, pvc, pv := probeObjects()
			mutate(pod, pvc, pv)
			r := watcherReconciler(pod, pvc, pv)
			mapping, timing, _, err := r.discoverProbe(context.Background(), config, daemon, node, 0)
			if err != nil {
				t.Fatal(err)
			}
			if name == "valid" {
				if mapping == nil || timing == nil || mapping.ClaimUID != "claim" {
					t.Fatal("valid mapping missing")
				}
			} else if mapping != nil {
				t.Fatal("invalid mapping accepted")
			}
		})
	}
}

func TestProbeTimingUsesOwningPodAndPendingRemovesOldDevice(t *testing.T) {
	config, node, daemon, pod, pvc, pv := probeObjects()
	pod.Spec.Containers[0].Env[0].Value = "10s"
	config.Spec.SBRTimeoutSeconds = ptr.To[int32](30)
	r := watcherReconciler(pod, pvc, pv)
	mapping, timing, _, err := r.discoverProbe(context.Background(), config, daemon, node, 0)
	if err != nil || timing == nil || timing.ProbeInterval.Duration != 10*time.Second {
		t.Fatalf("Pod timing not copied: %+v %v", timing, err)
	}
	assignment := blockpernode.WatcherTargets{Version: 1, Configuration: configIdentity(config), Group: "group", Targets: []blockpernode.WatcherTarget{{Name: node.Name, UID: node.UID, OwnershipGeneration: "generation", Probe: mapping, Timing: timing}}}
	bound, _ := buildWatcherDeployment(config, assignment, []string{"host"}, "image")
	pvc.Status.Phase = corev1.ClaimPending
	pvc.Spec.VolumeName = ""
	r = watcherReconciler(pod, pvc, pv)
	assignment.Targets[0].Probe, assignment.Targets[0].Timing, _, _ = r.discoverProbe(context.Background(), config, daemon, node, 0)
	pending, err := buildWatcherDeployment(config, assignment, []string{"host"}, "image")
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.Spec.Template.Spec.Volumes) != 1 || len(pending.Spec.Template.Spec.Volumes) != 0 || len(pending.Spec.Template.Spec.Containers[0].VolumeDevices) != 0 {
		t.Fatal("pending replacement retained old reference")
	}
	pod.Spec.Containers[0].Env[0].ValueFrom = &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}
	r = watcherReconciler(pod, pv)
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Spec.VolumeName = pv.Name
	_ = r.Create(context.Background(), pvc)
	_, timing, _, _ = r.discoverProbe(context.Background(), config, daemon, node, 0)
	if timing != nil {
		t.Fatal("unresolved Pod timing accepted")
	}
}

func csiFor(node corev1.Node) *storagev1.CSINode {
	return &storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: node.Name, OwnerReferences: []metav1.OwnerReference{{Kind: "Node", UID: node.UID}}}, Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{{Name: "csi.test"}}}}
}

func TestHostSelectionIntersectsAllVolumesAndExcludesUnmappedTargets(t *testing.T) {
	a, b, c := watcherNode("a"), watcherNode("b"), watcherNode("c")
	a.Labels["rack"] = "1"
	b.Labels["rack"] = "2"
	c.Labels["rack"] = "2"
	c.Labels["disk"] = "fast"
	_, _, _, _, _, pv := probeObjects()
	pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "rack", Operator: corev1.NodeSelectorOpIn, Values: []string{"2"}}}}}}}
	pv2 := pv.DeepCopy()
	pv2.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions = []corev1.NodeSelectorRequirement{{Key: "disk", Operator: corev1.NodeSelectorOpIn, Values: []string{"fast"}}}
	r := watcherReconciler(csiFor(a), csiFor(b), csiFor(c))
	hosts, err := r.eligibleWatcherHosts(context.Background(), watcherGroup{Nodes: []corev1.Node{a, b}}, []corev1.Node{a, b, c}, []*corev1.PersistentVolume{pv, pv2})
	if err != nil || !reflect.DeepEqual(hosts, []string{"c"}) {
		t.Fatalf("hosts %v %v", hosts, err)
	}
	old := csiFor(c)
	old.OwnerReferences[0].UID = "old-node"
	r = watcherReconciler(old)
	hosts, err = r.eligibleWatcherHosts(context.Background(), watcherGroup{Nodes: []corev1.Node{a, b}}, []corev1.Node{a, b, c}, []*corev1.PersistentVolume{pv})
	if err != nil || len(hosts) != 0 {
		t.Fatalf("old CSI owner accepted: %v %v", hosts, err)
	}
}

func TestWatcherTemplatesAreStableAndCheckDevicePaths(t *testing.T) {
	config, node, daemon, pod, pvc, pv := probeObjects()
	r := watcherReconciler(pod, pvc, pv)
	mapping, timing, _, _ := r.discoverProbe(context.Background(), config, daemon, node, 0)
	w := blockpernode.WatcherTargets{Version: 1, Configuration: configIdentity(config), Group: "group", Targets: []blockpernode.WatcherTarget{{Name: node.Name, UID: node.UID, OwnershipGeneration: "generation", Probe: mapping, Timing: timing}}}
	first, err := buildWatcherDeployment(config, w, []string{"host"}, "image")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := buildWatcherDeployment(config, w, []string{"host"}, "image")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("reconstruction changed template")
	}
	if *first.Spec.Replicas != 1 || first.Spec.Strategy.RollingUpdate.MaxSurge.IntVal != 0 || first.Spec.Strategy.RollingUpdate.MaxUnavailable.IntVal != 1 || !first.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly {
		t.Fatal("unsafe watcher workload settings")
	}
	first.Spec.Template.Spec.Containers[0].VolumeDevices[0].DevicePath = "/dev/wrong"
	if validateWatcherDevices(w, &first.Spec.Template.Spec) == nil {
		t.Fatal("payload/device mismatch accepted")
	}
	empty, _ := buildWatcherDeployment(config, w, nil, "image")
	terms := empty.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchFields) != 0 || len(terms[0].MatchExpressions) != 0 {
		t.Fatal("no-host template is not an empty term")
	}
}

func currentWatcherFixture() (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	config := watcherConfig()
	d, _ := buildWatcherDeployment(config, blockpernode.WatcherTargets{Version: 1, Configuration: configIdentity(config), Group: "group"}, []string{"host"}, "image")
	d.UID = "deployment"
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "ns", UID: "replicaset", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: d.UID, Controller: ptr.To(true)}}}, Spec: appsv1.ReplicaSetSpec{Template: *d.Spec.Template.DeepCopy()}}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "watcher", Namespace: "ns", UID: "pod", Labels: d.Spec.Selector.MatchLabels, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "host"}}
	return d, rs, p
}

func TestReadinessDistinguishesMissingReplicaSetFromReadErrors(t *testing.T) {
	d, _, pod := currentWatcherFixture()
	r := watcherReconciler(pod)
	current, err := r.currentWatcherPod(context.Background(), pod, d)
	if current || err != nil {
		t.Fatalf("missing ReplicaSet result %t %v", current, err)
	}
	r.Client = &assignmentClient{Client: r.Client, rsError: fmt.Errorf("cache read failed")}
	_, err = r.watcherGroupReadiness(context.Background(), d, []string{"host"}, newStructuralResult())
	if err == nil {
		t.Fatal("ReplicaSet read error was discarded")
	}
	result := newStructuralResult()
	result.unavailable = true
	status, reason, _ := result.condition()
	if status != metav1.ConditionUnknown || reason != "StateUnavailable" {
		t.Fatal("unavailable state did not remain Unknown")
	}
}

func TestReadinessUsesCurrentPodEventsAndRepairsPlacementOnce(t *testing.T) {
	ctx := context.Background()
	d, rs, pod := currentWatcherFixture()
	old := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "ns"}, InvolvedObject: corev1.ObjectReference{UID: "old-pod"}, Reason: "FailedMount"}
	r := watcherReconciler(d, rs, pod, old)
	if err := r.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatalf("read Deployment: %v", err)
	}
	if d.Spec.Template.Annotations != nil {
		t.Fatalf("client returned nonempty annotations: %+v", d.Spec.Template.Annotations)
	}
	if current, err := r.currentWatcherPod(ctx, pod, d); err != nil || !current {
		t.Fatalf("current Pod recognition: current=%t error=%v", current, err)
	}
	result := newStructuralResult()
	_, err := r.watcherGroupReadiness(ctx, d, []string{"host"}, result)
	if err != nil {
		t.Fatalf("evaluate old Pod Event: %v", err)
	}
	if result.failures["VolumeSetupFailed"] {
		t.Fatalf("old Pod Event changed readiness: %+v", result.failures)
	}
	current := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: pod.Namespace}, InvolvedObject: corev1.ObjectReference{UID: pod.UID}, Reason: "FailedMount"}
	if err := r.Create(ctx, current); err != nil {
		t.Fatalf("create current mount Event: %v", err)
	}
	result = newStructuralResult()
	if _, err := r.watcherGroupReadiness(ctx, d, []string{"host"}, result); err != nil {
		t.Fatalf("evaluate current mount Event: %v", err)
	}
	if !result.failures["VolumeSetupFailed"] {
		t.Fatalf("current mount Event ignored: %+v", result.failures)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatalf("set current Pod Ready: %v", err)
	}
	result = newStructuralResult()
	ready, err := r.watcherGroupReadiness(ctx, d, []string{"host"}, result)
	if err != nil || !ready || result.failures["VolumeSetupFailed"] {
		t.Fatalf("old Event overrode Ready: ready=%t failures=%+v error=%v", ready, result.failures, err)
	}
	counted := &assignmentClient{Client: r.Client}
	r.Client = counted
	result = newStructuralResult()
	_, err = r.watcherGroupReadiness(ctx, d, []string{"other"}, result)
	if err != nil {
		t.Fatal(err)
	}
	if d.Spec.Template.Annotations[placementRepair] != string(pod.UID) || counted.updates != 1 {
		t.Fatal("disallowed host did not request one replacement")
	}
	_, err = r.watcherGroupReadiness(ctx, d, []string{"other"}, newStructuralResult())
	if err != nil || counted.updates != 1 {
		t.Fatalf("old-template Pod caused repeated replacement: updates=%d error=%v", counted.updates, err)
	}
}

func TestScheduledWatcherIgnoresEarlierSchedulingFailure(t *testing.T) {
	ctx := context.Background()
	d, rs, pod := currentWatcherFixture()
	pod.Spec.NodeName = ""
	scheduling := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "scheduling", Namespace: pod.Namespace}, InvolvedObject: corev1.ObjectReference{UID: pod.UID}, Reason: "FailedScheduling"}
	r := watcherReconciler(d, rs, pod, scheduling)
	if err := r.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatalf("read Deployment: %v", err)
	}
	result := newStructuralResult()
	if _, err := r.watcherGroupReadiness(ctx, d, []string{"host"}, result); err != nil {
		t.Fatalf("evaluate unscheduled Pod: %v", err)
	}
	if _, reason, _ := result.condition(); reason != "SchedulingFailed" {
		t.Fatalf("unscheduled reason=%s failures=%+v", reason, result.failures)
	}
	pod.Spec.NodeName = "host"
	if err := r.Update(ctx, pod); err != nil {
		t.Fatalf("schedule the current Pod: %v", err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatalf("set current Pod scheduled: %v", err)
	}
	mount := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "mount", Namespace: pod.Namespace}, InvolvedObject: corev1.ObjectReference{UID: pod.UID}, Reason: "FailedMount"}
	if err := r.Create(ctx, mount); err != nil {
		t.Fatalf("create current mount Event: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(scheduling), &corev1.Event{}); err != nil {
		t.Fatalf("earlier scheduling Event disappeared: %v", err)
	}
	result = newStructuralResult()
	if _, err := r.watcherGroupReadiness(ctx, d, []string{"host"}, result); err != nil {
		t.Fatalf("evaluate scheduled Pod mount failure: %v", err)
	}
	if result.failures["SchedulingFailed"] {
		t.Fatalf("scheduled Pod retained scheduling failure: %+v", result.failures)
	}
	if _, reason, _ := result.condition(); reason != "VolumeSetupFailed" {
		t.Fatalf("scheduled reason=%s failures=%+v", reason, result.failures)
	}
}

func TestWatcherStructuralReasonsAndNodeEventFilter(t *testing.T) {
	for _, reason := range []string{"ConfigurationConflict", "AssignmentIncomplete", "ProbeClaimUnavailable", "NoEligibleHost", "SchedulingFailed", "VolumeSetupFailed", "PodNotReady"} {
		s := newStructuralResult()
		s.failures[reason] = true
		status, got, _ := s.condition()
		if status != metav1.ConditionFalse || got != reason {
			t.Fatalf("reason %s became %s", reason, got)
		}
	}
	s := newStructuralResult()
	if status, reason, _ := s.condition(); status != metav1.ConditionTrue || reason != "NoTargets" {
		t.Fatal("empty configuration not NoTargets")
	}
	s.targets, s.mapped, s.groups, s.ready = 2, 2, 1, 1
	if _, reason, _ := s.condition(); reason != "Ready" {
		t.Fatal("complete resources not Ready")
	}
	s.failures["ProbeClaimUnavailable"], s.failures["AssignmentIncomplete"] = true, true
	if _, reason, _ := s.condition(); reason != "AssignmentIncomplete" {
		t.Fatal("reason order changed")
	}
	a := watcherNode("a")
	b := a.DeepCopy()
	b.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue}}
	predicate := blockPerNodeNodePredicate()
	if predicate.Update(event.UpdateEvent{ObjectOld: &a, ObjectNew: b}) {
		t.Fatal("unrelated Node condition caused grouping")
	}
	b.Labels["rack"] = "2"
	if !predicate.Update(event.UpdateEvent{ObjectOld: &a, ObjectNew: b}) {
		t.Fatal("label change ignored")
	}
}

func TestShutdownInventoryTracksPendingStartAndContainerRestarts(t *testing.T) {
	status := &api.RetirementStatus{}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "ns", UID: "pod", ResourceVersion: "1"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "sbr-watcher", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}}}
	collectShutdownChecks(status, pod, "node", true)
	if len(status.ShutdownChecks) != 0 {
		t.Fatal("Pending container created an empty instance obligation")
	}
	status.InventoryComplete = true
	pod.Status.ContainerStatuses[0].ContainerID = "runtime://one"
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	collectShutdownChecks(status, pod, "node", true)
	if len(status.ShutdownChecks) != 1 {
		t.Fatal("started instance was not recorded")
	}
	if status.InventoryComplete {
		t.Fatal("newly started instance retained old inventory completeness")
	}
	status.InventoryComplete = true
	collectShutdownChecks(status, pod, "node", true)
	if !status.InventoryComplete {
		t.Fatal("unchanged instance invalidated authoritative inventory")
	}
	pod.ResourceVersion = "2"
	pod.Status.ContainerStatuses[0].ContainerID = "runtime://two"
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: "runtime://one"}}
	collectShutdownChecks(status, pod, "node", true)
	if status.InventoryComplete {
		t.Fatal("restarted container retained old inventory completeness")
	}
	if len(status.ShutdownChecks) != 2 || status.ShutdownChecks[0].TerminationEvidence == nil || status.ShutdownChecks[0].ContainerID != "runtime://one" {
		t.Fatal("restart lost the old process evidence")
	}
	encoded, _ := json.Marshal(status)
	restored := &api.RetirementStatus{}
	_ = json.Unmarshal(encoded, restored)
	collectShutdownChecks(restored, pod, "node", true)
	if len(restored.ShutdownChecks) != 2 {
		t.Fatal("controller restart duplicated instance obligations")
	}
	never := pod.DeepCopy()
	never.UID = "never"
	never.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "sbr-watcher", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "StartError"}}}}
	collectShutdownChecks(restored, never, "node", true)
	if len(restored.ShutdownChecks) != 2 {
		t.Fatal("never-started container created an empty obligation")
	}
}

func TestRetirementPersistsChecksBeforeDeletingWatcherAndRetainsPeers(t *testing.T) {
	ctx := context.Background()
	config := watcherConfig()
	node := watcherNode("a")
	ownerFor(config, &node, "group")
	d, _, pod := currentWatcherFixture()
	d.OwnerReferences = []metav1.OwnerReference{{UID: config.UID, Controller: ptr.To(true)}}
	pod.Labels[blockpernode.ConfigurationUIDLabel] = string(config.UID)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "sbr-watcher", ContainerID: "runtime://running"}}
	daemon := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "ns", Labels: map[string]string{blockpernode.ConfigurationUIDLabel: string(config.UID)}}}
	r := watcherReconciler(config, &node, d, pod, daemon)
	_ = r.Get(ctx, client.ObjectKeyFromObject(config), config)
	if _, err := r.retireBlockPerNode(ctx, config, true); err != nil {
		t.Fatal(err)
	}
	live := &api.StorageBasedRemediationConfig{}
	_ = r.Get(ctx, client.ObjectKeyFromObject(config), live)
	if live.Status.Retirement == nil || len(live.Status.Retirement.ShutdownChecks) != 1 || live.Status.Retirement.InventoryComplete || len(live.Finalizers) != 1 {
		t.Fatal("retirement lost outstanding inventory or finalizer")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(daemon), &appsv1.DaemonSet{}); err != nil {
		t.Fatal("retirement removed peers before release")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(d), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("watcher not removed: %v", err)
	}
	restarted := &StorageBasedRemediationConfigReconciler{Client: r.Client, Scheme: r.Scheme}
	if _, err := restarted.retireBlockPerNode(ctx, live, true); err != nil {
		t.Fatal(err)
	}
	if len(live.Status.Retirement.ShutdownChecks) != 1 {
		t.Fatal("controller restart lost retained instance")
	}
}

func TestPermissionsCleanupPreservesOtherConfiguration(t *testing.T) {
	ctx := context.Background()
	first := watcherConfig()
	second := watcherConfig()
	second.Namespace = "other-ns"
	second.UID = "second-uid"
	r := watcherReconciler()
	for _, config := range []*api.StorageBasedRemediationConfig{first, second} {
		if err := r.ensureWatcherPermissions(ctx, config); err != nil {
			t.Fatal(err)
		}
		if err := r.ensurePeerPermissions(ctx, config); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.removePerNodePermissions(ctx, first); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{watcherResourceName(second), peerResourceName(second)} {
		if err := r.Get(ctx, client.ObjectKey{Namespace: second.Namespace, Name: name}, &corev1.ServiceAccount{}); err != nil {
			t.Fatal("cleanup removed another service account")
		}
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &rbacv1.ClusterRoleBinding{}); err != nil {
			t.Fatal("cleanup removed another binding")
		}
	}
}

func TestPartialRetirementExcludesOnlyNamedNodes(t *testing.T) {
	config := watcherConfig()
	r := watcherReconciler()
	daemon := r.buildDaemonSet(config, "image")
	configurePerNodeAgent(daemon, config)
	checks := []api.RetirementReleaseCheck{{NodeName: "a"}, {NodeName: "b"}}
	excludeRetiringTargets(daemon, checks)
	before := daemon.DeepCopy()
	excludeRetiringTargets(daemon, checks)
	if !reflect.DeepEqual(before.Spec, daemon.Spec) {
		t.Fatal("repeated exclusion changed the template")
	}
	terms := daemon.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	for _, term := range terms {
		if len(term.MatchFields) != 2 {
			t.Fatalf("expected two Node-name exclusions, got %+v", term.MatchFields)
		}
		for _, field := range term.MatchFields {
			if field.Key != "metadata.name" || field.Operator != corev1.NodeSelectorOpNotIn || len(field.Values) != 1 {
				t.Fatalf("invalid single-name exclusion: %+v", field)
			}
		}
	}
}

func TestRetirementStopsFutureAgentPodsAfterReleaseBeforeInventoryCompletes(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		name := "removed-node"
		if deleting {
			name = "configuration-deletion"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			config := watcherConfig()
			oldConfig := config.DeepCopy()
			config.Spec.NodeSelector = map[string]string{"selected": "true"}
			removed, retained := watcherNode("removed"), watcherNode("retained")
			retained.Labels["selected"] = "true"
			ownerFor(config, &removed, "removed-group")
			ownerFor(config, &retained, "retained-group")
			checks := []api.RetirementReleaseCheck{{NodeName: removed.Name, NodeUID: string(removed.UID), OwnershipGeneration: "generation"}}
			if deleting {
				checks = append(checks, api.RetirementReleaseCheck{NodeName: retained.Name, NodeUID: string(retained.UID), OwnershipGeneration: "generation"})
			}
			config.Status.Retirement = &api.RetirementStatus{Phase: blockpernode.Retiring, ReleaseChecks: checks}
			watcherDeployment := func(name, group string) *appsv1.Deployment {
				return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.Namespace, UID: types.UID(name), Labels: map[string]string{blockpernode.ConfigurationUIDLabel: string(config.UID), "sbr.medik8s.io/watcher-group": group}, OwnerReferences: []metav1.OwnerReference{{UID: config.UID, Controller: ptr.To(true)}}}}
			}
			removedWatcher := watcherDeployment("removed-watcher", "removed-group")
			retainedWatcher := watcherDeployment("retained-watcher", "retained-group")
			r := watcherReconciler(config, &removed, &retained, removedWatcher, retainedWatcher)
			daemon := r.buildDaemonSet(oldConfig, "image")
			configurePerNodeAgent(daemon, oldConfig)
			if err := r.Create(ctx, daemon); err != nil {
				t.Fatal(err)
			}
			originalDaemon := daemon.DeepCopy()
			if err := r.ensureWatcherPermissions(ctx, config); err != nil {
				t.Fatal(err)
			}
			if err := r.ensurePeerPermissions(ctx, config); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
				t.Fatal(err)
			}
			if _, err := r.retireBlockPerNode(ctx, config, deleting); err != nil {
				t.Fatal(err)
			}
			if config.Status.Retirement.Phase != blockpernode.Retiring {
				t.Fatal("incomplete peer release entered Shutdown")
			}
			currentDaemon := &appsv1.DaemonSet{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(daemon), currentDaemon); err != nil || !reflect.DeepEqual(originalDaemon.Spec, currentDaemon.Spec) {
				t.Fatalf("incomplete release changed agent scheduling: %v", err)
			}
			for index := range config.Status.Retirement.ReleaseChecks {
				config.Status.Retirement.ReleaseChecks[index].Completed = true
			}
			if err := r.Status().Update(ctx, config); err != nil {
				t.Fatal(err)
			}
			if _, err := r.retireBlockPerNode(ctx, config, deleting); err != nil {
				t.Fatal(err)
			}
			if config.Status.Retirement.Phase != blockpernode.Shutdown || config.Status.Retirement.InventoryComplete {
				t.Fatal("completed release did not permit Shutdown before inventory closure")
			}
			if _, err := r.retireBlockPerNode(ctx, config, deleting); err != nil {
				t.Fatal(err)
			}
			err := r.Get(ctx, client.ObjectKeyFromObject(daemon), currentDaemon)
			if deleting {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("Shutdown retained the full DaemonSet: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				terms := currentDaemon.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
				for _, term := range terms {
					if len(term.MatchFields) != 1 || term.MatchFields[0].Key != "metadata.name" || term.MatchFields[0].Operator != corev1.NodeSelectorOpNotIn || !reflect.DeepEqual(term.MatchFields[0].Values, []string{removed.Name}) {
						t.Fatalf("partial retirement did not exclude only the removed Node: %+v", term.MatchFields)
					}
				}
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(removedWatcher), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
				t.Fatalf("removed group watcher remains: %v", err)
			}
			err = r.Get(ctx, client.ObjectKeyFromObject(retainedWatcher), &appsv1.Deployment{})
			if deleting && !apierrors.IsNotFound(err) {
				t.Fatalf("configuration deletion retained a watcher: %v", err)
			}
			if !deleting && err != nil {
				t.Fatalf("partial retirement removed the unaffected watcher: %v", err)
			}
			restarted := &StorageBasedRemediationConfigReconciler{Client: r.Client, Scheme: r.Scheme}
			if _, err := restarted.retireBlockPerNode(ctx, config, deleting); err != nil {
				t.Fatal(err)
			}
			live := &api.StorageBasedRemediationConfig{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(config), live); err != nil {
				t.Fatal(err)
			}
			if live.Status.Retirement.InventoryComplete || live.Status.Retirement.Phase != blockpernode.Shutdown || len(live.Finalizers) != 1 {
				t.Fatal("incomplete inventory released configuration retirement")
			}
			for _, target := range []*corev1.Node{&removed, &retained} {
				liveNode := &corev1.Node{}
				if err := r.Get(ctx, client.ObjectKeyFromObject(target), liveNode); err != nil {
					t.Fatal(err)
				}
				owner, err := blockpernode.DecodeOwner(liveNode.Annotations[blockpernode.OwnerAnnotation])
				if err != nil {
					t.Fatal(err)
				}
				phase, group := blockpernode.Shutdown, "removed-group"
				if target.Name == retained.Name {
					group = "retained-group"
					if !deleting {
						phase = blockpernode.Active
					}
				}
				if owner.Phase != phase || liveNode.Annotations[blockpernode.GroupAnnotation] != group {
					t.Fatalf("inventory gap released or changed Node %s: %+v", target.Name, liveNode.Annotations)
				}
			}
			for _, name := range []string{watcherResourceName(config), peerResourceName(config)} {
				if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: name}, &corev1.ServiceAccount{}); err != nil {
					t.Fatal("inventory gap removed service account")
				}
				if err := r.Get(ctx, client.ObjectKey{Name: name}, &rbacv1.ClusterRoleBinding{}); err != nil {
					t.Fatal("inventory gap removed permission binding")
				}
			}
		})
	}
}

func TestWatcherStatusSkipsUnchangedResultAndTracksGeneration(t *testing.T) {
	ctx := context.Background()
	config := watcherConfig()
	r := watcherReconciler(config)
	if err := r.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
		t.Fatalf("read configuration: %v", err)
	}
	result := newStructuralResult()
	if err := r.writeWatcherStatus(ctx, config, result); err != nil {
		t.Fatal(err)
	}
	version := config.ResourceVersion
	condition := config.GetCondition(api.SBRConfigConditionWatcherResourcesReady)
	if condition == nil {
		t.Fatal("initial readiness condition is absent")
	}
	transition := condition.LastTransitionTime
	if err := r.writeWatcherStatus(ctx, config, result); err != nil {
		t.Fatalf("write unchanged status: %v", err)
	}
	if config.ResourceVersion != version {
		t.Fatalf("unchanged status changed resourceVersion: previous=%s current=%s", version, config.ResourceVersion)
	}
	config.Spec.WatcherGroupSize = ptr.To[int32](17)
	expectedGeneration := config.Generation + 1
	config.Generation = expectedGeneration
	if err := r.Update(ctx, config); err != nil {
		t.Fatalf("update stored configuration generation: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
		t.Fatalf("read updated configuration: %v", err)
	}
	if config.Generation != expectedGeneration {
		t.Fatalf("stored generation=%d expected=%d", config.Generation, expectedGeneration)
	}
	if err := r.writeWatcherStatus(ctx, config, result); err != nil {
		t.Fatal(err)
	}
	condition = config.GetCondition(api.SBRConfigConditionWatcherResourcesReady)
	if condition == nil {
		t.Fatal("updated readiness condition is absent")
	}
	if condition.ObservedGeneration != config.Generation {
		t.Fatalf("observedGeneration=%d configuration generation=%d", condition.ObservedGeneration, config.Generation)
	}
	if !condition.LastTransitionTime.Equal(&transition) {
		t.Fatalf("same status changed transition time: previous=%s current=%s", transition.Time.Format(time.RFC3339Nano), condition.LastTransitionTime.Time.Format(time.RFC3339Nano))
	}
}

func TestConfigurationConflictIncludesOtherNamespacesAndStorageClasses(t *testing.T) {
	ctx := context.Background()
	a, b := watcherNode("a"), watcherNode("b")
	first, second := watcherConfig(), watcherConfig()
	second.Name = "second"
	second.Namespace = "other"
	second.UID = "second"
	first.Spec.NodeSelector = map[string]string{"target": "first"}
	second.Spec.NodeSelector = map[string]string{"target": "second"}
	a.Labels["target"] = "first"
	b.Labels["target"] = "second"
	second.Spec.SharedStorageClass = "other-class"
	r := watcherReconciler(first, second)
	for _, config := range []*api.StorageBasedRemediationConfig{first, second} {
		conflict, err := r.configurationConflicts(ctx, config, []corev1.Node{a, b})
		if err != nil || !conflict {
			t.Fatalf("disjoint class conflict missing: %t %v", conflict, err)
		}
	}
	second.Spec.SharedStorageClass = "rwx"
	second.Spec.NodeSelector = first.Spec.NodeSelector
	r = watcherReconciler(first, second)
	conflict, err := r.configurationConflicts(ctx, first, []corev1.Node{a, b})
	if err != nil || !conflict {
		t.Fatalf("overlap conflict missing: %t %v", conflict, err)
	}
	a.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	if len(selectedNodes(first, []corev1.Node{a, b})) != 1 {
		t.Fatal("NotReady target left selection")
	}
}
