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
	if err := db.WantAudiblePush(ctx, a.ID, "B000000001", 60_000, at); err != nil {
		t.Fatal(err)
	}
	if err := db.WantAudiblePush(ctx, a.ID, "B000000002", 90_000, at); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAudibleACR(ctx, a.ID, "B000000001", "CR!ACR1"); err != nil {
		t.Fatal(err)
	}
	if err := db.AudibleHeard(ctx, a.ID, "B000000001", 30_000); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetAudiblePush(ctx, a.ID, "B000000001")
	if err != nil || !p.Listed || p.WantMs == nil || *p.WantMs != 60_000 || p.RowUpdated != Timestamp(at) || p.LastMs != -1 ||
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
	_ = db.WantAudiblePush(ctx, a.ID, "B000000002", 90_000, at)
	if err := db.AudiblePushed(ctx, a.ID, "B000000001", 60_000, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.LastMs != 60_000 || p.At != Timestamp(at.Add(time.Minute)) {
		t.Fatalf("after the push = %+v", p)
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
	if err := db.WantAudiblePush(ctx, a.ID, "B000000002", 1, at); err != nil {
		t.Fatal(err)
	}
	if two, _ = db.GetAudiblePush(ctx, a.ID, "B000000002"); two.WantMs != nil {
		t.Fatalf("an unlisted title took a want: %+v", two)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.ACR != "CR!ACR1" || p.LastMs != 60_000 || p.HeardMs != 30_000 {
		t.Fatalf("a listing forgot the push state: %+v", p)
	}
	// A title whose book moved forgets its waiting place.
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", 5, at)
	_ = db.ListAudibleSync(ctx, a.ID, []AudibleSyncTitle{{ASIN: "B000000001", LibraryID: 1, Path: "A/One (moved)", RuntimeS: 3600}})
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.Path != "A/One (moved)" {
		t.Fatalf("a moved book kept its waiting place: %+v", p)
	}
	// Seen: the waiting place goes, the row's time stays.
	_ = db.WantAudiblePush(ctx, a.ID, "B000000001", 5, at)
	if err := db.SeenAudibleRow(ctx, a.ID, "B000000001", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetAudiblePush(ctx, a.ID, "B000000001"); p.WantMs != nil || p.RowUpdated != Timestamp(at.Add(time.Hour)) {
		t.Fatalf("seen = %+v", p)
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
		_ = db.WantAudiblePush(ctx, a.ID, "B000000001", 5, at)
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
		if got.PushCursor != "" || got.PushFrom != "" || p.WantMs != nil || p.Listed {
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
	if err := db.AudibleSyncFailed(ctx, a.ID, "no wait", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncRetryAt != "" {
		t.Fatalf("a failure with no wait kept the old one: %+v", got)
	}
	if err := db.AudibleSyncOK(ctx, a.ID, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.GetAudibleAccount(ctx, a.ID); got.SyncError != "" || got.SyncRetryAt != "" || got.SyncAt != Timestamp(at.Add(2*time.Hour)) {
		t.Fatalf("ok = %+v", got)
	}
}
