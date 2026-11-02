package importjob

import (
	"context"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/serverapi"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

func TestMatchInBooks(t *testing.T) {
	books := []serverapi.Book{
		{RelPath: "Wight/Cradle/C01 - Unsouled", Title: "Unsouled", Author: "Will Wight", ASIN: "B01"},
		{RelPath: "Sanderson/Mistborn", Title: "The Final Empire", Author: "Brandon Sanderson"},
	}

	// ASIN wins.
	if b, ok := MatchInBooks(books, source.Record{ASIN: "B01", Title: "different"}); !ok || b.RelPath != "Wight/Cradle/C01 - Unsouled" {
		t.Fatalf("ASIN match failed: %+v ok=%v", b, ok)
	}
	// Fall back to author + title (tolerant) when no ASIN.
	if b, ok := MatchInBooks(books, source.Record{Author: "brandon sanderson", Title: "the final empire"}); !ok || b.RelPath != "Sanderson/Mistborn" {
		t.Fatalf("author/title match failed: %+v ok=%v", b, ok)
	}
	// TitleShort also considered.
	if _, ok := MatchInBooks(books, source.Record{Author: "Will Wight", TitleShort: "Unsouled"}); !ok {
		t.Fatal("titleShort match failed")
	}
	// No match.
	if _, ok := MatchInBooks(books, source.Record{ASIN: "ZZZ", Author: "Nobody", Title: "Nothing"}); ok {
		t.Fatal("unexpected match")
	}
}

// Real library titles are wildly inconsistent (verified against a 1080-book server):
// full Audible titles with series prefixes + "(Book N)", clean book titles, shortcode
// prefixes, series suffixes, and "Series Name N" with the number only. The server
// rarely has ASINs or a series_index. These cases are drawn from that real data.
func TestMatchInBooksRealWorld(t *testing.T) {
	books := []serverapi.Book{
		{RelPath: "k/dwk05", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", Title: "Diary of a Wimpy Kid: The Ugly Truth (Book 5)"},
		{RelPath: "k/dwk04", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", Title: "Diary Of A Wimpy Kid 4 - Dog Days"},
		{RelPath: "k/dwk15", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", Title: "The Deep End - Diary of a Wimpy Kid Series, Book 15"},
		{RelPath: "a/rrp3", Author: "Actus", Series: "Return of the Runebound Professor", Title: "Return of the Runebound Professor 3"},
		{RelPath: "a/rrp5", Author: "Actus", Series: "Return of the Runebound Professor", Title: "Return of the Runebound Professor 5: A Progression Fantasy Epic"},
		{RelPath: "kay/legion", Author: "A. F. Kay", Series: "Divine Apostasy", Title: "Legion's Fifth Vault"},
		{RelPath: "oster/sand", Author: "Aaron Oster", Series: "Rise to Omniscience", Title: "RO07 - Sandqueen"},
		{RelPath: "oster/silver", Author: "Aaron Oster", Series: "Rise to Omniscience", Title: "Silverspear: Rise to Omniscience, Book 6"},
		{RelPath: "w/unsouled", Author: "Will Wight", Series: "Cradle", Title: "Unsouled", SeriesIndex: 1, ASIN: "B01"},
	}
	cases := []struct {
		name     string
		rec      source.Record
		wantPath string // "" = expect no match
	}{
		{"full title + (Book N)", recWith("Diary of a Wimpy Kid: The Ugly Truth (Book 5)", "Jeff Kinney", "Diary of a Wimpy Kid", 5), "k/dwk05"},
		{"alt scheme N - title", recWith("Diary of a Wimpy Kid: Dog Days (Book 4)", "Jeff Kinney", "Diary of a Wimpy Kid", 4), "k/dwk04"},
		{"series suffix on server", recWith("Diary of a Wimpy Kid: The Deep End (Book 15)", "Jeff Kinney", "Diary of a Wimpy Kid", 15), "k/dwk15"},
		{"series-name-N, residual is just the number", recWith("Return of the Runebound Professor 3", "Actus", "Return of the Runebound Professor", 3), "a/rrp3"},
		{"series-name-N picks the right number", recWith("Return of the Runebound Professor 5", "Actus", "Return of the Runebound Professor", 5), "a/rrp5"},
		{"clean server title vs subtitled audible", recWith("Legion's Fifth Vault: A Fantasy LitRPG Adventure", "A. F. Kay", "Divine Apostasy", 5), "kay/legion"},
		{"shortcode prefix on server", recWith("Sandqueen", "Aaron Oster", "Rise to Omniscience", 7), "oster/sand"},
		{"series suffix on server 2", recWith("Silverspear", "Aaron Oster", "Rise to Omniscience", 6), "oster/silver"},
		{"absent book number does not false-match", recWith("Return of the Runebound Professor 9", "Actus", "Return of the Runebound Professor", 9), ""},
		{"different author no match", recWith("The Ugly Truth", "Someone Else", "Whatever", 5), ""},
	}
	for _, tc := range cases {
		got, ok := MatchInBooks(books, tc.rec)
		if tc.wantPath == "" {
			if ok {
				t.Errorf("%s: expected no match, got %q", tc.name, got.Title)
			}
			continue
		}
		if !ok || got.RelPath != tc.wantPath {
			t.Errorf("%s: got %q (path %q, ok=%v), want path %q", tc.name, got.Title, got.RelPath, ok, tc.wantPath)
		}
	}
	// ASIN still wins outright.
	if got, ok := MatchInBooks(books, source.Record{ASIN: "B01", Title: "anything"}); !ok || got.RelPath != "w/unsouled" {
		t.Errorf("ASIN match failed: %+v ok=%v", got, ok)
	}
}

// recWith builds an Audible record with a series sequence (the common case).
func recWith(title, author, series string, seq float64) source.Record {
	return source.Record{
		Title: title, TitleShort: title, Author: author,
		Series: series, Sequence: seq, HasSequence: true,
	}
}

func TestSiblingsFromBooks(t *testing.T) {
	books := []serverapi.Book{
		{RelPath: "Kinney/Diary of a Wimpy Kid/DWK01", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", SeriesIndex: 1},
		{RelPath: "Kinney/Diary of a Wimpy Kid/DWK09", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", SeriesIndex: 9},
		{RelPath: "Other/Book", Author: "Someone Else", Series: "Other Series"},
	}
	got := SiblingsFromBooks(books, "Jeff Kinney", "Diary of a Wimpy Kid")
	if len(got) != 2 {
		t.Fatalf("want 2 siblings, got %d: %+v", len(got), got)
	}
	if SiblingsFromBooks(books, "Jeff Kinney", "") != nil {
		t.Fatal("a standalone (no series) should have no siblings")
	}
}

// Plan must make NO server calls when given the library books up front - passing a
// nil client proves it (any per-book query would nil-panic). This is the fix for
// the 429 the Audible pre-flight hit on a large library.
func TestPlanUsesLocalBooksNoQueries(t *testing.T) {
	books := []serverapi.Book{
		{RelPath: "Kinney/Diary of a Wimpy Kid/DWK01 - The First", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", SeriesIndex: 1, ASIN: "B1"},
	}
	recs := []source.Record{
		{ASIN: "B1", Title: "The First", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", Sequence: 1, HasSequence: true, FileName: "a.m4b"},
		{ASIN: "B5", Title: "The Ugly Truth", Author: "Jeff Kinney", Series: "Diary of a Wimpy Kid", Sequence: 5, HasSequence: true, FileName: "b.m4b"},
	}
	items, err := Plan(context.Background(), nil, 2, recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !items[0].ExistsOnServer {
		t.Error("book B1 should be matched as already on the server (local match)")
	}
	if items[1].ExistsOnServer {
		t.Error("book B5 should NOT be on the server")
	}
}
