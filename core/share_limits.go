package core

import (
	"context"
	"errors"
	"sync"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// Sentinel errors for share budgets. Handlers map these to HTTP status codes.
var (
	// ErrSharePlayLimit is returned when the share's concurrent playback budget
	// is fully reserved. Transient: retrying after an active stream ends works.
	ErrSharePlayLimit = errors.New("share concurrent play limit reached")
	// ErrShareDownloadLimit is returned when the cumulative download budget is
	// already fully settled. Permanent (until the owner raises the limit).
	ErrShareDownloadLimit = errors.New("share download limit reached")
	// ErrShareDownloadsBusy is returned when the remaining budget is reserved by
	// downloads still in flight. Transient: a failed/aborted download frees it.
	ErrShareDownloadsBusy = errors.New("share download budget busy, retry shortly")
)

// ticket represents one reserved resource. Rotation marks active tickets so
// in-flight responses abort: play contexts are canceled directly, download
// tickets start erroring through their revocation-aware writer.
type playSlot struct {
	cancel context.CancelFunc
}

type dlTicket struct {
	revoked bool
}

// DownloadLease is one reserved cumulative-download slot. It is safe to call
// either Commit or Release (whichever runs first wins; later calls are
// idempotent no-ops). Revoked reports whether the share's generation was
// rotated after the reservation; the public download writer uses it to abort
// the response mid-flight without settling.
type DownloadLease struct {
	ticket  *dlTicket
	budget  *shareBudget
	settle  func()
	abort   func()
}

// Revoked is true once the share has been rotated while the download was
// still streaming. A revoked delivery must never be settled.
func (l *DownloadLease) Revoked() bool {
	l.budget.mu.Lock()
	defer l.budget.mu.Unlock()
	return l.ticket.revoked
}

// Commit settles the download (persists download_count) after a fully delivered
// archive. If the lease was revoked it behaves like Release.
func (l *DownloadLease) Commit() { l.settle() }

// Release frees the reservation without settling it.
func (l *DownloadLease) Release() { l.abort() }

// shareBudget holds the in-memory, process-local budget state for one share.
// Persistent state (epoch, download_count) lives in the database; this struct
// only tracks live activity, which naturally resets to zero on restart while
// the persistent counters remain the safety baseline.
type shareBudget struct {
	mu sync.Mutex

	// Concurrent play budget: a plain counter guarded by mu (a channel would
	// fix the capacity at first use and could not follow owner edits).
	activePlays int
	playSlots   map[*playSlot]struct{}

	// Cumulative download budget: dlSettled is reconciled from the DB on every
	// reservation; dlInflight counts reserved-but-not-finished deliveries.
	dlSettled  int
	dlInflight int
	dlTickets  map[*dlTicket]struct{}
}

func newShareBudget() *shareBudget {
	return &shareBudget{
		playSlots: map[*playSlot]struct{}{},
		dlTickets: map[*dlTicket]struct{}{},
	}
}

// Rotate persists the new access generation and immediately revokes every
// in-flight play and download derived from earlier generations. Media links
// minted for the old generation are rejected by the public handlers on their
// next request (they carry the stale generation in their signed token); active
// streams/downloads are cut here so revocation does not wait for them to end.
// The share's public link (its ID) does not change.
func (s *shareService) Rotate(ctx context.Context, id string) (int, error) {
	epoch, err := s.ds.Share(ctx).RotateEpoch(id)
	if err != nil {
		return 0, err
	}
	s.revoke(id)
	log.Info(ctx, "Share access generation rotated", "share", id, "epoch", epoch)
	return epoch, nil
}

// revoke cancels every active play context for the share and marks every
// in-flight download ticket as revoked. It deliberately leaves the counters
// alone: the handlers' own release paths still run as their requests abort,
// and those paths are the single place where counts are decremented.
func (s *shareService) revoke(shareID string) {
	s.budgetsMu.Lock()
	b := s.budgets[shareID]
	s.budgetsMu.Unlock()
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for slot := range b.playSlots {
		slot.cancel()
	}
	for t := range b.dlTickets {
		t.revoked = true
	}
}

// ActivePlays reports the number of streams currently playing for the share.
// Used for owner-facing usage display.
func (s *shareService) ActivePlays(shareID string) int {
	s.budgetsMu.Lock()
	b := s.budgets[shareID]
	s.budgetsMu.Unlock()
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activePlays
}

// AcquirePlay reserves one concurrent-play slot for the share, before the
// stream is created. It covers raw and transcoded streams identically because
// it sits in the HTTP handler, independent of format. On success it returns a
// derived context (canceled when the share is rotated) and a release function
// that must be called exactly once when the request ends — normal completion,
// client disconnect, or startup failure all reach it via defer.
//
// Acquire never blocks: when the cap is reached it returns ErrSharePlayLimit.
// A zero/negative MaxConcurrentPlays means unlimited and costs nothing.
func (s *shareService) AcquirePlay(ctx context.Context, sh *model.Share) (context.Context, context.CancelFunc, error) {
	if sh.MaxConcurrentPlays <= 0 {
		return ctx, func() {}, nil
	}
	b := s.budgetFor(sh.ID)

	b.mu.Lock()
	if b.activePlays >= sh.MaxConcurrentPlays {
		b.mu.Unlock()
		return nil, nil, ErrSharePlayLimit
	}
	playCtx, cancel := context.WithCancel(ctx)
	slot := &playSlot{cancel: cancel}
	b.activePlays++
	b.playSlots[slot] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			b.mu.Lock()
			if b.activePlays > 0 {
				b.activePlays--
			}
			delete(b.playSlots, slot)
			b.mu.Unlock()
		})
	}
	return playCtx, release, nil
}

