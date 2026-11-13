package nodeidentity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ProbeOutcome classifies the result of one permission-matrix check.
type ProbeOutcome string

const (
	// OutcomePermitted means the API server accepted the write (or served
	// the read).
	OutcomePermitted ProbeOutcome = "permitted"
	// OutcomeRefused means the API server rejected the request with 403 —
	// for taint writes this is the expected NodeRestriction behavior.
	OutcomeRefused ProbeOutcome = "refused"
	// OutcomeFailed means the check could not be measured: a transport
	// failure, an identity mismatch, or an unexpected error class.
	OutcomeFailed ProbeOutcome = "failed"
)

// ProbeCheck is one measured permission.
type ProbeCheck struct {
	// Name identifies the check, for example "cordon (spec.unschedulable)".
	Name string
	// Expected is the outcome the measured platforms produce. A check whose
	// Outcome differs from Expected is a deviation the caller must see.
	Expected ProbeOutcome
	// Outcome is what this run measured.
	Outcome ProbeOutcome
	// Detail carries the deviation or failure reason, never the success path.
	Detail string
}

// AsExpected reports whether the check matched its expected outcome.
func (pc ProbeCheck) AsExpected() bool { return pc.Outcome == pc.Expected }

// ProbeResult is the measured permission matrix of one node identity.
type ProbeResult struct {
	// NodeName and NodeUID identify the measured node.
	NodeName string
	NodeUID  string
	// APIServer is the endpoint the matrix ran against.
	APIServer string
	// Checks are the measured checks, in execution order.
	Checks []ProbeCheck
}

// Unexpected returns the checks whose outcome deviates from the measured
// platforms. An empty slice means the matrix matches.
func (r *ProbeResult) Unexpected() []ProbeCheck {
	var out []ProbeCheck
	for _, c := range r.Checks {
		if !c.AsExpected() {
			out = append(out, c)
		}
	}
	return out
}

// AllExpected reports whether every check matched.
func (r *ProbeResult) AllExpected() bool { return len(r.Unexpected()) == 0 }

// ProbeOptions configures RunPermissionMatrix.
type ProbeOptions struct {
	// ConditionType is the type of the probe condition written and removed
	// during the matrix. Default "Medik8sNodeIdentityProbe".
	ConditionType string
	// TaintKey is the key of the taint whose write the matrix attempts (and
	// expects to be refused). Default "medik8s.io/nodeidentity-probe".
	TaintKey string
	// OtherNode, when non-empty, enables the other-node read check against
	// that node. Reads of other nodes are permitted to the node identity;
	// writes never are.
	OtherNode string
	// ConditionTimestamp is the RFC3339 timestamp written into the probe
	// condition's lastTransitionTime. The package sets no hidden clock; an
	// empty value omits the field.
	ConditionTimestamp string
}

func (o ProbeOptions) conditionType() string {
	if o.ConditionType != "" {
		return o.ConditionType
	}
	return "Medik8sNodeIdentityProbe"
}

func (o ProbeOptions) taintKey() string {
	if o.TaintKey != "" {
		return o.TaintKey
	}
	return "medik8s.io/nodeidentity-probe"
}

