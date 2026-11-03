package audiblesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/jobs"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

const acctID = "abcd1234"

// stubAccounts is the account manager: one account in the store, Audible's
// places, and the writes and content-reference reads asked of it.
type stubAccounts struct {
	db *store.DB

	mu        sync.Mutex
	places    map[string]audible.Position
	placesErr error
	reads     int
	acrs      int
	acrErr    error
	sets      []string // "asin@ms"
	setErr    error
}

func (s *stubAccounts) Account(ctx context.Context, id string) (store.AudibleAccount, error) {
	a, err := s.db.GetAudibleAccount(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return a, audibleacct.ErrNotFound
	}
	return a, err
}

func (s *stubAccounts) Accounts(ctx context.Context) ([]store.AudibleAccount, error) {
	return s.db.AudibleAccounts(ctx)
}

func (s *stubAccounts) Positions(_ context.Context, _ string, asins []string) (map[string]audible.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.placesErr != nil {
		return nil, s.placesErr
	}
	out := map[string]audible.Position{}
	for _, a := range asins {
		if p, ok := s.places[a]; ok {
			out[a] = p
		}
	}
	return out, nil
}

func (s *stubAccounts) ContentACR(_ context.Context, _, asin string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acrs++
	if s.acrErr != nil {
		return "", s.acrErr
	}
	return "CR!" + asin, nil
}

func (s *stubAccounts) SetPosition(_ context.Context, _, asin, acr string, positionMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if acr != "CR!"+asin {
		return fmt.Errorf("wrong acr %q", acr)
	}
	if s.setErr != nil {
		return s.setErr
	}
	s.sets = append(s.sets, fmt.Sprintf("%s@%d", asin, positionMs))
	return nil
}

// stubServer is the server's sync routes: it answers a listen ahead of its
// place applied and one not ahead behind (furthest only), and serves a feed of
// rows with an opaque cursor (the index past the last row read).
type stubServer struct {
	mu       sync.Mutex
	server   map[string]float64 // path -> the server's place
	posted   []link.AudibleListening
	postErr  error
	rows     []link.AudibleProgressRow
	feedErr  error
	feedAsks []string // since values
	pageSize int
}

func (s *stubServer) PostAudibleListening(_ context.Context, account string, in link.AudibleListening) (*link.SyncAnswer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if account != acctID {
		return nil, link.ErrUnknownAccount
	}
	if s.postErr != nil {
		return nil, s.postErr
	}
	s.posted = append(s.posted, in)
	ans := &link.SyncAnswer{ServerTime: "2026-11-03T12:00:00Z"}
	seen := map[string]bool{}
	for _, ev := range in.Events {
		r := link.SyncResult{ID: ev.ID, Status: link.SyncApplied}
		switch {
		case seen[ev.ID]:
			r.Status = link.SyncDuplicate
		case ev.Kind == link.EventListen && *ev.ToPos <= s.server[ev.Path]:
			r.Status, r.Reason = link.SyncNotApplied, link.SyncBehind
		case ev.Kind == link.EventListen:
			s.server[ev.Path] = *ev.ToPos
		}
		seen[ev.ID] = true
		ans.Results = append(ans.Results, r)
	}
	return ans, nil
}

func (s *stubServer) AudibleProgress(_ context.Context, account, since string, limit int) (*link.AudibleProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedAsks = append(s.feedAsks, since)
	if s.feedErr != nil {
		return nil, s.feedErr
	}
	start := 0
	if since != "" {
		_, _ = fmt.Sscanf(since, "c%d", &start)
	}
	size := limit
	if s.pageSize > 0 {
		size = s.pageSize
	}
	end := min(start+size, len(s.rows))
	page := &link.AudibleProgress{Rows: append([]link.AudibleProgressRow{}, s.rows[start:end]...), Next: since, More: end-start == size}
	if end > start {
		page.Next = fmt.Sprintf("c%d", end)
	}
	return page, nil
}

// add appends a server row (the feed's newest change).
func (s *stubServer) add(path, asin string, pos float64, at time.Time, device string) {
	s.mu.Lock()
	s.rows = append(s.rows, link.AudibleProgressRow{LibraryID: 1, Path: path, ASIN: asin, Position: pos, Duration: 36000,
		UpdatedAt: at.UTC().Format(time.RFC3339Nano), DeviceID: device, Version: int64(len(s.rows) + 1)})
	s.mu.Unlock()
}

