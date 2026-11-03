package audiblesync

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// list records the titles a sync listed (as a job would).
func (e *env) list(t *testing.T, titles ...store.AudibleSyncTitle) {
	t.Helper()
	if err := e.db.ListAudibleSync(context.Background(), acctID, titles); err != nil {
		t.Fatal(err)
	}
}

func st(asin, path string, runtimeS int64) store.AudibleSyncTitle {
	return store.AudibleSyncTitle{ASIN: asin, LibraryID: 1, Path: path, RuntimeS: runtimeS}
}

// started runs the first pass (the baseline: the feed's start), so later rows
// are new.
func (e *env) started(t *testing.T, p *Pusher) {
	t.Helper()
	p.Tick(context.Background())
	if a := e.account(t); a.PushFrom == "" {
		t.Fatalf("the first pass set no start: %+v", a)
	}
}

func (e *env) sets() []string {
	e.accts.mu.Lock()
	defer e.accts.mu.Unlock()
	return append([]string(nil), e.accts.sets...)
}

// A place is written to Audible once its row has been still for 3 minutes:
// not while the listener is still moving it, once after, and not again while
// it stays within 5 seconds of the place written (the debounce); a later move
// past 5 seconds is written again. The content reference is read once.
func TestPushDebounce(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)

	e.srv.add("A/One", "B000000001", 600, e.now, "web-1")
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if len(e.sets()) != 0 {
		t.Fatalf("a place a minute old was written: %v", e.sets())
	}
	// Still moving: the newest row restarts the wait.
	e.srv.add("A/One", "B000000001", 660, e.now, "web-1")
	e.now = e.now.Add(2 * time.Minute)
	p.Tick(context.Background())
	if len(e.sets()) != 0 {
		t.Fatalf("a place still moving was written: %v", e.sets())
	}
	e.now = e.now.Add(time.Minute) // 3 minutes still
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 || got[0] != "B000000001@660000" {
		t.Fatalf("sets = %v", got)
	}
	// Nothing new: nothing more.
	e.now = e.now.Add(5 * time.Minute)
	p.Tick(context.Background())
	// A row within 5 seconds of the place written isn't written again.
	e.srv.add("A/One", "B000000001", 664, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 {
		t.Fatalf("a place within 5 s of the last one was written again: %v", got)
	}
	// One further on is.
	e.srv.add("A/One", "B000000001", 900, e.now, "web-1")
	e.now = e.now.Add(3 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 2 || got[1] != "B000000001@900000" || e.accts.acrs != 1 {
		t.Fatalf("sets = %v (acr reads %d)", got, e.accts.acrs)
	}
	if a := e.account(t); a.SyncAt == "" || a.SyncError != "" || a.PushCursor == "" {
		t.Fatalf("account = %+v", a)
	}
}

// Echo suppression: a row Audible's own sync wrote (device audible:<account>;
// the server leaves them out, the Bridge guards too) and a row at the place
// the last sync heard from Audible (a player that moved to Audible's place)
// are never written back.
func TestPushEchoSuppression(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000), st("B000000002", "A/Two", 36000))
	if err := e.db.AudibleHeard(context.Background(), acctID, "B000000002", 1_200_000); err != nil {
		t.Fatal(err)
	}
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.add("A/One", "B000000001", 700, e.now, DeviceID(acctID))
	e.srv.add("A/Two", "B000000002", 1202, e.now, "web-1") // within 5 s of the place heard
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 0 {
		t.Fatalf("an echo was written back: %v", got)
	}
	// A real move on the second title is written.
	e.srv.add("A/Two", "B000000002", 1500, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 || got[0] != "B000000002@1500000" {
		t.Fatalf("sets = %v", got)
	}
}

// Denied: a place past Audible's runtime (the match looks wrong) is never
// written to the real account; a title the last sync didn't list (Don't sync
// this book, Not this book), or a row on another book than the title's match,
// neither.
func TestPushGuards(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 3600), st("B000000002", "A/Two", 36000))
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.add("A/One", "B000000001", 3600*1.05+121, e.now, "web-1") // past an hour's runtime
	e.srv.add("A/Other", "B000000002", 900, e.now, "web-1")         // not the title's book
	e.srv.add("A/Three", "B000000003", 900, e.now, "web-1")         // never listed
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 0 {
		t.Fatalf("a guarded place was written: %v", got)
	}
	// Allowed: within the runtime's slack it is written.
	e.srv.add("A/One", "B000000001", 3600*1.05+60, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 {
		t.Fatalf("sets = %v", got)
	}
	// Don't sync this book: the next sync lists it no longer; a place waiting
	// is dropped, and a new one isn't taken.
	e.srv.add("A/Two", "B000000002", 900, e.now, "web-1")
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	e.list(t, st("B000000001", "A/One", 3600))
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 {
		t.Fatalf("an unlisted title was written: %v", got)
	}
}

