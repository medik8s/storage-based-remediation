package nodeidentity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// unschedulablePatch is the wire shape of a cordon patch on the main Node
// resource. It carries exactly one spec field plus the expected Node UID:
// the API server rejects a patch that would change the immutable uid field,
// so including the expected UID makes the write an atomic precondition — a
// replaced Node object fails the write with HTTP 422 before any spec change
// is applied (verified against a live API server). A plain JSON merge patch
// and a strategic merge patch are byte-identical for this body.
//
// The optional annotations merge into metadata.annotations by key
// (strategic-merge map semantics): keys already on the Node that the patch
// does not name are left untouched. This shape cannot express an annotation
// deletion; releasing an annotation requires a separate write.
type unschedulablePatch struct {
	Metadata struct {
		UID             string            `json:"uid"`
		ResourceVersion string            `json:"resourceVersion,omitempty"`
		Annotations     map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
}

// PatchUnschedulable sets or clears the node's cordon flag
// (spec.unschedulable) through a patch on the main Node resource. This is the
// one scheduling write Kubernetes permits the node identity — measured on
// OCP 4.22 / Kubernetes 1.35: HTTP 200 in both directions, while taint writes
// are refused with 403.
//
// The patch is a merge on a single boolean with a UID precondition instead of
// a resourceVersion one. It does not provide snapshot-based concurrency
// control. A replaced Node object is never cordoned or uncordoned: the
// write itself fails (classified ErrNodeNameMismatch) before the spec
// changes. The response is decoded, validated against the configured
// identity, and its resourceVersion recorded for a subsequent
// PatchOwnNodeCondition.
//
// A cordon blocks new scheduling only. It does not stop kubelet from
// restarting pods already bound to the node and does not evict anything.
func (c *Client) PatchUnschedulable(ctx context.Context, unschedulable bool) (*Node, error) {
	return c.patchUnschedulableAtVersion(ctx, unschedulable, "", nil)
}

// PatchUnschedulableWithAnnotations sets or clears the node's cordon flag and
// merges annotations into the Node's metadata in the SAME write. See
// PatchUnschedulable for the cordon semantics; the annotations follow
// strategic-merge map semantics: keys already on the Node that the patch does
// not name are left untouched, and a key set to the empty string is stored as
// the empty string. Deletion cannot be expressed here — a caller that must
// remove an annotation does so in a separate write.
//
// The one-write guarantee is the point: a caller that marks a cordon as its
// own (an ownership key consumers match on to release it) must never be able
// to observe a cordon without its marker or a marker without its cordon.
func (c *Client) PatchUnschedulableWithAnnotations(ctx context.Context, unschedulable bool, annotations map[string]string) (*Node, error) {
	return c.patchUnschedulableAtVersion(ctx, unschedulable, "", annotations)
}

// patchUnschedulableAtVersion lets diagnostic cleanup condition its write on
// the exact snapshot it checked. Runtime Cordon/Uncordon remain UID-guarded.
func (c *Client) patchUnschedulableAtVersion(ctx context.Context, unschedulable bool, rv string, annotations map[string]string) (*Node, error) {
	var p unschedulablePatch
	p.Metadata.UID = c.cfg.ExpectedUID
	p.Metadata.ResourceVersion = rv
	p.Metadata.Annotations = annotations
	p.Spec.Unschedulable = unschedulable
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot marshal cordon patch: %v", ErrInvalidConfig, err)
	}
	resp, err := c.do(ctx, http.MethodPatch,
		"/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName), PatchTypeStrategicMerge, body)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)
	if err := classify(resp); err != nil {
		return nil, err
	}
	node, err := decodeNodeBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := c.validateOwn(node); err != nil {
		return nil, err
	}
	c.setObservedRV(node.Metadata.ResourceVersion)
	return node, nil
}

// Cordon sets the node's cordon flag. See PatchUnschedulable for semantics.
func (c *Client) Cordon(ctx context.Context) (*Node, error) {
	return c.PatchUnschedulable(ctx, true)
}

// Uncordon clears the node's cordon flag. See PatchUnschedulable for
// semantics. The write permission is the same one PatchUnschedulable uses —
// measured symmetric on live clusters.
func (c *Client) Uncordon(ctx context.Context) (*Node, error) {
	return c.PatchUnschedulable(ctx, false)
}

// GetNode fetches any Node by name through the node identity. Reads of other
// nodes are permitted to the node identity (measured); writes never are. A
// read of the caller's OWN node is validated with validateOwn — the expected
// UID invariant applies; reads of other nodes are validated for the requested
// name and for presence of uid and resourceVersion only.
func (c *Client) GetNode(ctx context.Context, name string) (*Node, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: GetNode requires a node name", ErrInvalidConfig)
	}
	resp, err := c.do(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(name), "", nil)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)
	if err := classify(resp); err != nil {
		return nil, err
	}
	node, err := decodeNodeBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if name == c.cfg.NodeName {
		// The caller's own node: enforce the full identity invariant.
		if err := c.validateOwn(node); err != nil {
			return nil, err
		}
		c.setObservedRV(node.Metadata.ResourceVersion)
		return node, nil
	}
	if node.Metadata.Name != name {
		return nil, fmt.Errorf("%w: server returned node %q for %q",
			ErrNodeNameMismatch, node.Metadata.Name, name)
	}
	if node.Metadata.UID == "" || node.Metadata.ResourceVersion == "" {
		return nil, fmt.Errorf("%w: response for %q is missing uid or resourceVersion",
			ErrAPIServer, name)
	}
	return node, nil
}
