package audiblesync

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// The push loop's pace.
const (
	// PushEvery is how often the loop reads the server's places.
	PushEvery = time.Minute
	// StillFor is how long a place must have been still (its row unchanged)
	// before it is written to Audible: the listener stopped.
	StillFor = 3 * time.Minute
	// offWait is how long an account the server answers sync_off for is left
	// before the loop asks again; refusedWait the same for an account whose
	// person can't be synced for now, or one the server doesn't know.
	offWait     = 5 * time.Minute
	refusedWait = 15 * time.Minute
	// errorWait is the least wait after a failure (the server's or Audible's).
	errorWait = 2 * time.Minute
	// baselineWindow: the first read of an account's feed (no cursor yet)
	// takes the places changed this recently as waiting to be pushed; older
	// ones are history and stay where they are.
	baselineWindow = 10 * time.Minute
	// maxPages bounds one read of an account's feed (200 rows a page).
	maxPages = 50
)

// Pusher is the push loop.
type Pusher struct {
	o Options

	mu sync.Mutex
	// paused: accounts left alone until a time (sync off on the server, a
	// person that can't be synced for), in memory: a restart asks again.
	paused map[string]time.Time
}

// NewPusher returns the push loop.
func NewPusher(o Options) *Pusher {
	o.defaults()
	return &Pusher{o: o, paused: map[string]time.Time{}}
}

// Run pushes every PushEvery until ctx ends.
func (p *Pusher) Run(ctx context.Context) {
	t := time.NewTicker(PushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick(ctx)
		}
	}
}

// Tick is one pass over the accounts: each account mapped on the paired
// server, not refused and not waiting reads its feed and writes the places
// that are due.
func (p *Pusher) Tick(ctx context.Context) {
	srv := p.o.server()
	serverID := p.o.ServerID()
	if srv == nil || serverID == "" {
		return
	}
	accts, err := p.o.Accounts.Accounts(ctx)
	if err != nil {
		p.o.Log.Warn("audible: listing the accounts for the push failed", "err", err)
		return
	}
	now := p.o.Now()
	for _, a := range accts {
		if ctx.Err() != nil {
			return
		}
		if a.OnOtherServer(serverID) || a.ServerID == "" || a.RefusedAt != "" || p.isPaused(a.ID, now) {
			continue
		}
		if _, ok := waiting(a, now); ok {
			continue
		}
		p.account(ctx, srv, a, now)
	}
}

func (p *Pusher) isPaused(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.paused[id]
	if ok && !now.Before(until) {
		delete(p.paused, id)
		return false
	}
	return ok
}

func (p *Pusher) pause(id string, until time.Time) {
	p.mu.Lock()
	p.paused[id] = until
	p.mu.Unlock()
}

// account reads a's feed from its cursor, records the places waiting, then
// writes the ones due.
func (p *Pusher) account(ctx context.Context, srv Server, a store.AudibleAccount, now time.Time) {
	from := now
	baseline := a.PushFrom == ""
	if !baseline {
		if t, err := time.Parse(time.RFC3339Nano, a.PushFrom); err == nil {
			from = t
		}
	}
	cursor := a.PushCursor
	for range maxPages {
		page, err := srv.AudibleProgress(ctx, a.ID, cursor, link.MaxAudibleProgress)
		switch {
		case errors.Is(err, link.ErrSyncOff):
			p.pause(a.ID, now.Add(offWait))
			return
		case errors.Is(err, link.ErrUserUnavailable), errors.Is(err, link.ErrUnknownAccount):
			p.pause(a.ID, now.Add(refusedWait))
			p.o.health(ctx, a, err, time.Time{})
			return
		case err != nil:
			if ctx.Err() == nil {
				p.o.Log.Warn("audible: reading the server's places failed", "account", a.ID, "err", err)
				p.o.health(ctx, a, errors.New("reading the server's places failed: "+err.Error()), retryAt(err, now, errorWait))
			}
			return
		}
		for _, row := range page.Rows {
			p.row(ctx, a, row, now, from, baseline)
		}
		cursor = page.Next
		if !page.More || page.Next == "" {
			break
		}
	}
	if err := p.o.DB.SetAudiblePushCursor(ctx, a.ID, cursor, now); err != nil {
		p.o.Log.Warn("audible: recording the push's cursor failed", "account", a.ID, "err", err)
	}
	p.due(ctx, a, now)
}

// row records one server place: a place on a listed title's book that changed
// since the row last seen becomes the title's waiting place. Audible's own
// rows (the server leaves them out too), a book the title isn't matched to
// and, at the first read, history (older than baselineWindow; after it, older
// than the feed's start) are left.
func (p *Pusher) row(ctx context.Context, a store.AudibleAccount, row link.AudibleProgressRow, now, from time.Time, baseline bool) {
	if row.DeviceID == DeviceID(a.ID) {
		return
	}
	updated, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return
	}
	x, err := p.o.DB.GetAudiblePush(ctx, a.ID, row.ASIN)
	if err != nil || !x.Listed || x.LibraryID != row.LibraryID || x.Path != row.Path {
		return
	}
	if x.RowUpdated != "" {
		if seen, err := time.Parse(time.RFC3339Nano, x.RowUpdated); err == nil && !updated.After(seen) {
			return
		}
	}
	history := updated.Before(from)
	if baseline {
		history = now.Sub(updated) > baselineWindow
	}
	if history {
		err = p.o.DB.SeenAudibleRow(ctx, a.ID, row.ASIN, updated)
	} else {
		err = p.o.DB.WantAudiblePush(ctx, a.ID, row.ASIN, ms(row.Position), updated)
	}
	if err != nil {
		p.o.Log.Warn("audible: recording a server place failed", "account", a.ID, "err", err)
	}
}

