package nodestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Size and retention defaults for a Store.
const (
	// DefaultMaxDocumentBytes caps what a Store will write. A document that
	// large means a caller is accumulating something it should be trimming,
	// and a state directory on a node's root filesystem is not the place to
	// find that out.
	DefaultMaxDocumentBytes = 1 << 20 // 1 MiB

	// DefaultMaxReadBytes caps what a Store will read into memory. It bounds
	// the cost of a hostile or runaway file; anything larger is treated as
	// unreadable rather than parsed.
	DefaultMaxReadBytes = 8 << 20 // 8 MiB

	// quarantineSuffix terminates every quarantined file name, so operators
	// and log scrapers can find them with one glob.
	quarantineSuffix = ".corrupt"

	// EventReasonStateFileRecovered is retained for decoding legacy event logs.
	// Deprecated: recovery is explicit and unknown budgets never restart at zero.
	EventReasonStateFileRecovered = "StateFileRecovered"

	// EventReasonClockWentBackwards is the Reason recorded when the wall
	// clock reads earlier than the previous write of this file. Durations
	// derived from the history across that point are unreliable, which
	// matters to anything computing a reboot budget over a time window.
	EventReasonClockWentBackwards = "ClockWentBackwards"

	// EventReasonPreservedFieldsDropped identifies legacy data-loss warnings.
	// Deprecated: preservation limits now refuse the operation without dropping fields.
	EventReasonPreservedFieldsDropped = "PreservedFieldsDropped"
)

// Status classifies what Store.Load found on disk.
type Status int

const (
	// StatusOK means the file was read and understood.
	StatusOK Status = iota
	// StatusAbsent means no file exists. It cannot distinguish first installation
	// from lost history; live execution and budget checks are still required.
	StatusAbsent
	// StatusCorrupt means corrupt history is retained in place.
	// No automatic repair or quarantine is performed. Ordinary updates are blocked.
	StatusCorrupt
	// StatusIncompatible means an unsupported schema is retained unchanged.
	StatusIncompatible
	// StatusDurabilityUnknown means the file is visible but synchronization failed.
	StatusDurabilityUnknown
	// StatusRecovered means the history is usable but was reconstructed by
	// ReconcileHistory rather than observed.
	//
	// It is a trusted status: ordinary reads and updates proceed. What it
	// adds is that the records covering this window were supplied by an
	// operator from authoritative API facts, not accumulated by the agent,
	// so a reset budget computed from them is only as complete as that
	// reconstruction was. Report it; do not block on it.
	//
	// It clears itself. The status reverts to StatusOK once the
	// reconciliation falls outside the reboot retention window, because by
	// then every reconstructed record has aged out and the remaining
	// history was observed normally. A permanent flag would mark a node
	// reconstructed for the rest of its life, which is the trap the
	// completeness horizon already avoids.
	StatusRecovered
)

// String implements fmt.Stringer.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusAbsent:
		return "Absent"
	case StatusCorrupt:
		return "Corrupt"
	case StatusIncompatible:
		return "Incompatible"
	case StatusDurabilityUnknown:
		return "DurabilityUnknown"
	case StatusRecovered:
		return "Recovered"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// LoadResult contains the classification, which must be checked before use.
// A placeholder Document in a non-OK result is not trusted execution history.
type LoadResult struct {
	// Document is the loaded state when Status.Trusted reports true, which
	// covers StatusOK and StatusRecovered. For any other status it is a
	// placeholder: somewhere to write, not history to trust, and only after
	// reconciliation.
	Document *Document

	// Status says what was found. Branch on it. Every value for which
	// Status.Trusted reports false means ordinary mutation must wait, which
	// is what HistoryUncertain reports.
	Status Status

	// Cause is why the contents could not be used, for StatusCorrupt,
	// StatusIncompatible and StatusDurabilityUnknown. Classify it with
	// errors.Is against ErrCorrupt, ErrSchemaUnsupported or
	// ErrDurabilityUnconfirmed.
	Cause error

	// Deprecated: Load no longer moves files, because moving a damaged file
	// before the fact of the damage is durable opens a window where a crash
	// leaves neither. Corrupt bytes stay at the live path until
	// ReconcileHistory replaces them. These fields remain zero.
	QuarantinePath   string
	QuarantineFailed bool
	QuarantineError  error

	// ForeignWrite reports that another process wrote this file after this
	// Store had written it, which the single-writer contract forbids.
	//
	// It is diagnostic. Nothing is refused on the strength of it: stalling a
	// fencing agent is worse than overwriting a record that should not
	// exist, and refusing at start-up would let a predecessor stuck in
	// uninterruptible sleep keep its replacement from ever running. Raise a
	// condition on it; do not gate execution on it, and do not treat it as a
	// substitute for the consumer's host executor lock.
	ForeignWrite bool
}

// HistoryLost reports corrupt or incompatible contents. False does not prove
// complete history: in particular, absence is unknown, not verified first boot.
func (r *LoadResult) HistoryLost() bool {
	return r != nil && (r.Status == StatusCorrupt || r.Status == StatusIncompatible)
}

// Trusted reports whether history with this status may be acted on.
//
// StatusOK and StatusRecovered qualify: the second is reconstructed rather
// than observed, which is worth reporting but is not a reason to stop. Every
// other status means the consumer must establish the facts first.
func (s Status) Trusted() bool { return s == StatusOK || s == StatusRecovered }

