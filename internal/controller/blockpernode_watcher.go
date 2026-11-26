package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

const placementRepair = "sbr.medik8s.io/placement-repair-for"

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods;persistentvolumeclaims;persistentvolumes;events,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses;csinodes;csidrivers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete

type watcherGroup struct {
	ID    string
	Nodes []corev1.Node
}
type topologyValue struct {
	Key     string
	Present bool
	Value   string
}

func watcherGroups(config *api.StorageBasedRemediationConfig, nodes []corev1.Node) ([]watcherGroup, int) {
	keys := []string{corev1.LabelTopologyRegion, corev1.LabelTopologyZone}
	custom := append([]string(nil), config.Spec.GetWatcherGroupLabels()...)
	sort.Strings(custom)
	for _, key := range custom {
		if key != corev1.LabelTopologyRegion && key != corev1.LabelTopologyZone {
			keys = append(keys, key)
		}
	}
	partitions := map[string][]corev1.Node{}
	incomplete := 0
	for _, node := range nodes {
		missing := node.UID == ""
		for _, key := range custom {
			if _, ok := node.Labels[key]; !ok {
				missing = true
			}
		}
		if missing {
			incomplete++
			continue
		}
		values := make([]topologyValue, 0, len(keys))
		for _, key := range keys {
			value, present := node.Labels[key]
			values = append(values, topologyValue{Key: key, Present: present, Value: value})
		}
		encoded, _ := json.Marshal(values)
		partitions[string(encoded)] = append(partitions[string(encoded)], node)
	}
	partitionKeys := make([]string, 0, len(partitions))
	for key := range partitions {
		partitionKeys = append(partitionKeys, key)
	}
	sort.Strings(partitionKeys)
	var groups []watcherGroup
	size := int(config.Spec.GetWatcherGroupSize())
	for _, partition := range partitionKeys {
		members := partitions[partition]
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
		for offset := 0; offset < len(members); offset += size {
			data, _ := json.Marshal([]string{string(config.UID), partition, strconv.Itoa(offset / size)})
			hash := sha256.Sum256(data)
			groups = append(groups, watcherGroup{ID: hex.EncodeToString(hash[:16]), Nodes: members[offset:min(offset+size, len(members))]})
		}
	}
	return groups, incomplete
}

func configIdentity(c *api.StorageBasedRemediationConfig) blockpernode.ConfigurationIdentity {
	return blockpernode.ConfigurationIdentity{Namespace: c.Namespace, Name: c.Name, UID: c.UID}
}

func (r *StorageBasedRemediationConfigReconciler) liveReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func selectedNodes(config *api.StorageBasedRemediationConfig, nodes []corev1.Node) []corev1.Node {
	selector := labels.SelectorFromSet(config.Spec.GetNodeSelector())
	var selected []corev1.Node
	for _, node := range nodes {
		if node.DeletionTimestamp == nil && node.Labels[corev1.LabelOSStable] == "linux" && selector.Matches(labels.Set(node.Labels)) {
			selected = append(selected, node)
		}
	}
	return selected
}

func (r *StorageBasedRemediationConfigReconciler) configurationConflicts(ctx context.Context, config *api.StorageBasedRemediationConfig, nodes []corev1.Node) (bool, error) {
	var configs api.StorageBasedRemediationConfigList
	if err := r.List(ctx, &configs); err != nil {
		return false, err
	}
	ours := selectedNodes(config, nodes)
	names := map[string]bool{}
	for _, n := range ours {
		names[n.Name] = true
	}
	for i := range configs.Items {
		other := &configs.Items[i]
		if other.UID == config.UID || other.DeletionTimestamp != nil {
			continue
		}
		if other.Spec.IsBlockPerNodeMode() && config.Spec.SharedStorageClass != other.Spec.SharedStorageClass {
			return true, nil
		}
		for _, n := range selectedNodes(other, nodes) {
			if names[n.Name] {
				return true, nil
			}
		}
	}
	return false, nil
}