// due writes each waiting place whose row has been still for StillFor.
func (p *Pusher) due(ctx context.Context, a store.AudibleAccount, now time.Time) {
	list, err := p.o.DB.AudiblePushes(ctx, a.ID)
	if err != nil {
		p.o.Log.Warn("audible: listing the places to push failed", "account", a.ID, "err", err)
		return
	}
	for _, x := range list {
		if ctx.Err() != nil {
			return
		}
		if !x.Listed || x.WantMs == nil {
			continue
		}
		updated, err := time.Parse(time.RFC3339Nano, x.RowUpdated)
		if err != nil || now.Sub(updated) < StillFor {
			continue
		}
		if stop := p.push(ctx, a, x, now); stop {
			return
		}
	}
}

// push writes x's waiting place to Audible, or drops it: a place at the
// start, one within echoMs of the last one written (the debounce) or of the
// last one heard from Audible (Audible's own place coming back), or one past
// the title's runtime (a wrong match: never written to the real account).
// stop: Audible asked to wait, so the account's other places wait too.
func (p *Pusher) push(ctx context.Context, a store.AudibleAccount, x store.AudiblePush, now time.Time) (stop bool) {
	want := *x.WantMs
	drop := ""
	switch {
	case want < 1000:
		drop = "at the start"
	case x.LastMs >= 0 && abs(want-x.LastMs) <= echoMs:
		drop = "the place written last"
	case x.HeardMs >= 0 && abs(want-x.HeardMs) <= echoMs:
		drop = "Audible's own place"
	case pastRuntime(float64(want)/1000, x.RuntimeS):
		drop = "past the runtime"
		p.o.Log.Warn("audible: a place on the server is past the title's runtime on Audible, so its match looks wrong; not written",
			"account", a.ID, "asin", x.ASIN)
	}
	if drop != "" {
		if err := p.o.DB.AudiblePushed(ctx, a.ID, x.ASIN, want, now, false); err != nil {
			p.o.Log.Warn("audible: dropping a place failed", "account", a.ID, "err", err)
		}
		return false
	}
	acr := x.ACR
	if acr == "" {
		var err error
		if acr, err = p.o.Accounts.ContentACR(ctx, a.ID, x.ASIN); err != nil {
			return p.failed(ctx, a, x, now, "reading the title's content reference on Audible", err)
		}
		if err := p.o.DB.SetAudibleACR(ctx, a.ID, x.ASIN, acr); err != nil {
			p.o.Log.Warn("audible: caching a content reference failed", "account", a.ID, "err", err)
		}
	}
	if err := p.o.Accounts.SetPosition(ctx, a.ID, x.ASIN, acr, want); err != nil {
		if s := audible.HTTPStatus(err); s == http.StatusBadRequest || s == http.StatusNotFound {
			// A content reference Audible no longer takes: read it again next
			// time.
			_ = p.o.DB.SetAudibleACR(ctx, a.ID, x.ASIN, "")
		}
		return p.failed(ctx, a, x, now, "writing the place to Audible", err)
	}
	if err := p.o.DB.AudiblePushed(ctx, a.ID, x.ASIN, want, now, true); err != nil {
		p.o.Log.Warn("audible: recording a push failed", "account", a.ID, "err", err)
	}
	p.o.Log.Info("audible: place written to Audible", "account", a.ID, "asin", x.ASIN, "position_ms", want)
	p.o.health(ctx, a, nil, time.Time{})
	return false
}

// failed records a failed push. Audible asking to wait (429/503) keeps the
// place and stops the account until its Retry-After; another HTTP answer (a
// title Audible won't take a place for) drops the place, so it isn't tried
// every minute; a network failure keeps it for the next pass.
func (p *Pusher) failed(ctx context.Context, a store.AudibleAccount, x store.AudiblePush, now time.Time, what string, err error) (stop bool) {
	if ctx.Err() != nil {
		return true
	}
	p.o.Log.Warn("audible: "+what+" failed", "account", a.ID, "asin", x.ASIN, "err", audible.RedactedError(err))
	if _, ok := audible.RetryWait(err); ok {
		p.o.health(ctx, a, errors.New(what+": Audible asked the Bridge to wait"), retryAt(err, now, errorWait))
		return true
	}
	if audible.HTTPStatus(err) != 0 {
		_ = p.o.DB.AudiblePushed(ctx, a.ID, x.ASIN, *x.WantMs, now, false)
	}
	p.o.health(ctx, a, errors.New(what+": "+err.Error()), time.Time{})
	return false
}
