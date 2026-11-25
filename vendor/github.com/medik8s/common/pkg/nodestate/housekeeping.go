package nodestate

import (
	"fmt"
	"time"
)

// Housekeeping defaults.
const (
	// DefaultQuarantineRetention is how many quarantined copies of a file
	// are kept. More than one is useful because a boot loop produces one per
	// boot and the first is usually the informative one; keeping them all
	// would let that loop fill the host path.
	DefaultQuarantineRetention = 3

	// DefaultQuarantineMaxAge is how long a quarantined copy is kept before
	// it is reclaimed. Evidence of a fault nobody looked at in a month is no
	// longer evidence; it is just a file on a node's root filesystem.
	DefaultQuarantineMaxAge = 30 * 24 * time.Hour

	// DefaultOrphanGrace is how long a temporary file must have gone
	// untouched before a sweep treats it as abandoned rather than in flight.
	// Generous on purpose: reclaiming a live write would be far worse than
	// leaving a dead one in place for an hour.
	DefaultOrphanGrace = time.Hour

	// DefaultMaxPreservedBytes permits extensions up to the document write limit.
	// Exceeding a configured preservation limit refuses the operation without
	// deleting fields or changing the original file.

	DefaultMaxPreservedBytes = DefaultMaxDocumentBytes
)

// HousekeepingPolicy bounds what accumulates in the state directory and in
// the parts of a document this library owns.
//
// These four settings are grouped because they are one concern and because
// none of them is a per-deployment decision: a caller that changes any of
// them is almost always a test. Grouping keeps the Store's option list to
// the settings a production caller actually reaches for.
//
// A zero value in any field means "use the default". Use
// DefaultHousekeeping and adjust.
type HousekeepingPolicy struct {
	// QuarantineRetention is how many quarantined copies of the owned file
	// to keep. Must be at least one when set: the evidence in the first bad
	// file is usually the only record of what went wrong.
	QuarantineRetention int

	// QuarantineMaxAge is how long a quarantined copy is kept. A negative
	// value is rejected; use NoExpiry to keep them indefinitely.
	QuarantineMaxAge time.Duration

	// OrphanGrace is how long a temporary file must have gone untouched
	// before a sweep reclaims it.
	//
	// Lower it only where no other process can possibly be writing this
	// component's file, such as a test. Setting it too low on a live node
	// risks deleting a write that is merely slow, which on failing storage
	// is the normal case.
	OrphanGrace time.Duration

	// MaxPreservedBytes caps unrecognised members carried forward from a
	// newer peer. Beyond it the operation fails with ErrTooLarge and leaves
	// the original file untouched. Use Unlimited to disable this separate cap.
	MaxPreservedBytes int
}

// Sentinel values for HousekeepingPolicy fields, so that "no limit" is
// expressible where zero already means "use the default".
const (
	// NoExpiry keeps quarantined copies indefinitely.
	NoExpiry time.Duration = -1
	// Unlimited disables a size cap.
	Unlimited int = -1
)

// DefaultHousekeeping returns the policy this package applies when none is
// given.
func DefaultHousekeeping() HousekeepingPolicy {
	return HousekeepingPolicy{
		QuarantineRetention: DefaultQuarantineRetention,
		QuarantineMaxAge:    DefaultQuarantineMaxAge,
		OrphanGrace:         DefaultOrphanGrace,
		MaxPreservedBytes:   DefaultMaxPreservedBytes,
	}
}

// Validate reports whether the policy is usable.
func (p HousekeepingPolicy) Validate() error {
	if p.QuarantineRetention < 0 {
		return fmt.Errorf("housekeeping policy: QuarantineRetention is %d", p.QuarantineRetention)
	}
	if p.QuarantineMaxAge < 0 && p.QuarantineMaxAge != NoExpiry {
		return fmt.Errorf("housekeeping policy: QuarantineMaxAge is %s; use NoExpiry to keep copies indefinitely",
			p.QuarantineMaxAge)
	}
	if p.OrphanGrace < 0 {
		return fmt.Errorf("housekeeping policy: OrphanGrace is %s", p.OrphanGrace)
	}
	if p.MaxPreservedBytes < 0 && p.MaxPreservedBytes != Unlimited {
		return fmt.Errorf("housekeeping policy: MaxPreservedBytes is %d; use Unlimited to disable the cap",
			p.MaxPreservedBytes)
	}
	return nil
}

// resolved fills in defaults for the zero fields and converts the sentinels
// into the forms the Store uses internally, where a non-positive value means
// "no limit".
func (p HousekeepingPolicy) resolved() HousekeepingPolicy {
	out := p
	if out.QuarantineRetention == 0 {
		out.QuarantineRetention = DefaultQuarantineRetention
	}
	switch out.QuarantineMaxAge {
	case 0:
		out.QuarantineMaxAge = DefaultQuarantineMaxAge
	case NoExpiry:
		out.QuarantineMaxAge = 0
	}
	if out.OrphanGrace == 0 {
		out.OrphanGrace = DefaultOrphanGrace
	}
	switch out.MaxPreservedBytes {
	case 0:
		out.MaxPreservedBytes = DefaultMaxPreservedBytes
	case Unlimited:
		out.MaxPreservedBytes = 0
	}
	return out
}