// updateNodeAssignment reads the live Node and updates both owned annotations together.
func (r *StorageBasedRemediationConfigReconciler) updateNodeAssignment(ctx context.Context, config *api.StorageBasedRemediationConfig, expected *corev1.Node, phase, group string) (*corev1.Node, error) {
	node := &corev1.Node{}
	if err := r.liveReader().Get(ctx, types.NamespacedName{Name: expected.Name}, node); err != nil {
		return nil, err
	}
	if node.UID != expected.UID || node.ResourceVersion != expected.ResourceVersion {
		return nil, apierrors.NewConflict(corev1.Resource("nodes"), node.Name, fmt.Errorf("Node changed before assignment update"))
	}
	oldValue := node.Annotations[blockpernode.OwnerAnnotation]
	owner := blockpernode.ConfigurationOwner{}
	if oldValue != "" {
		var err error
		owner, err = blockpernode.DecodeOwner(oldValue)
		if err != nil || owner.NodeUID != node.UID {
			return nil, fmt.Errorf("invalid existing owner on Node %s", node.Name)
		}
		if owner.Configuration != configIdentity(config) && owner.Phase != blockpernode.Released {
			return nil, fmt.Errorf("Node %s retains another configuration owner", node.Name)
		}
	}
	if phase != blockpernode.Active && owner.Configuration != configIdentity(config) {
		return nil, fmt.Errorf("Node %s is not owned by this configuration", node.Name)
	}
	if owner.Configuration != configIdentity(config) || owner.Phase == blockpernode.Released || (phase == blockpernode.Active && owner.Phase != blockpernode.Active) {
		if owner.Configuration == configIdentity(config) && owner.Phase != blockpernode.Conflict && owner.Phase != blockpernode.Released && oldValue != "" {
			return nil, fmt.Errorf("Node %s has not completed retirement", node.Name)
		}
		owner.OwnershipGeneration = string(uuid.NewUUID())
	}
	owner.Version, owner.NodeUID, owner.Configuration, owner.Phase = 1, node.UID, configIdentity(config), phase
	encoded, _ := json.Marshal(owner)
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if node.Annotations[blockpernode.OwnerAnnotation] == string(encoded) && node.Annotations[blockpernode.GroupAnnotation] == group {
		return node, nil
	}
	node.Annotations[blockpernode.OwnerAnnotation] = string(encoded)
	if group == "" {
		delete(node.Annotations, blockpernode.GroupAnnotation)
	} else {
		node.Annotations[blockpernode.GroupAnnotation] = group
	}
	if err := r.Update(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

type structuralResult struct {
	targets, mapped, groups, ready int
	failures                       map[string]bool
	unavailable                    bool
}

func newStructuralResult() *structuralResult { return &structuralResult{failures: map[string]bool{}} }

func (s *structuralResult) condition() (metav1.ConditionStatus, string, string) {
	message := fmt.Sprintf("targets=%d mappedProbes=%d groups=%d readyGroups=%d", s.targets, s.mapped, s.groups, s.ready)
	if s.failures["ConfigurationConflict"] {
		return metav1.ConditionFalse, "ConfigurationConflict", message
	}
	if s.unavailable {
		return metav1.ConditionUnknown, "StateUnavailable", message
	}
	for _, reason := range []string{"AssignmentIncomplete", "ProbeClaimUnavailable", "NoEligibleHost", "SchedulingFailed", "VolumeSetupFailed", "PodNotReady"} {
		if s.failures[reason] {
			return metav1.ConditionFalse, reason, message
		}
	}
	if s.targets == 0 {
		return metav1.ConditionTrue, "NoTargets", message
	}
	return metav1.ConditionTrue, "Ready", message
}

func (r *StorageBasedRemediationConfigReconciler) writeWatcherStatus(ctx context.Context, config *api.StorageBasedRemediationConfig, result *structuralResult) error {
	status, reason, message := result.condition()
	previous := config.GetCondition(api.SBRConfigConditionWatcherResourcesReady)
	if previous != nil && previous.Status == status && previous.Reason == reason && previous.Message == message && previous.ObservedGeneration == config.Generation {
		return nil
	}
	config.SetCondition(api.SBRConfigConditionWatcherResourcesReady, status, reason, message)
	return r.Status().Update(ctx, config)
}

func watcherResourceName(config *api.StorageBasedRemediationConfig) string {
	hash := sha256.Sum256([]byte(config.UID))
	return "sbr-watcher-" + hex.EncodeToString(hash[:16])
}

func (r *StorageBasedRemediationConfigReconciler) ensureWatcherPermissions(ctx context.Context, config *api.StorageBasedRemediationConfig) error {
	name := watcherResourceName(config)
	managed := map[string]string{blockpernode.ConfigurationUIDLabel: string(config.UID), "app.kubernetes.io/managed-by": SBROperatorName}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		if sa.ResourceVersion != "" && sa.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) {
			return fmt.Errorf("watcher service account belongs to another owner")
		}
		sa.Labels = managed
		return nil
	})
	if err != nil {
		return err
	}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		if binding.ResourceVersion != "" && binding.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) {
			return fmt.Errorf("watcher binding belongs to another owner")
		}
		binding.Labels = managed
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "sbr-operator-sbr-watcher-role"}
		binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: config.Namespace}}
		return nil
	})
	return err
}

