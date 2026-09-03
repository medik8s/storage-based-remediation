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

package blockdevice

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/go-logr/logr"
	"golang.org/x/sys/unix"
)

// ReopenDevice opens a fresh file descriptor for each read, then closes it.
// On NFS this leverages close-to-open (CTO) semantics, causing the client
// to revalidate file state when the server indicates that cached data is no
// longer valid. CTO is not an unconditional freshness guarantee — actual
// behavior depends on the NFS client implementation and mount options.
//
// When the filesystem supports POSIX byte-range locking (fcntl), each read
// acquires a shared lock and each write acquires an exclusive lock on the
// accessed range. On NFS, locking participates in the NFS client's
// cache-coherency protocol: the Linux NFS documentation recommends file
// locking as the mechanism for cache coherence, and NFS writes are flushed
// when locks are acquired/released. The actual coherency behavior depends
// on the NFS client implementation, server, and mount options — locking
// improves freshness but is not an absolute guarantee in all configurations.
//
// Locking support is probed once at startup. If the probe fails (e.g.
// unsupported filesystem), locking is disabled entirely with no runtime
// overhead. Note: a successful local probe does not prove that remote
// nodes participate in the same lock domain — for example, NFS mounted
// with 'nolock' allows local locking but does not synchronize across
// clients. SBR relies on correct NFS mount configuration by the CSI driver.
//
// FADV_DONTNEED is called before each read to request eviction of
// page-cache entries for the read range. This is advisory and does not
// guarantee cache invalidation.
//
// Writes use a persistent O_SYNC handle so each write uses synchronous
// write semantics.
//
// Locking and SBR slot layout:
//   - Readers use shared locks (F_RDLCK) — multiple nodes can read
//     simultaneously without blocking each other.
//   - Writers use exclusive locks (F_WRLCK) — but each node writes only to
//     its own slot (a different byte range), so writers on different nodes
//     never contend, provided all clients participate in the same lock
//     domain. A reader that overlaps a write-locked range blocks briefly
//     until the write completes, preventing partial reads.
//
// Lock implementation notes:
//
// Traditional POSIX fcntl locks (F_SETLKW) are used rather than OFD locks
// (F_OFD_SETLKW). POSIX locks are process-scoped: closing ANY fd referring
// to the same file releases ALL locks held by the process on that file.
// This is safe here because all operations are serialized by d.mu — there
// is never a concurrent lock/unlock on the same file from this process.
// OFD locks would avoid this footgun but are Linux-specific and their
// behavior over NFS NLM (the distributed lock protocol) is not established.
// Traditional POSIX locks are the standard NFS locking mechanism.
//
// On the read path, the lock is released implicitly by closing the read fd
// rather than by an explicit F_UNLCK call. Since d.mu prevents concurrent
// operations, closing the read fd cannot interfere with write-path locks.
//
// All I/O is synchronous and blocking. There is no artificial timeout
// mechanism — if the NFS server is unresponsive, the calling goroutine
// blocks indefinitely. For SBR this is the correct behavior: a blocked
// heartbeat write stops the watchdog from being fed, which triggers node
// fencing. An artificial timeout that returns "failure" while the I/O
// continues in the background would be worse — the caller cannot
// distinguish a genuinely failed write from one that completes later.
//
// All operations are serialized by a mutex. This means concurrent reads
// are not parallelized, which is acceptable for SBR's heartbeat/fence
// workload (one read per tick, every few seconds). A consequence is that
// if a read or write blocks indefinitely (unresponsive storage), Close()
// also blocks until that operation completes.
type ReopenDevice struct {
	// mu serializes all operations to prevent data races on closed/writeFile.
	// This also ensures that traditional POSIX fcntl locks (which are
	// process-scoped, not fd-scoped) do not interfere across operations:
	// no concurrent lock/unlock can occur on the same file.
	mu sync.Mutex
	// writeFile is kept open for the lifetime of the device for writes
	writeFile *os.File
	// path is the filesystem path to reopen (immutable after construction)
	path string
	// fadvise, when non-nil, is called on the fresh read fd before each
	// ReadAt to request page-cache eviction. Set to unix.Fadvise by
	// NewReopenDevice; tests may override.
	fadvise func(fd int, offset int64, length int64, advice int) error
	// fcntlFlock, when non-nil, is called to acquire/release POSIX byte-range
	// locks. Probed at startup and set to unix.FcntlFlock only if the local
	// filesystem supports it. When nil, locking is skipped entirely.
	// Note: a successful probe only proves local locking works; it does not
	// guarantee that remote NFS clients participate in the same lock domain.
	fcntlFlock func(fd uintptr, cmd int, lk *unix.Flock_t) error
	// logger for operations
	logger logr.Logger
	// closed tracks whether Close() has been called
	closed bool
}