// HistoryUncertain reports whether ordinary execution must wait for reconciliation.
func (r *LoadResult) HistoryUncertain() bool { return r == nil || !r.Status.Trusted() }

// HistoryReconstructed reports that this history was supplied by
// ReconcileHistory rather than accumulated by the agent. See StatusRecovered.
func (r *LoadResult) HistoryReconstructed() bool { return r != nil && r.Status == StatusRecovered }

// Clock supplies the current time. Replace it in tests with
// nodestatetest.FakeClock.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock returns the Clock backed by time.Now.
func SystemClock() Clock { return systemClock{} }

// Store reads and writes one component's node-local file.
//
// Operations on the same path are serialized within this process. They are
// not serialized across processes, and this package deliberately does not
// try: a cross-process lock in the feeding path of a hardware watchdog is a
// liveness hazard in precisely the storage failure the product exists to
// handle, because a holder stuck in uninterruptible sleep would take the
// watchdog loop down with it. What makes that safe to omit is the pairing of
// one writer per file with atomic replacement, so a reader always observes a
// whole version.
//
// "One writer" is a contract the consumer keeps, not one this package can
// enforce. Hold a stable host executor lock before loading mutable state or
// controlling the watchdog. Keep blocked I/O off the watchdog-feeding path,
// and allow only one outstanding journal operation; an expired wait does not
// prove the previous one stopped. Load reports a foreign write when it can
// see one, which is detection after the fact rather than prevention.
//
// Construct one with New.
type Store struct {
	self      Component
	dir       string
	fs        FS
	clock     Clock
	retention RetentionPolicy
	security  SecurityPolicy

	maxDocumentBytes int
	maxReadBytes     int
	housekeeping     HousekeepingPolicy
	writerID         string

	// mu serialises read-modify-write cycles on this file. It is held across
	// file I/O, which is why none of these operations is cancellable: see the
	// package documentation on what to do about that in a watchdog path.
	//
	// It is shared by every Store in this process that targets the same path,
	// not owned by this one. A consumer that builds two Stores for its own
	// component — one in main, one in a controller — is making a mistake, but
	// it is a mistake that would otherwise show up as silently lost records
	// rather than as anything diagnosable. Sharing the lock removes the whole
	// class, and leaves ForeignWrite meaning what it says: another *process*
	// wrote this file.
	// The three fields below are per-Store rather than per-file, but every
	// access to them happens under mu, so mu covers them too.
	mu *sync.Mutex
	// lastRevision is the highest revision this Store has written or read
	// from disk. Seeding it from every trusted read is what lets a fresh
	// process continue the sequence: a deliberate Write of a hand-built
	// document carries no revision, and without the seed the sequence would
	// restart and two different documents would claim the same revision.
	lastRevision uint64
	haveWritten  bool
	swept        bool

	// confirmedRevision and confirmedWriterID identify the bytes this Store
	// has established as crash-durable, and confirmedValid says whether they
	// mean anything yet.
	//
	// They exist to keep durability confirmation off the repeat path. A
	// process cannot know whether its predecessor's last write survived, so
	// the first Load of a Store must synchronize. Every Load after that is
	// looking at bytes this process either wrote and synchronized itself, or
	// already confirmed — and re-synchronizing them buys nothing while
	// costing another chance to block in uninterruptible sleep, on a node
	// whose storage is failing. That is the state this product exists for,
	// and §5.6 of the design asks for journal I/O to be bounded because of
	// it.
	//
	// The writer is part of the identity because a revision is a per-writer
	// sequence number, not a unique value across writers. A foreign process
	// that replaced the file can leave different bytes stamped with a
	// revision this Store happens to have confirmed, and a confirmation
	// established for one writer's bytes must not transfer to another's.
	confirmedRevision uint64
	confirmedWriterID string
	confirmedValid    bool
}

// fileLocks maps a state file path to the mutex serialising access to it
// within this process. Entries are never removed: a process uses one or two
// paths, and a test suite a few hundred, so the map is bounded in practice by
// the number of distinct directories a binary touches.
var fileLocks sync.Map // string -> *sync.Mutex

