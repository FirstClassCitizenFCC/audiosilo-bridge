package importjob

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/kodestar/audiosilo-manager/internal/placement"
	"github.com/kodestar/audiosilo-manager/internal/serverapi"
	"github.com/kodestar/audiosilo-manager/internal/source"
	"github.com/kodestar/audiosilo-server/pkg/match"
)

// PlannedItem is one import candidate with its computed destination and whether it
// already exists on the server.
type PlannedItem struct {
	Record         source.Record
	RelPath        string // suggested destination (folder + filename), library-relative
	Kind           string // placement kind (series existing/new / standalone)
	Note           string // human explanation of the placement
	ExistsOnServer bool   // already indexed on the server (matched by ASIN or author+title)
}

// PlanOptions configures Plan.
type PlanOptions struct {
	Mode     placement.Mode
	Template string // used when Mode == placement.ModeTemplate
	// SkipExistsCheck computes destinations without the per-book server search.
	// Used by the Audible pre-flight, which checks existence in bulk instead (see
	// MatchInBooks) so it doesn't issue one search per book.
	SkipExistsCheck bool
	// Books is the library's books, pre-fetched in one call. When set, Plan matches
	// siblings AND existence locally against this list instead of issuing a query
	// per record — essential for a large library, where per-book queries trip the
	// server's rate limiter (HTTP 429). When nil, Plan queries the server per book.
	Books []serverapi.Book
}

// Plan computes a destination and an existence check for each record against a
// library. For series books it fetches the existing siblings so the placement
// mirrors the established folder/shortcode convention.
func Plan(ctx context.Context, c *serverapi.Client, libraryID int64, recs []source.Record, opts PlanOptions) ([]PlannedItem, error) {
	items := make([]PlannedItem, 0, len(recs))
	// Build the bulk-match candidate list once (not per record) for the local
	// existence check.
	var matcher *BookMatcher
	if opts.Books != nil {
		matcher = NewBookMatcher(opts.Books)
	}
	for _, rec := range recs {
		var siblings []placement.Sibling
		if strings.TrimSpace(rec.Series) != "" {
			var found []serverapi.Book
			if opts.Books != nil {
				found = SiblingsFromBooks(opts.Books, rec.Author, rec.Series)
			} else {
				var err error
				if found, err = FindSiblings(ctx, c, libraryID, rec.Author, rec.Series); err != nil {
					return nil, err
				}
			}
			for _, b := range found {
				siblings = append(siblings, placement.Sibling{RelPath: b.RelPath, SeriesIndex: b.SeriesIndex})
			}
		}
		// Name by the clean book title — strip the series prefix and "(Book N)" /
		// "(Unabridged)" fluff so a series book lands as "SHORTCODE## - The Ugly
		// Truth", not "… - Diary of a Wimpy Kid: The Ugly Truth (Book 5)".
		clean := match.CleanTitle(rec.Title, rec.Series)
		sug, err := placement.SuggestWith(opts.Mode, opts.Template, placement.Input{
			Author:      rec.Author,
			Title:       clean,
			TitleShort:  clean,
			SeriesName:  rec.Series,
			Sequence:    rec.Sequence,
			HasSequence: rec.HasSequence,
			Filename:    recordFileName(rec),
			ASIN:        rec.ASIN,
			Narrator:    rec.Narrator,
			Year:        rec.Year,
			Siblings:    siblings,
		})
		if err != nil {
			return nil, err
		}
		exists := false
		if !opts.SkipExistsCheck {
			if matcher != nil {
				_, exists = matcher.Match(rec)
			} else {
				exists = existsOnServer(ctx, c, rec)
			}
		}
		items = append(items, PlannedItem{
			Record:         rec,
			RelPath:        path.Join(sug.RelPath, sug.Filename),
			Kind:           sug.Kind.String(),
			Note:           sug.Note,
			ExistsOnServer: exists,
		})
	}
	return items, nil
}

// BookMatcher matches records against a fixed server-book list via the shared
// pkg/match matcher. It converts the candidate slice once so matching many records
// against the same library is O(library) instead of O(records × library) — the
// pre-flight and stats-sync loops match the whole Audible library against it.
type BookMatcher struct {
	books []serverapi.Book
	cands []match.Book
}

// NewBookMatcher builds a matcher over an already-fetched library book list.
func NewBookMatcher(books []serverapi.Book) *BookMatcher {
	cands := make([]match.Book, len(books))
	for i, b := range books {
		cands[i] = match.Book{Title: b.Title, Author: b.Author, Series: b.Series, SeriesIndex: b.SeriesIndex, ASIN: b.ASIN}
	}
	return &BookMatcher{books: books, cands: cands}
}

// Match finds the server book corresponding to rec (ASIN → author + series +
// series-stripped title-token overlap, sequence parsed from the title when the
// server's index is 0). The matcher is shared with the server so both sides
// identify the same book identically.
func (m *BookMatcher) Match(rec source.Record) (serverapi.Book, bool) {
	i, ok := match.Best(m.cands, match.Query{
		Title: rec.Title, TitleShort: rec.TitleShort, Author: rec.Author,
		Series: rec.Series, Sequence: rec.Sequence, HasSequence: rec.HasSequence, ASIN: rec.ASIN,
	})
	if !ok {
		return serverapi.Book{}, false
	}
	return m.books[i], true
}

// MatchInBooks is a one-shot match over books (the per-book existence path). For
// repeated matching against the same library, build a BookMatcher once and reuse it.
func MatchInBooks(books []serverapi.Book, rec source.Record) (serverapi.Book, bool) {
	return NewBookMatcher(books).Match(rec)
}

func recordFileName(rec source.Record) string {
	if rec.FileName != "" {
		return rec.FileName
	}
	return filepath.Base(rec.SrcPath)
}

// existsOnServer reports whether the book is already present — matched by ASIN when
// available, otherwise by author + title (tolerant normalization). Used only on the
// per-book query path (no pre-fetched book list); the bulk path uses MatchInBooks.
func existsOnServer(ctx context.Context, c *serverapi.Client, rec source.Record) bool {
	q := strings.TrimSpace(rec.TitleShort)
	if q == "" {
		q = rec.Title
	}
	candidates, err := c.Search(ctx, q, 50)
	if err != nil {
		return false
	}
	_, ok := MatchInBooks(candidates, rec)
	return ok
}