func (r *StorageBasedRemediationConfigReconciler) reconcileBlockPerNode(ctx context.Context, config *api.StorageBasedRemediationConfig, image string) (ctrl.Result, error) {
	result := newStructuralResult()
	requeue := ctrl.Result{RequeueAfter: 30 * time.Second}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		result.unavailable = true
		_ = r.writeWatcherStatus(ctx, config, result)
		return requeue, err
	}
	targets := selectedNodes(config, nodes.Items)
	result.targets = len(targets)
	conflict, err := r.configurationConflicts(ctx, config, nodes.Items)
	if err != nil {
		result.unavailable = true
		_ = r.writeWatcherStatus(ctx, config, result)
		return requeue, err
	}
	selected := map[types.UID]bool{}
	for _, node := range targets {
		selected[node.UID] = true
	}
	var removed []corev1.Node
	for _, node := range nodes.Items {
		owner, err := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
		if err == nil && owner.NodeUID == node.UID && owner.Configuration == configIdentity(config) && owner.Phase != blockpernode.Released && !selected[node.UID] {
			removed = append(removed, node)
		}
	}
	if config.Status.Retirement != nil || len(removed) > 0 {
		return r.retireBlockPerNode(ctx, config, false)
	}
	if conflict {
		result.failures["ConfigurationConflict"] = true
		for i := range nodes.Items {
			node := &nodes.Items[i]
			owner, e := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
			if e == nil && owner.Configuration == configIdentity(config) && owner.NodeUID == node.UID && owner.Phase == blockpernode.Active {
				if _, e = r.updateNodeAssignment(ctx, config, node, blockpernode.Conflict, ""); e != nil {
					return requeue, e
				}
			}
		}
		return requeue, r.writeWatcherStatus(ctx, config, result)
	}
	groups, incomplete := watcherGroups(config, targets)
	result.groups = len(groups)
	result.failures["AssignmentIncomplete"] = incomplete > 0
	// Targets without required labels retain ownership, but receive no group assignment.
	assigned := map[types.UID]string{}
	for _, group := range groups {
		for _, node := range group.Nodes {
			assigned[node.UID] = group.ID
		}
	}
	updated := map[types.UID]corev1.Node{}
	for i := range targets {
		node, e := r.updateNodeAssignment(ctx, config, &targets[i], blockpernode.Active, assigned[targets[i].UID])
		if e != nil {
			result.failures["ConfigurationConflict"] = true
			_ = r.writeWatcherStatus(ctx, config, result)
			return requeue, e
		}
		updated[node.UID] = *node
	}
	if err := r.ensureWatcherPermissions(ctx, config); err != nil {
		return requeue, err
	}
	if err := r.ensurePeerPermissions(ctx, config); err != nil {
		return requeue, err
	}
	daemon := r.buildDaemonSet(config, image)
	configurePerNodeAgent(daemon, config)
	actual := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: daemon.Name, Namespace: daemon.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, actual, func() error {
		if actual.ResourceVersion != "" && actual.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) {
			return fmt.Errorf("agent DaemonSet owner does not match")
		}
		actual.Labels, actual.Spec = daemon.Labels, daemon.Spec
		actual.OwnerReferences = nil
		return nil
	})
	if err != nil {
		return requeue, err
	}
	wanted := map[string]bool{}
	for _, group := range groups {
		for i := range group.Nodes {
			group.Nodes[i] = updated[group.Nodes[i].UID]
		}
		assignment := blockpernode.WatcherTargets{Version: 1, Configuration: configIdentity(config), Group: group.ID}
		var pvs []*corev1.PersistentVolume
		for index, node := range group.Nodes {
			owner, _ := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
			target := blockpernode.WatcherTarget{Name: node.Name, UID: node.UID, OwnershipGeneration: owner.OwnershipGeneration}
			mapping, timing, pv, e := r.discoverProbe(ctx, config, actual, &node, index)
			if e != nil {
				result.unavailable = true
			}
			target.Probe, target.Timing = mapping, timing
			if mapping == nil {
				result.failures["ProbeClaimUnavailable"] = true
			} else {
				result.mapped++
				pvs = append(pvs, pv)
				if timing == nil {
					result.failures["AssignmentIncomplete"] = true
				}
			}
			assignment.Targets = append(assignment.Targets, target)
		}
		hosts, e := r.eligibleWatcherHosts(ctx, group, nodes.Items, pvs)
		if e != nil {
			result.unavailable = true
		}
		if len(hosts) == 0 {
			result.failures["NoEligibleHost"] = true
		}
		desired, e := buildWatcherDeployment(config, assignment, hosts, image)
		if e != nil {
			return requeue, e
		}
		wanted[desired.Name] = true
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
		_, e = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
			if deployment.ResourceVersion != "" && !ownedBy(deployment, config.UID) {
				return fmt.Errorf("watcher Deployment owner does not match")
			}
			if revision := deployment.Spec.Template.Annotations[placementRepair]; revision != "" {
				desired.Spec.Template.Annotations[placementRepair] = revision
			}
			deployment.Labels, deployment.Spec = desired.Labels, desired.Spec
			return controllerutil.SetControllerReference(config, deployment, r.Scheme)
		})
		if e != nil {
			return requeue, e
		}
		ready, e := r.watcherGroupReadiness(ctx, deployment, hosts, result)
		if e != nil {
			result.unavailable = true
		} else if ready {
			result.ready++
		}
	}
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(config.Namespace), client.MatchingLabels{blockpernode.ConfigurationUIDLabel: string(config.UID)}); err != nil {
		return requeue, err
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if !wanted[d.Name] && ownedBy(d, config.UID) {
			if err := r.Delete(ctx, d, client.Preconditions{UID: &d.UID, ResourceVersion: &d.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return requeue, err
			}
		}
	}
	return requeue, r.writeWatcherStatus(ctx, config, result)
}

