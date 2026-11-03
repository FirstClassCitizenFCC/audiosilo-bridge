package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The push rows: a sync lists titles (an unlisted title loses its waiting
// place), a want waits on a listed row only, a push records its place, and
// the cached content reference and the place heard stay through a listing.
func TestAudiblePushRows(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", ServerID: "srv-1", UserID: 3, LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	if err := db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{
		{ASIN: "B000000001", LibraryID: 1, Path: "A/One", RuntimeS: 3600},
		{ASIN: "B000000002", LibraryID: 1, Path: "A/Two", RuntimeS: 7200},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAudiblePush(ctx, a.ID, "B000000009"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown asin = %v", err)
	}
	if err := db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 60_000, Seen: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.WantAudiblePush(ctx, a.ID, "B000000002", AudibleRow{Version: 1, Ms: 90_000, Seen: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAudibleACR(ctx, a.ID, "B000000001", "CR!ACR1"); err != nil {
		t.Fatal(err)
	}
	if err := db.AudibleHeard(ctx, AudiblePush{Account: a.ID, ASIN: "B000000001", RowMs: 60_000, RowSeen: Timestamp(at)}, 30_000, false, at); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetAudiblePush(ctx, a.ID, "B000000001")
	if err != nil || !p.Listed || p.WantMs == nil || *p.WantMs != 60_000 || p.RowMs != 60_000 || p.RowVersion != 1 || p.RowSeen != Timestamp(at) || p.LastMs != -1 ||
		p.ACR != "CR!ACR1" || p.HeardMs != 30_000 || p.RuntimeS != 3600 || p.Path != "A/One" {
		t.Fatalf("push = %+v, %v", p, err)
	}
	if w, err := db.AudibleWaiting(ctx, a.ID); err != nil || len(w) != 2 {
		t.Fatalf("waiting = %+v, %v", w, err)
	}
	// Dropped: the waiting place goes without a write.
	if err := db.DropAudiblePush(ctx, a.ID, "B000000002"); err != nil {
		t.Fatal(err)
	}
	if w, _ := db.AudibleWaiting(ctx, a.ID); len(w) != 1 || w[0].ASIN != "B000000001" {
		t.Fatalf("waiting after a drop = %+v", w)
	}
	if two, _ := db.GetAudiblePush(ctx, a.ID, "B000000002"); two.WantMs != nil || two.LastMs != -1 {
		t.Fatalf("a drop wrote a push: %+v", two)
	}
	_ = db.WantAudiblePush(ctx, a.ID, "B000000002", AudibleRow{Version: 1, Ms: 90_000, Seen: at})
	if err := db.AudiblePushed(ctx, a.ID, "B000000001", 60_000, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.LastMs != 60_000 || p.At != Timestamp(at.Add(time.Minute)) ||
		p.HeardMs != -1 {
		t.Fatalf("after the push = %+v (the place heard before it is no longer Audible's)", p)
	}

	// The next sync lists only the first (the second was marked Don't sync):
	// the second is unlisted and its waiting place forgotten; a want on it is
	// refused from now on.
	if err := db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: 1, Path: "A/One", RuntimeS: 3600}}); err != nil {
		t.Fatal(err)
	}
	two, _ := db.GetAudiblePush(ctx, a.ID, "B000000002")
	if two.Listed || two.WantMs != nil {
		t.Fatalf("an unlisted title kept its place: %+v", two)
	}
	if err := db.WantAudiblePush(ctx, a.ID, "B000000002", AudibleRow{Version: 1, Ms: 1, Seen: at}); err != nil {
		t.Fatal(err)
	}
	if two, _ = db.GetAudiblePush(ctx, a.ID, "B000000002"); two.WantMs != nil {
		t.Fatalf("an unlisted title took a want: %+v", two)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.ACR != "CR!ACR1" || p.LastMs != 60_000 {
		t.Fatalf("a listing forgot the push state: %+v", p)
	}
	// A title whose book moved forgets its waiting place.
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 5, Seen: at})
	_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: 1, Path: "A/One (moved)", RuntimeS: 3600}})
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.Path != "A/One (moved)" {
		t.Fatalf("a moved book kept its waiting place: %+v", p)
	}
	// Seen: the waiting place goes, the row stays.
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 5, Seen: at})
	if err := db.SeenAudibleRow(ctx, a.ID, "B000000001", AudibleRow{Version: 4, Ms: 7, Duration: 3600, Seen: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.RowVersion != 4 || p.RowMs != 7 || p.RowDuration != 3600 ||
		p.RowSeen != Timestamp(at.Add(time.Hour)) {
		t.Fatalf("seen = %+v", p)
	}
	// Touched: the row read again at its place with another version keeps
	// the waiting place and when the place was first read.
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 5, Ms: 9, Seen: at})
	if err := db.TouchAudibleRow(ctx, a.ID, "B000000001", 6, 3700); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs == nil || *p.WantMs != 9 || p.RowVersion != 6 || p.RowMs != 9 ||
		p.RowDuration != 3700 || p.RowSeen != Timestamp(at) {
		t.Fatalf("touched = %+v", p)
	}
	list, err := db.AudiblePushes(ctx, a.ID)
	if err != nil || len(list) != 2 || list[0].ASIN != "B000000001" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// Removing the account removes its rows.
	if err := db.DeleteAudibleAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.AudiblePushes(ctx, a.ID); len(list) != 0 {
		t.Fatalf("rows outlived the account: %+v", list)
	}
}

