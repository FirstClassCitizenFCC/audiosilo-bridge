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
	if err := db.AudiblePolled(ctx, "abcd1234", 30, 12, at.Add(time.Hour), "kept"); err != nil {
		t.Fatal(err)
	}
	if err := db.RefuseAudibleAccount(ctx, "abcd1234", "Audible refused", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAudibleAccount(ctx, "abcd1234")
	if err != nil || got.RefusedAt != Timestamp(at.Add(2*time.Hour)) || got.LastError != "Audible refused" ||
		got.LastTitles != 30 || got.LastMatched != 12 || got.LastPollAt != Timestamp(at.Add(time.Hour)) || !got.Sync ||
		got.DeviceProfile != "" {
		t.Fatalf("account = %+v, %v", got, err)
	}
	if by, err := db.AudibleAccountByCustomer(ctx, "h1", "us"); err != nil || by.ID != "abcd1234" {
		t.Fatalf("by customer = %+v, %v", by, err)
	}
	if _, err := db.AudibleAccountByCustomer(ctx, "h1", "uk"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another marketplace = %v", err)
	}

	// A sign-in again forgets the refusal, keeps created_at and the counts,
	// and records its device's profile.
	a.Label, a.DeviceProfile = "Chris", "4.60/26.2/46000856"
	if err := db.SignInAudibleAccount(ctx, a, at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAudibleAccount(ctx, "abcd1234")
	if got.RefusedAt != "" || got.LastError != "" || got.CreatedAt != Timestamp(at) || got.LastTitles != 30 ||
		got.Label != "Chris" || got.LastOKAt != Timestamp(at.Add(3*time.Hour)) || got.DeviceProfile != "4.60/26.2/46000856" {
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
		list[0].ServerID != "srv-2" || list[0].LastError != "the server could not be reached" ||
		list[0].DeviceProfile != "4.60/26.2/46000856" {
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

// A poll ends a refusal unless its reason is keep (a device refusal a working
// library list doesn't end), and a poll's error never replaces a refusal's
// reason; a sign-in ends either.
func TestAudiblePolledKeepsADeviceRefusal(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := AudibleAccount{ID: "abcd1234", Label: "Chris", Marketplace: "us", CustomerHash: "h1", LibraryID: 1, Policy: "manual"}
	if err := db.SignInAudibleAccount(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	get := func() AudibleAccount {
		t.Helper()
		got, err := db.GetAudibleAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// A refusal for another reason ends with a poll.
	if err := db.RefuseAudibleAccount(ctx, a.ID, "sign-in refused", at); err != nil {
		t.Fatal(err)
	}
	if err := db.AudiblePolled(ctx, a.ID, 3, 1, at, "throttled"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.RefusedAt != "" || got.LastError != "" {
		t.Fatalf("a sign-in refusal outlived a poll: %+v", got)
	}
	// An error with no refusal is recorded.
	if err := db.AudibleAccountError(ctx, a.ID, "server down"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.LastError != "server down" {
		t.Fatalf("error = %+v", got)
	}

	// The kept reason stays through an error and a poll.
	if err := db.RefuseAudibleAccount(ctx, a.ID, "throttled", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.AudibleAccountError(ctx, a.ID, "server down"); err != nil {
		t.Fatal(err)
	}
	if err := db.AudiblePolled(ctx, a.ID, 4, 2, at.Add(2*time.Hour), "throttled"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.RefusedAt != Timestamp(at.Add(time.Hour)) || got.LastError != "throttled" || got.LastTitles != 4 {
		t.Fatalf("the device refusal didn't stay: %+v", got)
	}
	// Another reason's acceptance leaves it; its own ends it.
	if ended, err := db.AudibleAccepted(ctx, a.ID, "sign-in refused"); err != nil || ended {
		t.Fatalf("accepted another reason = %v, %v", ended, err)
	}
	if got := get(); got.LastError != "throttled" {
		t.Fatalf("the device refusal ended: %+v", got)
	}
	if err := db.RefuseAudibleAccount(ctx, a.ID, "sign-in refused", at); err != nil {
		t.Fatal(err)
	}
	if ended, err := db.AudibleAccepted(ctx, a.ID, "sign-in refused"); err != nil || !ended {
		t.Fatalf("accepted = %v, %v", ended, err)
	}
	if got := get(); got.RefusedAt != "" || got.LastError != "" {
		t.Fatalf("accepted kept the refusal: %+v", got)
	}
	if err := db.RefuseAudibleAccount(ctx, a.ID, "throttled", at); err != nil {
		t.Fatal(err)
	}
	if err := db.SignInAudibleAccount(ctx, a, at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.RefusedAt != "" || got.LastError != "" {
		t.Fatalf("a sign-in kept the refusal: %+v", got)
	}
}