// probeFcntlLocking tests whether the filesystem supports POSIX byte-range
// locking by attempting to acquire and release a lock on a single byte.
//
// A successful probe proves that the local kernel and filesystem support
// fcntl locking. It does NOT prove that remote clients (e.g. other NFS
// clients) participate in the same lock domain — that depends on the NFS
// mount options (e.g. 'nolock' disables remote lock coordination).
//
// Returns the locking function if supported, nil otherwise.
func probeFcntlLocking(f *os.File, logger logr.Logger) func(fd uintptr, cmd int, lk *unix.Flock_t) error {
	lk := unix.Flock_t{
		Type:   unix.F_WRLCK,
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    1, // Lock exactly 1 byte (Len=0 would mean "to EOF")
	}

	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
		logger.Info("fcntl byte-range locking not available on this filesystem, disabling",
			"error", err)
		return nil
	}

	// Unlock the probe range
	lk.Type = unix.F_UNLCK
	_ = unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)

	logger.Info("fcntl byte-range locking available, enabling for cache coherency")
	return unix.FcntlFlock
}

// NewReopenDevice creates a ReopenDevice. It opens the file once for writes
// (persistent O_SYNC handle) and reopens before each read to allow NFS
// close-to-open semantics to revalidate cached file state.
//
// At construction time, fcntl byte-range locking is probed on the write
// handle. If the probe succeeds, locking is enabled for all subsequent I/O
// and runtime lock failures are treated as errors (not silently ignored).
// If the probe fails (unsupported filesystem), locking is disabled and the
// device uses CTO + FADV_DONTNEED only. This ensures full backwards
// compatibility with all storage backends.
func NewReopenDevice(path string, logger logr.Logger) (*ReopenDevice, error) {
	if path == "" {
		return nil, fmt.Errorf("device path cannot be empty")
	}

	// Open the write handle with O_SYNC — kept open for the device lifetime
	writeFile, err := os.OpenFile(path, os.O_WRONLY|os.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s for writing: %w", path, err)
	}

	devLogger := logger.WithName("reopen-device").WithValues("path", path)

	// Probe whether fcntl locking works on this filesystem
	fcntlFn := probeFcntlLocking(writeFile, devLogger)

	if fcntlFn != nil {
		logger.Info("Opened reopen-per-read device with fcntl locking", "path", path)
	} else {
		logger.Info("Opened reopen-per-read device without fcntl locking (CTO + FADV_DONTNEED only)", "path", path)
	}

	return &ReopenDevice{
		writeFile:  writeFile,
		path:       path,
		fadvise:    unix.Fadvise,
		fcntlFlock: fcntlFn,
		logger:     devLogger,
	}, nil
}

// lockRange acquires a POSIX byte-range lock on the given fd. lockType should
// be unix.F_RDLCK for shared (read) locks or unix.F_WRLCK for exclusive
// (write) locks. The lock is acquired with F_SETLKW (blocking wait).
//
// On NFS, acquiring a lock participates in the NFS client's cache-coherency
// protocol: the client flushes dirty data and invalidates cached data for
// the locked region as part of the lock handshake. The exact behavior
// depends on the NFS client, server, and mount configuration.
//
// Skips locking if fcntlFlock is nil (locking disabled at startup) or if
// length is zero (fcntl Len=0 means "lock to EOF", which is not intended).
//
// When locking was enabled at startup (fcntlFlock != nil), a runtime lock
// failure returns an error — it is not silently ignored. This distinguishes
// "locking unsupported" (detected at startup, intentional legacy mode) from
// "locking unexpectedly failed" (runtime error that should not be masked).
func (d *ReopenDevice) lockRange(fd uintptr, lockType int16, off int64, length int64) error {
	if d.fcntlFlock == nil {
		return nil
	}

	// Len=0 in fcntl means "from Start to EOF" — skip to avoid locking
	// the entire file when the caller passes a zero-length buffer.
	if length == 0 {
		return nil
	}

	lk := unix.Flock_t{
		Type:   lockType,
		Whence: int16(io.SeekStart),
		Start:  off,
		Len:    length,
	}

	return d.fcntlFlock(fd, unix.F_SETLKW, &lk)
}

// unlockRange releases a POSIX byte-range lock on the given fd.
// Skips if locking is disabled or length is zero.
func (d *ReopenDevice) unlockRange(fd uintptr, off int64, length int64) error {
	if d.fcntlFlock == nil || length == 0 {
		return nil
	}

	lk := unix.Flock_t{
		Type:   unix.F_UNLCK,
		Whence: int16(io.SeekStart),
		Start:  off,
		Len:    length,
	}

	return d.fcntlFlock(fd, unix.F_SETLK, &lk)
}

