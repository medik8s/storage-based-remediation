package nodestate

import (
	"context"
	"sort"
	"time"
)

// Snapshot is one reading of every component's state on this node.
//
// It exists because the obvious composition of the primitives loses the one
// fact a reboot budget depends on. ReadAll returns the documents it could
// read and drops the rest, so a peer this process cannot read — a permission
// problem, an SELinux denial, a schema from a newer build — simply is not in
// the result. RebootHistoryComplete, handed what survived, then reports that
// the merged history is whole, because nothing told it a file was missing.
// The count comes out low and confident, and a reboot loop that peer carried
// out is invisible to the brake.
//
// A Snapshot keeps the read outcome for every component it looked at,
// including the ones it could not read, so the completeness question can be
// answered honestly. Prefer it to assembling MergeReboots and
// RebootHistoryComplete by hand.
type Snapshot struct {
	// Peers is the outcome for each component that was read, including this
	// Store's own. Never nil.
	Peers map[Component]*PeerRead

	// TakenAt is when the reading started.
	TakenAt time.Time

	// Err records invalid input or incomplete collection (including cancellation).
	Err error
}

// Snapshot reads the explicitly named components, or only this Store's own
// component when none are named. Other installed controllers are not implicitly
// part of this consumer's history. Pass all expected components explicitly when
// deliberately combining histories.
//
// Nothing is written: like ReadAll, this is a read-only view and does not
// quarantine, not even this Store's own damaged file. Use Load for the
// start-up classification and durability confirmation.
//
// A cancelled context is recorded in Err and in every unread component. Such
// a snapshot is incomplete and must not be used to authorize another reset.
func (s *Store) Snapshot(ctx context.Context, components ...Component) *Snapshot {
	snap := &Snapshot{
		Peers:   make(map[Component]*PeerRead, len(components)),
		TakenAt: s.clock.Now(),
	}
	if err := ctx.Err(); err != nil {
		snap.Err = err
	}
	if len(components) == 0 {
		components = []Component{s.self}
	}

	for _, c := range components {
		if _, done := snap.Peers[c]; done {
			continue
		}
		read, err := s.LoadPeer(ctx, c)
		if err != nil {
			// Cancellation or an unusable component name. The read did not
			// happen, which leaves the history incomplete, but it is not a
			// host-path fault and must not be reported as one.
			if snap.Err == nil {
				snap.Err = err
			}
			snap.Peers[c] = &PeerRead{Status: PeerNotCollected, Cause: err}
			continue
		}
		snap.Peers[c] = read
	}
	return snap
}

// Document returns the document read for c, or nil.
func (sn *Snapshot) Document(c Component) *Document {
	if sn == nil {
		return nil
	}
	if read, ok := sn.Peers[c]; ok {
		return read.Document
	}
	return nil
}

// Documents returns every document that was read, ordered by component so
// the result is stable.
func (sn *Snapshot) Documents() []*Document {
	if sn == nil {
		return nil
	}
	names := make([]Component, 0, len(sn.Peers))
	for c, read := range sn.Peers {
		if read.Document != nil {
			names = append(names, c)
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	out := make([]*Document, 0, len(names))
	for _, c := range names {
		out = append(out, sn.Peers[c].Document)
	}
	return out
}

// Reboots returns the merged reboot history across every document read, one
// entry per remediation. See MergeReboots.
func (sn *Snapshot) Reboots() []RebootEntry {
	return MergeReboots(sn.Documents()...)
}

// CountReboots counts distinct reboots at or after since, optionally
// filtered. See CountReboots.
//
// The result is a lower bound unless HistoryComplete agrees. Check it.
func (sn *Snapshot) CountReboots(since time.Time, match func(RebootEntry) bool) int {
	return CountReboots(since, match, sn.Documents()...)
}

// HistoryComplete reports whether this snapshot can answer a question about
// reboots since the given instant.
//
// It is false when any document's own horizon starts later than since, and
// also when any component could not be read at all. The second case is the
// one the primitives miss: a file that is there and unreadable removes an
// unknown number of records from the merge, and no amount of inspecting what
// did load will reveal that.
//
// An absent component is unknown: it may never have run, or its history may
// have been lost. Pass the explicit set of expected components when optional
// peers are not installed. Absence alone must not replenish a reboot budget.
func (sn *Snapshot) HistoryComplete(since time.Time) bool {
	if sn == nil || sn.Err != nil || len(sn.Peers) == 0 {
		return false
	}
	for _, read := range sn.Peers {
		if read == nil {
			return false
		}
		switch read.Status {
		case PeerOK:
		default:
			return false
		}
	}
	return RebootHistoryComplete(since, sn.Documents()...)
}

// Missing returns the components that could not be read, ordered by name.
//
// These are the reason HistoryComplete may be false, and each is worth
// reporting on its own: PeerRead.Configured distinguishes a deployment
// fault on this node, which will not fix itself, from a peer's own
// bookkeeping problem, which will.
func (sn *Snapshot) Missing() []Component {
	if sn == nil {
		return nil
	}
	var out []Component
	for c, read := range sn.Peers {
		switch read.Status {
		case PeerOK:
		default:
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
