package nodestate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Default paths and schema constants for component-owned host journals.
const (
	// Dir is the default node-local directory for participating components.
	Dir = "/var/lib/medik8s/state"

	// FileExtension is appended to a Component to form its file name.
	FileExtension = ".json"

	// SBRFile and SNRFile are conventional names, not required installed files.
	SBRFile = string(ComponentSBR) + FileExtension
	SNRFile = string(ComponentSNR) + FileExtension

	// Schema is the document schema version this build writes.
	//
	// Evolve the schema additively: new optional members are preserved across
	// a staggered upgrade by the unknown-member machinery, so they do not need
	// a version bump. Bump Schema only for a change that an older build would
	// misinterpret rather than merely ignore — and understand that doing so
	// makes every older peer treat the file as unreadable.
	Schema = 1

	// MinSupportedSchema is the oldest schema version this build will read.
	MinSupportedSchema = 1
)

// ShutdownState records how the writing agent last stopped. It is the input to
// the start-up classification: combined with a change in BootID it separates
// an orderly reboot from a watchdog reset, a panic or a power loss.
type ShutdownState string

const (
	// ShutdownUnknown means the agent did not record how it stopped. Treat an
	// unknown shutdown after a boot id change as unclean.
	ShutdownUnknown ShutdownState = ""
	// ShutdownRunning means the agent was running when the document was
	// written. Seeing this after a boot id change means the node went down
	// without the agent getting to record it.
	ShutdownRunning ShutdownState = "running"
	// ShutdownClean means the agent stopped in an orderly way.
	ShutdownClean ShutdownState = "clean"
)

// Method records how a reboot was carried out.
type Method string

const (
	// MethodUnknown means the writer did not record a method.
	MethodUnknown Method = ""
	// MethodWatchdog means a hardware watchdog was allowed to expire.
	MethodWatchdog Method = "watchdog"
	// MethodSoftware means the agent issued a reboot or a reset request.
	MethodSoftware Method = "software"
)

// Document is one agent's node-local state file.
//
// Exactly one component owns a given Document. Other components may explicitly
// read it if their integration requires it; no cross-component sharing is
// assumed. Required consumer state lives in Agent.Extra and diagnostic history
// lives in Reboots. This envelope alone grants no execution or cleanup authority.
// The consumer validates its state, identities and authorization policy.
// Privileged host processes are outside the trust boundary. See package docs.
//
// Unknown preserves members written by a newer peer. Encoding and decoding a
// Document round-trips them unchanged.
type Document struct {
	// SchemaVersion is the document format version. Store.Write always stamps
	// the current Schema.
	SchemaVersion int `json:"schemaVersion"`

	// Node is the Kubernetes node name this document describes.
	Node string `json:"node,omitempty"`
	// NodeUID is the node object's UID. It distinguishes a re-registered node
	// from the one whose history this file records.
	NodeUID string `json:"nodeUID,omitempty"`
	// BootID is the kernel boot identifier current when the document was
	// written. See ReadBootID.
	BootID string `json:"bootID,omitempty"`

	// UpdatedAt is when Store.Write last stamped this document.
	UpdatedAt time.Time `json:"updatedAt"`
	// Revision increases by one on every successful write by the owning Store.
	// It never decreases: a Store that finds a higher revision on disk than it
	// last wrote continues from the higher value.
	Revision uint64 `json:"revision,omitempty"`
	// WriterID identifies the process that last wrote the document. A
	// value other than the reader's own is how a foreign write is detected.
	WriterID string `json:"writerID,omitempty"`

	// Agent describes the writing agent.
	Agent AgentState `json:"agent"`
	// Config is the configuration the agent was operating under, as opaque
	// key/value pairs. It exists so a post-mortem can tell which settings were
	// live, and so an agent can fall back to its last known-good values when
	// the API is unreachable.
	Config map[string]string `json:"config,omitempty"`

	// IO is a bounded ring of recent storage operations, newest last. It is
	// diagnostic only.
	IO []IOEntry `json:"io,omitempty"`
	// Conditions is the agent's view of the node conditions it owns, one entry
	// per type.
	Conditions []ConditionEntry `json:"conditions,omitempty"`
	// Events is a bounded ring of recent notable occurrences, newest last.
	Events []EventEntry `json:"events,omitempty"`

	// Reboots is the reboot history, oldest first.
	//
	// It is trimmed by age, never routinely by count, because dropping the
	// oldest entries of a looping node would hand it a fresh reboot budget.
	// Two bounds qualify that: a floor below which the age trim will not go,
	// so a stepped clock cannot erase the history, and an outer ceiling that
	// keeps the document writable. Both move RebootHistoryCompleteSince when
	// they bite. See RetentionPolicy.
	Reboots []RebootEntry `json:"reboots"`

	// RebootHistoryCompleteSince is the earliest instant from which Reboots
	// is known to hold every record. Anything before it was deliberately
	// aged out, or dropped by the safety ceiling.
	//
	// A reboot budget is a count over a window, and a count is only an answer
	// if the history covers the window. Ask RebootHistoryComplete before
	// trusting one; if the window starts earlier than this, the count is a
	// lower bound and the caller must fail toward counting.
	//
	// It is a point in time rather than a "was truncated" flag on purpose. A
	// flag, once set, can never be cleared without lying, so a node that
	// briefly overflowed would be marked untrustworthy for the rest of its
	// life — and a caller treating that as "budget exhausted" would never let
	// it self-fence again. A horizon self-clears: as the window moves forward
	// past it, the same history becomes a complete answer again.
	RebootHistoryCompleteSince time.Time `json:"rebootHistoryCompleteSince"`

	// HistoryReconciledAt is when ReconcileHistory last replaced this
	// history, or zero if it never did.
	//
	// It lives in the envelope rather than the event ring because the ring
	// is pruned: a consumer that discovers a month later that its reset
	// budget was reconstructed rather than observed has discovered it too
	// late. Load turns it into StatusRecovered while it still describes the
	// records a budget would be computed from.
	HistoryReconciledAt time.Time `json:"historyReconciledAt"`

	// RebootHistoryUnknown records dropped entries with unknown timestamps.
	// Only explicit consumer reconciliation can clear it.
	RebootHistoryUnknown bool `json:"rebootHistoryUnknown,omitempty"`

	// Unknown holds top-level members written by a build newer than this one.
	// It is populated on decode and re-emitted on encode. Callers do not
	// normally touch it.
	Unknown map[string]json.RawMessage `json:"-"`
}

