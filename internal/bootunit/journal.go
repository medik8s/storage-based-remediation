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

package bootunit

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/medik8s/common/pkg/nodestate"
)

// SBRStateVersion is the version of the SBR-specific key set stored in the
// journal's Agent.Extra map. Bump when a key changes semantics.
const SBRStateVersion = "1"

// Journal keys inside Agent.Extra. The arming state value is a JSON string.
const (
	KeyStateVersion = "sbrStateVersion"
	KeyArmingState  = "armingState"
)

// ArmingState is the structured value stored in journal key "armingState".
// The agent writes it when it commits a reboot decision — together with the
// reboot record, in one durable Store.Write — and the boot unit reads it at
// the next boot to decide whether to cordon.
type ArmingState struct {
	// Version is the arming state schema version. Currently 1.
	Version int `json:"version"`

	// Phase is the lifecycle phase. PhaseArmed means the next boot must be
	// protected. The agent clears the key after the protected boot completes
	// or a pending request is confirmed cancelled.
	Phase ArmingPhase `json:"phase"`

	// NodeUID binds the arming state to one Node object. A replaced Node
	// must never be acted on with a stale arming state.
	NodeUID string `json:"nodeUID,omitempty"`

	// ConfigUID binds the arming state to one configuration generation, so
	// a stale decision cannot survive a config replacement.
	ConfigUID string `json:"configUID,omitempty"`

	// IncidentID is an unpredictable identifier for this remediation
	// incident. It is carried through to the SBRRejoinPending condition
	// and matched by the release protocol.
	IncidentID string `json:"incidentID,omitempty"`

	// BeforeBootID is the boot ID the decision was made in. The boot unit
	// must observe a different boot ID before acting on it.
	BeforeBootID string `json:"beforeBootID,omitempty"`

	// Source is "Autonomous" or "Requested". Autonomous attempts also bind
	// an outage epoch.
	Source string `json:"source,omitempty"`
}

// ArmingPhase is the lifecycle phase of an ArmingState.
type ArmingPhase string

const (
	// PhaseArmed means the next boot must be protected.
	PhaseArmed ArmingPhase = "Armed"

	// PhaseCleared means the arming state was explicitly cleared.
	PhaseCleared ArmingPhase = "Cleared"
)

// Sources recorded in ArmingState.Source. They say who decided the reboot, not
// why: the agent's own failure handling, or a remediation an operator or NHC
// requested.
const (
	// SourceAutonomous is a reboot the agent decided on its own evidence.
	SourceAutonomous = "Autonomous"

	// SourceRequested is a reboot requested through a remediation resource.
	SourceRequested = "Requested"
)

// String returns the JSON encoding of the arming state, suitable for
// storage in journal key "armingState".
func (a *ArmingState) String() string {
	b, err := json.Marshal(a)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ParseArmingState decodes the JSON string from journal key "armingState".
// It returns nil, nil when the input is empty (no arming state recorded).
// It returns an error when the input is present but invalid: undecodable
// JSON, an unknown schema version, or a phase outside the known lifecycle.
// Callers who act on the result must treat that error as fail-closed: a
// present-but-unreadable arming state is corruption, not absence — a
// recorded decision with a missing phase must not read as "not armed".
func ParseArmingState(raw string) (*ArmingState, error) {
	if raw == "" {
		return nil, nil
	}
	var a ArmingState
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("cannot decode armingState: %w", err)
	}
	if a.Version != 1 {
		return nil, fmt.Errorf("unknown armingState version %d", a.Version)
	}
	switch a.Phase {
	case PhaseArmed, PhaseCleared:
	default:
		return nil, fmt.Errorf("unknown armingState phase %q", a.Phase)
	}
	return &a, nil
}

// Journal is the SBR view of the node-local state file managed by the shared
// pkg/nodestate library. The boot unit reads it before kubelet starts; the
// agent writes it while running. Creating a Journal never touches the
// filesystem — the store only resolves paths — so the boot unit can open one
// on a host where no journal exists yet.
type Journal struct {
	store *nodestate.Store
	dir   string
}

// NewJournal creates a journal rooted at the given state directory. An empty
// dir selects HostStateRoot. The component is always nodestate.ComponentSBR.
func NewJournal(dir string) (*Journal, error) {
	if dir == "" {
		dir = HostStateRoot
	}
	store, err := nodestate.New(nodestate.ComponentSBR, nodestate.WithDir(dir))
	if err != nil {
		return nil, fmt.Errorf("create journal store: %w", err)
	}
	return &Journal{store: store, dir: dir}, nil
}