func configurePerNodeAgent(daemon *appsv1.DaemonSet, config *api.StorageBasedRemediationConfig) {
	daemon.Labels[blockpernode.ConfigurationUIDLabel] = string(config.UID)
	daemon.Spec.Template.Labels[blockpernode.ConfigurationUIDLabel] = string(config.UID)
	c := &daemon.Spec.Template.Spec.Containers[0]
	daemon.Spec.Template.Spec.ServiceAccountName = peerResourceName(config)
	c.Args = []string{"--block-per-node=true"}
	c.Env = append(c.Env, corev1.EnvVar{Name: "SBR_CONFIGURATION_UID", Value: string(config.UID)})
	c.Env = append(c.Env, corev1.EnvVar{Name: blockpernode.ProbeIntervalEnv, Value: config.Spec.GetSBRUpdateInterval().String()}, corev1.EnvVar{Name: blockpernode.IOTimeoutEnv, Value: config.Spec.GetIOTimeout().String()}, corev1.EnvVar{Name: blockpernode.FailuresEnv, Value: strconv.Itoa(int(config.Spec.GetMaxConsecutiveFailures()))})
	c.VolumeDevices = []corev1.VolumeDevice{{Name: blockpernode.ProbeVolume, DevicePath: blockpernode.ProbeDevice}}
	c.ReadinessProbe = nil
	// The probe runtime has no watchdog readiness contract until executor integration.
	c.VolumeMounts = nil
	c.SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	daemon.Spec.Template.Spec.HostPID = false
	daemon.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: blockpernode.ProbeVolume, VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, VolumeMode: ptr.To(corev1.PersistentVolumeBlock), StorageClassName: ptr.To(config.Spec.SharedStorageClass), Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Mi")}}}}}}}}
}

