package nodestate

import (
	"fmt"
	"sort"
	"time"
)

// Retention defaults. They bound the file without losing the records a reboot
// budget is computed from.
const (
	// DefaultMaxIOEntries is the size of the I/O ring.
	DefaultMaxIOEntries = 100
	// DefaultMaxEventEntries is the size of the event ring.
	DefaultMaxEventEntries = 100
	// DefaultRebootRetention is how long a reboot record is kept.
	DefaultRebootRetention = 7 * 24 * time.Hour

	// DefaultMaxRebootEntries is a ceiling that exists only to stop a
	// pathological node from growing the file without bound. At 1024 entries
	// over the seven day window it allows six reboots an hour sustained, which
	// is far beyond any rate a node can physically achieve, so in practice
	// reboot records age out and are never dropped by count.
	//
	// It is also chosen to keep a document that is full on every axis inside
	// DefaultMaxDocumentBytes. A reboot record carrying a resource reference
	// and two boot ids encodes to roughly 480 bytes, so the ceiling is worth
	// about 500 KiB against a 1 MiB write limit. Raising one of these three
	// constants without the others would let retention produce a document the
	// Store then refuses to write — and an agent that cannot write its state
	// file is in the worst position of all. TestRetentionFitsTheWriteLimit
	// fails if that balance is ever broken.
	DefaultMaxRebootEntries = 1024

	// DefaultMinRebootEntries is the floor the age trim will not go below.
	// Sixteen records is about 8 KiB and more than any plausible budget
	// window needs, so the floor never binds in normal operation; it exists
	// only so that a wrong clock cannot erase the history in one pass.
	DefaultMinRebootEntries = 16

	// DefaultMaxConditions caps the condition set. A well-behaved agent
	// publishes a handful of condition types, so this is an outer bound on a
	// caller mistake rather than a working limit.
	DefaultMaxConditions = 32
)

// RetentionPolicy bounds the size of a document. Store.Update applies it
// before every write; a caller that writes through Store.Write applies it
// itself, or accepts an unbounded file.
//
// A zero value in any field means "no limit", so RetentionPolicy{} keeps
// everything. Use DefaultRetention for the policy the design specifies.
type RetentionPolicy struct {
	// MaxIOEntries caps Document.IO, dropping the oldest. The I/O log is
	// diagnostic, so losing its tail costs nothing but context.
	MaxIOEntries int

	// MaxEventEntries caps Document.Events, dropping the oldest.
	MaxEventEntries int

	// RebootMaxAge drops reboot records older than this, relative to the time
	// passed to Apply.
	//
	// Age is the only dimension reboots are trimmed on, and that is a
	// correctness requirement rather than a preference. A node stuck in a
	// reboot loop is exactly the node with the most reboot records; trimming
	// by count would discard the evidence of the loop and hand it a fresh
	// budget on every pass.
	RebootMaxAge time.Duration

	// MaxRebootEntries is a safety ceiling on Document.Reboots, applied after
	// the age trim. Exceeding it moves Document.RebootHistoryCompleteSince
	// forward, so a caller computing a budget over a window that reaches
	// past it is told the count is a lower bound.
	//
	// Reaching this is a signal in itself. Do not lower it to control file
	// size; lower RebootMaxAge instead.
	MaxRebootEntries int

	// MinRebootEntries is the number of most recent reboot records the age
	// trim will never go below, whatever their apparent age.
	//
	// It guards against the wall clock, which is the only input the age trim
	// has and is least trustworthy at exactly the moment this matters: a node
	// returning from a total outage, where NTP steps the clock and every
	// record suddenly looks ancient. Without the floor a single step erases
	// the history, and an erased history is a fresh reboot budget for a node
	// that may be in a reboot loop.
	//
	// Zero disables the floor and restores pure age-based trimming.
	MinRebootEntries int

	// MaxConditions caps Document.Conditions, dropping the least recently
	// observed. See trimConditions for why this exists at all.
	MaxConditions int
}

// DefaultRetention returns the policy the design specifies: a ring of 100 I/O
// entries, a ring of 100 events, and reboot records kept for seven days.
func DefaultRetention() RetentionPolicy {
	return RetentionPolicy{
		MaxIOEntries:     DefaultMaxIOEntries,
		MaxEventEntries:  DefaultMaxEventEntries,
		RebootMaxAge:     DefaultRebootRetention,
		MaxRebootEntries: DefaultMaxRebootEntries,
		MinRebootEntries: DefaultMinRebootEntries,
		MaxConditions:    DefaultMaxConditions,
	}
}

