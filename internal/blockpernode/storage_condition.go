package blockpernode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PatchCondition preserves other Node fields and rejects a changed resource version.
func PatchCondition(ctx context.Context, c client.Client, node *corev1.Node, status corev1.ConditionStatus, reason, message string, now time.Time) error {
	if old := Condition(node); old != nil && old.Status == status {
		return nil
	}
	if node.UID == "" || node.ResourceVersion == "" {
		return fmt.Errorf("Node identity or resource version is missing")
	}
	patch := struct {
		Metadata map[string]string `json:"metadata"`
		Status   struct {
			Conditions []corev1.NodeCondition `json:"conditions"`
		} `json:"status"`
	}{Metadata: map[string]string{"uid": string(node.UID), "resourceVersion": node.ResourceVersion}}
	patch.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeConditionType(StorageCondition), Status: status, Reason: reason, Message: message, LastTransitionTime: metav1.NewTime(now)}}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return c.Status().Patch(ctx, node.DeepCopy(), client.RawPatch(types.StrategicMergePatchType, data))
}
