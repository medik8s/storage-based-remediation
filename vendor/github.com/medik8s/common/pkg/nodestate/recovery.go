package nodestate

import (
	"bytes"
	"context"
	"fmt"
)

// syncCurrent establishes durability of the current file and its directory.
// The caller holds mu and the consumer holds the host executor lock. No file
// content is changed. Kernel I/O is not cancellable: allow one outstanding
// operation and keep it independent of watchdog feeding.
func (s *Store) syncCurrent() error {
	path := s.Path(s.self)
	f, err := s.fs.Open(path, OpenOptions{FollowSymlinks: s.security.AllowSymlinks})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDurabilityUnconfirmed, pathErrorf("open", path, err))
	}
	info, err := f.Stat()
	if err == nil {
		err = s.security.checkFile(path, info, false)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = s.fs.SyncDir(s.dir)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDurabilityUnconfirmed, pathErrorf("sync", path, err))
	}
	return nil
}

// ConfirmDurability confirms the exact candidate returned by an unconfirmed
// Write or Update without invoking the original mutation again. A different
// document returns ErrStateChanged; unreadable or lost history is never success.
// Keep expected unchanged until resolution. After process restart, Load confirms
// the surviving document, but the consumer must still reconcile it with its
// live execution record; absence is not evidence that an uncertain attempt never ran.
func (s *Store) ConfirmDurability(ctx context.Context, expected *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected == nil {
		return fmt.Errorf("nodestate: expected document must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := s.loadLocked()
	if err != nil {
		return err
	}
	if !res.Status.Trusted() {
		return historyError(res)
	}
	want, err := encodeDocument(expected)
	if err != nil {
		return err
	}
	got, err := encodeDocument(res.Document)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return ErrStateChanged
	}
	return s.syncCurrent()
}

// ReconcileHistory is the explicit boundary for replacing unknown history.
// The consumer must hold the host executor lock, resolve live execution records
// and reset budgets, and supply an audited reason before calling it. Neither
// absence nor a timeout supplies that authority. The callback sees the current
// classification under the Store lock and must return the fully reconciled
// document, retaining all known commitments, identities and budget records.
// Operational read errors abort before the callback. Corrupt/unsupported bytes
// stay at the live path until this deliberate atomic replacement succeeds.
// On ErrDurabilityUnconfirmed, resolve with ConfirmDurability, not by repeating
// this callback. A failure before rename leaves the original bytes intact.
func (s *Store) ReconcileHistory(ctx context.Context, reason string, reconcile func(*LoadResult) (*Document, error)) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reason == "" || reconcile == nil {
		return nil, fmt.Errorf("nodestate: reconciliation requires a reason and callback")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	doc, err := reconcile(res)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("nodestate: reconciliation returned a nil document")
	}
	if doc.Agent.Component != "" && doc.Agent.Component != s.self {
		return nil, ErrNotOwner
	}
	doc = doc.Clone()
	// Record the reconstruction in the envelope, not only in the event ring.
	// The ring is pruned, and a consumer that learns a month later that its
	// budget was reconstructed has learned it too late.
	doc.HistoryReconciledAt = s.clock.Now()
	doc.AppendEvent(EventEntry{TS: s.clock.Now(), Type: "Warning", Reason: "HistoryReconciled", Message: reason})
	err = s.writeLocked(doc)
	if err != nil {
		// The candidate is returned even on failure, so the caller can inspect it;
		// only nil error establishes durability. Never infer success from non-nil doc.
		return doc, err
	}
	return doc, nil
}
