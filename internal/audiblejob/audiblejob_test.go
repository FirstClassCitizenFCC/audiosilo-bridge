package audiblejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/jobs"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// stubAccounts is the account manager: one account and its library.
type stubAccounts struct {
	acct   store.AudibleAccount
	recs   []source.Record
	libErr error
	polled []int
	failed []string
}

func (s *stubAccounts) Account(_ context.Context, id string) (store.AudibleAccount, error) {
	if id != s.acct.ID {
		return store.AudibleAccount{}, audibleacct.ErrNotFound
	}
	return s.acct, nil
}
func (s *stubAccounts) Library(context.Context, string) ([]source.Record, error) {
	return s.recs, s.libErr
}
func (s *stubAccounts) Polled(_ context.Context, _ string, titles, matched int) error {
	s.polled = append(s.polled, titles, matched)
	return nil
}
func (s *stubAccounts) Failed(_ context.Context, _ string, why string) error {
	s.failed = append(s.failed, why)
	return nil
}

// fakeServer is the server's Audible routes: a library's books in pages of
// pageSize, the titles calls and the enrichments it was sent.
type fakeServer struct {
	t        *testing.T
	mu       sync.Mutex
	books    []link.LibraryBook
	pageSize int
	calls    []link.AudibleTitles
	pages    int
	enriched map[string]string // path -> asin
	asinSet  map[string]bool   // paths answering 409 asin_set
	titleErr int
	// limit answers the n-th enrichment call (from 1) 429 with that
	// Retry-After ("-" for none) when it returns ok.
	limit   func(n int) (retryAfter string, ok bool)
	enrichN int
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bridge/libraries/1/books":
		f.pages++
		if l := r.URL.Query().Get("limit"); l != "500" {
			f.t.Errorf("limit = %q", l)
		}
		from := 0
		if a := r.URL.Query().Get("after"); a != "" {
			from, _ = strconv.Atoi(a)
		}
		end := min(from+f.pageSize, len(f.books))
		var next any
		if end < len(f.books) {
			next = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"books": f.books[from:end], "next": next})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/bridge/audible/accounts/") &&
		strings.HasSuffix(r.URL.Path, "/titles"):
		if f.titleErr != 0 {
			w.WriteHeader(f.titleErr)
			_, _ = w.Write([]byte(`{"error":"no such Audible account","code":"unknown_account"}`))
			return
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields() // as the server decodes it
		var body link.AudibleTitles
		if err := dec.Decode(&body); err != nil {
			f.t.Errorf("titles body: %v", err)
		}
		if len(body.Titles) > link.MaxAudibleTitles {
			f.t.Errorf("a call of %d titles", len(body.Titles))
		}
		f.calls = append(f.calls, body)
		_ = json.NewEncoder(w).Encode(map[string]int{"stored": len(body.Titles)})
	case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bridge/libraries/1/enrichment":
		f.enrichN++
		if f.limit != nil {
			if ra, ok := f.limit(f.enrichN); ok {
				if ra != "-" {
					w.Header().Set("Retry-After", ra)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
				return
			}
		}
		var body struct{ ASIN string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := r.URL.Query().Get("path")
		if f.asinSet[path] {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"asin_set"}`))
			return
		}
		if f.enriched == nil {
			f.enriched = map[string]string{}
		}
		f.enriched[path] = body.ASIN
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T, fs *fakeServer, accts *stubAccounts) *Handler {
	t.Helper()
	fs.t = t
	ts := httptest.NewServer(fs)
	t.Cleanup(ts.Close)
	cl, err := link.New(ts.URL, "tok", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Accounts: accts, Server: func() Server { return cl }, ServerID: func() string { return "srv-1" }})
	h.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() } // the pace, instantly
	return h
}

func account() store.AudibleAccount {
	return store.AudibleAccount{ID: "acct0001", ServerID: "srv-1", LibraryID: 1}
}

func run(t *testing.T, h *Handler, account string) (Result, error) {
	t.Helper()
	params, _ := json.Marshal(Params{Account: account})
	out, err := h.Run(context.Background(), jobs.Job{ID: 7, Kind: Kind, Params: params, Attempt: 1}, jobs.LocalReporter())
	if err != nil {
		return Result{}, err
	}
	return out.(Result), nil
}

func TestPollMatchesReportsAndEnriches(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{pageSize: 2, asinSet: map[string]bool{}, books: []link.LibraryBook{
		{Path: "Quill/Orchard 1", Title: "The Glass Orchard", Authors: []string{"Mara Quill"}, ASIN: "B0FAKE0001",
			Series: []link.SeriesRef{{Name: "Orchard Cycle", Position: "1"}}},
		{Path: "Quill/Orchard 2", Title: "The Copper Grove", Authors: []string{"Mara Quill"},
			Series: []link.SeriesRef{{Name: "Orchard Cycle", Position: "2"}}},
		{Path: "Marchetti/Lanterns", Title: "Lanterns Over Hollow Bay", Authors: []string{"Theo Marchetti"}},
		{Path: "Sato/Ledger", Title: "The Lighthouse Ledger", Authors: []string{"Imogen Sato"}},
	}}
	fs.asinSet["Sato/Ledger"] = true
	accts := &stubAccounts{acct: account(), recs: []source.Record{
		{ASIN: "B0FAKE0001", Title: "The Glass Orchard", Author: "Mara Quill", Authors: []string{"Mara Quill"}, Series: "Orchard Cycle",
			SeriesPosition: "1", Sequence: 1, HasSequence: true, ReleaseDate: "2020-01-02", RuntimeSec: 42720},
		{ASIN: "B0FAKE0002", Title: "The Copper Grove", Author: "Mara Quill", Series: "Orchard Cycle", Sequence: 2, HasSequence: true},
		// Two titles land on the same book: the book is contested, never enriched.
		{ASIN: "B0FAKE0006", Title: "Lanterns Over Hollow Bay", Author: "Theo Marchetti"},
		{ASIN: "B0FAKE0106", Title: "Lanterns Over Hollow Bay", Author: "Theo Marchetti"},
		// The server says another ASIN arrived: skipped, not a failure.
		{ASIN: "B0FAKE0024", Title: "The Lighthouse Ledger", Author: "Imogen Sato"},
		{ASIN: "B0FAKE0005", Title: "The Golden Canopy", Author: "Mara Quill", UnavailableReason: "Not released yet (pre-order)", Preorder: true, ReleaseDate: "2027-01-01"},
		{ASIN: "B0FAKE0029", Title: "The Borrowed Summer", Author: "Lena Albright", UnavailableReason: "Audible reports no download rights"},
		{ASIN: "", Title: "No ASIN"},
	}}
	h := setup(t, fs, accts)
	res, err := run(t, h, "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	if res.Titles != 7 || res.Skipped != 1 || res.Available != 5 || res.Preorder != 1 || res.Unavailable != 1 ||
		res.Matched != 5 || res.MatchedASIN != 1 || res.MatchedFuzzy != 4 || res.Contested != 1 || res.Enriched != 1 ||
		res.EnrichFailed != 0 || res.Books != 4 {
		t.Fatalf("result = %+v", res)
	}
	if fs.pages != 2 {
		t.Fatalf("read %d pages of books, want 2", fs.pages)
	}
	if len(fs.calls) != 1 || !fs.calls[0].Final || fs.calls[0].PollID != res.PollID {
		t.Fatalf("calls = %+v", fs.calls)
	}
	byASIN := map[string]link.AudibleTitle{}
	for _, tt := range fs.calls[0].Titles {
		byASIN[tt.ASIN] = tt
	}
	first := byASIN["B0FAKE0001"]
	if first.Match == nil || first.Match.How != link.MatchASIN || first.Match.Path != "Quill/Orchard 1" || first.Match.LibraryID != 1 ||
		first.ReleaseDate != "2020-01-02" || first.RuntimeS != 42720 || len(first.Series) != 1 || first.Series[0].Position != "1" ||
		first.State != link.AudibleAvailable || first.Reason != "" {
		t.Fatalf("first = %+v", first)
	}
	if g := byASIN["B0FAKE0002"]; g.Match == nil || g.Match.How != link.MatchFuzzy || g.Series[0].Position != "2" || len(g.Authors) != 1 {
		t.Fatalf("second = %+v", g)
	}
	if p := byASIN["B0FAKE0005"]; p.State != link.AudiblePreorder || p.Match != nil || p.Reason == "" {
		t.Fatalf("preorder = %+v", p)
	}
	if u := byASIN["B0FAKE0029"]; u.State != link.AudibleUnavailable || !strings.Contains(u.Reason, "download rights") {
		t.Fatalf("unavailable = %+v", u)
	}
	// Only the uncontested fuzzy match with no ASIN is enriched; the ASIN match
	// already has one, the contested book none.
	if len(fs.enriched) != 1 || fs.enriched["Quill/Orchard 2"] != "B0FAKE0002" {
		t.Fatalf("enriched = %v", fs.enriched)
	}
	if len(accts.polled) != 2 || accts.polled[0] != 7 || accts.polled[1] != 5 || len(accts.failed) != 0 {
		t.Fatalf("polled %v failed %v", accts.polled, accts.failed)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "Orchard") {
		t.Fatalf("the result carries titles: %s", raw)
	}
}

// A large library goes in calls of at most 500 titles, one poll id, only the
// last final.
func TestPollChunksTheTitles(t *testing.T) {
	t.Parallel()
	accts := &stubAccounts{acct: account()}
	for i := range 1201 {
		accts.recs = append(accts.recs, source.Record{ASIN: fmt.Sprintf("B%09d", i), Title: fmt.Sprintf("Title %d", i), Author: "Someone"})
	}
	fs := &fakeServer{pageSize: 500}
	res, err := run(t, setup(t, fs, accts), "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 3 || len(fs.calls[0].Titles) != 500 || len(fs.calls[1].Titles) != 500 || len(fs.calls[2].Titles) != 201 {
		t.Fatalf("calls = %d", len(fs.calls))
	}
	for i, c := range fs.calls {
		if c.PollID != res.PollID || c.Final != (i == 2) {
			t.Fatalf("call %d = %s final %v", i, c.PollID, c.Final)
		}
	}
}

// An empty library still ends the poll with a final call (every earlier title
// becomes gone).
func TestPollEmptyLibraryIsFinal(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{pageSize: 500}
	if _, err := run(t, setup(t, fs, &stubAccounts{acct: account()}), "acct0001"); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 1 || !fs.calls[0].Final || fs.calls[0].Titles == nil || len(fs.calls[0].Titles) != 0 {
		t.Fatalf("calls = %+v", fs.calls)
	}
}

// The poll keeps to what the server takes: an anthology's credits are cut to
// maxAuthors, a book at the library's root (".") is matched and enriched as
// ".", and a preorder's ASIN never goes onto a book (it isn't out, so that is another
// edition).
func TestPollKeepsToTheServersRules(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{pageSize: 500, books: []link.LibraryBook{
		{Path: ".", Title: "The Whole Shelf", Authors: []string{"Ada Root"}},
		{Path: "Quill/Canopy", Title: "The Golden Canopy", Authors: []string{"Mara Quill"}},
	}}
	many := make([]string, 0, 25)
	for i := range 25 {
		many = append(many, fmt.Sprintf("Author %d", i))
	}
	accts := &stubAccounts{acct: account(), recs: []source.Record{
		{ASIN: "B0FAKE0040", Title: "Forty Voices", Author: "Author 0", Authors: many},
		{ASIN: "B0FAKE0041", Title: "The Whole Shelf", Author: "Ada Root"},
		{ASIN: "B0FAKE0005", Title: "The Golden Canopy", Author: "Mara Quill", UnavailableReason: "Not released yet (pre-order)",
			Preorder: true, ReleaseDate: "2027-01-01"},
	}}
	res, err := run(t, setup(t, fs, accts), "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("calls = %+v", fs.calls)
	}
	byASIN := map[string]link.AudibleTitle{}
	for _, tt := range fs.calls[0].Titles {
		byASIN[tt.ASIN] = tt
	}
	if a := byASIN["B0FAKE0040"].Authors; len(a) != maxAuthors || a[0] != "Author 0" || a[maxAuthors-1] != "Author 19" {
		t.Fatalf("authors = %v", a)
	}
	if m := byASIN["B0FAKE0041"].Match; m == nil || m.Path != "." {
		t.Fatalf("the library's root match = %+v", m)
	}
	if p := byASIN["B0FAKE0005"]; p.State != link.AudiblePreorder || p.Match == nil || p.Match.How != link.MatchFuzzy {
		t.Fatalf("preorder = %+v", p)
	}
	if len(fs.enriched) != 1 || fs.enriched["."] != "B0FAKE0041" || res.Enriched != 1 || res.Matched != 2 {
		t.Fatalf("enriched %v, result %+v", fs.enriched, res)
	}
}

func TestMatchable(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		".": true, "Quill/Canopy": true, "Canopy": true,
		"": false, "..": false, "../Canopy": false, "Quill/../Canopy": false, "/Quill/Canopy": false,
		"Quill//Canopy": false, "Quill/Canopy/": false, "./Canopy": false, "Quill/.": false,
	} {
		if got := matchable(p); got != want {
			t.Errorf("matchable(%q) = %v, want %v", p, got, want)
		}
	}
}

// A poll stopped, cancelled or lost mid-way records no failure on the account:
// the job ends by its context's cause.
func TestPollCancelledRecordsNoFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	accts := &stubAccounts{acct: account(), libErr: context.Canceled}
	h := setup(t, &fakeServer{pageSize: 500}, accts)
	params, _ := json.Marshal(Params{Account: "acct0001"})
	if _, err := h.Run(ctx, jobs.Job{ID: 7, Kind: Kind, Params: params, Attempt: 1}, jobs.LocalReporter()); err == nil {
		t.Fatal("a cancelled poll succeeded")
	}
	if len(accts.failed) != 0 {
		t.Fatalf("a cancelled poll recorded %v", accts.failed)
	}
}

func TestPollRefusals(t *testing.T) {
	t.Parallel()
	// Audible refuses: nothing reported to the server, the refusal is the error.
	accts := &stubAccounts{acct: account(), libErr: fmt.Errorf("%w (HTTP 401)", audibleacct.ErrRefused)}
	fs := &fakeServer{pageSize: 500}
	h := setup(t, fs, accts)
	if _, err := run(t, h, "acct0001"); !errors.Is(err, audibleacct.ErrRefused) {
		t.Fatalf("refused = %v", err)
	}
	if len(fs.calls) != 0 || len(accts.failed) != 0 {
		t.Fatalf("calls %d failed %v", len(fs.calls), accts.failed)
	}
	// Another Audible failure is recorded on the account.
	accts.libErr = errors.New("connection reset")
	if _, err := run(t, h, "acct0001"); err == nil || len(accts.failed) != 1 {
		t.Fatalf("a failure = %v, %v", err, accts.failed)
	}
	// No such account here.
	if _, err := run(t, h, "nosuch01"); err == nil || !strings.Contains(err.Error(), "no Audible account") {
		t.Fatalf("unknown = %v", err)
	}
	// Bad params.
	for _, raw := range []string{`{"account":"acct0001","library_id":1}`, `{"account":"../x"}`, `nope`} {
		if _, err := h.Run(context.Background(), jobs.Job{ID: 1, Params: json.RawMessage(raw)}, jobs.LocalReporter()); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
	// Mapped on another server.
	accts.libErr = nil
	accts.acct.ServerID = "srv-2"
	if _, err := run(t, h, "acct0001"); err == nil || !strings.Contains(err.Error(), "another server") {
		t.Fatalf("another server = %v", err)
	}
	// The server lost the account.
	accts.acct.ServerID = "srv-1"
	fs.titleErr = http.StatusNotFound
	if _, err := run(t, h, "acct0001"); err == nil || !strings.Contains(err.Error(), "no such Audible account") {
		t.Fatalf("unknown on the server = %v", err)
	}
	// Unpaired.
	h.o.Server = func() Server { return nil }
	if _, err := run(t, h, "acct0001"); err == nil {
		t.Fatal("ran unpaired")
	}
}

func TestHandlerShape(t *testing.T) {
	t.Parallel()
	h := New(Options{})
	if h.Kind() != "audible.poll" || h.Lane() != jobs.LaneLight {
		t.Fatalf("%s %s", h.Kind(), h.Lane())
	}
	id := pollID(jobs.Job{ID: 12, Attempt: 3})
	if !strings.HasPrefix(id, "j12.3.") || len(id) > 64 {
		t.Fatalf("poll id %q", id)
	}
}

// amazonRefusal is a real refusal's shape with a made-up customer id.
const amazonRefusal = "Customer is not part of any plans; Customer id [A1B2C3D4E5F6G7] being throttled; " +
	"urn:cdo:AudibleApiExternalRouterService:Prod:Default[AAA], does not has access to asin[B00H3R0B0W]."

// Denied: Amazon's text reaching the poll unredacted (belt and braces: the
// client redacts it first) leaves the customer id out of the job's error and
// the account's recorded failure; the reason stays.
func TestPollFailureRedactsAccountIDs(t *testing.T) {
	t.Parallel()
	accts := &stubAccounts{acct: account(), libErr: errors.New(amazonRefusal)}
	h := setup(t, &fakeServer{pageSize: 500}, accts)
	_, err := run(t, h, "acct0001")
	if err == nil || len(accts.failed) != 1 {
		t.Fatalf("a failure = %v, %v", err, accts.failed)
	}
	for _, s := range []string{err.Error(), accts.failed[0]} {
		if strings.Contains(s, "A1B2C3D4E5F6G7") || !strings.Contains(s, "being throttled") || !strings.Contains(s, "B00H3R0B0W") {
			t.Fatalf("not redacted, or the reason lost: %s", s)
		}
	}
}