func lockForPath(path string) *sync.Mutex {
	if existing, ok := fileLocks.Load(path); ok {
		return existing.(*sync.Mutex)
	}
	actual, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// processIdentity is this process's identity as a writer, resolved once.
//
// It has to identify the *process*, not the Store. A random value per Store
// made two Stores in one process — a pattern the shared lock above already
// makes safe — report each other as foreign writes, and it made the identity
// meaningless across a restart, where it was most needed: a new value every
// time is indistinguishable from someone else's value.
//
// Boot id, PID and start time together are enough. The boot id separates two
// nodes and two boots of the same node; the PID separates two processes on
// one boot; the start time separates a reused PID. None is secret and none
// needs to be: this detects a contract violation, it does not defend against
// one.
var (
	processIdentityOnce  sync.Once
	processIdentityValue processWriter
)

func processIdentity() processWriter {
	processIdentityOnce.Do(func() {
		processIdentityValue = processWriter{
			PID:       os.Getpid(),
			StartedAt: time.Now(),
		}
		if bootID, err := ReadBootID(); err == nil {
			processIdentityValue.BootID = bootID
		} else if suffix, randErr := randomSuffix(); randErr == nil {
			// No kernel boot id, which means this is not a Linux node. A
			// random value keeps two processes distinguishable, which is all
			// the detection needs; it merely loses the ability to tell a
			// restart within one boot from a restart across boots.
			processIdentityValue.BootID = "nonlinux-" + suffix
		}
	})
	return processIdentityValue
}

type processWriter struct {
	BootID    string
	PID       int
	StartedAt time.Time
}

// id renders the identity into the opaque string stored in the document.
func (w processWriter) id() string {
	return fmt.Sprintf("%s/%d/%d", w.BootID, w.PID, w.StartedAt.UnixNano())
}

func processWriterID() string { return processIdentity().id() }

// config is the resolved option set. It is unexported: callers configure a
// Store through Option values, which keeps the defaults in one place and
// lets new settings be added without breaking anyone.
type config struct {
	dir              string
	fs               FS
	clock            Clock
	retention        RetentionPolicy
	security         SecurityPolicy
	maxDocumentBytes int
	maxReadBytes     int
	housekeeping     HousekeepingPolicy
	writerID         string
}

// Option configures a Store. See the With functions in this package.
type Option func(*config) error

// WithDir overrides the state directory. The default is Dir, which is the
// path the medik8s contract fixes; override it in tests, or in a deployment
// that mounts the host path somewhere else.
//
// The path must be absolute.
func WithDir(dir string) Option {
	return func(c *config) error {
		if dir == "" {
			return errors.New("nodestate: WithDir: the directory must not be empty")
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("nodestate: WithDir: %q is not absolute; a relative state directory would follow the process working directory", dir)
		}
		c.dir = filepath.Clean(dir)
		return nil
	}
}

// WithFileSystem replaces the filesystem implementation. Production callers
// do not need this; it exists so that consumers can test their behaviour when
// the host path fails. See the nodestatetest subpackage.
func WithFileSystem(filesystem FS) Option {
	return func(c *config) error {
		if filesystem == nil {
			return errors.New("nodestate: WithFileSystem: the filesystem must not be nil")
		}
		c.fs = filesystem
		return nil
	}
}

// WithClock replaces the time source.
func WithClock(clock Clock) Option {
	return func(c *config) error {
		if clock == nil {
			return errors.New("nodestate: WithClock: the clock must not be nil")
		}
		c.clock = clock
		return nil
	}
}

// WithRetention replaces the retention policy applied by Store.Update.
func WithRetention(policy RetentionPolicy) Option {
	return func(c *config) error {
		if err := policy.Validate(); err != nil {
			return err
		}
		c.retention = policy
		return nil
	}
}

// WithSecurityPolicy replaces the permission and ownership requirements.
func WithSecurityPolicy(policy SecurityPolicy) Option {
	return func(c *config) error {
		if err := policy.Validate(); err != nil {
			return err
		}
		c.security = policy
		return nil
	}
}

// WithMaxDocumentBytes caps the encoded size of a document the Store will
// write. A larger document fails with ErrTooLarge and the file on disk is
// left as it was.
func WithMaxDocumentBytes(n int) Option {
	return func(c *config) error {
		if n <= 0 {
			return fmt.Errorf("nodestate: WithMaxDocumentBytes: %d must be positive", n)
		}
		c.maxDocumentBytes = n
		return nil
	}
}

// WithMaxReadBytes caps how much of a file the Store will read into memory.
// A larger file returns ErrTooLarge for the owner and PeerUnreadable for a
// peer. Its contents remain unchanged.
func WithMaxReadBytes(n int) Option {
	return func(c *config) error {
		if n <= 0 {
			return fmt.Errorf("nodestate: WithMaxReadBytes: %d must be positive", n)
		}
		c.maxReadBytes = n
		return nil
	}
}

// WithHousekeeping replaces the policy bounding what accumulates in the
// state directory: how many quarantined copies are kept and for how long,
// how long a temporary file must sit untouched before a sweep reclaims it,
// and how much unrecognised data is carried forward from a newer peer.
//
// Zero fields take their defaults, so adjusting one setting does not mean
// restating the rest.
func WithHousekeeping(policy HousekeepingPolicy) Option {
	return func(c *config) error {
		if err := policy.Validate(); err != nil {
			return err
		}
		c.housekeeping = policy.resolved()
		return nil
	}
}

// WithWriterID overrides the identifier stamped into Document.WriterID.
//
// The default identifies the process, not this Store, and is almost always
// what you want. Override it in a test that needs a predictable value.
func WithWriterID(id string) Option {
	return func(c *config) error {
		if id == "" {
			return errors.New("nodestate: WithWriterID: the identifier must not be empty")
		}
		c.writerID = id
		return nil
	}
}

// New returns a Store that owns self's state file.
//
// It performs no file operations: a Store can be constructed before the host
// path is mounted, and nothing touches the disk until the first Load, Write,
// Update or EnsureDirectory. Options are applied in order and the first
// failure is returned.
func New(self Component, opts ...Option) (*Store, error) {
	if err := self.Validate(); err != nil {
		return nil, err
	}

	cfg := config{
		dir:              Dir,
		fs:               OSFileSystem(),
		clock:            SystemClock(),
		retention:        DefaultRetention(),
		security:         DefaultSecurityPolicy(),
		maxDocumentBytes: DefaultMaxDocumentBytes,
		maxReadBytes:     DefaultMaxReadBytes,
		housekeeping:     DefaultHousekeeping().resolved(),
	}
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("nodestate: New: option %d is nil", i)
		}
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	writerID := cfg.writerID
	if writerID == "" {
		writerID = processWriterID()
	}

	return &Store{
		self:             self,
		dir:              cfg.dir,
		mu:               lockForPath(filepath.Join(cfg.dir, filepath.Base(self.FileName()))),
		fs:               cfg.fs,
		clock:            cfg.clock,
		retention:        cfg.retention,
		security:         cfg.security,
		maxDocumentBytes: cfg.maxDocumentBytes,
		maxReadBytes:     cfg.maxReadBytes,
		housekeeping:     cfg.housekeeping,
		writerID:         writerID,
	}, nil
}

