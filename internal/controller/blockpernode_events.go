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

func soleBlockPerNodeConfig(ctx context.Context, reader client.Reader) (*api.StorageBasedRemediationConfig, error) {
	var configs api.StorageBasedRemediationConfigList
	if err := reader.List(ctx, &configs, client.Limit(2)); err != nil {
		return nil, err
	}
	if len(configs.Items) != 1 || configs.Continue != "" {
		return nil, nil
	}
	config := &configs.Items[0]
	if !config.Spec.IsBlockPerNodeMode() || config.DeletionTimestamp != nil {
		return nil, nil
	}
	return config, nil
}

func (r *StorageBasedRemediationConfigReconciler) blockPerNodeEvents() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []ctrl.Request {
		config, err := soleBlockPerNodeConfig(ctx, r.Client)
		if err != nil || config == nil {
			return nil
		}
		return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(config)}}
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