// RunPermissionMatrix measures what this node identity may do to its own
// Node: the permission matrix the pre-kubelet self-cordon design depends on
// (cordon and uncordon permitted, taint writes refused, own status conditions
// permitted, reads of other nodes permitted). It is the executable form of
// the per-platform verification that design requires, intended for
// qualification runs and conformance tests — one call per supported
// OpenShift version.
//
// Run on an exclusive qualification node with no concurrent scheduling-state
// writers. Cleanup is best-effort and failures are reported. A shared boolean
// cannot identify an administrator reasserting the same cordon. Initially
// cordoned nodes remain cordoned and report uncordon as unmeasured. Taint
// permission is measured using dry-run only; no live taint cleanup is sent.
func (c *Client) RunPermissionMatrix(ctx context.Context, opts ProbeOptions) *ProbeResult {
	res := &ProbeResult{
		NodeName:  c.cfg.NodeName,
		APIServer: c.cfg.APIServer,
	}
	record := func(name string, expected ProbeOutcome, err error) {
		switch {
		case err == nil:
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomePermitted})
		case errors.Is(err, ErrForbidden):
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomeRefused})
		default:
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomeFailed, Detail: err.Error()})
		}
	}

	// 1. Read the own node; it anchors the UID and the prior cordon state.
	node, err := c.GetOwnNode(ctx)
	record("get-own-node", OutcomePermitted, err)
	if err != nil {
		return res
	}
	res.NodeUID = node.Metadata.UID
	priorUnschedulable := node.Spec.Unschedulable
	cordonedByUs := false
	cordonAttempted := false
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		record("probe identity", OutcomePermitted, fmt.Errorf("create probe identity: %w", err))
		return res
	}
	token := hex.EncodeToString(tokenBytes)

	// Cleanup never reuses the matrix context, which may be expired by the
	// time it runs: each cleanup derives its own bounded context.
	cleanupCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}

	// Never turn an uncertain write or failed read into permission to uncordon.
	defer func() {
		if priorUnschedulable {
			record("uncordon (spec.unschedulable=false)", OutcomePermitted,
				fmt.Errorf("not measured: preserving a pre-existing cordon; use an initially uncordoned qualification node"))
			return
		}
		if !cordonAttempted {
			return
		}
		cctx, cancel := cleanupCtx()
		defer cancel()
		current, err := c.GetOwnNode(cctx)
		if err != nil {
			record("restore cordon (cleanup)", OutcomePermitted,
				fmt.Errorf("current state unknown; no restore attempted; inspect the node before manual recovery: %w", err))
			return
		}
		if !cordonedByUs {
			record("restore cordon (cleanup)", OutcomePermitted,
				fmt.Errorf("cordon was not confirmed; node is currently %s; no automatic restore; inspect ownership before manual recovery", cordonWord(current.Spec.Unschedulable)))
			return
		}
		if !current.Spec.Unschedulable {
			record("restore cordon (cleanup)", OutcomePermitted,
				fmt.Errorf("intervening cordon update detected; left intact; uncordon permission was not measured"))
			return
		}
		restored, err := c.patchUnschedulableAtVersion(cctx, false, current.Metadata.ResourceVersion)
		if err == nil && restored.Spec.Unschedulable {
			err = fmt.Errorf("%w: uncordon accepted but response is still cordoned", ErrAPIServer)
		}
		if err != nil {
			state := "unknown (a follow-up read failed)"
			if again, readErr := c.GetOwnNode(cctx); readErr == nil {
				state = cordonWord(again.Spec.Unschedulable)
			}
			// Cleanup failures are always failures, even a permission refusal.
			res.Checks = append(res.Checks, ProbeCheck{Name: "restore cordon (cleanup)", Expected: OutcomePermitted,
				Outcome: OutcomeFailed, Detail: fmt.Sprintf("restore failed: %v; node is currently %s; inspect before manual recovery", err, state)})
			return
		}
		record("uncordon (spec.unschedulable=false)", OutcomePermitted, nil)
	}()

	// 2. Cordon and read the flag back from the patch response.
	cordonAttempted = true
	cordoned, err := c.Cordon(ctx)
	if err == nil && !cordoned.Spec.Unschedulable {
		err = fmt.Errorf("%w: patch accepted but spec.unschedulable is not set in the response", ErrAPIServer)
	}
	record("cordon (spec.unschedulable=true)", OutcomePermitted, err)
	cordonedByUs = !priorUnschedulable && err == nil && cordoned.Spec.Unschedulable

	// 3. Write the probe condition; remove it on the way out.
	condType := opts.conditionType()
	if existing := findCondition(node, condType); existing {
		// Another run's condition is already there; overwriting it would
		// destroy its data and our cleanup would remove it. Refuse.
		res.Checks = append(res.Checks, ProbeCheck{
			Name: "condition-write (nodes/status)", Expected: OutcomePermitted,
			Outcome: OutcomeFailed,
			Detail:  fmt.Sprintf("a %q condition already exists (likely a previous run that did not clean up); refusing to overwrite it - remove it or set ProbeOptions.ConditionType", condType),
		})
	} else {
		cond := Condition{
			Type:    condType,
			Status:  "True",
			Reason:  "PermissionMatrix",
			Message: "pkg/nodeidentity permission matrix probe: " + token,
		}
		if opts.ConditionTimestamp != "" {
			cond.LastTransitionTime = opts.ConditionTimestamp
		}
		err = c.patchConditionWithRetry(ctx, cond, 5)
		record("condition-write (nodes/status)", OutcomePermitted, err)
		// The removal is deferred regardless of the write's reported result:
		// a failed response does not prove the write was not applied (a
		// timeout after the server processed it, for example), and removal
		// of an absent condition is a success.
		defer func() {
			cctx, cancel := cleanupCtx()
			defer cancel()
			if err := c.removeProbeCondition(cctx, cond); err != nil {
				res.Checks = append(res.Checks, ProbeCheck{
					Name: "remove probe condition (cleanup)", Expected: OutcomePermitted,
					Outcome: OutcomeFailed, Detail: err.Error(),
				})
			}
		}()
	}

	// Kubernetes dry-run runs admission but never persists a taint. A unique
	// value ensures this is a modification even if the key already exists.
	err = c.attemptProbeTaint(ctx, opts.taintKey(), token)
	record("taint-write (expect refusal)", OutcomeRefused, err)
	if err == nil {
		res.Checks[len(res.Checks)-1].Detail = "taint write was PERMITTED; dry-run stored nothing; no live rollback attempted"
	}

	// 5. Optionally read another node.
	if opts.OtherNode != "" {
		if opts.OtherNode == c.cfg.NodeName {
			res.Checks = append(res.Checks, ProbeCheck{
				Name: "get-other-node (read)", Expected: OutcomePermitted,
				Outcome: OutcomeFailed,
				Detail:  "OtherNode is the identity's own node; an own-node read is measured by get-own-node and cannot measure other-node permissions",
			})
		} else {
			_, err = c.GetNode(ctx, opts.OtherNode)
			record("get-other-node (read)", OutcomePermitted, err)
		}
	}

	return res
}

