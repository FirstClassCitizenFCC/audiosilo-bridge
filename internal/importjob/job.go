package importjob

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/kodestar/audiosilo-manager/internal/placement"
	"github.com/kodestar/audiosilo-manager/internal/serverapi"
	"github.com/kodestar/audiosilo-manager/internal/source"
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
		sug, err := placement.SuggestWith(opts.Mode, opts.Template, placement.Input{
			Author:      rec.Author,
			Title:       folderTitle(rec),
			TitleShort:  rec.TitleShort,
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
			if opts.Books != nil {
				_, exists = MatchInBooks(opts.Books, rec)
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

// MatchInBooks finds the server book corresponding to rec within an already-fetched
// list — by ASIN when both have one, otherwise by normalized author + title. The
// Audible pre-flight and stats sync fetch a library's books once and match locally,
// rather than issuing a search per book.
func MatchInBooks(books []serverapi.Book, rec source.Record) (serverapi.Book, bool) {
	for _, b := range books {
		if rec.ASIN != "" && b.ASIN != "" && strings.EqualFold(rec.ASIN, b.ASIN) {
			return b, true
		}
	}
	for _, b := range books {
		if sameNorm(b.Author, rec.Author) &&
			(sameNorm(b.Title, rec.Title) || sameNorm(b.Title, rec.TitleShort)) {
			return b, true
		}
	}
	return serverapi.Book{}, false
}

func folderTitle(rec source.Record) string {
	if t := strings.TrimSpace(rec.TitleShort); t != "" {
		return t
	}
	return rec.Title
}

// recordFileName is the destination file name: the record's explicit FileName (set
// by download-on-demand sources like Audible), else the source file's basename.
func recordFileName(rec source.Record) string {
	if rec.FileName != "" {
		return rec.FileName
	}
	return filepath.Base(rec.SrcPath)
}

// existsOnServer reports whether the book is already present — matched by ASIN when
// available, otherwise by author + title (tolerant normalization).
func existsOnServer(ctx context.Context, c *serverapi.Client, rec source.Record) bool {
	q := folderTitle(rec)
	if q == "" {
		q = rec.Title
	}
	candidates, err := c.Search(ctx, q, 50)
	if err != nil {
		return false
	}
	for _, cand := range candidates {
		if rec.ASIN != "" && cand.ASIN != "" && strings.EqualFold(rec.ASIN, cand.ASIN) {
			return true
		}
		if sameNorm(cand.Author, rec.Author) &&
			(sameNorm(cand.Title, rec.Title) || sameNorm(cand.Title, rec.TitleShort)) {
			return true
		}
	}
	return false
}

func sameNorm(a, b string) bool { return normalize(a) == normalize(b) && normalize(a) != "" }
