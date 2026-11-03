package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAudibleAccounts(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	if _, err := db.GetAudibleAccount(ctx, "abcd1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := db.AudibleAccountByCustomer(ctx, "", "us"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty hash matches nothing: %v", err)
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris (US)", Marketplace: "us", CustomerHash: "h1", ServerID: "srv-1",
		UserID: 3, UserName: "Chris", LibraryID: 1, Policy: "manual", Sync: true}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	if err := db.AudiblePolled(ctx, "abcd1234", 30, 12, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.RefuseAudibleAccount(ctx, "abcd1234", "Audible refused", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAudibleAccount(ctx, "abcd1234")
	if err != nil || got.RefusedAt != Timestamp(at.Add(2*time.Hour)) || got.LastError != "Audible refused" ||
		got.LastTitles != 30 || got.LastMatched != 12 || got.LastPollAt != Timestamp(at.Add(time.Hour)) || !got.Sync {
		t.Fatalf("account = %+v, %v", got, err)
	}
	if by, err := db.AudibleAccountByCustomer(ctx, "h1", "us"); err != nil || by.ID != "abcd1234" {
		t.Fatalf("by customer = %+v, %v", by, err)
	}
	if _, err := db.AudibleAccountByCustomer(ctx, "h1", "uk"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another marketplace = %v", err)
	}

	// A sign-in again forgets the refusal, keeps created_at and the counts.
	a.Label = "Chris"
	if err := db.SignInAudibleAccount(ctx, a, at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAudibleAccount(ctx, "abcd1234")
	if got.RefusedAt != "" || got.LastError != "" || got.CreatedAt != Timestamp(at) || got.LastTitles != 30 ||
		got.Label != "Chris" || got.LastOKAt != Timestamp(at.Add(3*time.Hour)) {
		t.Fatalf("after a sign-in again = %+v", got)
	}

	a.LibraryID, a.Policy, a.Sync, a.ServerID = 2, "all", false, "srv-2"
	if err := db.MapAudibleAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := db.MapAudibleAccount(ctx, AudibleAccount{ID: "nope0000"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mapping an unknown account = %v", err)
	}
	if err := db.AudibleAccountError(ctx, "abcd1234", "the server could not be reached"); err != nil {
		t.Fatal(err)
	}
	list, err := db.AudibleAccounts(ctx)
	if err != nil || len(list) != 1 || list[0].LibraryID != 2 || list[0].Policy != "all" || list[0].Sync ||
		list[0].ServerID != "srv-2" || list[0].LastError != "the server could not be reached" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := db.DeleteAudibleAccount(ctx, "abcd1234"); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.AudibleAccounts(ctx); len(list) != 0 {
		t.Fatalf("after delete = %+v", list)
	}
}

// One account per Amazon account and marketplace: a second row for the same
// pair is refused, another marketplace or an empty customer hash is not.
func TestAudibleAccountsUniquePerCustomer(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", CustomerHash: "h1", ServerID: "srv-1",
		UserID: 3, UserName: "Chris", LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	b := a
	b.ID = "efgh5678"
	if err := db.SignInAudibleAccount(ctx, b, at); err == nil {
		t.Fatal("a second account for one Amazon account and marketplace was stored")
	}
	b.Marketplace = "uk"
	if err := db.SignInAudibleAccount(ctx, b, at); err != nil {
		t.Fatalf("another marketplace: %v", err)
	}
	for _, id := range []string{"none0001", "none0002"} {
		c := a
		c.ID, c.CustomerHash = id, ""
		if err := db.SignInAudibleAccount(ctx, c, at); err != nil {
			t.Fatalf("an unknown customer: %v", err)
		}
	}
}
