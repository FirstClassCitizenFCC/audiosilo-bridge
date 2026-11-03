package audibleacct

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/secrets"
	"github.com/kodestar/audiosilo-bridge/internal/source"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// The secrets a stub registration hands out: none may appear anywhere but the
// vault.
const (
	stubAccess  = "Atna|ACCESS-SECRET-1"
	stubRefresh = "Atnr|REFRESH-SECRET-1"
	stubADP     = "{enc:}ADP-SECRET-1"
	stubKey     = "-----BEGIN RSA PRIVATE KEY-----\nKEY-SECRET-1\n-----END RSA PRIVATE KEY-----\n"
	stubCode    = "AUTHCODE-SECRET-1"
	stubCust    = "amzn1.account.CUSTOMER-SECRET-1"
)

var stubSecrets = []string{"ACCESS-SECRET", "REFRESH-SECRET", "ADP-SECRET", "KEY-SECRET", "AUTHCODE-SECRET", "CUSTOMER-SECRET"}

// stubService is Audible without a network: a registration per code, a
// library, refusals and the deregistrations it was asked for.
type stubService struct {
	mu           sync.Mutex
	n            int
	registerErr  error
	libraryErr   error
	deregistered []string // device serials
	deregErr     error
	customer     string
}

func (s *stubService) AuthorizeURL(mp audible.Marketplace, pkce audible.PKCE, serial string) string {
	return "https://www.amazon." + mp.TLD + "/ap/signin?challenge=" + pkce.Challenge
}