// The two directions' markers: a place heard from Audible ends the place last
// written (unless the push wrote again since the job started), a place the
// server applied drops the place waiting from an older server row (unless the
// push read a newer row since), and a place pending on a title not listed yet
// is kept by the listing that lists it for its book, else forgotten.
func TestAudiblePushMarkers(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", ServerID: "srv-1", UserID: 3, LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	one := AudibleSyncTitle{ASIN: "B000000001", LibraryID: 1, Path: "A/One", RuntimeS: 3600}
	if err := db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{one}); err != nil {
		t.Fatal(err)
	}
	get := func() AudiblePush {
		t.Helper()
		p, err := db.GetAudiblePush(ctx, a.ID, "B000000001")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// The push wrote 60 s an hour ago; a job starting now hears 90 s: the
	// write is no longer Audible's place. Not applied: the waiting place stays.
	_ = db.AudiblePushed(ctx, a.ID, "B000000001", 60_000, at.Add(-time.Hour))
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 70_000, Seen: at})
	snap := get()
	if err := db.AudibleHeard(ctx, snap, 90_000, false, at); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.HeardMs != 90_000 || p.LastMs != -1 || p.WantMs == nil {
		t.Fatalf("heard = %+v", p)
	}
	// Applied: the waiting place goes.
	if err := db.AudibleHeard(ctx, snap, 95_000, true, at); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.WantMs != nil || p.HeardMs != 95_000 {
		t.Fatalf("applied = %+v", p)
	}
	// The push wrote again and read a newer row while the job ran: both stay.
	snap = get()
	_ = db.AudiblePushed(ctx, a.ID, "B000000001", 100_000, at.Add(time.Minute))
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 110_000, Seen: at.Add(time.Minute)})
	if err := db.AudibleHeard(ctx, snap, 120_000, true, at); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.LastMs != 100_000 || p.WantMs == nil || *p.WantMs != 110_000 || p.HeardMs != 120_000 {
		t.Fatalf("a push during the job was undone: %+v", p)
	}

	// Pending: a place on a title the feed syncs but the last job didn't list.
	row := AudibleRow{Version: 1, Ms: 5_000, Seen: at}
	if err := db.PendAudiblePush(ctx, a.ID, "B000000002", 1, "A/Two", true, row); err != nil {
		t.Fatal(err)
	}
	if w, _ := db.AudibleWaiting(ctx, a.ID); len(w) != 1 || w[0].ASIN != "B000000001" {
		t.Fatalf("a pending place is waiting to be pushed: %+v", w)
	}
	// A re-match: the listed title's book changes on the server first.
	if err := db.PendAudiblePush(ctx, a.ID, "B000000001", 1, "A/One (right)", true,
		AudibleRow{Version: 2, Ms: 5_000, Seen: at.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.Listed || p.Path != "A/One (right)" || p.WantMs == nil || p.RowSeen != Timestamp(at.Add(2*time.Minute)) ||
		p.LastMs != 100_000 {
		t.Fatalf("re-matched = %+v", p)
	}
	// The next job lists both for those books: kept, and now waiting.
	if err := db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: 1, Path: "A/One (right)", RuntimeS: 3600},
		{ASIN: "B000000002", LibraryID: 1, Path: "A/Two", RuntimeS: 7200}}); err != nil {
		t.Fatal(err)
	}
	if w, _ := db.AudibleWaiting(ctx, a.ID); len(w) != 2 || w[1].RuntimeS != 7200 {
		t.Fatalf("listed pending places = %+v", w)
	}
	// A pending place the next job doesn't list for its book goes.
	_ = db.PendAudiblePush(ctx, a.ID, "B000000003", 1, "A/Three", true, row)
	// History pending: the row only, no waiting place.
	if err := db.PendAudiblePush(ctx, a.ID, "B000000004", 1, "A/Four", false, row); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.GetAudiblePush(ctx, a.ID, "B000000004"); p.WantMs != nil || p.RowMs != 5_000 || p.Listed {
		t.Fatalf("history pending = %+v", p)
	}
	_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000003", LibraryID: 1, Path: "A/Other", RuntimeS: 60}})
	if p, _ := db.GetAudiblePush(ctx, a.ID, "B000000003"); p.WantMs != nil || !p.Listed {
		t.Fatalf("a pending place on another book was kept: %+v", p)
	}
}