type env struct {
	db      *store.DB
	accts   *stubAccounts
	srv     *stubServer
	now     time.Time
	changed int
	o       Options
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{db: db, accts: &stubAccounts{db: db, places: map[string]audible.Position{}}, srv: &stubServer{server: map[string]float64{}},
		now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	if err := db.SignInAudibleAccount(context.Background(), store.AudibleAccount{ID: acctID, Label: "Chris (US)", Marketplace: "us",
		ServerID: "srv-1", UserID: 3, LibraryID: 1, Policy: "manual", Sync: true}, e.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.o = Options{Accounts: e.accts, DB: db, Server: func() Server { return e.srv }, ServerID: func() string { return "srv-1" },
		OnChange: func() { e.changed++ }, Now: func() time.Time { return e.now }}
	return e
}

func (e *env) account(t *testing.T) store.AudibleAccount {
	t.Helper()
	a, err := e.db.GetAudibleAccount(context.Background(), acctID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func params(t *testing.T, titles ...link.AudibleSyncTitle) json.RawMessage {
	t.Helper()
	if titles == nil {
		titles = []link.AudibleSyncTitle{}
	}
	raw, err := json.Marshal(link.AudibleSyncParams{Account: acctID, Titles: titles})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func title(asin, path string, runtimeS int64) link.AudibleSyncTitle {
	return link.AudibleSyncTitle{ASIN: asin, LibraryID: 1, Path: path, RuntimeS: runtimeS}
}

func (e *env) runSync(t *testing.T, raw json.RawMessage) (*Result, error) {
	t.Helper()
	out, err := New(e.o).Run(context.Background(), jobs.Job{ID: 7, Kind: Kind, Params: raw, Attempt: 1}, jobs.LocalReporter())
	if err != nil {
		return nil, err
	}
	return out.(*Result), nil
}

var eventIDPattern = regexp.MustCompile(`^aud-[0-9a-f]{32}$`)

// The job sends each Audible place as a listen of no length at the place (a
// finish beside it at the book's end), furthest only on the server: a place
// ahead applies, one behind is "behind", and the same place again is the same
// event. Places at the start, past the runtime (a wrong match) or too old for
// the server are never sent.
func TestSyncSendsAudiblePlaces(t *testing.T) {
	e := newEnv(t)
	heard := e.now.Add(-time.Hour).Truncate(time.Millisecond)
	e.accts.places = map[string]audible.Position{
		"B000000001": {PositionMs: 1_234_500, LastUpdated: heard},                  // ahead of the server's
		"B000000002": {PositionMs: 60_000, LastUpdated: heard},                     // behind it
		"B000000003": {PositionMs: 3_550_000, LastUpdated: heard},                  // at the end of an hour
		"B000000004": {PositionMs: 500, LastUpdated: heard},                        // at the start
		"B000000005": {PositionMs: 9_000_000, LastUpdated: heard},                  // past an hour's runtime
		"B000000006": {PositionMs: 70_000, LastUpdated: heard.AddDate(0, 0, -100)}, // too old
	}
	e.srv.server["A/Two"] = 600
	raw := params(t, title("B000000001", "A/One", 36000), title("B000000002", "A/Two", 36000), title("B000000003", "A/Three", 3600),
		title("B000000004", "A/Four", 3600), title("B000000005", "A/Five", 3600), title("B000000006", "A/Six", 3600),
		title("B000000007", "A/Seven", 3600)) // no place on Audible
	res, err := e.runSync(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Titles != 7 || res.Heard != 6 || res.Sent != 4 || res.Finished != 1 || res.Applied != 3 || res.Behind != 1 ||
		res.SkippedStart != 1 || res.SkippedPastRuntime != 1 || res.SkippedOld != 1 {
		t.Fatalf("result = %+v", res)
	}
	evs := e.srv.posted[0].Events
	one := evs[0]
	stamp := heard.Format(time.RFC3339)
	if one.Kind != link.EventListen || one.Path != "A/One" || *one.FromPos != 1234.5 || *one.ToPos != 1234.5 || one.Start != "jump" ||
		one.StartedAt != stamp || one.EndedAt != stamp || one.At != "" || !eventIDPattern.MatchString(one.ID) ||
		one.ID != EventID(acctID, "B000000001", 1_234_500, heard, "") {
		t.Fatalf("listen = %+v", one)
	}
	fin := evs[3]
	if fin.Kind != link.EventFinish || fin.Path != "A/Three" || fin.At != stamp || fin.FromPos != nil || fin.ID == evs[2].ID ||
		!eventIDPattern.MatchString(fin.ID) {
		t.Fatalf("finish = %+v", fin)
	}
	if _, err := time.Parse(time.RFC3339, e.srv.posted[0].SentAt); err != nil {
		t.Fatalf("sent_at = %q", e.srv.posted[0].SentAt)
	}
	// The place heard is kept for the push's echo guard.
	if p, _ := e.db.GetAudiblePush(context.Background(), acctID, "B000000001"); p.HeardMs != 1_234_500 || !p.Listed || p.RuntimeS != 36000 {
		t.Fatalf("push row = %+v", p)
	}
	if a := e.account(t); a.SyncAt == "" || a.SyncError != "" || a.RefusedAt != "" {
		t.Fatalf("account = %+v", a)
	}

	// The same places again are the same events: the ids repeat.
	res, err = e.runSync(t, raw)
	if err != nil || e.srv.posted[1].Events[0].ID != one.ID || res.Applied != 1 /* the finish */ || res.Behind != 3 {
		t.Fatalf("again = %+v, %v", res, err)
	}
}

// The event id is sha256(account|asin|position_ms|last_updated), cut to 32 hex
// characters: the same place is the same id, a moved or re-heard one another.
func TestEventID(t *testing.T) {
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a := EventID(acctID, "B000000001", 5000, at, "")
	if !eventIDPattern.MatchString(a) || a != EventID(acctID, "B000000001", 5000, at.In(time.FixedZone("x", 3600)), "") {
		t.Fatalf("id = %q", a)
	}
	for _, other := range []string{
		EventID(acctID, "B000000001", 5001, at, ""), EventID(acctID, "B000000002", 5000, at, ""),
		EventID("efgh5678", "B000000001", 5000, at, ""), EventID(acctID, "B000000001", 5000, at.Add(time.Millisecond), ""),
		EventID(acctID, "B000000001", 5000, at, link.EventFinish), EventID(acctID, "B000000001", 5000, time.Time{}, ""),
	} {
		if other == a {
			t.Fatal("two different places share an id")
		}
	}
}

// Denied: a refused account (the "Sign in again" state) doesn't sync: the job
// fails audible_refused (the server reads the code) before asking Audible
// anything. An unknown account, or one mapped on another server, is
// unknown_account.
func TestSyncRefusedAccount(t *testing.T) {
	e := newEnv(t)
	e.accts.places["B000000001"] = audible.Position{PositionMs: 60_000, LastUpdated: e.now}
	for _, kind := range []string{store.RefusalSignIn, store.RefusalDevice} {
		if err := e.db.RefuseAudibleAccount(context.Background(), acctID, kind, "refused", e.now); err != nil {
			t.Fatal(err)
		}
		_, err := e.runSync(t, params(t, title("B000000001", "A/One", 3600)))
		var f *Failure
		if !errors.As(err, &f) || f.Code != CodeRefused {
			t.Fatalf("%s: err = %v", kind, err)
		}
		if e.accts.reads != 0 || len(e.srv.posted) != 0 {
			t.Fatalf("%s: a refused account asked Audible (%d) or posted (%d)", kind, e.accts.reads, len(e.srv.posted))
		}
	}
	// Allowed: signed in again, it syncs.
	if err := e.db.SignInAudibleAccount(context.Background(), e.account(t), e.now); err != nil {
		t.Fatal(err)
	}
	if res, err := e.runSync(t, params(t, title("B000000001", "A/One", 3600))); err != nil || res.Applied != 1 {
		t.Fatalf("signed in again = %+v, %v", res, err)
	}

	raw, _ := json.Marshal(link.AudibleSyncParams{Account: "nope0000", Titles: []link.AudibleSyncTitle{}})
	_, err := e.runSync(t, raw)
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeUnknownAccount {
		t.Fatalf("unknown account = %v", err)
	}
	e.o.ServerID = func() string { return "srv-2" }
	if _, err := e.runSync(t, params(t)); !errors.As(err, &f) || f.Code != CodeUnknownAccount {
		t.Fatalf("another server = %v", err)
	}
}

// Denied: the params are read strictly (an unknown field, a bad account, a
// repeated or malformed ASIN, a title with no book).
func TestSyncParamsStrict(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field": `{"account":"abcd1234","titles":[],"extra":1}`,
		"title field":   `{"account":"abcd1234","titles":[{"asin":"B000000001","library_id":1,"path":"A","runtime_s":1,"x":2}]}`,
		"bad account":   `{"account":"ABC","titles":[]}`,
		"twice":         `{"account":"abcd1234","titles":[{"asin":"B000000001","library_id":1,"path":"A"},{"asin":"B000000001","library_id":1,"path":"B"}]}`,
		"bad asin":      `{"account":"abcd1234","titles":[{"asin":"b01","library_id":1,"path":"A"}]}`,
		"no book":       `{"account":"abcd1234","titles":[{"asin":"B000000001","library_id":0,"path":"A"}]}`,
		"no path":       `{"account":"abcd1234","titles":[{"asin":"B000000001","library_id":1,"path":""}]}`,
	} {
		if _, err := ParseParams(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if p, err := ParseParams(json.RawMessage(`{"account":"abcd1234","titles":[{"asin":"B000000001","library_id":1,"path":"A","runtime_s":60}]}`)); err != nil ||
		len(p.Titles) != 1 || p.Titles[0].RuntimeS != 60 {
		t.Fatalf("good params = %+v, %v", p, err)
	}
}

// A sync error never refuses the account: Audible asking to wait records the
// wait (its Retry-After) and the next job doesn't call Audible until it has
// passed; a sync turned off meanwhile ends the job quietly.
func TestSyncErrorBacksOffWithoutRefusing(t *testing.T) {
	e := newEnv(t)
	e.accts.places["B000000001"] = audible.Position{PositionMs: 60_000, LastUpdated: e.now}
	e.accts.placesErr = retryErr(t, http.StatusTooManyRequests, "600")
	raw := params(t, title("B000000001", "A/One", 3600))
	_, err := e.runSync(t, raw)
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeAudible {
		t.Fatalf("err = %v", err)
	}
	a := e.account(t)
	if a.RefusedAt != "" || a.SyncError == "" || a.SyncRetryAt != store.Timestamp(e.now.Add(10*time.Minute)) || e.changed != 1 {
		t.Fatalf("after a 429: %+v (changed %d)", a, e.changed)
	}
	e.accts.placesErr = nil
	res, err := e.runSync(t, raw)
	if err != nil || !res.Waiting || e.accts.reads != 1 {
		t.Fatalf("while waiting = %+v, %v (reads %d)", res, err, e.accts.reads)
	}
	e.now = e.now.Add(11 * time.Minute)
	if res, err := e.runSync(t, raw); err != nil || res.Applied != 1 {
		t.Fatalf("after the wait = %+v, %v", res, err)
	}
	if a := e.account(t); a.SyncError != "" || a.SyncRetryAt != "" || e.changed != 2 {
		t.Fatalf("after it went through: %+v (changed %d)", a, e.changed)
	}

	// The server's sync off since the job was queued: done, nothing recorded.
	e.srv.postErr = link.ErrSyncOff
	e.accts.places["B000000001"] = audible.Position{PositionMs: 90_000, LastUpdated: e.now}
	if res, err := e.runSync(t, raw); err != nil || !res.SyncOff {
		t.Fatalf("sync off = %+v, %v", res, err)
	}
	// A person who can't be synced for: a coded failure, recorded, not refused.
	e.srv.postErr = link.ErrUserUnavailable
	if _, err := e.runSync(t, raw); !errors.As(err, &f) || f.Code != CodeUserUnavailable {
		t.Fatalf("user unavailable = %v", err)
	}
	if a := e.account(t); a.RefusedAt != "" || a.SyncError == "" {
		t.Fatalf("after user_unavailable: %+v", a)
	}
}

// retryErr is an Audible 429/503 answer the client gave up on, made by the
// real client against a server that answers it.
func retryErr(t *testing.T, status int, retryAfter string) error {
	t.Helper()
	return audibleAnswer(t, status, retryAfter)
}

// The listening calls carry at most link.MaxAudibleEvents events each.
func TestSyncBatchesEvents(t *testing.T) {
	e := newEnv(t)
	var titles []link.AudibleSyncTitle
	for i := range 450 {
		asin := fmt.Sprintf("B%09d", i)
		titles = append(titles, title(asin, "A/"+asin, 36000))
		e.accts.places[asin] = audible.Position{PositionMs: 60_000, LastUpdated: e.now}
	}
	res, err := e.runSync(t, params(t, titles...))
	if err != nil || res.Sent != 450 || len(e.srv.posted) != 3 || len(e.srv.posted[0].Events) != link.MaxAudibleEvents {
		t.Fatalf("result = %+v, %v (%d calls)", res, err, len(e.srv.posted))
	}
}

// The job's result and failures carry no title or credential.
func TestSyncFailureRedacts(t *testing.T) {
	f := fail(CodeAudible, "Audible didn't answer: %v", errors.New("customer_id=A1B2C3D4E5F6G7 refused"))
	if strings.Contains(f.Message, "A1B2C3D4E5F6G7") {
		t.Fatalf("failure = %q", f.Message)
	}
}