func (s *stubService) Register(_ context.Context, mp audible.Marketplace, code string, _ audible.PKCE, serial string) (audible.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registerErr != nil {
		return audible.Credentials{}, s.registerErr
	}
	if code != stubCode {
		return audible.Credentials{}, errors.New("bad code")
	}
	s.n++
	cust := s.customer
	if cust == "" {
		cust = stubCust
	}
	return audible.Credentials{AccessToken: stubAccess, RefreshToken: stubRefresh, ADPToken: stubADP, DevicePrivateKey: stubKey,
		DeviceSerial: serial, CustomerID: cust, Marketplace: mp.Code, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

func (s *stubService) Deregister(_ context.Context, creds audible.Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deregistered = append(s.deregistered, creds.DeviceSerial)
	return s.deregErr
}

func (s *stubService) Library(context.Context, audible.Credentials) ([]source.Record, error) {
	if s.libraryErr != nil {
		return nil, s.libraryErr
	}
	return []source.Record{{ASIN: "B0FAKE0001", Title: "One"}}, nil
}

// stubServer is the server's account routes.
type stubServer struct {
	mu      sync.Mutex
	put     map[string]link.AudibleAccountInput
	deleted []string
	putErr  error
	delErr  error
}

func (s *stubServer) PutAudibleAccount(_ context.Context, id string, in link.AudibleAccountInput) (*link.AudibleAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return nil, s.putErr
	}
	if s.put == nil {
		s.put = map[string]link.AudibleAccountInput{}
	}
	s.put[id] = in
	return &link.AudibleAccount{ID: id}, nil
}

func (s *stubServer) DeleteAudibleAccount(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return s.delErr
}

type env struct {
	m     *Manager
	svc   *stubService
	srv   *stubServer
	vault *secrets.Vault
	db    *store.DB
	logs  *bytes.Buffer
	data  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(data, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, _, err := secrets.Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := &stubService{}
	m := New(Options{Vault: vault, DB: db, Service: svc, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	return &env{m: m, svc: svc, srv: &stubServer{}, vault: vault, db: db, logs: &logs, data: data}
}

func mapping() Mapping {
	return Mapping{UserID: 3, UserName: "Chris", LibraryID: 1, Policy: "manual", Sync: true}
}

func (e *env) signIn(t *testing.T) store.AudibleAccount {
	t.Helper()
	st, err := e.m.Start("us")
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login,
		RedirectURL: "https://www.amazon.com/ap/maplanding?openid.oa2.authorization_code=" + stubCode, Mapping: mapping()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// noSecrets fails when any stub secret is anywhere under the data dir but the
// vault's sealed files, or in the logs.
func (e *env) noSecrets(t *testing.T) {
	t.Helper()
	for _, s := range stubSecrets {
		if strings.Contains(e.logs.String(), s) {
			t.Fatalf("the log holds %s: %s", s, e.logs)
		}
	}
	err := filepath.Walk(e.data, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(path)
		for _, s := range stubSecrets {
			if bytes.Contains(raw, []byte(s)) {
				t.Errorf("%s holds %s in the clear", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSignInSealsTheCredentialsAndMapsOnTheServerFirst(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st, err := e.m.Start("US")
	if err != nil || st.Marketplace != "us" || !strings.Contains(st.URL, "amazon.com/ap/signin") ||
		time.Until(st.ExpiresAt) > LoginTTL || !IDPattern.MatchString(st.Login) {
		t.Fatalf("start = %+v, %v", st, err)
	}
	if _, err := e.m.Start("zz"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown marketplace = %v", err)
	}
	// A pasted page with no code keeps the sign-in for another try.
	_, err = e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login, RedirectURL: "https://www.amazon.com/", Mapping: mapping()})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("no code = %v", err)
	}
	a, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login,
		RedirectURL: " https://www.amazon.com/ap/maplanding?openid.oa2.authorization_code=" + stubCode + " ", Mapping: mapping()})
	if err != nil {
		t.Fatal(err)
	}
	if !IDPattern.MatchString(a.ID) || a.Label != "Chris (US)" || a.ServerID != "srv-1" || a.UserID != 3 || a.LibraryID != 1 ||
		a.Policy != "manual" || !a.Sync || a.CustomerHash == "" || a.LastOKAt == "" {
		t.Fatalf("account = %+v", a)
	}
	if in := e.srv.put[a.ID]; in != (link.AudibleAccountInput{Label: "Chris (US)", Marketplace: "us", UserID: 3, LibraryID: 1, Policy: "manual", Sync: true}) {
		t.Fatalf("server mapping = %+v", in)
	}
	// The login is spent.
	if _, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login,
		RedirectURL: "https://x/ap/maplanding?openid.oa2.authorization_code=" + stubCode, Mapping: mapping()}); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("a spent login = %v", err)
	}

	// The vault round trip: the sealed value opens to the same credentials.
	raw, err := e.vault.Get(secrets.AudibleName(a.ID))
	if err != nil || raw == "" {
		t.Fatalf("sealed = %q, %v", raw, err)
	}
	creds, err := audible.ParseCredentials(raw)
	if err != nil || creds.AccessToken != stubAccess || creds.RefreshToken != stubRefresh || creds.ADPToken != stubADP ||
		creds.DevicePrivateKey != stubKey || creds.CustomerID != stubCust || creds.Marketplace != "us" {
		t.Fatalf("round trip = %+v, %v", creds, err)
	}
	if names := secrets.Names(); strings.Contains(strings.Join(names, ","), "audible") {
		t.Fatalf("an operator never sets an Audible sign-in: %v", names)
	}
	e.noSecrets(t)
}

func TestSignInRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st, _ := e.m.Start("uk")
	redirect := "https://www.amazon.co.uk/ap/maplanding?openid.oa2.authorization_code=" + stubCode
	for _, bad := range []Mapping{
		{UserID: 3, LibraryID: 1, Policy: "everything"},
		{UserID: 0, LibraryID: 1, Policy: "all"},
		{UserID: 3, LibraryID: 0, Policy: "all"},
	} {
		if _, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login, RedirectURL: redirect, Mapping: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v = %v", bad, err)
		}
	}
	if _, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: "nosuchlogin", RedirectURL: redirect, Mapping: mapping()}); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("an unknown login = %v", err)
	}
	if _, err := e.m.Finish(context.Background(), nil, "", Finish{Login: st.Login, RedirectURL: redirect, Mapping: mapping()}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("finishing while unpaired = %v", err)
	}

	// The server refuses the mapping: the new device is deregistered again and
	// nothing is kept.
	e.srv.putErr = &link.APIError{Status: 404, Code: "unknown_user", Message: "no such user"}
	_, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login, RedirectURL: redirect, Mapping: mapping()})
	var apiErr *link.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "unknown_user" {
		t.Fatalf("a refused mapping = %v", err)
	}
	if len(e.svc.deregistered) != 1 {
		t.Fatalf("the device stays registered: %v", e.svc.deregistered)
	}
	if list, _ := e.m.Accounts(context.Background()); len(list) != 0 {
		t.Fatalf("a refused sign-in kept an account: %+v", list)
	}

	// Amazon refuses the code.
	e.srv.putErr = nil
	e.svc.registerErr = errors.New("400 invalid_grant")
	st, _ = e.m.Start("uk")
	if _, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login, RedirectURL: redirect, Mapping: mapping()}); !errors.Is(err, ErrRegister) {
		t.Fatalf("a refused code = %v", err)
	}

	// An expired sign-in.
	now := time.Now()
	e.m.o.Now = func() time.Time { return now }
	e.svc.registerErr = nil
	st, _ = e.m.Start("uk")
	e.m.o.Now = func() time.Time { return now.Add(LoginTTL) }
	if _, err := e.m.Finish(context.Background(), e.srv, "srv-1", Finish{Login: st.Login, RedirectURL: redirect, Mapping: mapping()}); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("an expired login = %v", err)
	}
	e.noSecrets(t)
}