// AgentState describes the process that owns the document.
type AgentState struct {
	// Component is the owning writer. Store.Write stamps it and refuses a
	// document stamped with anyone else.
	Component Component `json:"component,omitempty"`
	// Version is the agent's build version, for post-mortem correlation.
	Version string `json:"version,omitempty"`
	// PID is the writing process id. With BootID and StartedAt it identifies
	// the process, which is what makes a second live writer recognisable
	// rather than merely suspected.
	//
	// Store.Write stamps it. A reader seeing a PID other than its own, from
	// the same boot, is looking at a file another process owns — which the
	// single-writer contract forbids and which a DaemonSet surge or a pod
	// stuck terminating can produce.
	PID int `json:"pid,omitempty"`
	// StartedAt is when the writing process started.
	//
	// Note for anyone adding a field here: encoding/json's omitempty has no
	// effect on a time.Time, so a zero time is always written out in full.
	// Tagging one with omitempty only misleads the next reader.
	StartedAt time.Time `json:"startedAt"`
	// Shutdown records how the agent last stopped.
	Shutdown ShutdownState `json:"shutdown,omitempty"`
	// ResetCommitted reports that the agent has committed to a reset that has
	// not yet taken effect in its bound boot. A later write failure must not
	// disarm the committed watchdog in that boot. After a valid boot change, the
	// consumer atomically clears the old commitment and preserves attempt and
	// budget records before arming the new watchdog. Common does not perform
	// this policy.
	//
	// The name is "reset", not "fence", deliberately. The per-node block mode
	// this package serves has no fence: no shared NodeMap, no peer heartbeat
	// and no fence message. Carrying fencing vocabulary into it would
	// re-import the model the mode exists to escape. It is also one of three
	// names the design had for a single fact — a journal commitment — now
	// unified on "commit": this field, "local commitment" in prose, and
	// commitState: Committed inside the consumer's own record.
	ResetCommitted bool `json:"resetCommitted,omitempty"`
	// Extra carries agent-specific key/value state without requiring a schema
	// change in this shared package.
	Extra map[string]string `json:"extra,omitempty"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// IOEntry is one recorded storage operation.
type IOEntry struct {
	// TS is when the operation completed, or when it was abandoned.
	TS time.Time `json:"ts"`
	// Op is the operation, conventionally "read" or "write".
	Op string `json:"op,omitempty"`
	// Target is the device or path the operation addressed.
	Target string `json:"target,omitempty"`
	// LatencyMS is the observed duration in milliseconds.
	LatencyMS int64 `json:"latencyMS,omitempty"`
	// OK reports success.
	OK bool `json:"ok"`
	// Error is the failure text when OK is false.
	Error string `json:"error,omitempty"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// ConditionEntry mirrors a node condition the agent publishes, so that a
// post-mortem can see what the agent believed without the API.
type ConditionEntry struct {
	// Type is the condition type, for example "SBRStorageUnhealthy".
	Type string `json:"type"`
	// Status is "True", "False" or "Unknown".
	Status string `json:"status"`
	// Reason is a short CamelCase cause.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable explanation.
	Message string `json:"message,omitempty"`
	// LastTransitionTime is when Status last changed. Document.SetCondition
	// carries it forward when the status is unchanged.
	LastTransitionTime time.Time `json:"lastTransitionTime"`
	// ObservedAt is when the condition was last evaluated.
	ObservedAt time.Time `json:"observedAt"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// EventEntry records something worth seeing in a post-mortem. It is not a
// Kubernetes event; emit one of those as well when the API is reachable.
type EventEntry struct {
	// TS is when the event occurred.
	TS time.Time `json:"ts"`
	// Type is "Normal" or "Warning".
	Type string `json:"type,omitempty"`
	// Reason is a short CamelCase cause.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable explanation.
	Message string `json:"message,omitempty"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// RebootEntry records one reboot this node underwent or committed to.
//
// It is written before the reboot is triggered, with Completed false, and
// confirmed afterwards. An entry that is still pending after a boot id change
// is evidence that the reboot happened and the agent did not survive to say
// so.
type RebootEntry struct {
	// TS is when the reboot was committed to.
	TS time.Time `json:"ts"`
	// Source is the component that recorded the entry.
	Source Component `json:"source,omitempty"`
	// Method is how the reboot was carried out.
	Method Method `json:"method,omitempty"`
	// Reason is a short CamelCase cause, for example "TotalOutage" or
	// "RemediationCR".
	Reason string `json:"reason,omitempty"`
	// EpisodeID groups every escalation stage of one unhealthy period, as
	// stamped by the node health check that started it. Opaque; do not parse.
	EpisodeID string `json:"episodeID,omitempty"`
	// Remediation references the remediation custom resource this reboot
	// served, if any. A nil Remediation marks an autonomous reboot, which the
	// design budgets separately from a requested one.
	Remediation *CRRef `json:"remediation,omitempty"`
	// BeforeBootID is the kernel boot id current when the entry was written.
	BeforeBootID string `json:"beforeBootID,omitempty"`
	// AfterBootID is a diagnostic local boot observation. It does not establish
	// ordered execution evidence. SBR reads the exact Accepted Node record before
	// reading the local boot ID and conditionally publishes that observation;
	// a peer validates it before setting BootEnded. Raw Node status is insufficient.
	AfterBootID string `json:"afterBootID,omitempty"`
	// Completed reports a diagnostic completion observation, not API acceptance,
	// BootEnded, continuing isolation, or permission to clean up workloads.
	Completed bool `json:"completed"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// CRRef identifies one remediation custom resource.
type CRRef struct {
	// Kind is the resource kind, for example "SelfNodeRemediation".
	Kind string `json:"kind,omitempty"`
	// Namespace is the resource namespace.
	Namespace string `json:"namespace,omitempty"`
	// Name is the resource name. A node health check names the resource after
	// the node, so the name is reused across episodes and is not an identity.
	Name string `json:"name,omitempty"`
	// UID and NodeUID form the execution identity across state files.
	//
	// Both are required and generation is deliberately excluded. A request
	// keeps one identity for its whole life: a changed generation, a
	// replaced configuration or a later boot must not grant the same
	// resource another attempt, and treating them as distinct identities
	// would let one request be charged to the budget twice.
	UID string `json:"uid,omitempty"`
	// NodeUID is the immutable target Node identity, not its name or slot.
	//
	// The name is not an identity here: a node health check names the
	// remediation resource after the node, and a replacement node can reuse
	// the name. Keying on the UID stops a replacement inheriting its
	// predecessor's execution history.
	NodeUID string `json:"nodeUID,omitempty"`
	// Generation is the spec generation the reboot served.
	Generation int64 `json:"generation,omitempty"`

	// Unknown holds members written by a newer build. See Document.Unknown.
	Unknown map[string]json.RawMessage `json:"-"`
}

// Key returns the CR UID and Node UID pair. Generation is evidence metadata,
// never permission to execute a request again. Invalid references return
// an empty key and cannot participate in matching or deduplication.
func (r CRRef) Key() string {
	if r.Validate() != nil {
		return ""
	}
	// JSON array encoding avoids delimiter collisions without assuming UID syntax.
	key, _ := json.Marshal([2]string{r.UID, r.NodeUID})
	return string(key)
}

// Validate reports whether the reference can identify a remediation.
//
// Both the resource UID and target Node UID are required. A reference carrying a kind and a
// name but no UID looks informative and is not: a node health check names the
// remediation resource after the node, so the same name is reused for every
// episode on that node and distinguishes nothing.
func (r CRRef) Validate() error {
	if r.UID == "" || r.NodeUID == "" {
		return fmt.Errorf("%w: a remediation reference needs both CR UID and Node UID; kind %q and name %q do not identify one",
			ErrInvalidReference, r.Kind, r.Name)
	}
	if r.Generation < 0 {
		return fmt.Errorf("%w: generation %d is negative", ErrInvalidReference, r.Generation)
	}
	return nil
}

// identifies reports whether this reference may be de-duplicated on.
func (r *CRRef) identifies() bool { return r != nil && r.Validate() == nil }

// NewDocument returns an empty document owned by c, stamped with the current
// schema version. It is what Store.Load hands back when there is no file, or
// when the file could not be used.
func NewDocument(c Component) *Document {
	return &Document{
		SchemaVersion: Schema,
		Agent:         AgentState{Component: c},
		Reboots:       []RebootEntry{},
	}
}

// Clone returns a deep copy. Every slice, map and pointer reachable from the
// result is independent of the receiver, so a caller may hand a snapshot to
// another goroutine without sharing state with the Store.
//
// Clone on a nil receiver returns nil.
func (d *Document) Clone() *Document {
	if d == nil {
		return nil
	}
	out := *d
	out.Agent = d.Agent.clone()
	out.Config = cloneStringMap(d.Config)
	out.Unknown = cloneRawMap(d.Unknown)

	if d.IO != nil {
		out.IO = make([]IOEntry, len(d.IO))
		for i, e := range d.IO {
			out.IO[i] = e
			out.IO[i].Unknown = cloneRawMap(e.Unknown)
		}
	}
	if d.Conditions != nil {
		out.Conditions = make([]ConditionEntry, len(d.Conditions))
		for i, e := range d.Conditions {
			out.Conditions[i] = e
			out.Conditions[i].Unknown = cloneRawMap(e.Unknown)
		}
	}
	if d.Events != nil {
		out.Events = make([]EventEntry, len(d.Events))
		for i, e := range d.Events {
			out.Events[i] = e
			out.Events[i].Unknown = cloneRawMap(e.Unknown)
		}
	}
	if d.Reboots != nil {
		out.Reboots = make([]RebootEntry, len(d.Reboots))
		for i, e := range d.Reboots {
			out.Reboots[i] = e.clone()
		}
	}
	return &out
}

func (a AgentState) clone() AgentState {
	out := a
	out.Extra = cloneStringMap(a.Extra)
	out.Unknown = cloneRawMap(a.Unknown)
	return out
}

func (e RebootEntry) clone() RebootEntry {
	out := e
	out.Unknown = cloneRawMap(e.Unknown)
	if e.Remediation != nil {
		ref := *e.Remediation
		ref.Unknown = cloneRawMap(e.Remediation.Unknown)
		out.Remediation = &ref
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// preservedBytes totals the unrecognised members carried in this document,
// at every level. It is the size the next write has to find room for on top
// of everything the caller actually asked to store.
func (d *Document) preservedBytes() int {
	total := rawMapBytes(d.Unknown) + rawMapBytes(d.Agent.Unknown)
	for _, e := range d.IO {
		total += rawMapBytes(e.Unknown)
	}
	for _, e := range d.Conditions {
		total += rawMapBytes(e.Unknown)
	}
	for _, e := range d.Events {
		total += rawMapBytes(e.Unknown)
	}
	for _, e := range d.Reboots {
		total += rawMapBytes(e.Unknown)
		if e.Remediation != nil {
			total += rawMapBytes(e.Remediation.Unknown)
		}
	}
	return total
}

// dropPreserved discards every unrecognised member, at every level.
func (d *Document) dropPreserved() {
	d.Unknown = nil
	d.Agent.Unknown = nil
	for i := range d.IO {
		d.IO[i].Unknown = nil
	}
	for i := range d.Conditions {
		d.Conditions[i].Unknown = nil
	}
	for i := range d.Events {
		d.Events[i].Unknown = nil
	}
	for i := range d.Reboots {
		d.Reboots[i].Unknown = nil
		if d.Reboots[i].Remediation != nil {
			d.Reboots[i].Remediation.Unknown = nil
		}
	}
}

func rawMapBytes(m map[string]json.RawMessage) int {
	total := 0
	for name, raw := range m {
		total += len(name) + len(raw)
	}
	return total
}

// AppendIO adds one storage operation to the ring. Retention trims it on the
// next write; the caller does not have to.
func (d *Document) AppendIO(e IOEntry) {
	d.IO = append(d.IO, e)
}

// AppendEvent adds one event to the ring.
func (d *Document) AppendEvent(e EventEntry) {
	d.Events = append(d.Events, e)
}

// AppendReboot adds a diagnostic record. Unresolved requested identities stay
// requested and are never merged. AppendRebootChecked rejects invalid identities.
// Authoritative SBR identity and reset-budget records belong in Agent.Extra;
// retention does not prune that map.
func (d *Document) AppendReboot(e RebootEntry) {
	// Preserve unresolved requested identities as requested; never charge them
	// to the autonomous budget. They will not be merged or matched.
	d.Reboots = append(d.Reboots, e.clone())
}

// AppendRebootChecked is AppendReboot that reports a malformed remediation
// reference instead of silently dropping it.
//
// Prefer it wherever the reference is built from a live resource, because an
// empty UID there means the caller read the wrong field and the record would
// otherwise be filed as autonomous without anyone noticing.
func (d *Document) AppendRebootChecked(e RebootEntry) error {
	if e.Remediation != nil {
		if err := e.Remediation.Validate(); err != nil {
			return err
		}
	}
	d.Reboots = append(d.Reboots, e)
	return nil
}

// SetCondition inserts or replaces the entry with the same Type.
//
// When the status is unchanged, the existing LastTransitionTime is kept, so
// "how long has this been true?" stays answerable across writes. ObservedAt is
// always taken from c.
func (d *Document) SetCondition(c ConditionEntry) {
	for i := range d.Conditions {
		if d.Conditions[i].Type != c.Type {
			continue
		}
		if d.Conditions[i].Status == c.Status && !d.Conditions[i].LastTransitionTime.IsZero() {
			c.LastTransitionTime = d.Conditions[i].LastTransitionTime
		}
		// Carry forward members a newer peer added to this condition. The
		// package promises unknown fields survive a read-modify-write, and
		// replacing the entry wholesale would break that promise for the one
		// collection whose entries are replaced rather than appended. An
		// explicit value on the replacement still wins.
		if c.Unknown == nil {
			c.Unknown = d.Conditions[i].Unknown
		}
		d.Conditions[i] = c
		return
	}
	d.Conditions = append(d.Conditions, c)
}

// Condition returns the entry of the given type.
func (d *Document) Condition(conditionType string) (ConditionEntry, bool) {
	for _, c := range d.Conditions {
		if c.Type == conditionType {
			return c, true
		}
	}
	return ConditionEntry{}, false
}

// --- JSON, with preservation of members this build does not know about. ---
//
// Each type below defines an alias with identical fields but no methods, so
// that encoding/json can process it without recursing into the custom
// marshaller. See jsonext.go for the mechanism.

type documentAlias Document

var documentAliasType = reflect.TypeOf(documentAlias{})

// UnmarshalJSON decodes a document and retains every top-level member this
// build does not recognise.
func (d *Document) UnmarshalJSON(data []byte) error {
	var alias documentAlias
	unknown, err := decodeObject(data, &alias, documentAliasType)
	if err != nil {
		return err
	}
	*d = Document(alias)
	d.Unknown = unknown
	if d.Reboots == nil {
		d.Reboots = []RebootEntry{}
	}
	return nil
}

// MarshalJSON encodes a document, re-emitting preserved members.
func (d Document) MarshalJSON() ([]byte, error) {
	return encodeObject(documentAlias(d), d.Unknown, documentAliasType)
}

type agentStateAlias AgentState

var agentStateAliasType = reflect.TypeOf(agentStateAlias{})

// UnmarshalJSON decodes an agent state, retaining unrecognised members.
func (a *AgentState) UnmarshalJSON(data []byte) error {
	var alias agentStateAlias
	unknown, err := decodeObject(data, &alias, agentStateAliasType)
	if err != nil {
		return err
	}
	*a = AgentState(alias)
	a.Unknown = unknown
	return nil
}

// MarshalJSON encodes an agent state, re-emitting preserved members.
func (a AgentState) MarshalJSON() ([]byte, error) {
	return encodeObject(agentStateAlias(a), a.Unknown, agentStateAliasType)
}

type ioEntryAlias IOEntry

var ioEntryAliasType = reflect.TypeOf(ioEntryAlias{})

// UnmarshalJSON decodes an I/O entry, retaining unrecognised members.
func (e *IOEntry) UnmarshalJSON(data []byte) error {
	var alias ioEntryAlias
	unknown, err := decodeObject(data, &alias, ioEntryAliasType)
	if err != nil {
		return err
	}
	*e = IOEntry(alias)
	e.Unknown = unknown
	return nil
}

// MarshalJSON encodes an I/O entry, re-emitting preserved members.
func (e IOEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(ioEntryAlias(e), e.Unknown, ioEntryAliasType)
}

type conditionEntryAlias ConditionEntry

var conditionEntryAliasType = reflect.TypeOf(conditionEntryAlias{})

// UnmarshalJSON decodes a condition entry, retaining unrecognised members.
func (c *ConditionEntry) UnmarshalJSON(data []byte) error {
	var alias conditionEntryAlias
	unknown, err := decodeObject(data, &alias, conditionEntryAliasType)
	if err != nil {
		return err
	}
	*c = ConditionEntry(alias)
	c.Unknown = unknown
	return nil
}

// MarshalJSON encodes a condition entry, re-emitting preserved members.
func (c ConditionEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(conditionEntryAlias(c), c.Unknown, conditionEntryAliasType)
}

type eventEntryAlias EventEntry

var eventEntryAliasType = reflect.TypeOf(eventEntryAlias{})

// UnmarshalJSON decodes an event entry, retaining unrecognised members.
func (e *EventEntry) UnmarshalJSON(data []byte) error {
	var alias eventEntryAlias
	unknown, err := decodeObject(data, &alias, eventEntryAliasType)
	if err != nil {
		return err
	}
	*e = EventEntry(alias)
	e.Unknown = unknown
	return nil
}

// MarshalJSON encodes an event entry, re-emitting preserved members.
func (e EventEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(eventEntryAlias(e), e.Unknown, eventEntryAliasType)
}

type rebootEntryAlias RebootEntry

var rebootEntryAliasType = reflect.TypeOf(rebootEntryAlias{})

// UnmarshalJSON decodes a reboot entry, retaining unrecognised members.
func (e *RebootEntry) UnmarshalJSON(data []byte) error {
	var alias rebootEntryAlias
	unknown, err := decodeObject(data, &alias, rebootEntryAliasType)
	if err != nil {
		return err
	}
	*e = RebootEntry(alias)
	e.Unknown = unknown
	return nil
}

// MarshalJSON encodes a reboot entry, re-emitting preserved members.
func (e RebootEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(rebootEntryAlias(e), e.Unknown, rebootEntryAliasType)
}

type crRefAlias CRRef

var crRefAliasType = reflect.TypeOf(crRefAlias{})

// UnmarshalJSON decodes a resource reference, retaining unrecognised members.
func (r *CRRef) UnmarshalJSON(data []byte) error {
	var alias crRefAlias
	unknown, err := decodeObject(data, &alias, crRefAliasType)
	if err != nil {
		return err
	}
	*r = CRRef(alias)
	r.Unknown = unknown
	return nil
}

// MarshalJSON encodes a resource reference, re-emitting preserved members.
func (r CRRef) MarshalJSON() ([]byte, error) {
	return encodeObject(crRefAlias(r), r.Unknown, crRefAliasType)
}
