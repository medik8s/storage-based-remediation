/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package noderecord

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MutateFunc applies one change to a Node's record. Update may call it more
// than once: each retry hands it a freshly read Node and Record, and it must
// re-decide from those arguments alone. Reusing a decision reached before the
// conflict would apply it to state that has since moved on.
//
// Returning an error aborts the update and leaves the stored record as it was.
type MutateFunc func(node *corev1.Node, rec *Record) error

// ErrNodeUIDMismatch means the record describes a different Node incarnation
// than the live object. A Node deleted and recreated under the same name gets
// a new UID, and the old record must never authorize changes to the
// replacement.
var ErrNodeUIDMismatch = errors.New("record nodeUID does not match node")

// getNodeAndRecord reads a Node and the record it carries, refusing a record
// that does not belong to the live object.
//
// The reader must be uncached. A stale cache would have the caller validate
// phases and holders against a view the API server has already moved past,
// and every retry would re-read the same stale copy until the cache caught
// up.
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

// Update applies mutate to the Node's record and writes it back under the
// resourceVersion of the read that produced it.
//
// The read, the mutation and the write all sit inside the retry so that a
// conflict re-reads the Node and runs mutate again against current state,
// rather than replaying a decision made against the version that just lost.
//
// The record must already exist: Update never creates one, because a missing
// or unreadable record has to be repaired rather than invented.
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
		// getNodeAndRecord already read the annotation off this Node, so the
		// map exists and does not need initializing.
		node.Annotations[AnnotationKey] = encoded
		return c.Update(ctx, node)
	})
}