func TestLoginsAreBounded(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first, _ := e.m.Start("us")
	for range maxLogins {
		if _, err := e.m.Start("us"); err != nil {
			t.Fatal(err)
		}
	}
	e.m.mu.Lock()
	n := len(e.m.logins)
	_, kept := e.m.logins[first.Login]
	e.m.mu.Unlock()
	if n != maxLogins || kept {
		t.Fatalf("logins = %d, oldest kept %v", n, kept)
	}
}

// A second sign-in of the same Amazon account replaces the first: the same id,
// the new device's sign-in, the old device deregistered, a refusal forgotten.
func TestSignInAgainReplacesTheDevice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.signIn(t)
	first, _ := e.m.credentials(a.ID)
	if err := e.db.RefuseAudibleAccount(context.Background(), a.ID, RefusedText, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := e.signIn(t)
	second, _ := e.m.credentials(b.ID)
	if b.ID != a.ID || b.RefusedAt != "" || second.DeviceSerial == first.DeviceSerial {
		t.Fatalf("again = %+v (first %+v)", b, a)
	}
	if len(e.svc.deregistered) != 1 || e.svc.deregistered[0] != first.DeviceSerial {
		t.Fatalf("deregistered = %v, want the first device %s", e.svc.deregistered, first.DeviceSerial)
	}
	// Another Amazon account is another account.
	e.svc.customer = "amzn1.account.OTHER"
	c := e.signIn(t)
	if c.ID == a.ID {
		t.Fatal("another Amazon account replaced the first")
	}
	if list, _ := e.m.Accounts(context.Background()); len(list) != 2 {
		t.Fatalf("accounts = %+v", list)
	}
}

func TestMapRecordsOnTheServerThenHere(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.signIn(t)
	mp := Mapping{Label: "  Kids'   shelf ", UserID: 4, UserName: "Sam", LibraryID: 2, Policy: "SERIES"}
	got, err := e.m.Map(context.Background(), e.srv, "srv-2", a.ID, mp)
	if err != nil || got.Label != "Kids' shelf" || got.UserID != 4 || got.LibraryID != 2 || got.Policy != "series" || got.Sync ||
		got.ServerID != "srv-2" {
		t.Fatalf("mapped = %+v, %v", got, err)
	}
	if in := e.srv.put[a.ID]; in.UserID != 4 || in.Policy != "series" || in.Marketplace != "us" {
		t.Fatalf("server = %+v", in)
	}
	e.srv.putErr = &link.APIError{Status: 400, Code: "not_books"}
	if _, err := e.m.Map(context.Background(), e.srv, "srv-2", a.ID, mapping()); err == nil {
		t.Fatal("a refused mapping")
	}
	if kept, _ := e.m.Account(context.Background(), a.ID); kept.LibraryID != 2 {
		t.Fatalf("a refused mapping changed the account: %+v", kept)
	}
	if _, err := e.m.Map(context.Background(), e.srv, "srv-2", "nosuch00", mapping()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	if _, err := e.m.Account(context.Background(), "../../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a malformed id = %v", err)
	}
}

