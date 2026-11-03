package audiblejob

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

// backfill is a library of n books without an ASIN and the n titles that match
// them, one each.
func backfill(n int) (*fakeServer, *stubAccounts) {
	fs := &fakeServer{pageSize: 500}
	accts := &stubAccounts{acct: account()}
	for i := range n {
		title := fmt.Sprintf("Quiet Harbour Chronicle %c", 'A'+i)
		fs.books = append(fs.books, link.LibraryBook{Path: "Brook/" + strconv.Itoa(i), Title: title, Authors: []string{"Wren Brook"}})
		accts.recs = append(accts.recs, source.Record{ASIN: fmt.Sprintf("B0PACE%04d", i), Title: title, Author: "Wren Brook"})
	}
	return fs, accts
}

// clock is a fake time the stub sleep advances, recording each wait.
type clock struct {
	t     time.Time
	waits []time.Duration
}

func (c *clock) install(h *Handler) {
	c.t = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return c.t }
	h.sleep = func(ctx context.Context, d time.Duration) error {
		c.waits = append(c.waits, d)
		c.t = c.t.Add(d)
		return ctx.Err()
	}
}

// TestEnrichPaced: the PUTs go out enrichGap apart, never back to back.
func TestEnrichPaced(t *testing.T) {
	t.Parallel()
	fs, accts := backfill(4)
	h := setup(t, fs, accts)
	var c clock
	c.install(h)
	res, err := run(t, h, "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	if res.Enriched != 4 || res.EnrichFailed != 0 || res.EnrichDeferred != 0 || len(fs.enriched) != 4 {
		t.Fatalf("result = %+v, enriched %v", res, fs.enriched)
	}
	if len(c.waits) != 3 {
		t.Fatalf("waits = %v, want 3 gaps", c.waits)
	}
	for _, w := range c.waits {
		if w != enrichGap {
			t.Fatalf("waits = %v, want %v each", c.waits, enrichGap)
		}
	}
}

// TestEnrichHonoursRetryAfter: a 429 waits its Retry-After, slows the pace and
// tries the same book again; none is lost or failed.
func TestEnrichHonoursRetryAfter(t *testing.T) {
	t.Parallel()
	fs, accts := backfill(4)
	fs.limit = func(n int) (string, bool) {
		switch n {
		case 2:
			return "3", true
		case 3:
			return "-", true // no Retry-After: the backoff
		}
		return "", false
	}
	h := setup(t, fs, accts)
	var c clock
	c.install(h)
	res, err := run(t, h, "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	if res.Enriched != 4 || res.EnrichFailed != 0 || res.EnrichDeferred != 0 || len(fs.enriched) != 4 {
		t.Fatalf("result = %+v, enriched %v", res, fs.enriched)
	}
	// The second book: gap, 429 -> its Retry-After (3 s), 429 again -> the
	// backoff (the second in a row: 4 s), then through; the last two at the
	// slower pace (two 429s doubled the gap twice).
	want := []time.Duration{enrichGap, 3 * time.Second, 2 * enrichBackoff, 4 * enrichGap, 4 * enrichGap}
	if fmt.Sprint(c.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", c.waits, want)
	}
}

// TestEnrichDefersTheRest: a Retry-After past enrichMaxWait, too many 429s in
// a row, or the poll's time running out leave the rest to the next poll
// (counted, not failed), and a next poll finishes the backfill.
func TestEnrichDefersTheRest(t *testing.T) {
	t.Parallel()
	t.Run("long Retry-After", func(t *testing.T) {
		t.Parallel()
		fs, accts := backfill(5)
		fs.limit = func(n int) (string, bool) { return "600", n == 3 }
		h := setup(t, fs, accts)
		var c clock
		c.install(h)
		res, err := run(t, h, "acct0001")
		if err != nil {
			t.Fatal(err)
		}
		if res.Enriched != 2 || res.EnrichDeferred != 3 || res.EnrichFailed != 0 {
			t.Fatalf("result = %+v", res)
		}
		for _, w := range c.waits {
			if w > enrichMaxWait {
				t.Fatalf("waited %v", w)
			}
		}
		// The next poll: the enriched books carry their ASIN now (an ASIN
		// match, no PUT), the rest get theirs.
		fs.mu.Lock()
		for i := range fs.books {
			if a, ok := fs.enriched[fs.books[i].Path]; ok {
				fs.books[i].ASIN = a
			}
		}
		fs.limit = nil
		fs.mu.Unlock()
		res, err = run(t, h, "acct0001")
		if err != nil {
			t.Fatal(err)
		}
		if res.Enriched != 3 || res.EnrichDeferred != 0 || res.MatchedASIN != 2 || len(fs.enriched) != 5 {
			t.Fatalf("the next poll = %+v, enriched %v", res, fs.enriched)
		}
	})
	t.Run("429s in a row", func(t *testing.T) {
		t.Parallel()
		fs, accts := backfill(3)
		fs.limit = func(n int) (string, bool) { return "1", n > 1 }
		h := setup(t, fs, accts)
		var c clock
		c.install(h)
		res, err := run(t, h, "acct0001")
		if err != nil {
			t.Fatal(err)
		}
		if res.Enriched != 1 || res.EnrichDeferred != 2 || res.EnrichFailed != 0 || fs.enrichN != 2+enrichMaxLimited {
			t.Fatalf("result = %+v after %d calls", res, fs.enrichN)
		}
	})
	t.Run("the poll's time runs out", func(t *testing.T) {
		t.Parallel()
		fs, accts := backfill(4)
		h := setup(t, fs, accts)
		var c clock
		c.install(h)
		h.sleep = func(ctx context.Context, d time.Duration) error {
			c.t = c.t.Add(enrichBudget / 2) // each wait is half the budget
			return ctx.Err()
		}
		res, err := run(t, h, "acct0001")
		if err != nil {
			t.Fatal(err)
		}
		if res.Enriched != 2 || res.EnrichDeferred != 2 {
			t.Fatalf("result = %+v", res)
		}
	})
}

