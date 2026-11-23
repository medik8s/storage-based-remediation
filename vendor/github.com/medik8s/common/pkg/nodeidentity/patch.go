package nodeidentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"unicode/utf8"
)

// Patch content types accepted by the Node status subresource.
const (
	PatchTypeStrategicMerge = "application/strategic-merge-patch+json"
	PatchTypeJSONMerge      = "application/merge-patch+json"
	PatchTypeJSONPatch      = "application/json-patch+json"
)

// PatchOwnNodeStatus applies an arbitrary patch document to the caller's own
// Node status subresource. This is the general-purpose layer: the caller
// builds the patch (including any resourceVersion or UID precondition) and
// chooses the content type. The response is decoded, validated against the
// configured identity, and its resourceVersion recorded. This is a raw API:
// an unguarded patch can mutate a replacement Node BEFORE response validation
// reports ErrNodeNameMismatch. Callers must include an atomic ExpectedUID
// precondition (metadata.uid for merge patches, a UID test for JSON Patch).
// Prefer PatchOwnNodeConditionAtVersion for snapshot-dependent writes.
//
// High-level condition writes include ExpectedUID and resourceVersion.
// RemoveOwnNodeCondition instead uses JSON-Patch tests for UID and condition
// type. These guards are checked server-side before a mutation, unlike the
// response validation performed by this raw method.
func (c *Client) PatchOwnNodeStatus(ctx context.Context, patchType string, patch []byte) (*Node, error) {
	resp, err := c.do(ctx, http.MethodPatch,
		"/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName)+"/status", patchType, patch)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)
	if err := classify(resp); err != nil {
		// Invalidate the cached resourceVersion on conflict and identity
		// errors: the precondition is known-stale, and the next
		// PatchOwnNodeCondition must re-GET instead of reusing it.
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNodeNameMismatch) || errors.Is(err, ErrNodeNotFound) {
			c.clearObservedRV()
		}
		return nil, err
	}
	node, err := decodeNodeBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := c.validateOwn(node); err != nil {
		c.clearObservedRV()
		return nil, err
	}
	c.setObservedRV(node.Metadata.ResourceVersion)
	return node, nil
}

// PatchOwnNodeCondition strategic-merges exactly one condition into the
// caller's own Node status, preconditioned on the resourceVersion observed by
// the last GetOwnNode. If no version has been observed yet, it performs the
// GET first — the ordered flow the boot-hold reporter uses. On ErrConflict
// the caller must re-GET, re-validate, and re-patch with bounded jittered
// backoff; this package does not auto-retry, because retry ordering is the
// consumer's protocol decision. The cached version is shared across callers;
// this convenience method is for independent observations, not decisions
// derived from a particular GET. Use PatchOwnNodeConditionAtVersion for those.
func (c *Client) PatchOwnNodeCondition(ctx context.Context, cond Condition) (*Node, error) {
	if cond.Type == "" || cond.Status == "" {
		return nil, fmt.Errorf("%w: condition requires Type and Status", ErrInvalidConfig)
	}
	rv := c.getObservedRV()
	if rv == "" {
		if _, err := c.GetOwnNode(ctx); err != nil {
			return nil, err
		}
		rv = c.getObservedRV()
	}
	return c.PatchOwnNodeConditionAtVersion(ctx, rv, cond)
}

// PatchOwnNodeConditionAtVersion writes a condition only if the Node still has
// the supplied resourceVersion and enrolled UID. Pass the version of the exact
// snapshot used to derive the condition; concurrent client reads cannot replace
// it. On conflict, the caller must re-read AND reconsider its decision.
func (c *Client) PatchOwnNodeConditionAtVersion(ctx context.Context, rv string, cond Condition) (*Node, error) {
	if rv == "" || rv == "0" || cond.Type == "" || cond.Status == "" {
		return nil, fmt.Errorf("%w: a non-zero resourceVersion, condition Type and Status are required", ErrInvalidConfig)
	}
	body, err := buildConditionPatch(rv, c.cfg.ExpectedUID, cond)
	if err != nil {
		return nil, err
	}
	return c.PatchOwnNodeStatus(ctx, PatchTypeStrategicMerge, body)
}