// ReadForArming takes the point-in-time view of the journal that
// EvaluateArming consumes. It never returns nil. The zero ArmingSnapshot is
// unusable, so a failed journal open can be represented by a zero snapshot
// and still fails closed.
//
// expectedNodeUID is the Node object the caller acts on (the identity
// recorded at enrollment); it may be empty when the caller cannot supply
// one. An armed decision whose recorded NodeUID binds it to a DIFFERENT
// Node object is ignored: a replaced Node means the cluster forgot this
// host's workloads (deleting the Node object is itself the evacuation), so
// a stale decision must neither cordon the current Node nor publish its
// incident. The host-level tiers (unclean shutdown, storage health) remain
// valid — they describe the host, not the Node object. An arming state that
// records no NodeUID, or an empty expectedNodeUID, cannot be validated and
// is accepted as recorded.
func (j *Journal) ReadForArming(ctx context.Context, expectedNodeUID string) *ArmingSnapshot {
	snap := &ArmingSnapshot{}

	res, err := j.store.Load(ctx)
	switch {
	case err != nil:
		// Access, I/O or host-path faults: not evidence of anything except
		// that the journal cannot be trusted right now.
		snap.problem = err.Error()
	case res.Status == nodestate.StatusAbsent:
		snap.problem = "no journal exists"
	case !res.Status.Trusted():
		snap.problem = fmt.Sprintf("journal %s: %v", res.Status, res.Cause)
	default:
		snap.read = true
		doc := res.Document
		if raw := doc.Agent.Extra[KeyArmingState]; raw != "" {
			arming, perr := ParseArmingState(raw)
			switch {
			case perr != nil:
				snap.problem = perr.Error()
			case arming.Phase == PhaseArmed:
				if expectedNodeUID != "" && arming.NodeUID != "" && arming.NodeUID != expectedNodeUID {
					// The decision belongs to a different Node object —
					// see the method comment. Not armed on its strength,
					// and its incident ID must not travel.
					snap.foreignDecision = true
				} else {
					snap.hasArmingState = true
					snap.arming = arming
				}
			}
			// Any other phase is a recorded "not armed"; leave the flag unset.
		}
		snap.shutdown = doc.Agent.Shutdown
		if n := len(doc.IO); n > 0 && !doc.IO[n-1].OK {
			snap.lastIOFailed = true
		}
	}
	return snap
}

// ReadArmingState returns the recorded arming state, or nil when none is
// recorded (no journal, or no armingState key). A present-but-invalid
// arming state is an error.
func (j *Journal) ReadArmingState(ctx context.Context) (*ArmingState, error) {
	res, err := j.store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	if res.Status == nodestate.StatusAbsent {
		return nil, nil
	}
	if !res.Status.Trusted() {
		return nil, fmt.Errorf("journal %s: %w", res.Status, res.Cause)
	}
	return ParseArmingState(res.Document.Agent.Extra[KeyArmingState])
}

// WriteArmingState records the reboot decision durably. It stamps the SBR
// keys onto the current journal document and performs one Store write, so
// the decision becomes visible atomically with the rest of the document.
//
// Writing onto a corrupt journal is refused: the arming decision must not
// build on history that cannot be trusted. An absent journal is seeded with
// a fresh document.
func (j *Journal) WriteArmingState(ctx context.Context, arming *ArmingState) error {
	if arming == nil {
		return fmt.Errorf("arming state must not be nil")
	}
	if arming.Phase != PhaseArmed {
		return fmt.Errorf("arming state phase must be %q, got %q", PhaseArmed, arming.Phase)
	}
	if arming.Version == 0 {
		arming.Version = 1
	}

	res, err := j.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("read journal before arming: %w", err)
	}
	switch {
	case res.Status == nodestate.StatusAbsent:
		doc := nodestate.NewDocument(nodestate.ComponentSBR)
		stampArming(doc, arming)
		return j.store.Write(ctx, doc)
	case !res.Status.Trusted():
		return fmt.Errorf("cannot arm on %s journal: %w", res.Status, res.Cause)
	}

	_, err = j.store.Update(ctx, func(doc *nodestate.Document) error {
		stampArming(doc, arming)
		return nil
	})
	return err
}

