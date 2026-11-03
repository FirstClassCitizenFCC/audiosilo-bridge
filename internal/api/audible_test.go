package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeAudible is the fake Backend's Audible section.
type fakeAudible struct {
	accounts []AudibleAccount
	finished []AudibleFinish
	mapped   map[string]AudibleMapping
	removed  []string
	warning  string
}

func (b *backend) Audible(context.Context) AudibleAccounts {
	return AudibleAccounts{Accounts: append([]AudibleAccount{}, b.aud.accounts...), Marketplaces: []string{"uk", "us"},
		Policies: []string{"all", "series", "manual"}, Paired: true, Libraries: []AudibleLibrary{{ID: 1, Name: "Books"}},
		Users: []AudibleUser{{ID: 3, Name: "Chris", Admin: true}}}
}

func (b *backend) StartAudibleSignIn(_ context.Context, marketplace string) (AudibleSignIn, error) {
	if marketplace != "us" {
		return AudibleSignIn{}, Errorf(ErrInvalid, "unknown marketplace %q", marketplace)
	}
	return AudibleSignIn{Login: "login0001", URL: "https://www.amazon.com/ap/signin?x=1", Marketplace: "us",
		ExpiresAt: time.Date(2026, 11, 3, 12, 15, 0, 0, time.UTC)}, nil
}

func (b *backend) FinishAudibleSignIn(_ context.Context, f AudibleFinish) error {
	if f.Login != "login0001" {
		return Errorf(ErrNotFound, "the sign-in expired: open the Amazon sign-in again")
	}
	b.aud.finished = append(b.aud.finished, f)
	b.aud.accounts = append(b.aud.accounts, AudibleAccount{ID: "acct0001", Label: f.Label, UserID: f.UserID, LibraryID: f.LibraryID, Policy: f.Policy})
	return nil
}

func (b *backend) MapAudibleAccount(_ context.Context, id string, m AudibleMapping) error {
	if id != "acct0001" {
		return Errorf(ErrNotFound, "no such Audible account on this Bridge")
	}
	if b.aud.mapped == nil {
		b.aud.mapped = map[string]AudibleMapping{}
	}
	b.aud.mapped[id] = m
	return nil
}

func (b *backend) RemoveAudibleAccount(_ context.Context, id string) (string, error) {
	if id != "acct0001" || len(b.aud.removed) > 0 {
		return "", Errorf(ErrNotFound, "no such Audible account on this Bridge")
	}
	b.aud.removed = append(b.aud.removed, id)
	b.aud.accounts = nil
	return b.aud.warning, nil
}

func TestAudibleRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	w := e.do(http.MethodGet, "/api/accounts/audible", e.token, "")
	var got AudibleAccounts
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil || len(got.Marketplaces) != 2 ||
		got.Accounts == nil || got.Users[0].Name != "Chris" {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}

	w = e.do(http.MethodPost, "/api/accounts/audible/sign-in", e.token, `{"marketplace":" us "}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"login":"login0001"`) || !strings.Contains(w.Body.String(), `"url":"https://www.amazon.com/ap/signin?x=1"`) {
		t.Fatalf("sign-in = %d %s", w.Code, w.Body.String())
	}
	if w = e.do(http.MethodPost, "/api/accounts/audible/sign-in", e.token, `{"marketplace":"zz"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown marketplace = %d", w.Code)
	}
	if w = e.do(http.MethodPost, "/api/accounts/audible/sign-in", e.token, `{"marketplace":"us","extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field = %d", w.Code)
	}

	code := "https://www.amazon.com/ap/maplanding?openid.oa2.authorization_code=SECRETCODE"
	body := `{"login":"login0001","redirect_url":"` + code + `","label":"Chris (US)","user_id":3,"library_id":1,"policy":"manual","sync":true}`
	w = e.do(http.MethodPost, "/api/accounts/audible", e.token, body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"acct0001"`) {
		t.Fatalf("finish = %d %s", w.Code, w.Body.String())
	}
	// The pasted page's code is taken, never answered back.
	if strings.Contains(w.Body.String(), "SECRETCODE") {
		t.Fatalf("the answer echoes the code: %s", w.Body.String())
	}
	if f := e.b.aud.finished[0]; f.RedirectURL != code || f.UserID != 3 || f.LibraryID != 1 || f.Policy != "manual" || !f.Sync {
		t.Fatalf("finished = %+v", f)
	}
	if w = e.do(http.MethodPost, "/api/accounts/audible", e.token, strings.Replace(body, "login0001", "expired1", 1)); w.Code != http.StatusNotFound {
		t.Fatalf("an expired sign-in = %d", w.Code)
	}

	w = e.do(http.MethodPut, "/api/accounts/audible/acct0001", e.token, `{"label":"","user_id":4,"library_id":2,"policy":"all","sync":false}`)
	if w.Code != http.StatusOK || e.b.aud.mapped["acct0001"].UserID != 4 {
		t.Fatalf("map = %d %s", w.Code, w.Body.String())
	}
	if w = e.do(http.MethodPut, "/api/accounts/audible/nosuch00", e.token, `{"user_id":4,"library_id":2,"policy":"all"}`); w.Code != http.StatusNotFound {
		t.Fatalf("map unknown = %d", w.Code)
	}

	e.b.aud.warning = "Amazon didn't confirm removing the AudioSilo device"
	w = e.do(http.MethodDelete, "/api/accounts/audible/acct0001", e.token, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"warning":"Amazon didn't confirm`) || !strings.Contains(w.Body.String(), `"audible":{`) {
		t.Fatalf("remove = %d %s", w.Code, w.Body.String())
	}
	if w = e.do(http.MethodDelete, "/api/accounts/audible/acct0001", e.token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("remove twice = %d", w.Code)
	}
}

// Every Audible route needs the page's session, and none answers off the LAN.
func TestAudibleRoutesNeedASession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/accounts/audible", ""},
		{http.MethodPost, "/api/accounts/audible/sign-in", `{"marketplace":"us"}`},
		{http.MethodPost, "/api/accounts/audible", `{"login":"login0001","redirect_url":"x","user_id":3,"library_id":1,"policy":"manual"}`},
		{http.MethodPut, "/api/accounts/audible/acct0001", `{"user_id":3,"library_id":1,"policy":"manual"}`},
		{http.MethodDelete, "/api/accounts/audible/acct0001", ""},
	}
	for _, rt := range routes {
		if w := e.do(rt.method, rt.path, "", rt.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d", rt.method, rt.path, w.Code)
		}
		if w := e.do(rt.method, rt.path, "not-a-session", rt.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a bad session = %d", rt.method, rt.path, w.Code)
		}
		if w := e.do(rt.method, rt.path, e.token, rt.body, "203.0.113.9:4000"); w.Code != http.StatusForbidden {
			t.Errorf("%s %s off the LAN = %d", rt.method, rt.path, w.Code)
		}
	}
	if len(e.b.aud.finished) != 0 || len(e.b.aud.removed) != 0 || e.b.aud.mapped != nil {
		t.Fatalf("a refused request reached the backend: %+v", e.b.aud)
	}
}