// Self returns the component this Store writes for.
func (s *Store) Self() Component { return s.self }

// Directory returns the state directory.
func (s *Store) Directory() string { return s.dir }

// Path returns the file path of c's state file. The component is reduced to
// its base name, so an unvalidated value cannot escape the state directory.
func (s *Store) Path(c Component) string {
	return filepath.Join(s.dir, filepath.Base(c.FileName()))
}

// WriterID returns the identifier this Store stamps into the documents it
// writes.
func (s *Store) WriterID() string { return s.writerID }

// EnsureDirectory creates the state directory if it is missing and verifies
// it against the security policy if it is not.
//
// Call it once at start-up to get a clear error early. Every write path calls
// it too, so skipping it is safe; what you lose is the chance to report a
// misconfigured host path before the first thing you need to record.
//
// It never relaxes or repairs an existing directory's mode: an unexpected
// mode means something else is managing the path, and that is worth an alert
// rather than a silent fix.
func (s *Store) EnsureDirectory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureDirectory()
}

func (s *Store) ensureDirectory() error {
	fi, err := s.fs.Lstat(s.dir)
	switch {
	case err == nil:
		if err := s.checkResolvedPath(); err != nil {
			return err
		}
		return s.security.checkDirectory(s.dir, fi)
	case !errors.Is(err, fs.ErrNotExist):
		return pathErrorf("lstat", s.dir, err)
	}

	if err := s.checkResolvedPath(); err != nil {
		return err
	}
	// Note which components are about to be created, so only those get
	// synchronized afterwards.
	created := s.missingAncestors()
	if err := s.fs.MkdirAll(s.dir, s.security.DirMode); err != nil {
		return pathErrorf("mkdir", s.dir, err)
	}

	// Verify before adjusting the mode, not after. MkdirAll treats an existing
	// directory as success and resolves symlinks to find it, so a link planted
	// at this path between the check above and here would be followed — and
	// Chmod follows symlinks too. Checking first means the only thing this
	// code will ever chmod is something Lstat has just confirmed is a real
	// directory.
	fi, err = s.fs.Lstat(s.dir)
	if err != nil {
		return pathErrorf("lstat", s.dir, err)
	}
	if err := s.checkResolvedPath(); err != nil {
		return err
	}
	if err := s.security.checkDirectory(s.dir, fi); err != nil {
		return err
	}

	// A newly created directory needs its name persisted in its parent, or a
	// power loss can leave the directory entry behind and the journal with
	// nowhere to live.
	//
	// Only the parents of directories this call actually created are
	// synchronized. Walking all the way to the root instead would fsync
	// /var, / and whatever else is above the state directory — paths this
	// agent did not touch and does not own. That is wasted I/O on the
	// start-up path, and on a node with failing storage every extra fsync is
	// another chance to block; a failure on one of them would stop the agent
	// starting at all, over a directory it never modified.
	for _, dir := range created {
		parent := filepath.Dir(dir)
		if err := s.fs.SyncDir(parent); err != nil {
			return pathErrorf("fsync", parent, err)
		}
	}

	// MkdirAll applies the process umask, so the directory may be more
	// restrictive than asked for but never less. Set the mode explicitly, and
	// only when it differs, so the result does not depend on how the agent was
	// launched.
	if fi.Mode().Perm() == s.security.DirMode.Perm() {
		return nil
	}
	if err := s.fs.Chmod(s.dir, s.security.DirMode); err != nil {
		return pathErrorf("chmod", s.dir, err)
	}
	fi, err = s.fs.Lstat(s.dir)
	if err != nil {
		return pathErrorf("lstat", s.dir, err)
	}
	return s.security.checkDirectory(s.dir, fi)
}

// checkResolvedPath refuses a state directory reached through a symbolic
// link at any point in its path.
//
// The per-file protections elsewhere — Lstat before open, O_NOFOLLOW, a
// re-check on the descriptor — all guard the *final* component. None of them
// notices a link one level up: with /var/lib/medik8s a link, every file this
// package writes lands wherever that link points, and every check still
// passes because the leaf really is a regular root-owned file in a 0700
// directory.
//
// Comparing the configured path against its fully resolved form closes that
// for the realistic case, which is a misconfiguration — an installer or an
// operator who symlinked the host path — rather than an attack. It is not a
// defence against a race: an attacker able to create directories under
// /var/lib can swap a component after this check. Closing *that* needs a
// directory file descriptor held open for the Store's lifetime with all work
// done through openat, which is a much larger change and defends against an
// attacker who, by definition, already has privileged write access to the
// node. See the package documentation on what these controls are for.
func (s *Store) checkResolvedPath() error {
	if s.security.AllowSymlinks {
		return nil
	}
	for path := s.dir; ; path = filepath.Dir(path) {
		fi, err := s.fs.Lstat(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return pathErrorf("lstat", path, err)
		}
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return pathErrorf("verify", path, ErrSymlink)
		}
		if filepath.Dir(path) == path {
			return nil
		}
	}
}