// ReadAt opens a fresh file descriptor, acquires a shared fcntl lock for
// cache coherency, reads, then closes the fd (which implicitly releases
// the lock).
//
// The read fd is opened, locked, read, and closed within a single d.mu
// critical section. Because traditional POSIX locks are process-scoped
// (closing ANY fd for a file releases ALL process locks on it), the mutex
// ensures no concurrent operation can be affected by the close.
//
// The call blocks until the I/O completes. If the storage backend is
// unresponsive, the caller blocks indefinitely — this is intentional for
// a fencing device (see type comment).
func (d *ReopenDevice) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return 0, fmt.Errorf("device %q is closed", d.path)
	}

	if off < 0 {
		return 0, fmt.Errorf("negative offset %d not allowed", off)
	}

	if len(p) == 0 {
		return 0, nil
	}

	f, err := os.OpenFile(d.path, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("failed to reopen %s for reading: %w", d.path, err)
	}

	// Acquire shared (read) lock. On NFS this participates in the client's
	// cache-coherency protocol, causing the client to revalidate/invalidate
	// cached data for the locked range. When locking was enabled at startup,
	// a runtime failure is an error, not a silent degradation.
	if err := d.lockRange(f.Fd(), unix.F_RDLCK, off, int64(len(p))); err != nil {
		f.Close()
		return 0, fmt.Errorf("failed to acquire read lock on %s at offset %d: %w", d.path, off, err)
	}

	// Evict page-cache for this range before reading. This is advisory and
	// complements locking: on NFS, locking handles the NFS client cache
	// while FADV_DONTNEED targets the local kernel page cache.
	if d.fadvise != nil {
		alignedOff, alignedLen, alignErr := pageAlignedRange(off, int64(len(p)))
		if alignErr != nil {
			d.logger.V(2).Info("Page alignment failed, skipping FADV_DONTNEED",
				"offset", off, "length", len(p), "error", alignErr)
		} else if alignedLen > 0 {
			if err := d.fadvise(int(f.Fd()), alignedOff, alignedLen, unix.FADV_DONTNEED); err != nil {
				d.logger.V(2).Info("FADV_DONTNEED failed, proceeding with read",
					"offset", off, "alignedOffset", alignedOff, "alignedLength", alignedLen, "error", err)
			}
		}
	}

	n, readErr := f.ReadAt(p, off)

	// Close the fd — this implicitly releases the POSIX lock. No explicit
	// unlock needed on the read path because the fd is not kept open.
	closeErr := f.Close()

	if readErr != nil {
		return n, readErr
	}
	if closeErr != nil {
		return n, fmt.Errorf("close after read failed on %s: %w", d.path, closeErr)
	}

	d.logger.V(3).Info("Reopen read completed",
		"offset", off, "bytesRead", n)

	return n, nil
}

// WriteAt acquires an exclusive fcntl lock on the write range, writes to the
// persistent write handle, then explicitly releases the lock. The exclusive
// lock participates in NFS cache-coherency: subsequent readers that acquire
// a lock will have their cached data invalidated as part of the lock protocol.
//
// An explicit unlock is required on the write path because the write fd
// is kept open for the lifetime of the device — closing it would make the
// device unusable.
func (d *ReopenDevice) WriteAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return 0, fmt.Errorf("device %q is closed", d.path)
	}

	if off < 0 {
		return 0, fmt.Errorf("negative offset %d not allowed", off)
	}

	if len(p) == 0 {
		return 0, nil
	}

	// Acquire exclusive (write) lock. When locking was enabled at startup,
	// a runtime failure is an error, not a silent degradation.
	if err := d.lockRange(d.writeFile.Fd(), unix.F_WRLCK, off, int64(len(p))); err != nil {
		return 0, fmt.Errorf("failed to acquire write lock on %s at offset %d: %w", d.path, off, err)
	}

	n, writeErr := d.writeFile.WriteAt(p, off)

	// Explicit unlock is required because the write fd stays open.
	// An unlock failure is logged but the write result is preserved. The
	// write may already have completed successfully, so returning an error
	// here could cause callers to incorrectly retry a successful write.
	// The lock will be implicitly released when the fd is eventually closed.
	if unlockErr := d.unlockRange(d.writeFile.Fd(), off, int64(len(p))); unlockErr != nil {
		d.logger.V(1).Info("fcntl F_UNLCK failed on write fd",
			"offset", off, "error", unlockErr)
	}

	if writeErr != nil {
		return n, fmt.Errorf("failed to write to %s at offset %d: %w", d.path, off, writeErr)
	}

	if n != len(p) {
		return n, fmt.Errorf("partial write to %s: wrote %d bytes, expected %d", d.path, n, len(p))
	}

	return n, nil
}

// Sync flushes the write handle.
func (d *ReopenDevice) Sync() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return fmt.Errorf("device %q is closed", d.path)
	}
	return d.writeFile.Sync()
}

// Close closes the persistent write handle.
func (d *ReopenDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil
	}
	d.closed = true
	return d.writeFile.Close()
}

// Path returns the filesystem path.
func (d *ReopenDevice) Path() string {
	return d.path
}

// IsClosed returns true if Close() has been called.
func (d *ReopenDevice) IsClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}
