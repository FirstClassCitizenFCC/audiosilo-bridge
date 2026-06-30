package importjob

import (
	"context"
	"testing"

	"github.com/kodestar/audiosilo-manager/internal/placement"
	"github.com/kodestar/audiosilo-manager/internal/serverapi"
	"github.com/kodestar/audiosilo-manager/internal/source"
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

// Plan must make NO server calls when given the library books up front — passing a
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
