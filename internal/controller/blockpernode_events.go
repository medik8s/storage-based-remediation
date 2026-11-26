package controller

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

func (r *StorageBasedRemediationConfigReconciler) blockPerNodeEvents() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []ctrl.Request {
		var configs api.StorageBasedRemediationConfigList
		if err := r.List(ctx, &configs); err != nil {
			return nil
		}
		var requests []ctrl.Request
		for _, config := range configs.Items {
			if config.Spec.IsBlockPerNodeMode() {
				requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&config)})
			}
		}
		return requests
	})
}

func blockPerNodeNodePredicate() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		old, ok1 := e.ObjectOld.(*corev1.Node)
		current, ok2 := e.ObjectNew.(*corev1.Node)
		if !ok1 || !ok2 {
			return true
		}
		return old.UID != current.UID || !reflect.DeepEqual(old.Labels, current.Labels) || !reflect.DeepEqual(old.Spec, current.Spec) || !reflect.DeepEqual(old.DeletionTimestamp, current.DeletionTimestamp) || old.Annotations[blockpernode.OwnerAnnotation] != current.Annotations[blockpernode.OwnerAnnotation] || old.Annotations[blockpernode.GroupAnnotation] != current.Annotations[blockpernode.GroupAnnotation] || nodeReady(old) != nodeReady(current)
	}}
}

func nodeReady(node *corev1.Node) corev1.ConditionStatus {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status
		}
	}
	return corev1.ConditionUnknown
}