func ownedBy(object metav1.Object, uid types.UID) bool {
	ref := metav1.GetControllerOf(object)
	return ref != nil && ref.UID == uid
}

func (r *StorageBasedRemediationConfigReconciler) discoverProbe(ctx context.Context, config *api.StorageBasedRemediationConfig, daemon *appsv1.DaemonSet, node *corev1.Node, index int) (*blockpernode.ProbeMapping, *blockpernode.ProbeTiming, *corev1.PersistentVolume, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(config.Namespace)); err != nil {
		return nil, nil, nil, err
	}
	var candidates []*corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == node.Name && p.UID != "" && p.DeletionTimestamp == nil && !p.CreationTimestamp.Before(&node.CreationTimestamp) && ownedBy(p, daemon.UID) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) != 1 {
		return nil, nil, nil, nil
	}
	pod := candidates[0]
	claimName := ""
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == blockpernode.ProbeVolume && volume.Ephemeral != nil {
			claimName = pod.Name + "-" + volume.Name
		}
	}
	if claimName == "" {
		return nil, nil, nil, nil
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: claimName}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	if pvc.UID == "" || !ownedBy(pvc, pod.UID) || pvc.DeletionTimestamp != nil || pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" || !validProbeSpec(pvc.Spec.StorageClassName, pvc.Spec.VolumeMode, pvc.Spec.AccessModes, config.Spec.SharedStorageClass) {
		return nil, nil, nil, nil
	}
	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, pv); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	ref := pv.Spec.ClaimRef
	if pv.UID == "" || pv.DeletionTimestamp != nil || ref == nil || ref.UID != pvc.UID || ref.Namespace != pvc.Namespace || ref.Name != pvc.Name || !validProbeSpec(&pv.Spec.StorageClassName, pv.Spec.VolumeMode, pv.Spec.AccessModes, config.Spec.SharedStorageClass) || pv.Spec.CSI == nil || pv.Spec.CSI.Driver == "" {
		return nil, nil, nil, nil
	}
	values := map[string]string{}
	for _, container := range pod.Spec.Containers {
		if container.Name == "sbr-agent" {
			for _, env := range container.Env {
				if env.ValueFrom == nil {
					if _, duplicate := values[env.Name]; duplicate {
						values[env.Name] = ""
					} else {
						values[env.Name] = env.Value
					}
				}
			}
		}
	}
	timing, timingErr := blockpernode.ParseTiming(values)
	if timingErr != nil {
		timing = nil
	}
	mapping := &blockpernode.ProbeMapping{AgentPodUID: pod.UID, ClaimNamespace: pvc.Namespace, ClaimName: pvc.Name, ClaimUID: pvc.UID, DevicePath: blockpernode.DevicePath(index)}
	return mapping, timing, pv, nil
}

func validProbeSpec(class *string, mode *corev1.PersistentVolumeMode, access []corev1.PersistentVolumeAccessMode, expected string) bool {
	if class == nil || *class != expected || mode == nil || *mode != corev1.PersistentVolumeBlock {
		return false
	}
	for _, m := range access {
		if m == corev1.ReadWriteMany {
			return true
		}
	}
	return false
}