// missingAncestors returns the directories MkdirAll will have to create, from
// the shallowest to the deepest. An unreadable ancestor stops the walk: the
// subsequent MkdirAll reports that failure with better context than a guess
// made here would.
func (s *Store) missingAncestors() []string {
	var missing []string
	for path := s.dir; ; path = filepath.Dir(path) {
		if _, err := s.fs.Lstat(path); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			break
		}
		missing = append(missing, path)
		if filepath.Dir(path) == path {
			break
		}
	}
	// Reverse: a parent must be durable before the child that names it.
	for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
		missing[i], missing[j] = missing[j], missing[i]
	}
	return missing
}

// Load classifies the owner's current history without discarding any bytes.
// Absent is not proof of first installation: the caller must check live execution,
// budgets and its deployment history before deliberately initializing with Write.
// Corrupt and incompatible files remain at their original path until an explicit
// ReconcileHistory, so uncertainty survives process restart and housekeeping.
// Before returning StatusOK, Load synchronizes the existing file and directory.
// This also resolves an earlier visible but durability-unconfirmed replacement.
// A readback alone is never a durability confirmation.
func (s *Store) Load(ctx context.Context) (*LoadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.loadLocked()
	if err != nil {
		return res, err
	}
	if res.Status.Trusted() {
		if err := s.confirmLocked(res.Document); err != nil {
			res.Status, res.Cause = StatusDurabilityUnknown, err
			return res, err
		}
	}
	return res, nil
}

// confirmLocked establishes that doc is crash-durable, skipping the work when
// this Store has already established it for the same revision by the same
// writer.
//
// Skipping is safe because there are only two ways the live file can reach a
// (revision, writer) pair this Store has confirmed: this process synchronized
// it here, or this process wrote it through atomicReplace, which synchronizes
// the file and the directory before returning success. A write that could not
// be confirmed clears the mark, and so does any revision this Store has not
// seen — including every revision left behind by a previous process.
//
// The writer is part of the key because a revision is a per-writer sequence
// number, not a unique value across writers. A contract-violating second
// writer can leave different bytes stamped with a revision this Store happens
// to have confirmed, and a confirmation established for this process's bytes
// must not transfer to bytes it never synchronized.
//
// The caller holds mu.
func (s *Store) confirmLocked(doc *Document) error {
	if s.confirmedValid && doc != nil &&
		doc.Revision == s.confirmedRevision && doc.WriterID == s.confirmedWriterID {
		return nil
	}
	if err := s.syncCurrent(); err != nil {
		s.confirmedValid = false
		return err
	}
	if doc != nil {
		s.confirmedRevision, s.confirmedWriterID, s.confirmedValid = doc.Revision, doc.WriterID, true
	}
	return nil
}

// Read returns trusted current history, or an error.
//
// It fails closed. Absent, corrupt, incompatible and durability-unknown
// history all come back as ErrHistoryUnknown rather than as an empty
// document, because "this node has never run" and "the history is gone" lead
// to opposite decisions about a reset budget and must never be confused.
// StatusOK and StatusRecovered are returned: both are trusted, the second
// having been reconstructed rather than observed.
//
// Use Load at start-up, where the distinction between those cases is the
// whole question. Use Read once history is known good.
func (s *Store) Read(ctx context.Context) (*Document, error) {
	res, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !res.Status.Trusted() {
		return nil, historyError(res)
	}
	return res.Document, nil
}

func historyError(res *LoadResult) error {
	if res.Cause != nil {
		return fmt.Errorf("%w: %s: %w", ErrHistoryUnknown, res.Status, res.Cause)
	}
	return fmt.Errorf("%w: %s; reconcile or explicitly initialize before updating", ErrHistoryUnknown, res.Status)
}

func (s *Store) loadLocked() (*LoadResult, error) {
	if err := s.verifyDirectoryIfPresent(); err != nil {
		return nil, err
	}
	doc, err := s.readDocument(s.Path(s.self), s.self)
	switch {
	case err == nil:
		if err := s.checkPreserved(doc); err != nil {
			return nil, err
		}
		// A fresh process continues the revision sequence from what it finds
		// on disk. A deliberate Write of a hand-built document carries no
		// revision of its own, and without this seed the sequence would
		// restart at 1 while the file's history still speaks of higher ones.
		if doc.Revision > s.lastRevision {
			s.lastRevision = doc.Revision
		}
		return &LoadResult{Document: doc, Status: s.classify(doc), ForeignWrite: s.isForeign(doc)}, nil
	case errors.Is(err, fs.ErrNotExist):
		return &LoadResult{Document: s.newDocument(), Status: StatusAbsent}, nil
	case errors.Is(err, ErrCorrupt), errors.Is(err, ErrSchemaUnsupported):
		status := StatusCorrupt
		if errors.Is(err, ErrSchemaUnsupported) {
			status = StatusIncompatible
		}
		return &LoadResult{Document: s.newDocument(), Status: status, Cause: err}, nil
	default:
		// Access and I/O failures are not evidence of corrupt contents.
		return nil, err
	}
}

