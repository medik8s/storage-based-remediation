package nodestate

import (
	"errors"
	"fmt"
)

// Sentinel errors. Every error returned by this package wraps one of these, so
// callers classify with errors.Is rather than by matching strings.
//
// The three groups matter to different callers:
//
//   - Programming errors (ErrInvalidComponent, ErrNotOwner) mean the caller
//     asked for something the contract forbids. Fix the call site.
//   - Host-path faults (ErrInsecurePermissions, ErrInsecureOwner, ErrSymlink,
//     ErrNotRegular, ErrNotDirectory) mean the node's state directory is not
//     what this library requires. Per the design, a host-path fault is evidence
//     about this agent's bookkeeping, never about the cluster's storage.
//   - Content faults (ErrCorrupt, ErrSchemaUnsupported, ErrTooLarge) are
//     classified without replacing the owner's file. Peer readers report
//     unusable or unsupported history without changing it.
var (
	// ErrInvalidComponent means a Component is not a safe, single path element.
	ErrInvalidComponent = errors.New("nodestate: invalid component name")

	// ErrInvalidReference means a CRRef cannot identify a remediation, almost
	// always because its UID is empty. See CRRef.Validate.
	ErrInvalidReference = errors.New("nodestate: invalid remediation reference")

	// ErrNotOwner means a write was attempted against a file this Store does
	// not own. Single-writer ownership is what makes the lock-free atomic
	// replace safe, so the library refuses rather than warns.
	ErrNotOwner = errors.New("nodestate: not the owner of this state file")

	// ErrSymlink means a path in the state directory is a symbolic link. A
	// symlink is the cheapest way to turn a node-local write into an
	// arbitrary-file overwrite, so it is refused by default.
	ErrSymlink = errors.New("nodestate: path is a symbolic link")

	// ErrNotRegular means a state file path is not a regular file.
	ErrNotRegular = errors.New("nodestate: path is not a regular file")

	// ErrNotDirectory means the state directory path is not a directory.
	ErrNotDirectory = errors.New("nodestate: path is not a directory")

	// ErrInsecurePermissions means a path is readable or writable by users
	// other than its owner.
	ErrInsecurePermissions = errors.New("nodestate: insecure permissions")

	// ErrInsecureOwner means a path is owned by an unexpected user.
	ErrInsecureOwner = errors.New("nodestate: unexpected owner")

	// ErrSchemaUnsupported means the document declares a schema version this
	// build cannot interpret. See SchemaVersionError for the specifics.
	ErrSchemaUnsupported = errors.New("nodestate: unsupported schema version")

	// ErrCorrupt means a state file could not be parsed as a document.
	ErrCorrupt = errors.New("nodestate: corrupt state file")

	// ErrTooLarge means a document exceeds the configured size limit, either on
	// read or on write. It bounds the memory a hostile or runaway file can cost.
	ErrTooLarge = errors.New("nodestate: size limit exceeded")

	// ErrHistoryUnknown blocks ordinary updates until history is reconciled.
	ErrHistoryUnknown = errors.New("nodestate: history requires reconciliation")

	// ErrStateChanged means a durability confirmation no longer identifies the live document.
	ErrStateChanged = errors.New("nodestate: state changed before confirmation")

	// ErrDurabilityUnconfirmed means the replacement is already visible to
	// every reader but has not been shown to survive a crash, because the
	// directory entry could not be flushed after the rename.
	//
	// It MUST NOT authorize a new reset. The reset is itself the crash that
	// would test this: issuing one on an unconfirmed record risks losing the
	// very commitment that records it, and a lost commitment is an uncharged
	// budget and a possible reset loop.
	//
	// Nor does it mean nothing was written. The bytes are there, so do not
	// rerun a non-idempotent mutator — resolve the exact candidate with
	// ConfirmDurability instead. Load confirms surviving history after a
	// restart, where this process cannot know what its predecessor achieved.
	// A commitment established before this failure stays in force.
	ErrDurabilityUnconfirmed = errors.New("nodestate: write is visible but its durability is unconfirmed")

	// ErrForeignWrite means the file this Store owns was last written by
	// another process. This must never happen under the single-writer
	// contract; see Store.Load for how it is reported and why the library
	// carries on rather than refusing.
	ErrForeignWrite = errors.New("nodestate: state file was modified by another writer")

	// ErrNotDurable means the state directory is on a filesystem that cannot
	// hold state across a pod replacement or a reboot — the container's own
	// writable layer, or memory. It almost always means a host path volume
	// was not mounted where it was supposed to be. See
	// Store.CheckDurableStorage.
	ErrNotDurable = errors.New("nodestate: state directory is not durable storage")

	// ErrBootIDUnavailable means the kernel boot identifier could not be read
	// or did not look like one.
	ErrBootIDUnavailable = errors.New("nodestate: boot id unavailable")

	// ErrNoChange may be returned by a Store.Update mutator to skip the write.
	// Update treats it as success and does not touch the file.
	ErrNoChange = errors.New("nodestate: no change")
)

// PathError attaches the offending path and operation to one of the sentinel
// errors above.
type PathError struct {
	// Op is the operation that failed, for example "open" or "rename".
	Op string
	// Path is the file or directory the operation was attempted on.
	Path string
	// Err is the underlying cause. It wraps one of the package sentinels
	// whenever the cause is classifiable.
	Err error
}

func (e *PathError) Error() string {
	return fmt.Sprintf("nodestate: %s %s: %v", e.Op, e.Path, e.Err)
}

// Unwrap exposes the cause so that errors.Is and errors.As see through.
func (e *PathError) Unwrap() error { return e.Err }

func pathErrorf(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &PathError{Op: op, Path: path, Err: err}
}

// SchemaVersionError reports a document whose declared schema version falls
// outside the range this build understands.
type SchemaVersionError struct {
	// Found is the schemaVersion read from the document.
	Found int
	// Min and Max are the inclusive bounds this build supports.
	Min, Max int
}

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("nodestate: schema version %d is outside the supported range [%d, %d]", e.Found, e.Min, e.Max)
}

// Unwrap reports ErrSchemaUnsupported so callers can classify with errors.Is.
func (e *SchemaVersionError) Unwrap() error { return ErrSchemaUnsupported }
