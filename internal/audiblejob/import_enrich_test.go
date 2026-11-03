package audiblejob

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

// pack is a box set of the Orchard Cycle's first three books: a range
// position and no single sequence, as Audible lists one.
func pack() source.Record {
	r := rec("B0FAKE0030", "The Orchard Cycle: Books 1-3", "Mara Quill", "Orchard Cycle", 0)
	r.SeriesPosition = "1-3"
	return r
}

// The book a placed file lands in takes the title's ASIN only when it is the
// title: a new book (a pack placed as a pack included), or a book the library
// had that the poll's matcher would give the title. A file that joins another
// book (a dest_path into its folder) leaves that book's ASIN alone.
func TestImportEnrichesOnlyTheTitlesBook(t *testing.T) {
	copper := link.LibraryBook{Path: "Mara Quill/Orchard Cycle/OC02 - The Copper Grove", Title: "The Copper Grove",
		Authors: []string{"Mara Quill"}, Series: []link.SeriesRef{{Name: "Orchard Cycle", Position: "2"}}}
	for _, tc := range []struct {
		name   string
		title  source.Record
		books  []link.LibraryBook
		dest   string
		enrich bool
	}{
		{name: "a new book", title: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2), enrich: true},
		{name: "a pack placed as a pack", title: pack(), enrich: true},
		{name: "a pack joins a single volume", title: pack(), dest: "Mara Quill/Orchard Cycle/OC01 - The Glass Orchard"},
		{name: "a title joins another book", title: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2),
			dest: "Mara Quill/Orchard Cycle/OC03 - The Iron Thicket"},
		{name: "a title joins a disc folder of another book", title: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2),
			dest: "Mara Quill/Orchard Cycle/OC03 - The Iron Thicket/Disc 2"},
		{name: "a title joins its own book", title: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2),
			books: append(goldenBooks(), copper), dest: copper.Path, enrich: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newImportEnv(t)
			e.accts.title = tc.title
			if tc.books != nil {
				e.srv.books = tc.books
			}
			res, err := e.run(context.Background(), e.params(func(p *ImportParams) { p.ASIN = tc.title.ASIN; p.DestPath = tc.dest }))
			if err != nil {
				t.Fatal(err)
			}
			got := res.(*ImportResult)
			if got.Enriched != tc.enrich {
				t.Fatalf("enriched = %v, want %v (result %s)", got.Enriched, tc.enrich, mustJSON(t, got))
			}
			if tc.enrich {
				if want := got.Path + "=" + tc.title.ASIN; !slices.Equal(e.srv.enriched, []string{want}) {
					t.Fatalf("enriched %v, want %s", e.srv.enriched, want)
				}
				return
			}
			if len(e.srv.enriched) != 0 || slices.Contains(e.steps, StepEnrich) {
				t.Fatalf("enriched %v, steps %v: the joined book took the title's ASIN", e.srv.enriched, e.steps)
			}
			if len(e.libraryFiles()) != 1 || len(e.srv.rescanned) != 1 {
				t.Fatalf("files %v, rescanned %v: the book must still be placed and indexed", e.libraryFiles(), e.srv.rescanned)
			}
		})
	}
}

// A retry after the install keeps the first run's verdict on the ASIN: the
// marker carries it, so the joined book isn't given it on the second try.
func TestImportRetryKeepsNoEnrich(t *testing.T) {
	e := newImportEnv(t)
	e.accts.title = pack()
	e.srv.rescanErr = []error{errors.New("busy"), errors.New("busy"), errors.New("busy")}
	params := e.params(func(p *ImportParams) {
		p.ASIN = "B0FAKE0030"
		p.DestPath = "Mara Quill/Orchard Cycle/OC01 - The Glass Orchard"
	})
	if _, err := e.run(context.Background(), params); err == nil {
		t.Fatal("the rescan failing must fail the job")
	}
	res, err := e.run(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if res.(*ImportResult).Enriched || len(e.srv.enriched) != 0 || e.accts.downloads != 1 {
		t.Fatalf("result %s, enriched %v, downloads %d", mustJSON(t, res), e.srv.enriched, e.accts.downloads)
	}
}

func tooMany(after time.Duration) error {
	return &link.APIError{Status: http.StatusTooManyRequests, RetryAfter: after}
}

// The import's one enrichment PUT waits out the server's 429s as the poll's
// does: Retry-After when sent, else the doubling backoff; after
// enrichMaxLimited in a row the book is placed without the ASIN, no failure.
func TestImportEnrichPacedOn429(t *testing.T) {
	t.Run("Retry-After then accepted", func(t *testing.T) {
		e := newImportEnv(t)
		e.srv.enrichErrs = []error{tooMany(3 * time.Second), tooMany(0)}
		res, err := e.run(context.Background(), e.params(nil))
		if err != nil {
			t.Fatal(err)
		}
		if !res.(*ImportResult).Enriched || len(e.srv.enriched) != 3 {
			t.Fatalf("result %s, enriched %v", mustJSON(t, res), e.srv.enriched)
		}
		if want := []time.Duration{3 * time.Second, 2 * enrichBackoff}; !slices.Equal(e.waits, want) {
			t.Fatalf("waits %v, want %v", e.waits, want)
		}
	})
	t.Run("always 429", func(t *testing.T) {
		e := newImportEnv(t)
		e.srv.enrichErr = tooMany(0)
		res, err := e.run(context.Background(), e.params(nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.(*ImportResult).Enriched || len(e.srv.enriched) != enrichMaxLimited {
			t.Fatalf("result %s, %d PUTs, want %d", mustJSON(t, res), len(e.srv.enriched), enrichMaxLimited)
		}
		want := []time.Duration{enrichBackoff, 2 * enrichBackoff, 4 * enrichBackoff, 8 * enrichBackoff}
		if !slices.Equal(e.waits, want) {
			t.Fatalf("waits %v, want %v", e.waits, want)
		}
		if res.(*ImportResult).Path != path.Dir(copperGrove) {
			t.Fatalf("path = %s", res.(*ImportResult).Path)
		}
	})
	t.Run("a Retry-After past the cap", func(t *testing.T) {
		e := newImportEnv(t)
		e.srv.enrichErr = tooMany(enrichMaxWait + time.Second)
		res, err := e.run(context.Background(), e.params(nil))
		if err != nil || res.(*ImportResult).Enriched || len(e.srv.enriched) != 1 || len(e.waits) != 0 {
			t.Fatalf("res %v, err %v, PUTs %v, waits %v", res, err, e.srv.enriched, e.waits)
		}
	})
}