// TestEnrichCancelled: a job cancelled while it waits ends with its context's
// error and records no failure.
func TestEnrichCancelled(t *testing.T) {
	t.Parallel()
	fs, accts := backfill(3)
	h := setup(t, fs, accts)
	h.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := run(t, h, "acct0001"); err == nil {
		t.Fatal("a cancelled wait finished the poll")
	}
	if len(accts.failed) != 0 || len(fs.enriched) != 1 {
		t.Fatalf("failed %v, enriched %v", accts.failed, fs.enriched)
	}
}

// TestPollContestedKeepsTheBest: titles claiming one book keep the match on
// the best score only; a tie between two works keeps none; the same work
// owned twice keeps both; the losers go out with the reason contested; a
// contested book is never enriched.
func TestPollContestedKeepsTheBest(t *testing.T) {
	t.Parallel()
	fs := &fakeServer{pageSize: 500, books: []link.LibraryBook{
		{Path: "Val/Road to Craft/RC03", Title: "Road to Craft 3", Authors: []string{"Val Erio"},
			Series: []link.SeriesRef{{Name: "Road to Craft", Position: "3"}}},
		{Path: "Aar/Lantern Street/LS02 - Moon over Tidewater", Title: "Moon over Tidewater: Lanterns of London, Book 2",
			Authors: []string{"Ben Aar"}, Series: []link.SeriesRef{{Name: "Lantern Street", Position: ""}}},
		{Path: "Prat/Discs/D03 - Equal Measures", Title: "Equal Measures", Authors: []string{"Tess Prat"},
			Series: []link.SeriesRef{{Name: "Discs", Position: "3"}}},
	}}
	accts := &stubAccounts{acct: account(), recs: []source.Record{
		{ASIN: "B0CONT0001", Title: "Road to Craft", Author: "Val Erio", Series: "Road to Craft", SeriesPosition: "1", Sequence: 1, HasSequence: true},
		{ASIN: "B0CONT0003", Title: "Road to Craft 3", Author: "Val Erio", Series: "Road to Craft", SeriesPosition: "3", Sequence: 3, HasSequence: true},
		{ASIN: "B0CONT0011", Title: "Lanterns of London", Author: "Ben Aar", Series: "Lanterns of London", SeriesPosition: "1", Sequence: 1, HasSequence: true},
		{ASIN: "B0CONT0012", Title: "Moon over Tidewater", Author: "Ben Aar", Series: "Lanterns of London", SeriesPosition: "2", Sequence: 2, HasSequence: true},
		{ASIN: "B0CONT0021", Title: "Equal Measures", Author: "Tess Prat", Series: "Discs", SeriesPosition: "3", Sequence: 3, HasSequence: true},
		{ASIN: "B0CONT0022", Title: "Equal Measures", Author: "Tess Prat", Series: "Discs", SeriesPosition: "3", Sequence: 3, HasSequence: true},
		{ASIN: "B0CONT0031", Title: "A Title Nowhere", Author: "Tess Prat"},
	}}
	h := setup(t, fs, accts)
	res, err := run(t, h, "acct0001")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tt := range fs.calls[0].Titles {
		if tt.Match != nil {
			got[tt.ASIN] = tt.Match.Path
		}
	}
	want := map[string]string{
		"B0CONT0003": "Val/Road to Craft/RC03",
		"B0CONT0021": "Prat/Discs/D03 - Equal Measures",
		"B0CONT0022": "Prat/Discs/D03 - Equal Measures",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	// The titles that lost a contested book say so; a title that matched
	// nothing, or kept its match, carries no reason.
	for _, tt := range fs.calls[0].Titles {
		wantReason := ""
		switch tt.ASIN {
		case "B0CONT0001", "B0CONT0011", "B0CONT0012":
			wantReason = link.ReasonContested
		}
		if tt.Reason != wantReason || tt.State != link.AudibleAvailable {
			t.Errorf("%s: state %q reason %q, want available %q", tt.ASIN, tt.State, tt.Reason, wantReason)
		}
	}
	if res.Matched != 3 || res.Contested != 3 || res.Enriched != 0 || len(fs.enriched) != 0 {
		t.Fatalf("result = %+v, enriched %v", res, fs.enriched)
	}
}