// findCondition reports whether a condition of the given type is present.
func findCondition(node *Node, condType string) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == condType {
			return true
		}
	}
	return false
}

// patchConditionWithRetry checks absence on every attempt and binds that
// check to the write's explicit resourceVersion, not the shared client cache.
func (c *Client) patchConditionWithRetry(ctx context.Context, cond Condition, attempts int) error {
	for i := 0; i < attempts; i++ {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			return err
		}
		if findCondition(node, cond.Type) {
			return fmt.Errorf("%w: condition %q appeared before probe write; refusing to overwrite it", ErrConflict, cond.Type)
		}
		_, err = c.PatchOwnNodeConditionAtVersion(ctx, node.Metadata.ResourceVersion, cond)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("%w: probe condition write retries exhausted", ErrConflict)
}

// removeProbeCondition removes only this invocation's condition. The unique
// message proves ownership; a resourceVersion test binds that observation to
// removal without round-tripping unknown condition fields.
func (c *Client) removeProbeCondition(ctx context.Context, owned Condition) error {
	for attempt := 0; attempt < 5; attempt++ {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			return err
		}
		index := -1
		for i, cond := range node.Status.Conditions {
			if cond.Type == owned.Type {
				if cond.Message != owned.Message {
					return fmt.Errorf("%w: condition %q belongs to another writer; left intact", ErrConflict, owned.Type)
				}
				index = i
				break
			}
		}
		if index < 0 {
			return nil
		}
		patch, err := buildConditionRemovePatch(index, owned.Type, c.cfg.ExpectedUID)
		if err != nil {
			return err
		}
		// buildConditionRemovePatch returns a JSON array. Prepend an RV test
		// so an ownership change after our GET cannot pass the type-only test.
		guarded := fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":%s},%s`, mustQuote(node.Metadata.ResourceVersion), patch[1:])
		_, err = c.PatchOwnNodeStatus(ctx, PatchTypeJSONPatch, []byte(guarded))
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrAPIServer) {
			return err
		}
	}
	return fmt.Errorf("%w: probe condition cleanup retries exhausted", ErrConflict)
}

// attemptProbeTaint strategic-merges one probe taint onto the own node,
// requesting dry-run processing: authorization and admission still run — on
// the measured platforms NodeRestriction refuses with 403 — but a platform
// that accepts the write stores nothing. On the measured platforms this is
// refused with 403 before anything is stored.
//
// The patch carries the expected UID as an atomic server-side precondition:
// a replaced Node object fails the write with HTTP 422 before any spec
// change is applied.
func (c *Client) attemptProbeTaint(ctx context.Context, key, value string) error {
	body, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]string{"uid": c.cfg.ExpectedUID},
		"spec":     map[string]interface{}{"taints": []Taint{{Key: key, Value: value, Effect: "NoSchedule"}}},
	})
	if err != nil {
		return fmt.Errorf("%w: cannot marshal taint probe: %w", ErrInvalidConfig, err)
	}
	resp, err := c.do(ctx, http.MethodPatch,
		"/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName)+"?dryRun=All", PatchTypeStrategicMerge, body)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)
	return classify(resp)
}

func cordonWord(unschedulable bool) string {
	if unschedulable {
		return "cordoned"
	}
	return "uncordoned"
}