// verifyDirectoryIfPresent checks every read path, without creating directories.
func (s *Store) verifyDirectoryIfPresent() error {
	if err := s.checkResolvedPath(); err != nil {
		return err
	}
	stat := s.fs.Lstat
	if s.security.AllowSymlinks {
		stat = s.fs.Stat
	}
	fi, err := stat(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return pathErrorf("lstat", s.dir, err)
	}
	return s.security.checkDirectory(s.dir, fi)
}

// Refuse writes we cannot preserve; never silently discard newer fields.
func (s *Store) checkPreserved(doc *Document) error {
	if s.housekeeping.MaxPreservedBytes > 0 && doc.preservedBytes() > s.housekeeping.MaxPreservedBytes {
		return fmt.Errorf("%w: preserved fields exceed %d bytes; original history is unchanged", ErrTooLarge, s.housekeeping.MaxPreservedBytes)
	}
	return nil
}

// classify separates history the agent accumulated from history an operator
// reconstructed, for as long as that distinction still describes the records
// a budget would be computed from.
func (s *Store) classify(doc *Document) Status {
	if doc == nil || doc.HistoryReconciledAt.IsZero() {
		return StatusOK
	}
	window := s.retention.RebootMaxAge
	if window <= 0 {
		// Records never age out, so the reconstruction never stops
		// describing them.
		return StatusRecovered
	}
	if s.clock.Now().Sub(doc.HistoryReconciledAt) < window {
		return StatusRecovered
	}
	return StatusOK
}

func (s *Store) isForeign(doc *Document) bool {
	if !s.haveWritten {
		return false
	}
	return doc.WriterID != s.writerID || doc.Revision < s.lastRevision
}

func (s *Store) newDocument() *Document { return NewDocument(s.self) }

// ReadPeer reads another component's file.
//
// It returns (nil, nil) for every condition that is the peer's problem rather
// than the caller's: no file, an empty or truncated one, a schema version
// this build does not understand, a symlink, a directory, a file with
// permissions that make its contents untrustworthy, or one too large to read.
// That tolerance is deliberate and is part of the contract. An agent deciding
// whether to fence a node must not be stopped by its peer's bookkeeping, and
// a peer's file carries history and de-duplication hints, never authority.
//
// An error is returned only for a programming mistake — an invalid component
// name — or a cancelled context.
//
// ReadPeer never writes. A peer's damaged file is left exactly as found, for
// explicit consumer reconciliation.
//
// Passing this Store's own component is allowed and reads the Store's own
// file with the same tolerance — no quarantine, no classification. That is
// occasionally what a diagnostic tool wants; it is not what an agent wants at
// start-up, which is Load.
func (s *Store) ReadPeer(ctx context.Context, c Component) (*Document, error) {
	res, err := s.LoadPeer(ctx, c)
	if err != nil {
		return nil, err
	}
	return res.Document, nil
}

// PeerStatus says why a peer's document is or is not available.
type PeerStatus int

const (
	// PeerOK means the document was read.
	PeerOK PeerStatus = iota
	// PeerAbsent means no file exists. It does not distinguish an optional
	// peer that never ran from a peer whose history was lost.
	PeerAbsent
	// PeerUnusable means the file exists but its contents could not be used:
	// empty, truncated, not a document, or claiming another owner. That is
	// the peer's own bookkeeping problem and it will recover from it; the
	// only cost here is that its history is missing from any merge.
	PeerUnusable
	// PeerUnreadable means the file is there and this process could not get
	// at it: permission denied, an owner or mode the security policy
	// rejects, a symlink, or a size over the read limit.
	//
	// This is the one status that is not the peer's problem. It is a
	// deployment fault on this node — an agent not running as the owning
	// user, a user namespace mapping, an SELinux denial — and it silently
	// removes that peer's reboots from every budget merged here. See
	// PeerRead.Configured.
	PeerUnreadable
	// PeerUnsupportedSchema means the file declares a schema version outside
	// the range this build understands, which during a staggered upgrade
	// means the peer is newer. Its reboots are missing from every budget
	// until this side is upgraded too.
	PeerUnsupportedSchema
	// PeerNotCollected means the read never happened: the context was
	// cancelled, or the component name was not usable.
	//
	// It is nobody's fault on the host. Keeping it separate from
	// PeerUnreadable matters because that status tells a consumer to raise a
	// deployment-fault condition — an SELinux denial or a wrong owner — and
	// a cancelled context is neither. The history is still incomplete.
	PeerNotCollected
)

// String implements fmt.Stringer.
func (p PeerStatus) String() string {
	switch p {
	case PeerOK:
		return "OK"
	case PeerAbsent:
		return "Absent"
	case PeerUnusable:
		return "Unusable"
	case PeerUnreadable:
		return "Unreadable"
	case PeerUnsupportedSchema:
		return "UnsupportedSchema"
	case PeerNotCollected:
		return "NotCollected"
	default:
		return fmt.Sprintf("PeerStatus(%d)", int(p))
	}
}

// PeerRead is what LoadPeer returns.
type PeerRead struct {
	// Document is the peer's state, or nil when there is nothing usable.
	Document *Document
	// Status says which of those it is.
	Status PeerStatus
	// Cause is the underlying error for every status but PeerOK and
	// PeerAbsent.
	Cause error
}

