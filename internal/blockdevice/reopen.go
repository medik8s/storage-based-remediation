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
// Before each read, FADV_DONTNEED is called on the fresh fd to request
// eviction of the corresponding page-cache range. This addresses a
// different caching layer than CTO: on NFS, CTO revalidates the NFS
// client cache while FADV_DONTNEED evicts the local page cache. On
// non-NFS backends that reject O_DIRECT, CTO has no effect but
// FADV_DONTNEED provides best-effort page-cache eviction.
//
// Writes use a persistent O_SYNC handle so each write uses synchronous
// write semantics. Visibility to other NFS clients still depends on their
// cache-revalidation behavior.
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
	// mu serializes all operations to prevent data races on closed/writeFile
	mu sync.Mutex
	// writeFile is kept open for the lifetime of the device for writes
	writeFile *os.File
	// path is the filesystem path to reopen (immutable after construction)
	path string
	// fadvise, when non-nil, is called on the fresh read fd before each
	// ReadAt to request page-cache eviction. Set to unix.Fadvise by
	// NewReopenDevice; tests may override.
	fadvise func(fd int, offset int64, length int64, advice int) error
	// logger for operations
	logger logr.Logger
	// closed tracks whether Close() has been called
	closed bool
}

// NewReopenDevice creates a ReopenDevice. It opens the file once for writes
// (persistent O_SYNC handle) and reopens before each read to allow NFS
// close-to-open semantics to revalidate cached file state.
func NewReopenDevice(path string, logger logr.Logger) (*ReopenDevice, error) {
	if path == "" {
		return nil, fmt.Errorf("device path cannot be empty")
	}

	// Open the write handle with O_SYNC — kept open for the device lifetime
	writeFile, err := os.OpenFile(path, os.O_WRONLY|os.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s for writing: %w", path, err)
	}

	logger.Info("Opened reopen-per-read device", "path", path)

	return &ReopenDevice{
		writeFile: writeFile,
		path:      path,
		fadvise:   unix.Fadvise,
		logger:    logger.WithName("reopen-device").WithValues("path", path),
	}, nil
}

// ReadAt opens a fresh file descriptor, reads, then closes it. On NFS this
// allows close-to-open semantics to revalidate cached file state.
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

	f, err := os.OpenFile(d.path, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("failed to reopen %s for reading: %w", d.path, err)
	}

	// Evict page-cache for this range before reading. On NFS this
	// complements CTO (which handles the NFS client cache). On non-NFS
	// backends this is the primary cache-eviction mechanism.
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

// WriteAt writes to the persistent write handle. The call blocks until the
// kernel (and, with O_SYNC, the server) acknowledges the write.
func (d *ReopenDevice) WriteAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return 0, fmt.Errorf("device %q is closed", d.path)
	}

	if off < 0 {
		return 0, fmt.Errorf("negative offset %d not allowed", off)
	}

	n, err := d.writeFile.WriteAt(p, off)
	if err != nil {
		return n, fmt.Errorf("failed to write to %s at offset %d: %w", d.path, off, err)
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
