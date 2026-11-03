package audiblesync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
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
	if err := e.db.AudibleHeard(context.Background(), store.AudiblePush{Account: acctID, ASIN: "B000000002"}, 1_200_000, false, e.now); err != nil {
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

// Furthest wins both ways: a place Audible moved further on since the Bridge
// last wrote or heard it (listening in the Audible app no sync has brought in
// yet, here during a sync off) isn't overwritten by an older AudioSilo place:
// the waiting place goes and the next sync sends Audible's. A move back made
// on AudioSilo while Audible stayed where the Bridge left it is written.
func TestPushLeavesAudibleFurtherOn(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.add("A/One", "B000000001", 3000, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 || got[0] != "B000000001@3000000" {
		t.Fatalf("sets = %v", got)
	}

	// The person listens on in the Audible app to 5000 s, then plays a little
	// on AudioSilo from the stale 3000 s.
	e.accts.mu.Lock()
	e.accts.places["B000000001"] = audible.Position{PositionMs: 5_000_000, LastUpdated: e.now}
	e.accts.mu.Unlock()
	e.now = e.now.Add(time.Minute)
	e.srv.add("A/One", "B000000001", 3010, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 1 {
		t.Fatalf("Audible's further place was overwritten: %v", got)
	}
	if x, _ := e.db.GetAudiblePush(context.Background(), acctID, "B000000001"); x.WantMs != nil {
		t.Fatalf("the older place still waits: %+v", x)
	}
	res, err := e.runSync(t, params(t, title("B000000001", "A/One", 36000)))
	if err != nil || res.Applied != 1 || e.srv.server["A/One"] != 5000 {
		t.Fatalf("the sync = %+v, %v (server %v)", res, err, e.srv.server["A/One"])
	}

	// Audible is where the sync heard it; a move back on AudioSilo is written.
	e.now = e.now.Add(time.Minute)
	e.srv.add("A/One", "B000000001", 1200, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 2 || got[1] != "B000000001@1200000" {
		t.Fatalf("a move back wasn't written: %v", got)
	}
}

// A place the sync applies (Audible was further on) ends the place waiting
// from the older server row, so the push never moves Audible back to it.
func TestPushAfterAppliedListen(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.server["A/One"] = 3000
	e.srv.add("A/One", "B000000001", 3100, e.now, "web-1") // stopped behind Audible's place
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if x, _ := e.db.GetAudiblePush(context.Background(), acctID, "B000000001"); x.WantMs == nil {
		t.Fatal("no place waiting")
	}
	e.accts.mu.Lock()
	e.accts.places["B000000001"] = audible.Position{PositionMs: 4_000_000, LastUpdated: e.now.Add(-5 * time.Minute)}
	e.accts.mu.Unlock()
	res, err := e.runSync(t, params(t, title("B000000001", "A/One", 36000)))
	if err != nil || res.Applied != 1 {
		t.Fatalf("the sync = %+v, %v", res, err)
	}
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 0 {
		t.Fatalf("the place the sync replaced was written over Audible's: %v", got)
	}
}

// The two directions' markers end each other: once the sync hears Audible
// elsewhere, a move back to the place last written is no debounce; once the
// push writes, a move back to the place last heard is no echo. Both are
// written.
func TestPushMarkersSupersede(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.add("A/One", "B000000001", 3000, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background()) // writes 3000 s
	// Audible plays on to 5000 s, and the sync brings it in.
	e.now = e.now.Add(time.Minute)
	e.accts.mu.Lock()
	e.accts.places["B000000001"] = audible.Position{PositionMs: 5_000_000, LastUpdated: e.now}
	e.accts.mu.Unlock()
	if res, err := e.runSync(t, params(t, title("B000000001", "A/One", 36000))); err != nil || res.Applied != 1 {
		t.Fatalf("the sync = %+v, %v", res, err)
	}
	// Back on AudioSilo to within 5 s of the place written before.
	e.now = e.now.Add(time.Minute)
	e.srv.add("A/One", "B000000001", 3002, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 2 || got[1] != "B000000001@3002000" {
		t.Fatalf("a move back to the old write was taken for the debounce: %v", got)
	}
	// Now the place heard (5000 s) is no longer Audible's: moving to it on
	// AudioSilo is written too.
	e.now = e.now.Add(time.Minute)
	e.srv.add("A/One", "B000000001", 5001, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 3 || got[2] != "B000000001@5001000" {
		t.Fatalf("a move to the old place heard was taken for Audible's own: %v", got)
	}
}

// Audible's answers: a license it denies (no content reference) drops the
// place and isn't asked again every minute (a device denied every license
// leaves the account for a while); a 5xx keeps the place for a later pass (no
// wait for the sync); a cached content reference Audible no longer takes is
// read again and the place written in the same pass; a wait is counted from
// when the 429 came back, after the client's own waits.
func TestPushAudibleAnswers(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)

	e.accts.acrErr = fmt.Errorf("%w: no content reference (acr) returned for B000000001", audible.ErrNoRights)
	e.srv.add("A/One", "B000000001", 600, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	for range 3 {
		p.Tick(ctx)
		e.now = e.now.Add(time.Minute)
	}
	if x, _ := e.db.GetAudiblePush(ctx, acctID, "B000000001"); x.WantMs != nil || e.accts.acrs != 1 {
		t.Fatalf("a denied license: %+v (%d license requests)", x, e.accts.acrs)
	}
	e.accts.acrErr = fmt.Errorf("%w: no content reference (acr) returned for B000000001", audible.ErrThrottled)
	e.srv.add("A/One", "B000000001", 700, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(ctx)
	asks := len(e.srv.feedAsks)
	e.now = e.now.Add(time.Minute)
	p.Tick(ctx)
	if a := e.account(t); len(e.srv.feedAsks) != asks || a.RefusedAt != "" || e.accts.acrs != 2 {
		t.Fatalf("a throttled device: asked %d -> %d, %+v (%d license requests)", asks, len(e.srv.feedAsks), a, e.accts.acrs)
	}
	e.accts.acrErr = nil
	e.now = e.now.Add(refusedWait)

	// A 502: kept, left for errorWait (the sync isn't held), then written.
	e.accts.setErr = audibleAnswer(t, http.StatusBadGateway, "")
	e.srv.add("A/One", "B000000001", 800, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(ctx)
	if x, _ := e.db.GetAudiblePush(ctx, acctID, "B000000001"); x.WantMs == nil {
		t.Fatal("a 502 dropped the place")
	}
	if a := e.account(t); a.SyncError == "" || a.SyncRetryAt != "" {
		t.Fatalf("after a 502: %+v", a)
	}
	e.accts.setErr = nil
	e.now = e.now.Add(errorWait)
	p.Tick(ctx)
	if got := e.sets(); len(got) != 1 || got[0] != "B000000001@800000" {
		t.Fatalf("sets = %v", got)
	}

	// A stale cached content reference: read again, written in this pass.
	if err := e.db.SetAudibleACR(ctx, acctID, "B000000001", "CR!OLD"); err != nil {
		t.Fatal(err)
	}
	e.accts.wrongACR = audibleAnswer(t, http.StatusNotFound, "")
	acrs := e.accts.acrs
	e.srv.add("A/One", "B000000001", 900, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(ctx)
	if got := e.sets(); len(got) != 2 || got[1] != "B000000001@900000" || e.accts.acrs != acrs+1 {
		t.Fatalf("a stale content reference: sets %v (%d license requests)", got, e.accts.acrs-acrs)
	}

	// The 429 comes back 3 minutes into the call (the client's own waits):
	// the wait runs from then.
	e.accts.setErr = audibleAnswer(t, http.StatusTooManyRequests, "600")
	e.accts.onSet = func() { e.now = e.now.Add(3 * time.Minute) }
	e.srv.add("A/One", "B000000001", 1000, e.now, "web-1")
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(ctx)
	if a := e.account(t); a.SyncRetryAt != store.Timestamp(e.now.Add(10*time.Minute)) {
		t.Fatalf("wait until %q, want %q", a.SyncRetryAt, store.Timestamp(e.now.Add(10*time.Minute)))
	}
}

// A feed row on a title the last sync didn't list for that book (a new match,
// a re-match) is kept pending, never skipped past: the next sync lists it and
// the place is written.
func TestPushPendingTitle(t *testing.T) {
	e := newEnv(t)
	e.list(t, st("B000000001", "A/One", 36000))
	p := NewPusher(e.o)
	e.started(t, p)
	e.srv.add("A/Two", "B000000002", 900, e.now, "web-1")          // a title matched since the last sync
	e.srv.add("A/One (right)", "B000000001", 1500, e.now, "web-1") // a title matched again
	e.now = e.now.Add(4 * time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 0 {
		t.Fatalf("a title not listed yet was written: %v", got)
	}
	e.list(t, st("B000000001", "A/One (right)", 36000), st("B000000002", "A/Two", 36000))
	e.now = e.now.Add(time.Minute)
	p.Tick(context.Background())
	if got := e.sets(); len(got) != 2 || got[0] != "B000000001@1500000" || got[1] != "B000000002@900000" {
		t.Fatalf("sets = %v", got)
	}
}

// The server asking the push to wait (its own 429/503) pauses the push only:
// it is no wait for Audible, so the sync isn't held.
func TestPushServerWait(t *testing.T) {
	e := newEnv(t)
	p := NewPusher(e.o)
	e.srv.feedErr = &link.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", RetryAfter: 10 * time.Minute}
	p.Tick(context.Background())
	if a := e.account(t); a.SyncRetryAt != "" || a.SyncError == "" {
		t.Fatalf("after the server's 503: %+v", a)
	}
	e.now = e.now.Add(5 * time.Minute)
	p.Tick(context.Background())
	if len(e.srv.feedAsks) != 1 {
		t.Fatalf("the server's wait wasn't kept: %v", e.srv.feedAsks)
	}
}
