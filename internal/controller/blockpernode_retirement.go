package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

const disarmMarker = "sbr-watchdog-disarmed-v1"

func shutdownKey(check api.RetirementShutdownCheck) string {
	return check.PodUID + "/" + check.ContainerName + "/" + check.ContainerID
}

// collectShutdownChecks records real container instances and retains older restart evidence.
func collectShutdownChecks(status *api.RetirementStatus, pod *corev1.Pod, nodeUID types.UID, watcher bool) {
	index := map[string]int{}
	for i, check := range status.ShutdownChecks {
		index[shutdownKey(check)] = i
	}
	for _, container := range append(append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		instances := []struct {
			id         string
			terminated *corev1.ContainerStateTerminated
		}{{container.ContainerID, container.State.Terminated}}
		if old := container.LastTerminationState.Terminated; old != nil {
			instances = append(instances, struct {
				id         string
				terminated *corev1.ContainerStateTerminated
			}{old.ContainerID, old})
		}
		for _, instance := range instances {
			id := instance.id
			if instance.terminated != nil && instance.terminated.ContainerID != "" {
				id = instance.terminated.ContainerID
			}
			// A Pending container has no process identity. A later status adds its real instance.
			if id == "" {
				continue
			}
			check := api.RetirementShutdownCheck{PodNamespace: pod.Namespace, PodName: pod.Name, PodUID: string(pod.UID), NodeUID: string(nodeUID), ContainerName: container.Name, ContainerID: id}
			key := shutdownKey(check)
			i, exists := index[key]
			if !exists {
				status.InventoryComplete = false
				if watcher {
					check.DisarmRequired = ptr.To(false)
				}
				status.ShutdownChecks = append(status.ShutdownChecks, check)
				i = len(status.ShutdownChecks) - 1
				index[key] = i
			}
			checkPtr := &status.ShutdownChecks[i]
			if instance.terminated != nil && instance.terminated.ContainerID == id && pod.ResourceVersion != "" {
				checkPtr.TerminationEvidence = &api.RetirementTerminationEvidence{PodUID: string(pod.UID), ContainerID: id, SourceResourceVersion: pod.ResourceVersion, State: *instance.terminated.DeepCopy()}
				checkPtr.DisarmConfirmed = strings.TrimSpace(instance.terminated.Message) == disarmMarker
			}
		}
	}
}

func (r *StorageBasedRemediationConfigReconciler) persistRetirement(ctx context.Context, config *api.StorageBasedRemediationConfig, status *api.RetirementStatus) error {
	live := &api.StorageBasedRemediationConfig{}
	if err := r.liveReader().Get(ctx, client.ObjectKeyFromObject(config), live); err != nil {
		return err
	}
	if live.UID != config.UID || live.ResourceVersion != config.ResourceVersion {
		return apierrors.NewConflict(api.GroupVersion.WithResource("storagebasedremediationconfigs").GroupResource(), config.Name, fmt.Errorf("configuration changed before retirement persistence"))
	}
	if reflect.DeepEqual(live.Status.Retirement, status) {
		*config = *live
		return nil
	}
	live.Status.Retirement = status
	if err := r.Status().Update(ctx, live); err != nil {
		return err
	}
	*config = *live
	return nil
}