// The feed's cursor and its start: kept through a mapping that changes only
// the label or policy, started again (with the waiting places and listing
// forgotten) when the account moves to another server, person or library.
func TestAudiblePushCursorRemap(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", ServerID: "srv-1", UserID: 3, LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: 1, Path: "A/One"}})
	set := func() {
		t.Helper()
		if err := db.SetAudiblePushCursor(ctx, a.ID, "c42", at); err != nil {
			t.Fatal(err)
		}
		_ = db.WantAudiblePush(ctx, a.ID, "B000000001", AudibleRow{Version: 1, Ms: 5, Seen: at})
	}
	// A baseline still under way (no from) leaves the start unset.
	if err := db.SetAudiblePushCursor(ctx, a.ID, "c41", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetAudibleAccount(ctx, a.ID); got.PushCursor != "c41" || got.PushFrom != "" {
		t.Fatalf("mid-baseline = %q from %q", got.PushCursor, got.PushFrom)
	}
	set()
	// The start is kept once set.
	if err := db.SetAudiblePushCursor(ctx, a.ID, "c43", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetAudibleAccount(ctx, a.ID)
	if got.PushCursor != "c43" || got.PushFrom != Timestamp(at) {
		t.Fatalf("cursor = %q from %q", got.PushCursor, got.PushFrom)
	}
	a.Label, a.Policy = "Chris again", "all"
	if err := db.MapAudibleAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := db.SignInAudibleAccount(ctx, a, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAudibleAccount(ctx, a.ID)
	p, _ := db.GetAudiblePush(ctx, a.ID, "B000000001")
	if got.PushCursor != "c43" || p.WantMs == nil || !p.Listed {
		t.Fatalf("a mapping in place started the feed again: %+v, %+v", got, p)
	}
	for name, move := range map[string]func(*AudibleAccount){
		"server":  func(a *AudibleAccount) { a.ServerID = "srv-2" },
		"user":    func(a *AudibleAccount) { a.UserID = 4 },
		"library": func(a *AudibleAccount) { a.LibraryID = 2 },
	} {
		_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: a.LibraryID, Path: "A/One"}})
		set()
		move(&a)
		if err := db.MapAudibleAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, _ = db.GetAudibleAccount(ctx, a.ID)
		p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001")
		if got.PushCursor != "" || got.PushFrom != "" || p.WantMs != nil || p.Listed || p.RowMs != -1 || p.RowSeen != "" {
			t.Fatalf("%s: a move kept the feed: %+v, %+v", name, got, p)
		}
	}
	// A sign-in again that maps it elsewhere starts it again too.
	_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: a.LibraryID, Path: "A/One"}})
	set()
	a.UserID = 9
	if err := db.SignInAudibleAccount(ctx, a, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.PushCursor != "" {
		t.Fatalf("a sign-in mapping it elsewhere kept the feed: %+v", got)
	}
}

// The sync's health: a failure records why and the wait, a success clears both;
// neither touches a refusal.
func TestAudibleSyncHealth(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	if err := db.AudibleSyncFailed(ctx, a.ID, "Audible asked to wait", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetAudibleAccount(ctx, a.ID)
	if got.SyncError != "Audible asked to wait" || got.SyncRetryAt != Timestamp(at.Add(time.Hour)) || got.RefusedAt != "" {
		t.Fatalf("failed = %+v", got)
	}
	// The job and the push share the wait: a failure with no wait of its
	// own, a shorter one or a success before it has passed never ends it.
	if err := db.AudibleSyncFailed(ctx, a.ID, "no wait", time.Time{}); err != nil {
		t.Fatal(err)
	}
	_ = db.AudibleSyncFailed(ctx, a.ID, "a shorter wait", at.Add(time.Minute))
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncRetryAt != Timestamp(at.Add(time.Hour)) || got.SyncError != "a shorter wait" {
		t.Fatalf("a later failure shortened the wait: %+v", got)
	}
	if err := db.AudibleSyncOK(ctx, a.ID, at.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncError != "" || got.SyncRetryAt != Timestamp(at.Add(time.Hour)) {
		t.Fatalf("a success ended a wait still ahead: %+v", got)
	}
	_ = db.AudibleSyncFailed(ctx, a.ID, "a longer wait", at.Add(90*time.Minute))
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncRetryAt != Timestamp(at.Add(90*time.Minute)) {
		t.Fatalf("a longer wait was not kept: %+v", got)
	}
	if err := db.AudibleSyncOK(ctx, a.ID, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncError != "" || got.SyncRetryAt != "" || got.SyncAt != Timestamp(at.Add(2*time.Hour)) {
		t.Fatalf("ok = %+v", got)
	}
}