// RemoveOwnNodeCondition removes exactly one condition, by type, from the
// caller's own Node status. The removal uses a JSON-Patch test+remove pair
// guarded on the condition's type field, so an index shift between the GET
// and the patch can never remove a foreign condition: the test op fails, the
// attempt is retried against a fresh GET, and a condition that is already
// gone is a success. Guarding on the subpath (not the whole stored value) is
// deliberate — the API server may store fields the typed projection does not
// carry, and a whole-value test would fail with 422 while a type-field test
// still guarantees the invariant.
func (c *Client) RemoveOwnNodeCondition(ctx context.Context, condType string) error {
	if condType == "" {
		return fmt.Errorf("%w: condition type must not be empty", ErrInvalidConfig)
	}
	for attempt := 0; attempt < 5; attempt++ {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			return err
		}
		found := false
		for i, cond := range node.Status.Conditions {
			if cond.Type != condType {
				continue
			}
			found = true
			patch, err := buildConditionRemovePatch(i, condType, c.cfg.ExpectedUID)
			if err != nil {
				return err
			}
			if _, err := c.PatchOwnNodeStatus(ctx, PatchTypeJSONPatch, patch); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrAPIServer) {
					break // index may have shifted, or a stale precondition; re-GET and retry
				}
				return err
			}
			return nil
		}
		if !found {
			return nil // condition no longer present
		}
	}
	return fmt.Errorf("%w: could not remove condition %q after retries", ErrAPIServer, condType)
}

// buildConditionRemovePatch marshals the guarded test+remove operations for
// one condition index: a UID test (a replaced Node object fails before any
// change), the condition-type test (an index shift can never remove a
// foreign condition), and the remove. Fields are validated for UTF-8 for the
// same reason as buildConditionPatch: json.Marshal would silently replace
// invalid bytes.
func buildConditionRemovePatch(index int, condType, expectedUID string) ([]byte, error) {
	if !utf8.ValidString(condType) || !utf8.ValidString(expectedUID) {
		return nil, fmt.Errorf("%w: patch fields must be valid UTF-8; invalid bytes would be silently replaced", ErrInvalidConfig)
	}
	type jsonPatchOp struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value,omitempty"`
	}
	ops := []jsonPatchOp{
		{Op: "test", Path: "/metadata/uid", Value: json.RawMessage(mustQuote(expectedUID))},
		{Op: "test", Path: fmt.Sprintf("/status/conditions/%d/type", index), Value: json.RawMessage(mustQuote(condType))},
		{Op: "remove", Path: fmt.Sprintf("/status/conditions/%d", index)},
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot marshal patch: %v", ErrInvalidConfig, err)
	}
	return b, nil
}

// mustQuote marshals one string to a JSON string literal.
func mustQuote(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// conditionPatch is the wire shape of a preconditioned single-condition
// strategic merge patch. The metadata carries both preconditions: the
// resourceVersion (stale writes conflict) and the expected UID (a replaced
// Node object fails the write with HTTP 422 before any status change,
// because the API server rejects changes to the immutable uid field).
type conditionPatch struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
		UID             string `json:"uid"`
	} `json:"metadata"`
	Status struct {
		Conditions []patchCondition `json:"conditions"`
	} `json:"status"`
}

// patchCondition is the patch-specific wire form of a Condition. Unlike
// Condition, Reason and Message are always serialized — an explicitly empty
// value CLEARS the field in the strategic merge, while an omitted key would
// leave the stored value untouched. LastTransitionTime keeps omitempty: an
// absent timestamp means "let the server keep what it has".
type patchCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// buildConditionPatch marshals the patch body. It carries exactly one
// condition and never re-sends conditions owned by other writers. Fields
// containing invalid UTF-8 are rejected: json.Marshal would silently replace
// invalid bytes with U+FFFD, mutating the payload the caller validated.
func buildConditionPatch(resourceVersion, expectedUID string, cond Condition) ([]byte, error) {
	if !utf8.ValidString(resourceVersion) || !utf8.ValidString(expectedUID) ||
		!utf8.ValidString(cond.Type) || !utf8.ValidString(cond.Status) ||
		!utf8.ValidString(cond.Reason) || !utf8.ValidString(cond.Message) ||
		!utf8.ValidString(cond.LastTransitionTime) {
		return nil, fmt.Errorf("%w: patch fields must be valid UTF-8; invalid bytes would be silently replaced", ErrInvalidConfig)
	}
	var p conditionPatch
	p.Metadata.ResourceVersion = resourceVersion
	p.Metadata.UID = expectedUID
	p.Status.Conditions = []patchCondition{{
		Type:               cond.Type,
		Status:             cond.Status,
		Reason:             cond.Reason,
		Message:            cond.Message,
		LastTransitionTime: cond.LastTransitionTime,
	}}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot marshal patch: %v", ErrInvalidConfig, err)
	}
	return b, nil
}
