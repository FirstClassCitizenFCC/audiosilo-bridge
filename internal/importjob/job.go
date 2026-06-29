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
}

// Plan computes a destination and an existence check for each record against a
// library. For series books it fetches the existing siblings so the placement
// mirrors the established folder/shortcode convention.
func Plan(ctx context.Context, c *serverapi.Client, libraryID int64, recs []source.Record, opts PlanOptions) ([]PlannedItem, error) {
	items := make([]PlannedItem, 0, len(recs))
	for _, rec := range recs {
		var siblings []placement.Sibling
		if strings.TrimSpace(rec.Series) != "" {
			found, err := FindSiblings(ctx, c, libraryID, rec.Author, rec.Series)
			if err != nil {
				return nil, err
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
			Filename:    filepath.Base(rec.SrcPath),
			ASIN:        rec.ASIN,
			Narrator:    rec.Narrator,
			Year:        rec.Year,
			Siblings:    siblings,
		})
		if err != nil {
			return nil, err
		}
		items = append(items, PlannedItem{
			Record:         rec,
			RelPath:        path.Join(sug.RelPath, sug.Filename),
			Kind:           sug.Kind.String(),
			Note:           sug.Note,
			ExistsOnServer: existsOnServer(ctx, c, rec),
		})
	}
	return items, nil
}

func folderTitle(rec source.Record) string {
	if t := strings.TrimSpace(rec.TitleShort); t != "" {
		return t
	}
	return rec.Title
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
