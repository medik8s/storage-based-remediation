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

// Package noderecord stores the node execution record: the per-Node
// annotation that peers use to coordinate a BlockPerNode remediation.
//
// Every peer reads and writes the same key on the same Node, so the record is
// shared state guarded only by the API server's optimistic concurrency. Two
// rules follow for all callers: write through Update, so the write carries a
// resourceVersion precondition, and preserve fields this build does not
// understand instead of dropping them (see Record.Unknown).
package noderecord

import (
	"encoding/json"
	"time"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// AnnotationKey is the Node annotation holding the record. Peers write it and
// the target agent reads it to decide whether a reboot is authorized.
const AnnotationKey = "sbr.medik8s.io/remediation-owner"

// SchemaVersion is the record layout this build understands. A record at any
// other version is rejected rather than rewritten, because a newer SBR may
// have written fields this build would silently drop.
const SchemaVersion = 1

// ExecutionPhase is the lifecycle state of one remediation request.
// Cancelled, Superseded and BootEnded are terminal: a CR that reaches one is
// never offered again, which is what stops a peer from re-offering a request
// the target already declined or outlived.
type ExecutionPhase string

const (
	PhaseOffered    ExecutionPhase = "Offered"
	PhaseAccepted   ExecutionPhase = "Accepted"
	PhaseCancelled  ExecutionPhase = "Cancelled"
	PhaseSuperseded ExecutionPhase = "Superseded"
	PhaseBootEnded  ExecutionPhase = "BootEnded"
)

// OOSTaintPolicy decides when a peer may apply the out-of-service taint.
// OnReboot demands evidence that the original boot ended; OnTimeout also
// allows it once the reboot grace period expires, without that evidence.
type OOSTaintPolicy string

const (
	PolicyOnTimeout OOSTaintPolicy = "OnTimeout"
	PolicyOnReboot  OOSTaintPolicy = "OnReboot"
)

// Record is the decoded annotation. Decode and Encode are the only supported
// conversions between it and the stored string.
type Record struct {
	SchemaVersion int `json:"schemaVersion"`
	// NodeUID binds the record to one Node incarnation. A Node deleted and
	// recreated under the same name gets a new UID, and a record left over
	// from the old one must never authorize changes to its replacement.
	NodeUID            types.UID    `json:"nodeUID"`
	Execution          *Execution   `json:"execution,omitempty"`
	PreviousExecutions []Execution  `json:"previousExecutions,omitempty"`
	Policy             *Policy      `json:"policy,omitempty"`
	Budget             Budget       `json:"budget"`
	Restrictions       Restrictions `json:"restrictions"`
	// CoordinationRevision is a Node-wide counter, not a per-request one. It
	// starts at zero, survives execution replacement and restriction release,
	// and is incremented on every deletion-release update including retries.
	// It does not replace the resourceVersion precondition; it makes a
	// completed release visible to a writer whose own fields look unchanged.
	CoordinationRevision int64 `json:"coordinationRevision"`
	// Unknown holds fields this build does not recognize, kept verbatim so a
	// round trip through an older SBR does not discard what a newer one
	// wrote. It is excluded from the struct tags and re-merged by
	// MarshalJSON. UnmarshalJSON strips the known keys first, so an entry
	// here can never shadow a declared field.
	Unknown map[string]json.RawMessage `json:"-"`
}

type CRRef struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

type ConfigRef struct {
	UID        types.UID                                   `json:"uid"`
	VolumeMode medik8sv1alpha1.SharedStorageVolumeModeType `json:"volumeMode"`
}

// Execution is one remediation request and what has been observed about it.
type Execution struct {
	CR     CRRef     `json:"cr"`
	Config ConfigRef `json:"config"`
	// InitialBootID is the boot the request was made against. The target
	// refuses to execute when its current boot ID differs, so a request
	// cannot survive a reboot it did not cause.
	InitialBootID string         `json:"initialBootID"`
	Phase         ExecutionPhase `json:"phase"`
	// ObservedBootID is kept separate from Phase: evidence that a boot ended
	// is not the same fact as the target having accepted the request, and a
	// late report must not be read as acceptance.
	ObservedBootID string `json:"observedBootID,omitempty"`
}

// Policy is the out-of-service decision for the request currently holding it.
type Policy struct {
	RequestUID  types.UID      `json:"requestUID"`
	TaintPolicy OOSTaintPolicy `json:"taintPolicy"`
	// OfferedAt and RebootDeadline are persisted rather than recomputed: the
	// deadline is fixed when the request is first offered, and acceptance,
	// peer restart, API recovery or a configuration change must not restart
	// it.
	OfferedAt       time.Time `json:"offeredAt"`
	RebootDeadline  time.Time `json:"rebootDeadline"`
	TaintConfirmed  bool      `json:"taintConfirmed"`
	CanStartOrRetry bool      `json:"canStartOrRetry"`
}

// Budget is the autonomous reboot history used to check the attempt limit.
type Budget struct {
	// HistoryKnown false means the history could not be read. Callers must
	// treat that as unknown and never as "no attempts so far", since an
	// unreadable history must not grant another autonomous reboot.
	HistoryKnown bool     `json:"historyKnown"`
	OutageIDs    []string `json:"outageIDs,omitempty"`
}

// Restrictions records the scheduling restrictions SBR added and who still
// needs them.
type Restrictions struct {
	// OriginalCordon and OriginalOOSTaint are pointers so that "never
	// captured" stays distinguishable from "captured as false". They are
	// taken when the first holder enters and cleared when the last leaves,
	// which is what keeps an administrator's pre-existing cordon from being
	// released as if SBR had set it.
	OriginalCordon   *bool `json:"originalCordon,omitempty"`
	OriginalOOSTaint *bool `json:"originalOOSTaint,omitempty"`
	// RequiredByCRs are the holders. Restrictions SBR added are released only
	// once this is empty.
	RequiredByCRs []types.UID `json:"requiredByCRs,omitempty"`
}

// recordAlias drops Record's MarshalJSON and UnmarshalJSON methods so the
// codec can run encoding/json over the declared fields without recursing back
// into itself.
type recordAlias Record

// knownKeys must list every JSON tag declared on Record. UnmarshalJSON
// subtracts these from the raw object to find the unknown fields, so a field
// added to Record but not here would be stored twice: once decoded into its
// own field and once verbatim in Unknown.
var knownKeys = []string{
	"schemaVersion", "nodeUID", "execution", "previousExecutions",
	"policy", "budget", "restrictions", "coordinationRevision",
}

// MaxRecordBytes bounds the encoded record. Kubernetes caps all of a
// resource's annotations at 256KB combined, so the record claims a fraction
// of that and leaves the rest to other writers. Encode refuses to produce a
// larger record instead of letting the Node update fail at the API server.
const MaxRecordBytes = 64 * 1024