// The first pass reads the whole feed but writes no history: only places
// changed in the last baselineWindow wait to be written. The cursor is kept,
// so the next pass reads from it.
func TestPushBaselineSkipsHistory(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000), st("B000000002", "A/Two", 36000))
	e.srv.pageSize = 1 // a page a row: the pass follows more
	e.srv.add("A/One", "B000000001", 600, e.now.Add(-24*time.Hour), "web-1")
	e.srv.add("A/Two", "B000000002", 900, e.now.Add(-2*time.Minute), "web-1")
	p := NewPusher(e.o)
	p.Tick(context.Background())
	e.now = e.now.Add(2 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 || got[0] != "B000000002@900000" {
		t.Fatalf("sets = %v", got)
	}
	a := e.account(t)
	if a.PushCursor != "c2" {
		t.Fatalf("cursor = %q", a.PushCursor)
	}
	if last := e.srv.feedAsks[len(e.srv.feedAsks)-1]; last != "c2" {
		t.Fatalf("the second pass asked from %q", last)
	}
}

// Denied: a refused account (the "Sign in again" state) neither reads the feed
// nor writes; an account the server answers sync_off for is left for a while.
// A failure writing to Audible that asks to wait keeps the place and records
// the wait, never a refusal.
func TestPushRefusedOffAndBackoff(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	if err := e.db.RefuseAudibleAccount(context.Background(), acctID, store.RefusalDevice, "refused", e.now); err != nil {
		t.Fatal(err)
	}
	p.Tick(context.Background())
	if len(e.srv.feedAsks) != 0 {
		t.Fatalf("a refused account read the feed: %v", e.srv.feedAsks)
	}
	if err := e.db.SignInAudibleAccount(context.Background(), e.account(t), e.now); err != nil {
		t.Fatal(err)
	}

	e.srv.feedErr = link.ErrSyncOff
	p.Tick(context.Background())
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if len(e.srv.feedAsks) != 1 {
		t.Fatalf("a sync-off account was asked again at once: %v", e.srv.feedAsks)
	}
	if a := e.account(t); a.SyncError != "" {
		t.Fatalf("sync off is no error: %+v", a)
	}
	e.srv.feedErr = nil
	e.now = e.now.Add(offWait)
	e.started(t, p)

	e.srv.add("A/One", "B000000001", 600, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	e.accts.setErr = audibleAnswer(t, http.StatusServiceUnavailable, "900")
	p.Tick(context.Background())
	a := e.account(t)
	if a.RefusedAt != "" || !strings.Contains(a.SyncError, "wait") || a.SyncRetryAt != store.Timestamp(e.now.Add(15*time.Minute)) {
		t.Fatalf("after a 503: %+v", a)
	}
	if x, _ := e.db.GetAudiblePush(context.Background(), acctID, "B000000001"); x.WantMs == nil {
		t.Fatal("the place was dropped on a wait")
	}
	// While it waits nothing is asked; after, the place is written.
	e.accts.setErr = nil
	asks := len(e.srv.feedAsks)
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if len(e.srv.feedAsks) != asks {
		t.Fatal("a waiting account read the feed")
	}
	e.now = e.now.Add(15 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 {
		t.Fatalf("sets = %v", got)
	}
	if a := e.account(t); a.SyncError != "" || a.SyncRetryAt != "" {
		t.Fatalf("after the write: %+v", a)
	}

	// Another HTTP refusal drops the place (not tried every minute) and
	// records why; it never refuses the account.
	e.srv.add("A/One", "B000000001", 2000, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	e.accts.setErr = audibleAnswer(t, http.StatusForbidden, "")
	p.Tick(context.Background())
	x, _ := e.db.GetAudiblePush(context.Background(), acctID, "B000000001")
	if a := e.account(t); a.RefusedAt != "" || a.SyncError == "" || x.WantMs != nil {
		t.Fatalf("after a 403: %+v, %+v", a, x)
	}
}

// A person the server can't sync for pauses the account and records why.
func TestPushUserUnavailable(t *testing.T) {
	e := newEnv(t)
	e.srv.feedErr = link.ErrUserUnavailable
	p := NewPusher(e.o)
	p.Tick(context.Background())
	if a := e.account(t); !errors.Is(e.srv.feedErr, link.ErrUserUnavailable) || a.SyncError == "" || e.changed != 1 {
		t.Fatalf("account = %+v (changed %d)", a, e.changed)
	}
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if len(e.srv.feedAsks) != 1 {
		t.Fatalf("asked again at once: %v", e.srv.feedAsks)
	}
}

// Nothing runs while unpaired.
func TestPushUnpaired(t *testing.T) {
	e := newEnv(t)
	e.o.Server = func() Server { return nil }
	NewPusher(e.o).Tick(context.Background())
	if len(e.srv.feedAsks) != 0 {
		t.Fatal("an unpaired Bridge read the feed")
	}
}