func (r *StorageBasedRemediationConfigReconciler) eligibleWatcherHosts(ctx context.Context, group watcherGroup, nodes []corev1.Node, pvs []*corev1.PersistentVolume) ([]string, error) {
	excluded := map[string]bool{}
	for _, node := range group.Nodes {
		excluded[node.Name] = true
	}
	var hosts []string
	for i := range nodes {
		node := &nodes[i]
		if excluded[node.Name] || node.DeletionTimestamp != nil || node.UID == "" || node.Labels[corev1.LabelOSStable] != "linux" {
			continue
		}
		csi := &storagev1.CSINode{}
		if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, csi); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		validOwner := false
		for _, owner := range csi.OwnerReferences {
			if owner.Kind == "Node" && owner.UID == node.UID {
				validOwner = true
			}
		}
		if !validOwner {
			continue
		}
		valid := true
		for _, pv := range pvs {
			registered := false
			for _, driver := range csi.Spec.Drivers {
				if pv.Spec.CSI != nil && driver.Name == pv.Spec.CSI.Driver {
					registered = true
				}
			}
			if !registered || !volumeMatchesNode(pv, node) {
				valid = false
				break
			}
		}
		if valid {
			hosts = append(hosts, node.Name)
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

func volumeMatchesNode(pv *corev1.PersistentVolume, node *corev1.Node) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return true
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		if len(term.MatchExpressions)+len(term.MatchFields) == 0 {
			continue
		}
		match := true
		for _, requirement := range term.MatchExpressions {
			if !matchesRequirement(requirement, node.Labels) {
				match = false
			}
		}
		for _, requirement := range term.MatchFields {
			if requirement.Key != "metadata.name" || !matchesRequirement(requirement, map[string]string{"metadata.name": node.Name}) {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

func matchesRequirement(r corev1.NodeSelectorRequirement, values map[string]string) bool {
	v, present := values[r.Key]
	contains := false
	for _, item := range r.Values {
		if item == v {
			contains = true
		}
	}
	switch r.Operator {
	case corev1.NodeSelectorOpIn:
		return present && contains
	case corev1.NodeSelectorOpNotIn:
		return !present || !contains
	case corev1.NodeSelectorOpExists:
		return present
	case corev1.NodeSelectorOpDoesNotExist:
		return !present
	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if !present || len(r.Values) != 1 {
			return false
		}
		x, e1 := strconv.ParseInt(v, 10, 64)
		y, e2 := strconv.ParseInt(r.Values[0], 10, 64)
		return e1 == nil && e2 == nil && ((r.Operator == corev1.NodeSelectorOpGt && x > y) || (r.Operator == corev1.NodeSelectorOpLt && x < y))
	}
	return false
}

func buildWatcherDeployment(config *api.StorageBasedRemediationConfig, assignment blockpernode.WatcherTargets, hosts []string, image string) (*appsv1.Deployment, error) {
	if err := assignment.Validate(""); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(assignment)
	managed := map[string]string{blockpernode.ConfigurationUIDLabel: string(config.UID), "sbr.medik8s.io/watcher-group": assignment.Group}
	container := corev1.Container{Name: "sbr-watcher", Image: image, Command: []string{"/usr/local/bin/sbr-watcher"}, Env: []corev1.EnvVar{{Name: blockpernode.TargetsEnv, Value: string(data)}, {Name: blockpernode.NodeNameEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}
	var volumes []corev1.Volume
	for index, target := range assignment.Targets {
		if target.Probe != nil {
			name := fmt.Sprintf("probe-%d", index)
			volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: target.Probe.ClaimName, ReadOnly: true}}})
			container.VolumeDevices = append(container.VolumeDevices, corev1.VolumeDevice{Name: name, DevicePath: target.Probe.DevicePath})
		}
	}
	terms := []corev1.NodeSelectorTerm{{}}
	if len(hosts) > 0 {
		terms = nil
		for _, host := range hosts {
			terms = append(terms, corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{host}}}})
		}
	}
	nameHash := sha256.Sum256([]byte(assignment.Group))
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: watcherResourceName(config) + "-" + hex.EncodeToString(nameHash[:8]), Namespace: config.Namespace, Labels: managed}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: managed}, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: ptr.To(intstr.FromInt32(0)), MaxUnavailable: ptr.To(intstr.FromInt32(1))}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: managed, Annotations: map[string]string{}}, Spec: corev1.PodSpec{ServiceAccountName: watcherResourceName(config), Containers: []corev1.Container{container}, Volumes: volumes, NodeSelector: map[string]string{corev1.LabelOSStable: "linux"}, Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}}}}}
	return d, validateWatcherDevices(assignment, &d.Spec.Template.Spec)
}