// Configured reports whether the peer's absence is this node's problem rather
// than the peer's.
//
// It is true for PeerUnreadable: the file is there and this process may not
// read it. That is a deployment fault — an agent not running as the owning
// user, a user namespace mapping, an SELinux denial — and it is worth a
// condition, because every reboot budget merged on this node is quietly
// missing that peer's history until it is fixed.
func (r *PeerRead) Configured() bool {
	return r != nil && r.Status == PeerUnreadable
}

// LoadPeer reads another component's file and says why, when it cannot.
//
// ReadPeer deliberately flattens every failure to nil, which is right for
// liveness and wrong for diagnosis: "the peer has never run" and "the file is
// there and I am not allowed to read it" produce the same answer, and the
// second silently removes that peer's reboots from every budget on the node.
// Use this where the distinction matters, and raise a condition on
// PeerRead.Configured.
//
// Like ReadPeer it returns an error only for a programming mistake — an
// invalid component name — or a cancelled context. It never writes.
func (s *Store) LoadPeer(ctx context.Context, c Component) (*PeerRead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}

	doc, err := s.readDocument(s.Path(c), c)
	switch {
	case err == nil:
		return &PeerRead{Document: doc, Status: PeerOK}, nil
	case errors.Is(err, fs.ErrNotExist):
		return &PeerRead{Status: PeerAbsent}, nil
	case errors.Is(err, ErrSchemaUnsupported):
		return &PeerRead{Status: PeerUnsupportedSchema, Cause: err}, nil
	case isAccessFault(err):
		return &PeerRead{Status: PeerUnreadable, Cause: err}, nil
	default:
		return &PeerRead{Status: PeerUnusable, Cause: err}, nil
	}
}

// isAccessFault reports whether err means this process could not get at the
// file, as opposed to the file's contents being unusable.
//
// The distinction is the whole point of PeerUnreadable: the first is a
// deployment problem on this node and the second is the peer's own business.
func isAccessFault(err error) bool {
	return errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, ErrInsecurePermissions) ||
		errors.Is(err, ErrInsecureOwner) ||
		errors.Is(err, ErrSymlink) ||
		errors.Is(err, ErrNotRegular) ||
		errors.Is(err, ErrNotDirectory) ||
		errors.Is(err, ErrTooLarge)
}

// ReadAll reads several components' files and returns the ones that could be
// read, keyed by component. With no components named it reads only this Store's
// own file. Pass an explicit set for deliberate multi-component diagnostics.
//
// Nothing is written, including for this Store's own file: ReadAll is a
// read-only view and does not quarantine. Use it to assemble the inputs to
// MergeReboots:
//
//	docs := store.ReadAll(ctx, nodestate.ComponentSBR, nodestate.ComponentSNR)
//	reboots := nodestate.MergeReboots(docs[nodestate.ComponentSBR], docs[nodestate.ComponentSNR])
//
// An unreadable file is simply absent from the result. There is no error
// return by design: every failure mode here is one the caller must carry on
// through. Use LoadPeer when you need to know *why* a peer is missing — in
// particular to distinguish absence (which may mean lost history) from denial,
// which is a deployment fault that silently drops that peer's history out of
// every budget merged here.
func (s *Store) ReadAll(ctx context.Context, components ...Component) map[Component]*Document {
	out := make(map[Component]*Document, len(components))
	if ctx.Err() != nil {
		return out
	}
	if len(components) == 0 {
		components = []Component{s.self}
	}
	seen := make(map[Component]struct{}, len(components))
	for _, c := range components {
		if _, done := seen[c]; done {
			continue // a repeated component is read once, not once per mention
		}
		seen[c] = struct{}{}
		if err := c.Validate(); err != nil {
			continue
		}
		doc, err := s.readDocument(s.Path(c), c)
		if err != nil {
			continue
		}
		out[c] = doc
	}
	return out
}

// Write deliberately replaces a valid document, or initializes an absent file.
// Before initializing, the consumer must independently establish that no live
// unresolved execution exists and that its budget is known. File
// absence alone is insufficient. Use Update for normal state transitions;
// corrupt/unsupported history requires explicit ReconcileHistory.
//
// Write stamps metadata on doc and performs temp-write, file sync, rename and
// directory sync. Only nil error confirms durability. ErrDurabilityUnconfirmed
// means the candidate is visible but MUST NOT authorize a new reset; resolve
// that exact candidate with ConfirmDurability without repeating the mutation.
// Other errors leave the previous live file unchanged. Write does not prune.
func (s *Store) Write(ctx context.Context, doc *Document) error {
	return s.WriteTo(ctx, s.self, doc)
}

