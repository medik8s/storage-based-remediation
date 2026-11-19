/*
Copyright 2025.

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

// Package hostlock enforces that exactly one process on a node controls the watchdog
// and writes the node-local journal (RHWA-2065, AGT-01).
//
// The lock is a non-blocking exclusive advisory lock on a file that is separate from
// the atomically replaced journal, so the journal's write-temp-and-rename never
// destroys it. The file is never unlinked while a process may hold it: unlinking it
// would let a later process lock a different file of the same name and arm a second
// watchdog on the same host.
//
// A process that cannot take the lock must not open or arm the watchdog, must not
// load mutable journal state, and must not accept a remediation offer. It reports
// ReasonLocalAgentConflict and keeps reconciling requests for other nodes.
package hostlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultStateDir is the node-local directory holding SBR's durable state.
	DefaultStateDir = "/var/lib/medik8s/state"

	// DefaultLockPath is the lock file. It is deliberately not the journal file
	// so that the journal can be atomically replaced without losing the lock.
	DefaultLockPath = DefaultStateDir + "/sbr-local-agent.lock"

	// stateDirMode keeps the state directory private to its owner.
	stateDirMode os.FileMode = 0o700

	// lockFileMode keeps the lock file private to its owner.
	lockFileMode os.FileMode = 0o600
)

// ReasonLocalAgentConflict is the reason reported through status and events when this
// process is not the one permitted to perform local actions on the host.
const ReasonLocalAgentConflict = "LocalAgentConflict"

// ErrLocalAgentConflict reports that this process may not perform local actions on the
// host. Every failure to establish the lock maps to it, including unsupported locking,
// so that an unlockable filesystem can never read as a silent success.
var ErrLocalAgentConflict = errors.New(ReasonLocalAgentConflict)

// IsConflict reports whether err means this process may not perform local actions.
func IsConflict(err error) bool {
	return errors.Is(err, ErrLocalAgentConflict)
}

// Lock is a held host lock. Acquire returns one; the holder keeps it for the lifetime
// of the process and releases it on shutdown.
type Lock struct {
	file     *os.File
	mu       sync.Mutex
	path     string
	released bool
}

// Acquire opens the lock file at path and takes a non-blocking exclusive lock on it.
//
// It creates the parent directory and the file if they are missing. It never removes
// either. Any failure, including the lock already being held by another process and
// locking being unsupported by the filesystem, is returned wrapped in
// ErrLocalAgentConflict.
//
// The caller must call Acquire before opening or arming the watchdog, before loading
// mutable journal state, and before accepting an offer.
func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty lock path", ErrLocalAgentConflict)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return nil, fmt.Errorf("%w: cannot create state directory %q: %w", ErrLocalAgentConflict, dir, err)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockFileMode)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot open lock file %q: %w", ErrLocalAgentConflict, path, err)
	}

	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: cannot lock %q: %w", ErrLocalAgentConflict, path, err)
	}

	return &Lock{file: file, path: path}, nil
}

// Path returns the lock file this lock is held on.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release gives up the lock so a successor process on this host can take it.
//
// It closes the open file and leaves the file itself in place, and it is safe to call
// more than once. Releasing a lock that was never taken is a no-op.
//
// Its errors are never ErrLocalAgentConflict: a failure here says this process could not
// let go of the lock, not that another process holds it, and IsConflict must not report
// that as a conflict.
func (l *Lock) Release() error {
	if l == nil || l.file == nil || l.path == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	err := l.file.Close()
	l.released = true
	if err != nil {
		return fmt.Errorf("cannot close lock file %q: %w", l.path, err)
	}
	return nil
}