// ReserveDownload reserves one cumulative-download slot for the share, before
// any byte of the archive is written. The check-and-increment of the in-flight
// counter happens under a single lock, so concurrent requests can never
// over-issue the last slot. It returns two mutually-exclusive, idempotent
// closures: commit must be called only after the archive has been fully
// delivered (it then persists the settled counter), release on every other
// outcome (archiver error, client disconnect, rotation revocation).
func (s *shareService) ReserveDownload(ctx context.Context, sh *model.Share) (*DownloadLease, error) {
	b := s.budgetFor(sh.ID)

	b.mu.Lock()
	// Reconcile against the DB: after a restart the in-memory settled counter
	// starts at zero, and another request may have settled while this budget
	// entry was idle. The DB value loaded with the share is the baseline.
	if sh.DownloadCount > b.dlSettled {
		b.dlSettled = sh.DownloadCount
	}
	if sh.MaxDownloads > 0 {
		if b.dlSettled >= sh.MaxDownloads {
			b.mu.Unlock()
			return nil, ErrShareDownloadLimit
		}
		if b.dlSettled+b.dlInflight >= sh.MaxDownloads {
			b.mu.Unlock()
			return nil, ErrShareDownloadsBusy
		}
	}
	b.dlInflight++
	ticket := &dlTicket{}
	b.dlTickets[ticket] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	finish := func(settle bool) {
		once.Do(func() {
			b.mu.Lock()
			b.dlInflight--
			delete(b.dlTickets, ticket)
			revoked := ticket.revoked
			if settle && !revoked {
				b.dlSettled++
			}
			b.mu.Unlock()

			if !settle || revoked {
				return
			}
			ok, err := s.ds.Share(ctx).SettleDownload(sh.ID)
			if err != nil {
				log.Error(ctx, "Could not settle share download count", "share", sh.ID, err)
				return
			}
			if !ok {
				// Defense in depth: the conditional update found the budget
				// exhausted. The reservation gate makes this unreachable in
				// normal operation; roll back the in-memory settled counter so
				// subsequent reservations stay consistent with the DB.
				log.Warn(ctx, "Share download settlement lost the budget race", "share", sh.ID)
				b.mu.Lock()
				if b.dlSettled > 0 {
					b.dlSettled--
				}
				b.mu.Unlock()
			}
		})
	}
	return &DownloadLease{
		ticket: ticket,
		budget: b,
		settle:  func() { finish(true) },
		abort:   func() { finish(false) },
	}, nil
}

func (s *shareService) budgetFor(shareID string) *shareBudget {
	s.budgetsMu.Lock()
	defer s.budgetsMu.Unlock()
	b, ok := s.budgets[shareID]
	if !ok {
		b = newShareBudget()
		s.budgets[shareID] = b
	}
	return b
}