// Remove forgets locally first, then tells the server and deregisters; a
// failure of either is a warning, never the account kept.
func TestRemoveForgetsFirstThenDeregisters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.signIn(t)
	creds, _ := e.m.credentials(a.ID)
	warning, err := e.m.Remove(context.Background(), e.srv, a.ID)
	if err != nil || warning != "" {
		t.Fatalf("remove = %q, %v", warning, err)
	}
	if ok, _ := e.vault.Present(secrets.AudibleName(a.ID)); ok {
		t.Fatal("the sign-in is still sealed")
	}
	if _, err := e.m.Account(context.Background(), a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the account is kept: %v", err)
	}
	if len(e.srv.deleted) != 1 || e.srv.deleted[0] != a.ID || len(e.svc.deregistered) != 1 || e.svc.deregistered[0] != creds.DeviceSerial {
		t.Fatalf("server %v, deregistered %v", e.srv.deleted, e.svc.deregistered)
	}
	if _, err := e.m.Remove(context.Background(), e.srv, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice = %v", err)
	}

	b := e.signIn(t)
	e.srv.delErr = errors.New("unreachable")
	e.svc.deregErr = errors.New("500")
	warning, err = e.m.Remove(context.Background(), e.srv, b.ID)
	if err != nil || !strings.Contains(warning, "server didn't confirm") || !strings.Contains(warning, "device list") {
		t.Fatalf("remove with failures = %q, %v", warning, err)
	}
	if list, _ := e.m.Accounts(context.Background()); len(list) != 0 {
		t.Fatalf("kept = %+v", list)
	}
	c := e.signIn(t)
	if warning, err = e.m.Remove(context.Background(), nil, c.ID); err != nil || !strings.Contains(warning, "isn't paired") {
		t.Fatalf("remove unpaired = %q, %v", warning, err)
	}
	e.noSecrets(t)
}

func TestLibraryMarksARefusal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.signIn(t)
	changed := 0
	e.m.o.OnChange = func() { changed++ }
	recs, err := e.m.Library(context.Background(), a.ID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("library = %v, %v", recs, err)
	}
	e.svc.libraryErr = fmt.Errorf("%w (HTTP 403)", ErrRefused)
	if _, err := e.m.Library(context.Background(), a.ID); !errors.Is(err, ErrRefused) {
		t.Fatalf("refused = %v", err)
	}
	got, _ := e.m.Account(context.Background(), a.ID)
	if got.RefusedAt == "" || got.LastError != RefusedText || changed != 1 {
		t.Fatalf("after a refusal = %+v (changed %d)", got, changed)
	}
	// Another failure is no refusal.
	e.svc.libraryErr = errors.New("connection reset")
	if _, err := e.m.Library(context.Background(), a.ID); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("a network failure = %v", err)
	}
	if err := e.m.Polled(context.Background(), a.ID, 30, 4); err != nil {
		t.Fatal(err)
	}
	got, _ = e.m.Account(context.Background(), a.ID)
	if got.RefusedAt != "" || got.LastTitles != 30 || got.LastMatched != 4 {
		t.Fatalf("after a poll = %+v", got)
	}
	if err := e.vault.Delete(secrets.AudibleName(a.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Library(context.Background(), a.ID); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("a missing sign-in = %v", err)
	}
	e.noSecrets(t)
}
