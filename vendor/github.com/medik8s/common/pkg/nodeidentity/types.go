package nodeidentity

import (
	"encoding/json"
	"fmt"
	"io"
)

// Node is the minimal typed projection of a Node object. Unknown fields in
// the server's response are ignored on decode; this package never
// round-trips a Node (patch bodies are built, not derived from a decoded
// object), so the projection can stay small.
type Node struct {
	Metadata NodeMeta   `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

// NodeSpec carries the scheduling fields this package reads and writes: the
// cordon flag (the one spec write the node identity holds) and existing taints.
type NodeSpec struct {
	Unschedulable bool    `json:"unschedulable,omitempty"`
	Taints        []Taint `json:"taints,omitempty"`
}

// Taint mirrors the Node taint fields relevant to the permission matrix.
type Taint struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Effect    string `json:"effect,omitempty"`
	TimeAdded string `json:"timeAdded,omitempty"`
}

// NodeMeta carries the fields this package validates and uses as preconditions.
type NodeMeta struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

// NodeStatus carries the conditions this package writes.
type NodeStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
}

// Condition mirrors the Node condition fields relevant to status reporting.
// LastTransitionTime is passed through as an RFC3339 string only when the
// caller provides it; this package never sets a hidden clock in the patch path.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// maxNodeBodyBytes bounds response decoding. A Node object with a realistic
// condition set is far smaller; the bound exists so a hostile or broken server
// cannot make the client allocate unbounded memory.
const maxNodeBodyBytes = 4 << 20 // 4 MiB

// decodeNode parses a Node JSON document. Unknown fields are ignored.
func decodeNode(data []byte) (*Node, error) {
	var n Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("%w: cannot decode node response: %v", ErrAPIServer, err)
	}
	return &n, nil
}

// decodeNodeBody reads and parses a Node JSON document from an http response
// body with a size bound.
func decodeNodeBody(r io.Reader) (*Node, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxNodeBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read node response: %w", ErrAPIServer, err)
	}
	if len(data) > maxNodeBodyBytes {
		return nil, fmt.Errorf("%w: node response exceeds %d bytes", ErrAPIServer, maxNodeBodyBytes)
	}
	return decodeNode(data)
}
