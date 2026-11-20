// Package nodestatetest provides test doubles for the nodestate package.
//
// It exists because the two things most worth testing about a state file are
// awkward to arrange with a real filesystem: the exact order of the syscalls
// that make a write durable, and what an agent does when the host path fails.
// The first needs observation, the second needs injection, and both are
// needed by the operators that consume nodestate, not only by nodestate
// itself.
//
// A typical use in a consuming operator:
//
//	fsys := nodestatetest.NewFaultyFS(nodestatetest.OSFileSystem())
//	store, err := nodestate.New(nodestate.ComponentSBR,
//	    nodestate.WithDir(nodestatetest.TempStateDir(t)),
//	    nodestate.WithFileSystem(fsys))
//	// ...
//	fsys.FailAlways(nodestatetest.OpWrite, syscall.ENOSPC)
//	// assert that the agent reports the fault, and does not disarm a latched fence
//
// Like nodestate itself, this package depends only on the standard library.
package nodestatetest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodestate"
)

// Op names one filesystem operation, for recording and for fault injection.
type Op string

// The operations a nodestate.Store performs.
const (
	OpLstat      Op = "lstat"
	OpStat       Op = "stat"
	OpMkdirAll   Op = "mkdirall"
	OpChmod      Op = "chmod"
	OpOpen       Op = "open"
	OpCreateTemp Op = "createtemp"
	OpRename     Op = "rename"
	OpRemove     Op = "remove"
	OpReadDir    Op = "readdir"
	OpSyncDir    Op = "syncdir"

	// Operations on an open file.
	OpRead     Op = "read"
	OpWrite    Op = "write"
	OpSyncFile Op = "syncfile"
	OpClose    Op = "close"
)

// Record is one observed filesystem operation.
type Record struct {
	// Op is the operation.
	Op Op
	// Path is the path it addressed, or the file's name for an operation on
	// an open file.
	Path string
	// Arg is the second path, for Rename. It is empty otherwise.
	Arg string
}

// OSFileSystem returns the real filesystem, for wrapping.
func OSFileSystem() nodestate.FS { return nodestate.OSFileSystem() }

// TempStateDir returns a fresh temporary directory that a nodestate.Store
// accepts under the default security policy, with its removal registered on
// t's cleanup. Use it wherever a test needs a real state directory.
//
// A plain t.TempDir is not one, for two reasons, and a test that uses it
// with the real filesystem fails on the first store operation:
//
//   - Privacy. The default policy refuses a state directory other users can
//     act on, but t.TempDir creates the directory it returns with
//     os.Mkdir(path, 0o777), so its mode is 0777 masked by the umask —
//     group- and world-readable under the usual 022. The parent directory
//     t.TempDir makes once per test is private; the numbered directory it
//     hands out is not.
//   - A real path. The policy refuses a state directory reached through a
//     symbolic link, and temporary directories sit under one on some
//     platforms: macOS puts them under /var, a link to /private/var.
//
// The returned path is resolved, so it names the directory itself rather
// than a path to it.
func TempStateDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// hookFS is a nodestate.FS that calls a hook before delegating. The hook may
// return an error, which is returned to the caller without the inner
// operation being attempted.
type hookFS struct {
	inner nodestate.FS
	hook  func(Record) error
}

func (h hookFS) call(r Record) error {
	if h.hook == nil {
		return nil
	}
	return h.hook(r)
}

func (h hookFS) Lstat(name string) (os.FileInfo, error) {
	if err := h.call(Record{Op: OpLstat, Path: name}); err != nil {
		return nil, err
	}
	return h.inner.Lstat(name)
}

func (h hookFS) Stat(name string) (os.FileInfo, error) {
	if err := h.call(Record{Op: OpStat, Path: name}); err != nil {
		return nil, err
	}
	return h.inner.Stat(name)
}

func (h hookFS) MkdirAll(name string, perm os.FileMode) error {
	if err := h.call(Record{Op: OpMkdirAll, Path: name}); err != nil {
		return err
	}
	return h.inner.MkdirAll(name, perm)
}

func (h hookFS) Chmod(name string, perm os.FileMode) error {
	if err := h.call(Record{Op: OpChmod, Path: name}); err != nil {
		return err
	}
	return h.inner.Chmod(name, perm)
}

func (h hookFS) Open(name string, opts nodestate.OpenOptions) (nodestate.File, error) {
	if err := h.call(Record{Op: OpOpen, Path: name}); err != nil {
		return nil, err
	}
	f, err := h.inner.Open(name, opts)
	if err != nil {
		return nil, err
	}
	return hookFile{inner: f, hook: h.hook}, nil
}

func (h hookFS) CreateTemp(dir, prefix string, perm os.FileMode) (nodestate.File, error) {
	if err := h.call(Record{Op: OpCreateTemp, Path: dir, Arg: prefix}); err != nil {
		return nil, err
	}
	f, err := h.inner.CreateTemp(dir, prefix, perm)
	if err != nil {
		return nil, err
	}
	return hookFile{inner: f, hook: h.hook}, nil
}

func (h hookFS) Rename(oldpath, newpath string) error {
	if err := h.call(Record{Op: OpRename, Path: oldpath, Arg: newpath}); err != nil {
		return err
	}
	return h.inner.Rename(oldpath, newpath)
}

func (h hookFS) Remove(name string) error {
	if err := h.call(Record{Op: OpRemove, Path: name}); err != nil {
		return err
	}
	return h.inner.Remove(name)
}

