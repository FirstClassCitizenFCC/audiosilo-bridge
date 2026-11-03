//go:build audiblefake

package audiblejob

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audible/audiblefake"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/secrets"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// An import through the real accounts and the audible client against the
// fake. Allowed: a fresh sign-in (the current device profile) is granted its
// license and the title lands in the library. Denied: the same account's
// device on the older profile is refused as audible_refused (not no_rights)
// with the sign-in-again advice, and no customer id or device serial reaches
// the job's error, result, progress or logs.
func TestImportAgainstTheFakeDeviceProfile(t *testing.T) {
	const customer = "A1FAKECUSTOMER77"
	fake := audiblefake.New(audiblefake.Options{KeyBits: 1024, CustomerID: customer, FFmpeg: "-"})
	ts := httptest.NewServer(fake.Handler())
	defer ts.Close()

	e := newImportEnv(t)
	data := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(data, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, _, err := secrets.Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	m := audibleacct.New(audibleacct.Options{Vault: vault, DB: db, Log: e.opts.Log,
		Service: audibleacct.Audible(audible.WithEndpoints(audible.Endpoints{Amazon: ts.URL, AmazonAPI: ts.URL, API: ts.URL}))})
	st, err := m.Start("us")
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	acct, err := m.Finish(context.Background(), accountServer{&fakeServer{}}, "srv1", audibleacct.Finish{Login: st.Login,
		RedirectURL: resp.Header.Get("Location"), Mapping: audibleacct.Mapping{UserID: 3, UserName: "Chris", LibraryID: 1, Policy: "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	if acct.DeviceProfile != audible.ProfileVersion || audibleacct.OldProfile(acct) {
		t.Fatalf("a fresh sign-in's profile = %q", acct.DeviceProfile)
	}
	e.opts.Accounts = m
	params := e.params(func(p *ImportParams) { p.Account = acct.ID })

	res, err := e.run(context.Background(), params)
	if err != nil {
		t.Fatalf("a fresh sign-in's import: %v", err)
	}
	got := res.(*ImportResult)
	if len(got.Files) != 1 || got.Files[0].Path != copperGrove {
		t.Fatalf("result = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.root, filepath.FromSlash(copperGrove))); err != nil {
		t.Fatal(err)
	}
	if lic := fake.Calls().Licenses; len(lic) != 1 || lic[0].Status != "Granted" {
		t.Fatalf("licenses = %+v", lic)
	}

	// The device on the older profile, as one an earlier Bridge registered.
	fake.OldProfile()
	e.progress = nil
	res, err = e.run(context.Background(), e.params(func(p *ImportParams) {
		p.Account, p.ASIN = acct.ID, "B0FAKE0006"
	}))
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeRefused || f.Message != ThrottledText {
		t.Fatalf("an old-profile device's import = %#v, %v", res, err)
	}
	if lic := fake.Calls().Licenses; len(lic) != 2 || lic[1].Status != "Denied" {
		t.Fatalf("licenses = %+v", lic)
	}
	raw, _ := vault.Get(secrets.AudibleName(acct.ID))
	creds, _ := audible.ParseCredentials(raw)
	pb, _ := json.Marshal(e.progress)
	for what, text := range map[string]string{"error": err.Error(), "result": mustJSON(t, res), "progress": string(pb), "log": e.logs.String()} {
		for _, id := range []string{customer, creds.CustomerID, creds.DeviceSerial} {
			if id != "" && strings.Contains(text, id) {
				t.Fatalf("the %s carries an account id: %.400s", what, text)
			}
		}
	}

	// The denial marked the account: the next queued import fails at once,
	// asking Audible nothing, with the same advice.
	if a, _ := m.Account(context.Background(), acct.ID); !audibleacct.Throttled(a) {
		t.Fatalf("the throttled account isn't marked: %+v", a)
	}
	_, err = e.run(context.Background(), e.params(func(p *ImportParams) { p.Account, p.ASIN = acct.ID, "B0FAKE0007" }))
	if !errors.As(err, &f) || f.Code != CodeRefused || f.Message != ThrottledText {
		t.Fatalf("the next import = %v", err)
	}
	if lic := fake.Calls().Licenses; len(lic) != 2 {
		t.Fatalf("the next import asked for a license: %+v", lic)
	}

	// Signing in again registers a current device and ends the refusal.
	st, err = m.Start("us")
	if err != nil {
		t.Fatal(err)
	}
	if resp, err = browser.Get(st.URL); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	again, err := m.Finish(context.Background(), accountServer{&fakeServer{}}, "srv1", audibleacct.Finish{Login: st.Login,
		RedirectURL: resp.Header.Get("Location"), Mapping: audibleacct.Mapping{UserID: 3, UserName: "Chris", LibraryID: 1, Policy: "manual"}})
	if err != nil || again.ID != acct.ID || again.RefusedAt != "" {
		t.Fatalf("signed in again = %+v, %v", again, err)
	}
	if _, err := e.run(context.Background(), e.params(func(p *ImportParams) { p.Account, p.ASIN = acct.ID, "B0FAKE0006" })); err != nil {
		t.Fatalf("the import after signing in again: %v", err)
	}
	if lic := fake.Calls().Licenses; len(lic) != 3 || lic[2].Status != "Granted" {
		t.Fatalf("licenses = %+v", lic)
	}
}
