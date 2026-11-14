package nodestate

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// FS is the set of file operations a Store performs.
//
// It exists for two reasons. The first is that this package's durability
// guarantee is an ordering of syscalls, and an ordering can only be tested by
// observing the calls. The second is that the consumers of this library have
// to test their own behaviour when the host path fails — the agent that must
// not disarm its watchdog because it could not write a file — and injecting a
// failure is far cheaper than arranging a full disk.
//
// Use OSFileSystem for production. The nodestatetest subpackage provides
// recording and fault-injecting wrappers.
//
// An implementation must be safe for concurrent use.
type FS interface {
	// Lstat returns information about name without following a final symlink.
	Lstat(name string) (os.FileInfo, error)
	// Stat returns information about name, following symlinks.
	Stat(name string) (os.FileInfo, error)
	// MkdirAll creates name and any missing parents with the given mode,
	// subject to the process umask.
	MkdirAll(name string, perm os.FileMode) error
	// Chmod sets the mode of name.
	Chmod(name string, perm os.FileMode) error
	// Open opens name for reading.
	Open(name string, opts OpenOptions) (File, error)
	// CreateTemp creates a new file in dir whose name begins with prefix, with
	// the given mode, failing rather than reusing an existing name.
	CreateTemp(dir, prefix string, perm os.FileMode) (File, error)
	// Rename renames oldpath to newpath, replacing newpath if it exists. Both
	// paths are in the same directory, so the operation is atomic.
	Rename(oldpath, newpath string) error
	// Remove deletes name.
	Remove(name string) error
	// ReadDir lists the entries of the directory name.
	ReadDir(name string) ([]os.DirEntry, error)
	// SyncDir flushes the directory name's entries to stable storage, so that
	// a rename survives a power loss.
	SyncDir(name string) error
}

// OpenOptions controls how FS.Open resolves a path.
type OpenOptions struct {
	// FollowSymlinks permits a final symlink. The default, false, makes Open
	// fail on one, which is how this package keeps a planted link in the state
	// directory from redirecting a read.
	FollowSymlinks bool
}

// File is the subset of *os.File a Store uses.
type File interface {
	io.Reader
	io.Writer
	io.Closer
	// Name returns the path the file was opened or created with.
	Name() string
	// Stat returns information about the open file, which is what makes a
	// check immune to the path being swapped after it was resolved.
	Stat() (os.FileInfo, error)
	// Sync flushes the file's contents to stable storage.
	Sync() error
}

// OSFileSystem returns the FS backed by the real filesystem. It is the
// default, and the only implementation a production caller needs.
func OSFileSystem() FS { return osFS{} }

type osFS struct{}

func (osFS) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (osFS) Stat(name string) (os.FileInfo, error)  { return os.Stat(name) }

func (osFS) MkdirAll(name string, perm os.FileMode) error { return os.MkdirAll(name, perm) }
func (osFS) Chmod(name string, perm os.FileMode) error    { return os.Chmod(name, perm) }

func (osFS) Open(name string, opts OpenOptions) (File, error) {
	flags := os.O_RDONLY | osCloexec
	if !opts.FollowSymlinks {
		flags |= osNoFollow
	}
	f, err := os.OpenFile(name, flags, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// createTempAttempts bounds the retry loop on a name collision. Collisions
// need 64 bits of randomness to repeat, so more than a handful of attempts
// means something other than chance is wrong with the directory.
const createTempAttempts = 10

func (osFS) CreateTemp(dir, prefix string, perm os.FileMode) (File, error) {
	// os.CreateTemp is not usable here: it fixes the mode at 0600 and does not
	// pass O_NOFOLLOW, so a pre-planted symlink with a guessed name would be
	// followed.
	flags := os.O_RDWR | os.O_CREATE | os.O_EXCL | osCloexec | osNoFollow
	var lastErr error
	for i := 0; i < createTempAttempts; i++ {
		suffix, err := randomSuffix()
		if err != nil {
			return nil, err
		}
		name := filepath.Join(dir, prefix+suffix)
		f, err := os.OpenFile(name, flags, perm)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error             { return os.Remove(name) }

func (osFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

func (osFS) SyncDir(name string) error { return syncDir(name) }

// randomSuffix returns 16 hex characters from the system CSPRNG. Temporary
// file names must be unpredictable, not merely unique: a predictable name in a
// directory another process can read is a pre-creation target.
func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
