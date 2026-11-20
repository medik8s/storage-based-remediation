package nodestate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// --- housekeeping ---
//
// Everything in this section removes files. None of it ever writes the live
// document, which is replaced only by atomicReplace. That is what keeps the
// guarantee intact under a crash: the worst a process killed in the middle of
// housekeeping can leave behind is a file the next sweep reclaims, and a
// reader at any instant still sees a whole state file or none.

// CleanupReport says what a housekeeping pass removed. Log it, or feed the
// counters to a metric: a node steadily accumulating orphans is a node whose
// agent keeps dying mid-write.
type CleanupReport struct {
	// TemporaryFilesRemoved counts abandoned temporary files reclaimed. Each
	// one is a write this component started and did not finish, so a non-zero
	// count means the agent was killed between creating the file and renaming
	// it into place.
	TemporaryFilesRemoved int
	// QuarantineFilesRemoved counts quarantined copies aged or pruned out.
	QuarantineFilesRemoved int
	// BytesReclaimed is the total size of everything removed, as far as the
	// filesystem reported it.
	BytesReclaimed int64
}

// Cleanup reclaims this component's abandoned files from the state directory
// and returns what it removed.
//
// Two kinds accumulate. A temporary file is left behind whenever a process is
// killed between creating it and renaming it into place — a panic, an
// OOM kill, or the watchdog reset this product exists to cause. A quarantined
// copy is left behind deliberately, as evidence, and is kept to a fixed
// number and age.
//
// It is called once per Store on the first write. Call it directly from a
// periodic janitor on a long-lived agent,
// or at start-up to get the counts into a log line.
//
// Only this component's files are touched, by the same ownership rule that
// governs writing. A temporary file younger than the orphan grace period is
// left alone, because it may belong to a write still in flight.
func (s *Store) Cleanup(ctx context.Context) (*CleanupReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweep()
}

// sweep performs one housekeeping pass. The caller holds s.mu.
//
// Failures are reported but not fatal to the caller that triggered the sweep:
// being unable to tidy up is not a reason to fail a read or a write.
func (s *Store) sweep() (*CleanupReport, error) {
	if err := s.verifyDirectoryIfPresent(); err != nil {
		return nil, err
	}
	s.swept = true
	report := &CleanupReport{}

	entries, err := s.fs.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, nil
		}
		return report, pathErrorf("readdir", s.dir, err)
	}

	base := filepath.Base(s.Path(s.self))
	tempPrefix := temporaryPrefix(base)
	quarantinePrefix := base + "."
	now := s.clock.Now()

	type candidate struct {
		name string
		size int64
	}
	var quarantined []candidate

	remove := func(name string, size int64, counter *int) {
		if err := s.fs.Remove(filepath.Join(s.dir, name)); err == nil {
			*counter++
			report.BytesReclaimed += size
		}
	}

	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, tempPrefix):
			info, infoErr := e.Info()
			if infoErr != nil {
				continue // it went away underneath us, which is the outcome we wanted
			}
			// A write that has been in flight this long is not in flight. The
			// grace period is what separates "a process died holding this"
			// from "a process is writing this right now", and getting it
			// wrong in the other direction would delete a live write.
			if now.Sub(info.ModTime()) < s.housekeeping.OrphanGrace {
				continue
			}
			remove(name, info.Size(), &report.TemporaryFilesRemoved)

		case strings.HasPrefix(name, quarantinePrefix) && strings.HasSuffix(name, quarantineSuffix):
			var size int64
			if info, infoErr := e.Info(); infoErr == nil {
				if s.housekeeping.QuarantineMaxAge > 0 && now.Sub(info.ModTime()) > s.housekeeping.QuarantineMaxAge {
					remove(name, info.Size(), &report.QuarantineFilesRemoved)
					continue
				}
				size = info.Size()
			}
			quarantined = append(quarantined, candidate{name: name, size: size})
		}
	}

	// Names sort chronologically because the timestamp embedded in them is
	// fixed-width, so the oldest are the ones to drop.
	if len(quarantined) > s.housekeeping.QuarantineRetention {
		sort.Slice(quarantined, func(i, j int) bool { return quarantined[i].name < quarantined[j].name })
		for _, c := range quarantined[:len(quarantined)-s.housekeeping.QuarantineRetention] {
			remove(c.name, c.size, &report.QuarantineFilesRemoved)
		}
	}

	return report, nil
}

// temporaryPrefix returns the name prefix atomicReplace gives the temporary
// file it writes for base. The leading dot keeps it out of a casual listing
// and, more usefully, out of the quarantine prefix's way.
func temporaryPrefix(base string) string { return "." + base + ".tmp-" }

// CheckDurableStorage reports whether the state directory is on a filesystem
// that can hold state across the things it has to survive.
//
// The failure it catches is a deployment mistake with no symptom. If the
// host path is not mounted — forgotten, misspelled, or an emptyDir used by
// accident — the agent writes happily into the container's own layer or into
// memory. Nothing errors. Every write succeeds, every read succeeds, and the
// state silently resets on every pod replacement or every reboot, so the
// reboot budget never accumulates and the brake it exists to apply never
// applies. An agent could run that way for months.
//
// Call it once at start-up and treat a failure as a configuration error
// worth refusing to proceed on, or at least worth a loud condition. It is
// deliberately not wired into Write: whether a node without durable state
// should run degraded or not start at all is the consumer's decision, not
// this library's.
//
// A consumer with readOnlyRootFilesystem set is already protected against
// the worst version of this, because a missing mount fails loudly with
// EROFS instead of landing in the container layer. The check still catches
// tmpfs and an emptyDir mounted on purpose.
//
// It reports nil on a platform without statfs, and on any filesystem it does
// not recognise as unsuitable — it is a check for known-bad, not a warranty
// of known-good.
func (s *Store) CheckDurableStorage(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	magic, err := filesystemType(s.dir)
	if err != nil {
		// Either the platform does not report it or the directory is not
		// there yet. Neither is evidence of a problem with the storage, and
		// EnsureDirectory is the call that reports a missing directory.
		return nil
	}
	name, reason := describeFilesystem(magic)
	if name == "" {
		return nil
	}
	return pathErrorf("verify", s.dir, fmt.Errorf(
		"%w: the state directory is on %s, and %s", ErrNotDurable, name, reason))
}
