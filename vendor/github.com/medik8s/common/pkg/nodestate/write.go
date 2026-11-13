package nodestate

import (
	"fmt"
	"path/filepath"
)

// --- writing ---

// atomicReplace writes data to path by the only sequence that is safe for a
// concurrent reader and for a power loss:
//
//	create a temporary file in the same directory
//	write
//	fsync the file
//	close
//	rename over the target
//	fsync the directory
//
// Each step matters. The temporary file must share the directory, or the
// rename is a cross-device copy and not atomic. The file sync must precede
// the rename, or a crash can leave the directory pointing at a file with no
// contents. The directory sync must follow it, or the rename itself can be
// lost. Failures before rename leave the target unchanged. A directory-sync
// failure after rename returns ErrDurabilityUnconfirmed: visible, not durable.
func (s *Store) atomicReplace(path string, data []byte) error {
	dir := filepath.Dir(path)
	prefix := temporaryPrefix(filepath.Base(path))

	f, err := s.fs.CreateTemp(dir, prefix, s.security.FileMode)
	if err != nil {
		return pathErrorf("createtemp", dir, err)
	}

	tmp := f.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if !committed {
			// Best effort. If this fails the file is still a temporary name
			// that no reader looks for, and the next successful write will
			// not collide with it.
			_ = s.fs.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return pathErrorf("write", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return pathErrorf("fsync", tmp, err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return pathErrorf("close", tmp, err)
	}
	if err := s.fs.Rename(tmp, path); err != nil {
		return pathErrorf("rename", tmp, err)
	}
	committed = true

	if err := s.fs.SyncDir(dir); err != nil {
		// The candidate is visible but not confirmed durable. Callers must
		// resolve this outcome before authorizing a new action.

		return fmt.Errorf("%w: %s was replaced but the directory entry could not be flushed: %w",
			ErrDurabilityUnconfirmed, path, pathErrorf("fsync", dir, err))
	}
	return nil
}