// WriteTo is Write with the target component named explicitly. It returns
// ErrNotOwner, without touching the filesystem, for any component other than
// this Store's own.
//
// It exists so the ownership rule is enforced at a call site that can express
// a violation, rather than only being documented. Code that iterates over
// components and writes each one is a bug this turns into an error.
func (s *Store) WriteTo(ctx context.Context, c Component, doc *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c != s.self {
		return fmt.Errorf("%w: this store writes %q and may not write %q; each file has exactly one writer",
			ErrNotOwner, s.self, c)
	}
	if doc == nil {
		return errors.New("nodestate: Write: the document must not be nil")
	}
	if doc.Agent.Component != "" && doc.Agent.Component != s.self {
		return fmt.Errorf("%w: the document is stamped %q but this store writes %q",
			ErrNotOwner, doc.Agent.Component, s.self)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.loadLocked()
	if err != nil {
		return err
	}
	if !res.Status.Trusted() && res.Status != StatusAbsent {
		return historyError(res)
	}
	return s.writeLocked(doc)
}

func (s *Store) writeLocked(doc *Document) error {
	if err := s.checkPreserved(doc); err != nil {
		return err
	}
	identity := processIdentity()
	// Decoding normalizes an absent reboots array to an empty slice, so a
	// hand-built document with a nil slice would encode differently from the
	// same document read back. ConfirmDurability compares encodings, and the
	// mismatch would surface as ErrStateChanged against the caller's own
	// write — which reads as "another process touched this file".
	if doc.Reboots == nil {
		doc.Reboots = []RebootEntry{}
	}
	doc.SchemaVersion = Schema
	doc.Agent.Component = s.self
	if doc.Agent.PID != 0 && doc.Agent.PID != identity.PID {
		// The persisted (PID, StartedAt) pair identifies a process, and a
		// document read back after a restart carries the predecessor's pair.
		// Keeping the predecessor's start time under this process's PID
		// would describe a process that never existed. A caller supplying
		// its own start time does so on a document whose PID is unset, and
		// is left alone.
		doc.Agent.StartedAt = time.Time{}
	}
	doc.Agent.PID = identity.PID
	if doc.Agent.StartedAt.IsZero() {
		doc.Agent.StartedAt = identity.StartedAt
	}
	doc.UpdatedAt = s.clock.Now()
	doc.WriterID = s.writerID
	doc.Revision = s.nextRevision(doc.Revision)

	encoded, err := encodeDocument(doc)
	if err != nil {
		return fmt.Errorf("nodestate: encoding the document for %s: %w", s.self, err)
	}
	if len(encoded) > s.maxDocumentBytes {
		return fmt.Errorf("%w: the encoded document is %d bytes, over the %d byte limit; trim the history you are accumulating",
			ErrTooLarge, len(encoded), s.maxDocumentBytes)
	}

	if err := s.ensureDirectory(); err != nil {
		return err
	}

	// Reclaim anything a previous process left behind before adding to it.
	// Once per Store: the cost is one directory listing per process, and
	// skipping it would let a crash loop fill the host path with temporary
	// files that nothing else ever looks for.
	if !s.swept {
		_, _ = s.sweep()
	}

	err = s.atomicReplace(s.Path(s.self), encoded)
	if err != nil && !errors.Is(err, ErrDurabilityUnconfirmed) {
		return err
	}

	// Commit the bookkeeping for an unconfirmed-durability result too. The
	// new version is on disk and visible; pretending otherwise would reissue
	// this revision on the next write, leaving two different documents
	// claiming the same one and breaking the ordering that foreign-write
	// detection reads.
	s.lastRevision = doc.Revision
	s.haveWritten = true
	if err == nil {
		// atomicReplace synchronized the file and the directory before
		// returning, so these bytes are durable without another pass.
		s.confirmedRevision, s.confirmedWriterID, s.confirmedValid = doc.Revision, doc.WriterID, true
	} else {
		s.confirmedValid = false
	}
	return err
}

// nextRevision never lets the revision go backwards, including when the file
// on disk has a higher one than this Store last wrote. That happens after a
// foreign write, and in a fresh process that loaded a predecessor's file —
// loadLocked seeds lastRevision from every trusted read, so a deliberate
// Write of a hand-built document continues the sequence rather than
// restarting it. Continuing from the larger value keeps the sequence
// meaningful to anyone reading the history afterwards.
func (s *Store) nextRevision(onDisk uint64) uint64 {
	next := s.lastRevision
	if onDisk > next {
		next = onDisk
	}
	return next + 1
}

func encodeDocument(doc *Document) ([]byte, error) {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	// A trailing newline costs one byte and makes the file behave for every
	// text tool an operator will reach for on a broken node.
	return append(encoded, '\n'), nil
}

// Update serializes loading, durability confirmation, mutation, retention and
// replacement. Absent, corrupt, unsupported or unreadable history prevents the
// callback from running. Initialize deliberately with Write only after verifying
// first-installation prerequisites, or use ReconcileHistory for resolved history.
//
// The callback runs under the per-path mutex; do not call this Store from it.
// ErrNoChange skips replacement. Any other callback error aborts the mutation.
// A non-nil document with ErrDurabilityUnconfirmed identifies the visible
// candidate, not permission to reset. Use ConfirmDurability to resolve it. If
// confirmation of the existing file failed before mutation, the document is nil.
// All other errors return nil document and leave live content unchanged.
func (s *Store) Update(ctx context.Context, mutate func(*Document) error) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mutate == nil {
		return nil, errors.New("nodestate: Update: the mutator must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	if !res.Status.Trusted() {
		return nil, historyError(res)
	}
	// Confirm the predecessor is durable before building on it. Cached, so
	// a steady stream of updates pays for one confirmation, not one per call.
	if err := s.confirmLocked(res.Document); err != nil {
		return nil, err
	}
	doc := res.Document
	if err := mutate(doc); err != nil {
		if errors.Is(err, ErrNoChange) {
			return doc, nil
		}
		return nil, err
	}

	s.retention.Apply(doc, s.clock.Now())
	if err := s.writeLocked(doc); err != nil {
		if errors.Is(err, ErrDurabilityUnconfirmed) {
			// Written and visible. Hand back the document as well as the
			// error so the caller can confirm this candidate without rerunning mutate.
			return doc, err
		}
		return nil, err
	}
	return doc, nil
}