func (h hookFS) ReadDir(name string) ([]os.DirEntry, error) {
	if err := h.call(Record{Op: OpReadDir, Path: name}); err != nil {
		return nil, err
	}
	return h.inner.ReadDir(name)
}

func (h hookFS) SyncDir(name string) error {
	if err := h.call(Record{Op: OpSyncDir, Path: name}); err != nil {
		return err
	}
	return h.inner.SyncDir(name)
}

type hookFile struct {
	inner nodestate.File
	hook  func(Record) error
}

func (f hookFile) call(op Op) error {
	if f.hook == nil {
		return nil
	}
	return f.hook(Record{Op: op, Path: f.inner.Name()})
}

func (f hookFile) Read(p []byte) (int, error) {
	if err := f.call(OpRead); err != nil {
		return 0, err
	}
	return f.inner.Read(p)
}

func (f hookFile) Write(p []byte) (int, error) {
	if err := f.call(OpWrite); err != nil {
		return 0, err
	}
	return f.inner.Write(p)
}

func (f hookFile) Close() error {
	if err := f.call(OpClose); err != nil {
		return err
	}
	return f.inner.Close()
}

func (f hookFile) Sync() error {
	if err := f.call(OpSyncFile); err != nil {
		return err
	}
	return f.inner.Sync()
}

func (f hookFile) Name() string               { return f.inner.Name() }
func (f hookFile) Stat() (os.FileInfo, error) { return f.inner.Stat() }

// RecordingFS wraps a nodestate.FS and records every operation in order. It
// changes no behaviour.
//
// Use it to assert on a sequence — most usefully that a write is a create,
// write, sync, close, rename, sync-directory, in that order — or to assert
// that an operation did not happen at all.
//
// It is safe for concurrent use.
type RecordingFS struct {
	nodestate.FS

	mu      sync.Mutex
	records []Record
}

// NewRecordingFS returns a RecordingFS delegating to inner.
func NewRecordingFS(inner nodestate.FS) *RecordingFS {
	r := &RecordingFS{}
	r.FS = hookFS{inner: inner, hook: r.record}
	return r
}

func (r *RecordingFS) record(rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

// Operations returns a copy of everything recorded so far, in order.
func (r *RecordingFS) Operations() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.records))
	copy(out, r.records)
	return out
}

// Sequence returns the recorded operations restricted to the kinds named,
// preserving order. It is the readable way to assert on an ordering without
// coupling the assertion to every incidental stat and lstat.
//
//	got := rec.Sequence(OpWrite, OpSyncFile, OpRename, OpSyncDir)
//	// got is exactly those four, in the order they happened
func (r *RecordingFS) Sequence(kinds ...Op) []Op {
	want := make(map[Op]struct{}, len(kinds))
	for _, k := range kinds {
		want[k] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Op, 0, len(r.records))
	for _, rec := range r.records {
		if _, ok := want[rec.Op]; ok {
			out = append(out, rec.Op)
		}
	}
	return out
}

// Count returns how many times op was recorded.
func (r *RecordingFS) Count(op Op) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.records {
		if rec.Op == op {
			n++
		}
	}
	return n
}

// Reset discards the recorded operations.
func (r *RecordingFS) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

// FaultyFS wraps a nodestate.FS and fails chosen operations.
//
// This is how a consumer tests the host-path fault paths the design requires
// it to get right: reporting a fault without claiming the cluster's storage
// is unhealthy, and never disarming a watchdog while a fence is latched just
// because a file could not be written.
//
// It is safe for concurrent use.
type FaultyFS struct {
	nodestate.FS

	mu     sync.Mutex
	once   map[Op]error
	always map[Op]error
	filter func(Record) bool
}

// NewFaultyFS returns a FaultyFS delegating to inner. With no faults
// configured it behaves exactly like inner.
func NewFaultyFS(inner nodestate.FS) *FaultyFS {
	f := &FaultyFS{
		once:   make(map[Op]error),
		always: make(map[Op]error),
	}
	f.FS = hookFS{inner: inner, hook: f.maybeFail}
	return f
}

// FailOnce makes the next occurrence of op return err, after which the
// operation succeeds again. Use it to test recovery.
func (f *FaultyFS) FailOnce(op Op, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.once[op] = err
}

// FailAlways makes every occurrence of op return err until Clear is called.
// Use it to test a host path that has gone away and stayed away.
func (f *FaultyFS) FailAlways(op Op, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.always[op] = err
}

// OnlyFailWhen restricts the configured faults to operations the predicate
// accepts, so a test can fail writes to one file and leave a peer's alone.
// A nil predicate removes the restriction.
func (f *FaultyFS) OnlyFailWhen(predicate func(Record) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filter = predicate
}

// Clear removes every configured fault.
func (f *FaultyFS) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.once = make(map[Op]error)
	f.always = make(map[Op]error)
	f.filter = nil
}

func (f *FaultyFS) maybeFail(rec Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.filter != nil && !f.filter(rec) {
		return nil
	}
	if err, ok := f.once[rec.Op]; ok {
		delete(f.once, rec.Op)
		return err
	}
	if err, ok := f.always[rec.Op]; ok {
		return err
	}
	return nil
}

// FakeClock is a Clock a test controls. The zero value is not useful; call
// NewFakeClock.
//
// It is safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a clock reading t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

// Now implements nodestate.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to t. Unlike Advance it may move backwards, which is
// what an NTP step after a total outage looks like.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
