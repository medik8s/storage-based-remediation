package nodestate

import (
	"sort"
	"time"
)

// MergeReboots combines the reboot histories of several documents into one
// list, oldest first, counting each remediation once.
//
// This optional diagnostic aggregation works with one or more explicitly
// selected documents. It does not require SBR and SNR to share history, and
// is not an authoritative reset ledger or proof of execution ordering.
//
// Identity is the remediation resource's UID and target Node UID. An entry with no
// remediation reference is autonomous and is never collapsed with anything:
// two independent self-fences are two reboots even if they look alike.
//
// Where two entries describe the same remediation:
//
//   - the timestamp is the earliest, which is when the node committed;
//   - Completed is true if either writer observed completion;
//   - the method, source and reason come from the writer that observed
//     completion, because that is the mechanism that actually took effect;
//   - boot ids and the episode id are filled in from whichever entry has them.
//
// Nil documents are skipped. Inputs are never modified. The result is always
// non-nil.
func MergeReboots(docs ...*Document) []RebootEntry {
	merged := make([]RebootEntry, 0, 16)
	index := make(map[string]int)

	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, entry := range d.Reboots {
			// A reference with no UID is not an identity. Collapsing on it
			// would fold every such record into one — three remediations
			// counted as one reboot — and under-counting a budget is how a
			// reboot loop escapes its brake. Keep the record, keep it
			// distinguishable as requested rather than autonomous, and
			// simply never merge it with anything.
			if !entry.Remediation.identifies() {
				merged = append(merged, entry.clone())
				continue
			}
			key := entry.Remediation.Key()
			if at, ok := index[key]; ok {
				merged[at] = combineReboots(merged[at], entry)
				continue
			}
			index[key] = len(merged)
			merged = append(merged, entry.clone())
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].TS.Equal(merged[j].TS) {
			return merged[i].TS.Before(merged[j].TS)
		}
		return rebootSortKey(merged[i]) < rebootSortKey(merged[j])
	})
	return merged
}

// combineReboots folds b into a. See MergeReboots for the rules.
func combineReboots(a, b RebootEntry) RebootEntry {
	out := a.clone()

	// The authoritative description is the one from the writer that saw the
	// reboot complete.
	if b.Completed && !a.Completed {
		out.Source = b.Source
		out.Method = b.Method
		if b.Reason != "" {
			out.Reason = b.Reason
		}
	}
	out.Completed = a.Completed || b.Completed

	if !b.TS.IsZero() && (out.TS.IsZero() || b.TS.Before(out.TS)) {
		out.TS = b.TS
		if b.Remediation != nil {
			ref := *b.Remediation
			ref.Unknown = cloneRawMap(b.Remediation.Unknown)
			out.Remediation = &ref
		}
	}
	if out.EpisodeID == "" {
		out.EpisodeID = b.EpisodeID
	}
	if out.BeforeBootID == "" {
		out.BeforeBootID = b.BeforeBootID
	}
	if out.AfterBootID == "" {
		out.AfterBootID = b.AfterBootID
	}
	if out.Method == MethodUnknown {
		out.Method = b.Method
	}
	if out.Source == "" {
		out.Source = b.Source
	}
	if out.Reason == "" {
		out.Reason = b.Reason
	}
	if out.Remediation == nil && b.Remediation != nil {
		ref := *b.Remediation
		out.Remediation = &ref
	}
	return out
}

func rebootSortKey(e RebootEntry) string {
	if e.Remediation != nil {
		return e.Remediation.Key()
	}
	return string(e.Source) + "/" + string(e.Method) + "/" + e.Reason
}

// CountReboots returns the number of distinct reboots at or after since,
// across every supplied document, optionally restricted by match.
//
// It counts retained diagnostic records, not the authoritative SBR budget.
// SBR budgets use stable attempt IDs and unresolved outage epochs in retained
// consumer state. Diagnostic records can still be filtered by origin:
//
//	autonomous := nodestate.CountReboots(windowStart,
//	    func(r nodestate.RebootEntry) bool { return r.Remediation == nil },
//	    mine, peer)
//
// A nil match counts everything.
//
// A count is only an answer if the history covers the window it is taken
// over. Ask RebootHistoryComplete with the same documents and the same start
// instant; when it reports false, this count is a lower bound and the caller
// must fail toward counting.
func CountReboots(since time.Time, match func(RebootEntry) bool, docs ...*Document) int {
	n := 0
	for _, entry := range MergeReboots(docs...) {
		// A record with no timestamp counts in every window. Retention keeps
		// it for the same reason it is counted here: an undated reset is more
		// likely a writer that forgot to stamp it than one that never
		// happened, and the safe direction for a budget is to count.
		if !entry.TS.IsZero() && entry.TS.Before(since) {
			continue
		}
		if match != nil && !match(entry) {
			continue
		}
		n++
	}
	return n
}

// FindReboot returns the merged record of the reboot that served ref, if any
// document holds one.
//
// It answers "has this remediation already been carried out on this node?",
// which is how a restarted agent avoids rebooting twice for one request. The
// answer is advisory: neither a diagnostic record nor raw Node boot status
// establishes ordered completion. SBR requires an observation tied to the
// Accepted Node execution, followed by peer validation and BootEnded.
// Missing diagnostic history never grants another attempt.
func FindReboot(ref CRRef, docs ...*Document) (RebootEntry, bool) {
	if ref.Validate() != nil {
		return RebootEntry{}, false
	}
	key := ref.Key()
	var (
		found RebootEntry
		ok    bool
	)
	for _, entry := range MergeReboots(docs...) {
		if entry.Remediation == nil || entry.Remediation.Key() != key {
			continue
		}
		found, ok = entry, true
	}
	return found, ok
}

// PendingReboots returns the merged records that have been committed to but
// not confirmed complete, oldest first.
//
// A pending record surviving a change of boot id is the local evidence that
// the reboot happened and the agent did not live to confirm it.
func PendingReboots(docs ...*Document) []RebootEntry {
	all := MergeReboots(docs...)
	pending := make([]RebootEntry, 0, len(all))
	for _, entry := range all {
		if !entry.Completed {
			pending = append(pending, entry)
		}
	}
	return pending
}
