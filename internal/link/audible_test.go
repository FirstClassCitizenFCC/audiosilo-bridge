package link

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Audible routes as the server's handlers_audible.go answers them: methods,
// paths, the bridge token in the header only, the bodies' field names, and the
// error codes each maps.
func TestAudibleWireMirror(t *testing.T) {
	t.Parallel()
	type seen struct{ method, path, query, body string }
	var calls []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(raw)})
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("token") != "" {
			t.Errorf("%s %s: the token goes in the header only", r.Method, r.URL)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bridge/users":
			_, _ = w.Write([]byte(`{"users":[{"id":3,"username":"chris","display_name":"Chris","admin":true}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/audible/accounts/abcd1234":
			_, _ = w.Write([]byte(`{"account":{"id":"abcd1234","label":"Chris (US)","marketplace":"us","user_id":3,
				"user":{"id":3,"display_name":"Chris"},"library_id":1,"library":{"id":1,"name":"Books"},"paused":false,
				"policy":"manual","sync":true,"poll_hours":6,"created_at":"x","updated_at":"x","last_poll_at":null}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/audible/accounts/badbad99":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such user","code":"unknown_user"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/bridge/audible/accounts/abcd1234":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/audible/accounts/abcd1234/titles":
			_, _ = w.Write([]byte(`{"stored":2}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/audible/accounts/gone0000/titles":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such Audible account","code":"unknown_account"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bridge/libraries/1/books":
			if r.URL.Query().Get("after") == "" {
				_, _ = w.Write([]byte(`{"books":[{"path":"A/One","title":"One","authors":["A"],"narrators":[],
					"series":[{"name":"S","position":"1-3"}],"asin":"B000000001","isbn":"","duration":36012.4}],"next":"QQ"}`))
				return
			}
			_, _ = w.Write([]byte(`{"books":[],"next":null}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/libraries/1/enrichment":
			switch r.URL.Query().Get("path") {
			case "A/One", ".":
				w.WriteHeader(http.StatusNoContent)
			case "A/Set":
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"the book already has an ASIN","code":"asin_set"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no such book","code":"book_not_found"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "tok", "0.9.0")
	ctx := context.Background()

	users, err := c.Users(ctx)
	if err != nil || len(users) != 1 || users[0] != (User{ID: 3, Username: "chris", DisplayName: "Chris", Admin: true}) {
		t.Fatalf("users = %+v, %v", users, err)
	}

	in := AudibleAccountInput{Label: "Chris (US)", Marketplace: "us", UserID: 3, LibraryID: 1, Policy: "manual", Sync: true}
	acct, err := c.PutAudibleAccount(ctx, "abcd1234", in)
	if err != nil || acct.ID != "abcd1234" || acct.User.DisplayName != "Chris" || acct.LibraryID == nil || *acct.LibraryID != 1 ||
		acct.PollHours != 6 || acct.Paused {
		t.Fatalf("account = %+v, %v", acct, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(calls[len(calls)-1].body), &body)
	for _, k := range []string{"label", "marketplace", "user_id", "library_id", "policy", "sync"} {
		if _, ok := body[k]; !ok {
			t.Errorf("account body has no %q: %v", k, body)
		}
	}
	var apiErr *APIError
	if _, err := c.PutAudibleAccount(ctx, "badbad99", in); !errors.As(err, &apiErr) || apiErr.Code != "unknown_user" {
		t.Fatalf("unknown user = %v", err)
	}
	if err := c.DeleteAudibleAccount(ctx, "abcd1234"); err != nil {
		t.Fatal(err)
	}

	n, err := c.PutAudibleTitles(ctx, "abcd1234", AudibleTitles{PollID: "p1", Final: true, Titles: []AudibleTitle{
		{ASIN: "B000000001", Title: "One", Authors: []string{"A"}, Series: []SeriesRef{{Name: "S", Position: "1"}},
			ReleaseDate: "2026-11-20", RuntimeS: 36000, State: AudibleAvailable,
			Match: &AudibleMatch{LibraryID: 1, Path: "A/One", How: MatchASIN}},
		{ASIN: "B000000002", State: AudiblePreorder},
	}})
	if err != nil || n != 2 {
		t.Fatalf("titles = %d, %v", n, err)
	}
	raw := calls[len(calls)-1].body
	for _, want := range []string{`"poll_id":"p1"`, `"final":true`, `"release_date":"2026-11-20"`, `"runtime_s":36000`,
		`"state":"available"`, `"match":{"library_id":1,"path":"A/One","how":"asin"}`, `"match":null`, `"series":[{"name":"S","position":"1"}]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("titles body lacks %s: %s", want, raw)
		}
	}
	// An empty final call still sends a list, never null.
	if _, err := c.PutAudibleTitles(ctx, "abcd1234", AudibleTitles{PollID: "p2", Final: true}); err != nil ||
		!strings.Contains(calls[len(calls)-1].body, `"titles":[]`) {
		t.Fatalf("empty final = %v %s", err, calls[len(calls)-1].body)
	}
	if _, err := c.PutAudibleTitles(ctx, "gone0000", AudibleTitles{PollID: "p3"}); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("unknown account = %v", err)
	}

	page, err := c.LibraryBooks(ctx, 1, "", 500)
	if err != nil || page.Next != "QQ" || len(page.Books) != 1 || page.Books[0].Series[0].Position != "1-3" ||
		page.Books[0].ASIN != "B000000001" || page.Books[0].Duration != 36012.4 {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	if q := calls[len(calls)-1].query; q != "limit=500" {
		t.Fatalf("query = %q", q)
	}
	page, err = c.LibraryBooks(ctx, 1, "QQ", 0)
	if err != nil || page.Next != "" || page.Books == nil {
		t.Fatalf("last page = %+v, %v", page, err)
	}
	if q := calls[len(calls)-1].query; q != "after=QQ" {
		t.Fatalf("query = %q", q)
	}

	if err := c.Enrich(ctx, 1, "A/One", "B000000001"); err != nil {
		t.Fatal(err)
	}
	if b := calls[len(calls)-1].body; b != `{"asin":"B000000001"}` {
		t.Fatalf("enrichment body = %s", b)
	}
	if q := calls[len(calls)-1].query; q != "path=A%2FOne" {
		t.Fatalf("enrichment query = %q", q)
	}
	// The library's root book (a library that is one book) is "." on the wire.
	if err := c.Enrich(ctx, 1, ".", "B000000001"); err != nil {
		t.Fatal(err)
	}
	if q := calls[len(calls)-1].query; q != "path=." {
		t.Fatalf("root enrichment query = %q", q)
	}
	if err := c.Enrich(ctx, 1, "A/Set", "B000000001"); !errors.Is(err, ErrASINSet) {
		t.Fatalf("asin set = %v", err)
	}
	if err := c.Enrich(ctx, 1, "A/None", "B000000001"); !errors.Is(err, ErrNoBook) {
		t.Fatalf("no book = %v", err)
	}
}

// A 429 carries its Retry-After (seconds or an HTTP date) on the APIError, so
// a caller can wait what the server asked; none is 0.
func TestRateLimitedRetryAfter(t *testing.T) {
	t.Parallel()
	var retryAfter string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
	}))
	defer server.Close()
	c, _ := New(server.URL, "tok", "0.9.0")
	for _, tc := range []struct {
		header   string
		min, max time.Duration
	}{
		{"7", 7 * time.Second, 7 * time.Second},
		{"", 0, 0},
		{"soon", 0, 0},
		{"-3", 0, 0},
		{"99999999999999", 24 * time.Hour, 24 * time.Hour},
		{time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat), 80 * time.Second, 90 * time.Second},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), 0, 0},
	} {
		retryAfter = tc.header
		err := c.Enrich(context.Background(), 1, "A/One", "B000000001")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
			t.Fatalf("Retry-After %q: err = %v", tc.header, err)
		}
		if apiErr.RetryAfter < tc.min || apiErr.RetryAfter > tc.max {
			t.Errorf("Retry-After %q: wait %v, want %v..%v", tc.header, apiErr.RetryAfter, tc.min, tc.max)
		}
	}
}