// ClearArmingState removes the arming state key, leaving the rest of the
// journal intact. Called after the protected boot completes, or when a
// pending request is confirmed cancelled. Clearing an absent journal, or one
// with no arming state, succeeds without writing.
func (j *Journal) ClearArmingState(ctx context.Context) error {
	res, err := j.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("read journal before clear: %w", err)
	}
	if res.Status == nodestate.StatusAbsent {
		return nil
	}
	if !res.Status.Trusted() {
		return fmt.Errorf("cannot clear on %s journal: %w", res.Status, res.Cause)
	}
	if _, ok := res.Document.Agent.Extra[KeyArmingState]; !ok {
		return nil
	}

	_, err = j.store.Update(ctx, func(doc *nodestate.Document) error {
		delete(doc.Agent.Extra, KeyArmingState)
		return nil
	})
	return err
}

// ClearStaleArmingState clears an armed decision that was recorded in the boot
// the caller is still running in, and reports whether it did.
//
// A decision is written immediately before the agent reboots this host, so
// seeing the same boot ID that the decision recorded means the reboot never
// took place: the agent died between the write and the reboot, or the reboot
// failed. That decision describes a boot that will not happen, and if it were
// left in place the next ordinary reboot — a maintenance restart, a kernel
// update — would be cordoned and announced as an SBR rejoin, which nothing
// would then release.
//
// A decision recorded in a DIFFERENT boot is left alone: it is the normal,
// healthy case of a node that really was rebooted under an arming state, and
// clearing it here would erase the evidence before the boot unit reads it.
// currentBootID is required; a caller that cannot say which boot it is in
// cannot judge a decision.
func (j *Journal) ClearStaleArmingState(ctx context.Context, currentBootID string) (bool, error) {
	if currentBootID == "" {
		return false, fmt.Errorf("current boot ID is required to judge an arming state")
	}

	arming, err := j.ReadArmingState(ctx)
	if err != nil {
		return false, err
	}
	if arming == nil || arming.Phase != PhaseArmed || arming.BeforeBootID != currentBootID {
		return false, nil
	}

	if err := j.ClearArmingState(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Path returns the journal file path.
func (j *Journal) Path() string {
	return filepath.Join(j.dir, nodestate.ComponentSBR.FileName())
}

// stampArming writes the SBR key set onto doc.
func stampArming(doc *nodestate.Document, arming *ArmingState) {
	if doc.Agent.Extra == nil {
		doc.Agent.Extra = make(map[string]string)
	}
	doc.Agent.Extra[KeyStateVersion] = SBRStateVersion
	doc.Agent.Extra[KeyArmingState] = arming.String()
}

// ArmingSnapshot is the boot unit's point-in-time view of the journal. It
// implements ArmingJournal. Its zero value is unusable, which makes
// "journal could not be opened" indistinguishable from "journal could not
// be read" — deliberately, because both fail closed.
type ArmingSnapshot struct {
	hasArmingState  bool
	foreignDecision bool
	arming          *ArmingState
	shutdown        nodestate.ShutdownState
	lastIOFailed    bool
	read            bool
	problem         string
}

// HasArmingState reports whether the journal contains a valid armed state.
func (s *ArmingSnapshot) HasArmingState() bool { return s.hasArmingState }

// IsUncleanShutdown reports whether the journal's shutdown state means the
// agent went down without recording an orderly stop. The boot unit only
// runs after a boot ID change, so ShutdownUnknown and ShutdownRunning both
// mean the previous boot ended without the agent's say-so.
func (s *ArmingSnapshot) IsUncleanShutdown() bool {
	return s.shutdown != nodestate.ShutdownClean
}

// StorageWasUnhealthyAtLastWrite reports whether the newest I/O ring entry
// recorded a failure.
func (s *ArmingSnapshot) StorageWasUnhealthyAtLastWrite() bool { return s.lastIOFailed }

// JournalUnusable reports whether the journal could not be read or trusted:
// never read (including the zero snapshot), absent, unreadable, corrupt, or
// carrying an undecodable arming state.
func (s *ArmingSnapshot) JournalUnusable() bool { return !s.read || s.problem != "" }

// Problem returns why the journal is unusable, or "" when it is usable.
func (s *ArmingSnapshot) Problem() string { return s.problem }

// Arming returns the parsed ArmingState when HasArmingState is true.
func (s *ArmingSnapshot) Arming() *ArmingState { return s.arming }

// ForeignDecision reports whether the journal carries an armed decision
// bound to a different Node object than the expected one. Such a decision
// was ignored: it did not arm the boot and its incident ID was not carried.
// Callers should log it loudly — it means the Node object was replaced while
// the host and its journal survived.
func (s *ArmingSnapshot) ForeignDecision() bool { return s.foreignDecision }