func validateWatcherDevices(assignment blockpernode.WatcherTargets, spec *corev1.PodSpec) error {
	for index, target := range assignment.Targets {
		if target.Probe == nil {
			continue
		}
		name := fmt.Sprintf("probe-%d", index)
		deviceOK, claimOK := false, false
		for _, device := range spec.Containers[0].VolumeDevices {
			if device.Name == name && device.DevicePath == target.Probe.DevicePath {
				deviceOK = true
			}
		}
		for _, volume := range spec.Volumes {
			if volume.Name == name && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == target.Probe.ClaimName && volume.PersistentVolumeClaim.ReadOnly {
				claimOK = true
			}
		}
		if !deviceOK || !claimOK {
			return fmt.Errorf("probe devices do not match target %s", target.Name)
		}
	}
	return nil
}

func (r *StorageBasedRemediationConfigReconciler) currentWatcherPod(ctx context.Context, pod *corev1.Pod, deployment *appsv1.Deployment) (bool, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "ReplicaSet" {
		return false, nil
	}
	rs := &appsv1.ReplicaSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, rs); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if rs.UID != owner.UID || !ownedBy(rs, deployment.UID) {
		return false, nil
	}
	want, actual := deployment.Spec.Template.DeepCopy(), rs.Spec.Template.DeepCopy()
	delete(actual.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return reflect.DeepEqual(want, actual), nil
}

func (r *StorageBasedRemediationConfigReconciler) watcherGroupReadiness(ctx context.Context, deployment *appsv1.Deployment, hosts []string, result *structuralResult) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(deployment.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels)); err != nil {
		return false, err
	}
	eligible := map[string]bool{}
	for _, host := range hosts {
		eligible[host] = true
	}
	ready := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		current, err := r.currentWatcherPod(ctx, pod, deployment)
		if err != nil {
			return false, err
		}
		if !current {
			continue
		}
		if pod.Spec.NodeName != "" && !eligible[pod.Spec.NodeName] && pod.DeletionTimestamp == nil {
			if deployment.Spec.Template.Annotations[placementRepair] != string(pod.UID) {
				if deployment.Spec.Template.Annotations == nil {
					deployment.Spec.Template.Annotations = map[string]string{}
				}
				deployment.Spec.Template.Annotations[placementRepair] = string(pod.UID)
				if err := r.Update(ctx, deployment); err != nil {
					return false, err
				}
			}
		}
		podReady := false
		if pod.DeletionTimestamp == nil && eligible[pod.Spec.NodeName] {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					ready, podReady = true, true
				}
			}
		}
		if podReady {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		var events corev1.EventList
		if err := r.List(ctx, &events, client.InNamespace(pod.Namespace)); err != nil {
			return false, err
		}
		for _, event := range events.Items {
			if event.InvolvedObject.UID != pod.UID {
				continue
			}
			if event.Reason == "FailedScheduling" && pod.Spec.NodeName == "" {
				result.failures["SchedulingFailed"] = true
			}
			if strings.HasPrefix(event.Reason, "FailedMount") || strings.HasPrefix(event.Reason, "FailedAttachVolume") || event.Reason == "FailedMapVolume" {
				result.failures["VolumeSetupFailed"] = true
			}
		}
	}
	if !ready {
		result.failures["PodNotReady"] = true
	}
	return ready, nil
}