func (r *StorageBasedRemediationConfigReconciler) retireBlockPerNode(ctx context.Context, config *api.StorageBasedRemediationConfig, deleting bool) (ctrl.Result, error) {
	requeue := ctrl.Result{RequeueAfter: 30 * time.Second}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return requeue, err
	}
	var owned []corev1.Node
	nodeUIDs := map[string]types.UID{}
	retainedChecks := map[string]bool{}
	if config.Status.Retirement != nil {
		for _, check := range config.Status.Retirement.ReleaseChecks {
			retainedChecks[check.NodeUID] = true
		}
	}
	selected := map[types.UID]bool{}
	for _, node := range selectedNodes(config, nodes.Items) {
		selected[node.UID] = true
	}
	retiringGroups := map[string]bool{}
	for _, node := range nodes.Items {
		nodeUIDs[node.Name] = node.UID
		owner, e := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
		if e == nil && owner.ConfigUID == config.UID && owner.NodeUID == node.UID && owner.Phase != blockpernode.Released && (deleting || !selected[node.UID] || retainedChecks[string(node.UID)]) {
			owned = append(owned, node)
			retiringGroups[node.Annotations[blockpernode.GroupAnnotation]] = true
		}
	}
	status := config.DeepCopy().Status.Retirement
	if status == nil {
		status = &api.RetirementStatus{Phase: blockpernode.Retiring}
	}
	for _, node := range owned {
		owner, _ := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
		found := false
		for _, check := range status.ReleaseChecks {
			if check.NodeUID == string(node.UID) && check.OwnershipGeneration == owner.OwnershipGeneration {
				found = true
			}
		}
		if !found {
			status.ReleaseChecks = append(status.ReleaseChecks, api.RetirementReleaseCheck{NodeName: node.Name, NodeUID: string(node.UID), OwnershipGeneration: owner.OwnershipGeneration})
		}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(config.Namespace)); err != nil {
		return requeue, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) {
			continue
		}
		watcher := pod.Labels["sbr.medik8s.io/watcher-group"] != ""
		if !deleting {
			included := watcher && retiringGroups[pod.Labels["sbr.medik8s.io/watcher-group"]]
			for _, check := range status.ReleaseChecks {
				if !watcher && pod.Spec.NodeName == check.NodeName && nodeUIDs[pod.Spec.NodeName] == types.UID(check.NodeUID) {
					included = true
				}
			}
			if !included {
				continue
			}
		}
		collectShutdownChecks(status, pod, nodeUIDs[pod.Spec.NodeName], watcher)
	}
	status.OutstandingChecks = nil
	if !status.InventoryComplete {
		status.OutstandingChecks = append(status.OutstandingChecks, "RHWA-2085 must establish complete historical process inventory")
	}
	allReleased := true
	for _, check := range status.ReleaseChecks {
		if !check.Completed {
			allReleased = false
			if len(status.OutstandingChecks) < 64 {
				status.OutstandingChecks = append(status.OutstandingChecks, "peer release is incomplete for Node "+check.NodeName)
			}
		}
	}
	if err := r.persistRetirement(ctx, config, status); err != nil {
		return requeue, err
	}
	// Confirm persisted identities before the controller requests any workload removal.
	live := &api.StorageBasedRemediationConfig{}
	if err := r.liveReader().Get(ctx, client.ObjectKeyFromObject(config), live); err != nil {
		return requeue, err
	}
	if live.UID != config.UID || !reflect.DeepEqual(live.Status.Retirement, status) {
		return requeue, fmt.Errorf("retirement identities are not confirmed")
	}
	for i := range owned {
		node := &owned[i]
		owner, _ := blockpernode.DecodeOwner(node.Annotations[blockpernode.OwnerAnnotation])
		phase := blockpernode.Retiring
		if status.Phase == blockpernode.Shutdown || status.Phase == blockpernode.Released {
			phase = blockpernode.Shutdown
		}
		if owner.Phase != phase {
			updated, err := r.updateNodeAssignment(ctx, config, node, phase, node.Annotations[blockpernode.GroupAnnotation])
			if err != nil {
				return requeue, err
			}
			owned[i] = *updated
		}
	}
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(config.Namespace), client.MatchingLabels{blockpernode.ConfigurationUIDLabel: string(config.UID)}); err != nil {
		return requeue, err
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if ownedBy(d, config.UID) && d.DeletionTimestamp == nil && (deleting || retiringGroups[d.Labels["sbr.medik8s.io/watcher-group"]]) {
			if err := r.Delete(ctx, d, client.Preconditions{UID: &d.UID, ResourceVersion: &d.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return requeue, err
			}
		}
	}
	if !allReleased {
		return requeue, nil
	}
	if status.Phase == blockpernode.Retiring {
		status.Phase = blockpernode.Shutdown
		return requeue, r.persistRetirement(ctx, config, status)
	}
	var daemons appsv1.DaemonSetList
	if err := r.List(ctx, &daemons, client.InNamespace(config.Namespace), client.MatchingLabels{blockpernode.ConfigurationUIDLabel: string(config.UID)}); err != nil {
		return requeue, err
	}
	for i := range daemons.Items {
		d := &daemons.Items[i]
		if d.DeletionTimestamp != nil {
			continue
		}
		if deleting {
			if err := r.Delete(ctx, d, client.Preconditions{UID: &d.UID, ResourceVersion: &d.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return requeue, err
			}
		} else {
			before := d.DeepCopy()
			excludeRetiringTargets(d, status.ReleaseChecks)
			if !reflect.DeepEqual(before.Spec, d.Spec) {
				if err := r.Update(ctx, d); err != nil {
					return requeue, err
				}
			}
		}
	}
	// Stop future agent Pod creation before the executor closes the shutdown inventory.
	if !status.InventoryComplete {
		return requeue, r.persistRetirement(ctx, config, status)
	}
	for _, check := range status.ShutdownChecks {
		evidence := check.TerminationEvidence
		if evidence == nil || evidence.PodUID != check.PodUID || evidence.ContainerID != check.ContainerID || evidence.State.ContainerID != check.ContainerID || evidence.SourceResourceVersion == "" || check.DisarmRequired == nil || (*check.DisarmRequired && !check.DisarmConfirmed) {
			if len(status.OutstandingChecks) < 64 {
				status.OutstandingChecks = append(status.OutstandingChecks, "termination or disarm is incomplete for "+check.PodNamespace+"/"+check.PodName+"/"+check.ContainerName+"/"+check.ContainerID)
			}
		}
	}
	if len(status.OutstandingChecks) > 0 {
		return requeue, r.persistRetirement(ctx, config, status)
	}
	for i := range owned {
		if _, err := r.updateNodeAssignment(ctx, config, &owned[i], blockpernode.Released, ""); err != nil {
			return requeue, err
		}
	}
	if deleting {
		if err := r.removePerNodePermissions(ctx, config); err != nil {
			return requeue, err
		}
	}
	if status.Phase != blockpernode.Released {
		status.Phase = blockpernode.Released
		return requeue, r.persistRetirement(ctx, config, status)
	}
	if deleting {
		controllerutil.RemoveFinalizer(config, StorageBasedRemediationConfigFinalizerName)
		return ctrl.Result{}, r.Update(ctx, config)
	}
	config.Status.Retirement = nil
	return requeue, r.Status().Update(ctx, config)
}

func (r *StorageBasedRemediationConfigReconciler) removePerNodePermissions(ctx context.Context, config *api.StorageBasedRemediationConfig) error {
	for _, name := range []string{watcherAccountName, perNodeAgentAccountName} {
		binding := &rbacv1.ClusterRoleBinding{}
		if err := r.liveReader().Get(ctx, types.NamespacedName{Name: name}, binding); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			if binding.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != name || binding.Subjects[0].Namespace != config.Namespace {
				return fmt.Errorf("permission binding identity does not match")
			}
			if err := r.Delete(ctx, binding, client.Preconditions{UID: &binding.UID, ResourceVersion: &binding.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		sa := &corev1.ServiceAccount{}
		if err := r.liveReader().Get(ctx, types.NamespacedName{Namespace: config.Namespace, Name: name}, sa); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			if sa.Labels[blockpernode.ConfigurationUIDLabel] != string(config.UID) {
				return fmt.Errorf("service account identity does not match")
			}
			if err := r.Delete(ctx, sa, client.Preconditions{UID: &sa.UID, ResourceVersion: &sa.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func excludeRetiringTargets(daemon *appsv1.DaemonSet, checks []api.RetirementReleaseCheck) {
	var names []string
	for _, check := range checks {
		names = append(names, check.NodeName)
	}
	sort.Strings(names)
	if daemon.Spec.Template.Spec.Affinity == nil {
		daemon.Spec.Template.Spec.Affinity = &corev1.Affinity{}
	}
	if daemon.Spec.Template.Spec.Affinity.NodeAffinity == nil {
		daemon.Spec.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity := daemon.Spec.Template.Spec.Affinity.NodeAffinity
	if affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		affinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}
	}
	for i := range affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		term := &affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
		for _, name := range names {
			requirement := corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{name}}
			found := false
			for _, existing := range term.MatchFields {
				if reflect.DeepEqual(existing, requirement) {
					found = true
				}
			}
			if !found {
				term.MatchFields = append(term.MatchFields, requirement)
			}
		}
	}
}

func (r *StorageBasedRemediationConfigReconciler) ensurePeerPermissions(ctx context.Context, config *api.StorageBasedRemediationConfig) error {
	return r.ensurePerNodePermissions(ctx, config, perNodeAgentAccountName, "sbr-operator-sbr-agent-role",
		fmt.Errorf("peer service account identity does not match"), fmt.Errorf("peer binding identity does not match"))
}
