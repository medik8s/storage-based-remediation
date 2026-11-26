package noderecord

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MutateFunc func(node *corev1.Node, rec *Record) error

var ErrNodeUIDMismatch = errors.New("record nodeUID does not match node")

func getNodeAndRecord(ctx context.Context, reader client.Reader, nodeName string) (*corev1.Node, *Record, error) {
	node := &corev1.Node{}
	if err := reader.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return nil, nil, err
	}
	rec, err := Decode(node)
	if err != nil {
		return nil, nil, err
	}
	if rec.NodeUID != node.UID {
		return nil, nil, ErrNodeUIDMismatch
	}
	return node, rec, nil
}

func Update(ctx context.Context, reader client.Reader, c client.Client,
	nodeName string, mutate MutateFunc) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {

		node, rec, err := getNodeAndRecord(ctx, reader, nodeName)
		if err != nil {
			return err
		}
		if err := mutate(node, rec); err != nil {
			return err
		}
		encoded, err := Encode(rec)
		if err != nil {
			return err
		}
		node.Annotations[AnnotationKey] = encoded
		return c.Update(ctx, node)
	})
}
