package importjob

import (
	"testing"

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