// Validate reports whether the policy is usable.
func (p RetentionPolicy) Validate() error {
	if p.MaxIOEntries < 0 {
		return fmt.Errorf("retention policy: MaxIOEntries is %d", p.MaxIOEntries)
	}
	if p.MaxEventEntries < 0 {
		return fmt.Errorf("retention policy: MaxEventEntries is %d", p.MaxEventEntries)
	}
	if p.MaxRebootEntries < 0 {
		return fmt.Errorf("retention policy: MaxRebootEntries is %d", p.MaxRebootEntries)
	}
	if p.MinRebootEntries < 0 {
		return fmt.Errorf("retention policy: MinRebootEntries is %d", p.MinRebootEntries)
	}
	if p.MaxRebootEntries > 0 && p.MinRebootEntries > p.MaxRebootEntries {
		return fmt.Errorf("retention policy: MinRebootEntries %d exceeds MaxRebootEntries %d",
			p.MinRebootEntries, p.MaxRebootEntries)
	}
	if p.MaxConditions < 0 {
		return fmt.Errorf("retention policy: MaxConditions is %d", p.MaxConditions)
	}
	if p.RebootMaxAge < 0 {
		return fmt.Errorf("retention policy: RebootMaxAge is %s", p.RebootMaxAge)
	}
	return nil
}

// Apply trims d in place, treating now as the current time.
//
// Reboot records are sorted into ascending time order first, so that a
// document assembled out of order still ages out correctly. Conditions are
// never trimmed: there is one per type and the set is small and bounded by
// the agent, not by time.
//
// Apply on a nil document does nothing.
func (p RetentionPolicy) Apply(d *Document, now time.Time) {
	if d == nil {
		return
	}

	// A backward clock step cannot restore discarded records. Keep the horizon.
	p.noteClockRegression(d, now)

	d.IO = tail(d.IO, p.MaxIOEntries)
	d.Events = tail(d.Events, p.MaxEventEntries)

	if len(d.Reboots) > 1 {
		sort.SliceStable(d.Reboots, func(i, j int) bool {
			return d.Reboots[i].TS.Before(d.Reboots[j].TS)
		})
	}

	p.trimRebootsByAge(d, now)
	p.trimRebootsByCount(d, now)
	p.trimConditions(d)
}

// trimRebootsByAge drops records older than the retention window, subject to
// the floor described on MinRebootEntries.
func (p RetentionPolicy) trimRebootsByAge(d *Document, now time.Time) {
	if p.RebootMaxAge <= 0 || len(d.Reboots) == 0 {
		return
	}
	cutoff := now.Add(-p.RebootMaxAge)

	var keep, dropped []RebootEntry
	for _, r := range d.Reboots {
		// An entry with no timestamp is kept: it is more likely a writer that
		// forgot to stamp it than a record from before the epoch, and the
		// failure direction for a budget is to count, not to forget.
		if r.TS.IsZero() || !r.TS.Before(cutoff) {
			keep = append(keep, r)
			continue
		}
		dropped = append(dropped, r)
	}

	// The floor. Records are only ever deleted on the word of the wall clock,
	// and the wall clock is least trustworthy exactly when this matters: the
	// node has just come back from a total outage and NTP has stepped it. A
	// single forward step makes every record look ancient and would otherwise
	// erase the whole history in one pass, which hands a node in a reboot
	// loop a clean budget. Keeping the most recent few regardless of apparent
	// age costs a few kilobytes and makes a bad clock non-destructive.
	if p.MinRebootEntries > 0 && len(keep) < p.MinRebootEntries && len(dropped) > 0 {
		short := p.MinRebootEntries - len(keep)
		if short > len(dropped) {
			short = len(dropped)
		}
		keep = append(keep, dropped[len(dropped)-short:]...)
		dropped = dropped[:len(dropped)-short]
		sort.SliceStable(keep, func(i, j int) bool { return keep[i].TS.Before(keep[j].TS) })
	}

	clearTail(d.Reboots, 0)
	d.Reboots = keep
	if len(dropped) > 0 {
		advanceDroppedHorizon(d, dropped, now)
	}
}

// trimRebootsByCount enforces the safety ceiling.
func (p RetentionPolicy) trimRebootsByCount(d *Document, now time.Time) {
	if p.MaxRebootEntries <= 0 || len(d.Reboots) <= p.MaxRebootEntries {
		return
	}
	drop := len(d.Reboots) - p.MaxRebootEntries
	dropped := append([]RebootEntry(nil), d.Reboots[:drop]...)
	d.Reboots = append(d.Reboots[:0], d.Reboots[drop:]...)
	clearTail(d.Reboots[:cap(d.Reboots)], len(d.Reboots))
	advanceDroppedHorizon(d, dropped, now)
}

// Inclusive queries at a discarded timestamp are incomplete. Start strictly
// after the newest discarded timestamp; also retain the prior conservative
// oldest-retained boundary. Unknown timestamps make completeness unknowable.
func advanceDroppedHorizon(d *Document, dropped []RebootEntry, now time.Time) {
	for _, r := range dropped {
		if r.TS.IsZero() {
			d.RebootHistoryUnknown = true
			continue
		}
		advanceHorizon(d, r.TS.Add(time.Nanosecond))
	}
	for _, r := range d.Reboots {
		if !r.TS.IsZero() {
			advanceHorizon(d, r.TS)
			return
		}
	}
	advanceHorizon(d, now)
}

// advanceHorizon moves the completeness point forward, never backward, and
// only when records were actually dropped.
//
// Never backward because a later horizon is the weaker claim, and the weaker
// claim is the safe one. Only on an actual drop because the alternative —
// moving it to the retention cutoff on every pass — would let one bad clock
// reading pin it in the future and mark the node's history untrustworthy for
// good.
func advanceHorizon(d *Document, at time.Time) {
	if at.After(d.RebootHistoryCompleteSince) {
		d.RebootHistoryCompleteSince = at
	}
}

// noteClockRegression records a wall clock that has moved backwards since the
// last write.
//
// Only backwards. A clock that has jumped forwards is indistinguishable from
// a node that was powered off for a long time, and guessing would produce a
// warning on every maintenance window. That is why the floor in
// trimRebootsByAge exists: the forward case is handled by not trusting the
// clock with destruction, rather than by trying to detect it.
func (p RetentionPolicy) noteClockRegression(d *Document, now time.Time) {
	if d.UpdatedAt.IsZero() || !now.Before(d.UpdatedAt) {
		return
	}
	d.AppendEvent(EventEntry{
		TS:     now,
		Type:   "Warning",
		Reason: EventReasonClockWentBackwards,
		Message: fmt.Sprintf("the clock reads %s but this file was last written at %s, %s later; "+
			"timings derived from the history before this point are unreliable",
			now.UTC().Format(time.RFC3339), d.UpdatedAt.UTC().Format(time.RFC3339), d.UpdatedAt.Sub(now)),
	})
}

// trimConditions bounds the condition set.
//
// Conditions are keyed by type and a well-behaved agent publishes a handful,
// so this never fires in normal use. It exists because nothing stops a caller
// deriving a type from a device name or a resource id, and this is the one
// collection with no other bound: the rings are capped by count and the
// reboots by age, but without this a dynamic type would grow the file for as
// long as the node lives.
func (p RetentionPolicy) trimConditions(d *Document) {
	if p.MaxConditions <= 0 || len(d.Conditions) <= p.MaxConditions {
		return
	}

	// Keep the most recently observed, in the order they already appear, so
	// that a document whose conditions are stable encodes to stable bytes.
	byRecency := make([]int, len(d.Conditions))
	for i := range byRecency {
		byRecency[i] = i
	}
	sort.SliceStable(byRecency, func(a, b int) bool {
		return d.Conditions[byRecency[a]].ObservedAt.After(d.Conditions[byRecency[b]].ObservedAt)
	})
	keep := make(map[int]struct{}, p.MaxConditions)
	for _, idx := range byRecency[:p.MaxConditions] {
		keep[idx] = struct{}{}
	}

	out := d.Conditions[:0]
	for i, c := range d.Conditions {
		if _, ok := keep[i]; ok {
			out = append(out, c)
		}
	}
	clearTail(d.Conditions, len(out))
	d.Conditions = out
}

// RebootHistoryComplete reports whether the supplied documents together hold
// every reboot record from since onward.
//
// A merged history is only as complete as its least complete input, so this
// takes the latest horizon across all of them. A nil document contributes
// nothing: an absent peer is not evidence of incompleteness, because the
// peer may simply never have run.
//
//	window := time.Now().Add(-resetAfter)
//	n := nodestate.CountReboots(window, autonomous, mine, peer)
//	if !nodestate.RebootHistoryComplete(window, mine, peer) {
//	    // n is a lower bound; treat the budget as at least this far spent
//	}
func RebootHistoryComplete(since time.Time, docs ...*Document) bool {
	for _, d := range docs {
		if d == nil {
			continue
		}
		if d.RebootHistoryUnknown || d.RebootHistoryCompleteSince.After(since) {
			return false
		}
	}
	return true
}

// tail keeps the newest limit elements of a ring, in place. A limit of zero
// or less means no limit.
func tail[T any](in []T, limit int) []T {
	if limit <= 0 || len(in) <= limit {
		return in
	}
	out := append(in[:0], in[len(in)-limit:]...)
	clearTail(in[:cap(in)], len(out))
	return out
}

// clearTail zeroes the elements beyond length n of a slice that was trimmed in
// place, so the dropped entries' strings, maps and pointers become collectable
// instead of being pinned by the backing array.
func clearTail[T any](backing []T, n int) {
	var zero T
	for i := n; i < len(backing); i++ {
		backing[i] = zero
	}
}
