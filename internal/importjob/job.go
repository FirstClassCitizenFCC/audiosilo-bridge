package importjob

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/source"
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
	// SkipExistsCheck computes destinations without the existence check (a caller
	// that matched the records itself, see BookMatcher).
	SkipExistsCheck bool
	// Books is the library's books (the server's paged listing, read whole). Plan
	// matches siblings AND existence locally against it; nil is an empty library.
	// (The manager could query the server per record; the Bridge always holds the
	// listing, so there is no per-record path.)
	Books []link.LibraryBook
	// Rejected marks records (by Record.Key - the ASIN for Audible records) whose
	// automatic server match the user has declared wrong ("Not on server"). Plan
	// reports them as not existing, so Run imports them instead of skipping them -
	// folded into planning so every caller gets correct items by construction.
	Rejected map[string]bool
}

// Plan computes a destination and an existence check for each record against a
// library's books (opts.Books). For series books the existing siblings anchor the
// placement so it mirrors the established folder/shortcode convention.
func Plan(recs []source.Record, opts PlanOptions) ([]PlannedItem, error) {
	items := make([]PlannedItem, 0, len(recs))
	// Build the bulk-match candidate list once (not per record) for the existence
	// check, and authorFolders (normalized author -> existing folder spelling) so a
	// differently-spaced author reuses its existing folder. See AuthorFolders.
	matcher := NewBookMatcher(opts.Books)
	authorFolders := AuthorFolders(opts.Books)
	for _, rec := range recs {
		var siblings []placement.Sibling
		if strings.TrimSpace(rec.Series) != "" {
			for _, b := range SiblingsFromBooks(opts.Books, rec.Author, rec.Series) {
				idx, _ := SeriesPosition(b, rec.Series)
				siblings = append(siblings, placement.Sibling{RelPath: b.Path, SeriesIndex: idx})
			}
		}
		// Name by the clean book title - strip the series prefix and "(Book N)" /
		// "(Unabridged)" fluff so a series book lands as "SHORTCODE## - The Ugly
		// Truth", not "... - Diary of a Wimpy Kid: The Ugly Truth (Book 5)".
		clean := match.CleanTitle(rec.Title, rec.Series)
		sug, err := placement.SuggestWith(opts.Mode, opts.Template, placement.Input{
			Author:       rec.Author,
			Title:        clean,
			TitleShort:   clean,
			SeriesName:   rec.Series,
			Sequence:     rec.Sequence,
			HasSequence:  rec.HasSequence,
			Filename:     recordFileName(rec),
			ASIN:         rec.ASIN,
			Narrator:     rec.Narrator,
			Year:         rec.Year,
			Siblings:     siblings,
			AuthorFolder: authorFolders[match.Normalize(rec.Author)],
		})
		if err != nil {
			return nil, err
		}
		exists := false
		if !opts.SkipExistsCheck && !opts.Rejected[rec.Key] {
			_, exists = matcher.Match(rec)
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

// ApplyDestinations replaces planned destinations with user-chosen ones, keyed by
// Record.Key (the ASIN for Audible records). A destination is the full
// library-relative path including the filename, as the admin chose it.
// Anything unsafe (absolute, empty, or escaping the root after Clean) is ignored
// and the planned path kept; transfer.SafeJoin re-checks at place time.
func ApplyDestinations(items []PlannedItem, dests map[string]string) {
	for i, it := range items {
		d, ok := dests[it.Record.Key]
		if !ok {
			continue
		}
		d = strings.TrimLeft(path.Clean(strings.TrimSpace(d)), "/")
		if d == "" || d == "." || d == ".." || strings.HasPrefix(d, "../") {
			continue
		}
		items[i].RelPath = d
		items[i].Note = "manually chosen destination"
	}
}

// BookMatcher matches records against a fixed server-book list via the shared
// pkg/match matcher. It converts the candidate slice once so matching many records
// against the same library is O(library) instead of O(records x library) - the
// poll matches the whole Audible library against it. A book in several series is
// a candidate under each of them (any one may be the series Audible names). It
// also counts how many records have claimed each matched path (see Contested).
// Not safe for concurrent use.
type BookMatcher struct {
	books  []link.LibraryBook
	cands  []match.Book
	owner  []int          // cands[i] is a candidate for books[owner[i]]
	claims map[string]int // matched path -> how many records claimed it
}

// NewBookMatcher builds a matcher over an already-fetched library book list.
func NewBookMatcher(books []link.LibraryBook) *BookMatcher {
	m := &BookMatcher{books: books, claims: map[string]int{}}
	for i, b := range books {
		base := match.Book{Title: b.Title, Author: Credit(b.Authors), ASIN: b.ASIN, Narrators: b.Narrators}
		if len(b.Series) == 0 {
			m.cands = append(m.cands, base)
			m.owner = append(m.owner, i)
			continue
		}
		for _, s := range b.Series {
			c := base
			c.Series = s.Name
			c.SeriesIndex, _ = parsePosition(s.Position)
			m.cands = append(m.cands, c)
			m.owner = append(m.owner, i)
		}
	}
	return m
}

// Match finds the server book corresponding to rec (ASIN exact first, then author
// + series + series-stripped title-token overlap, sequence parsed from the title
// when the server has no position). The matcher is shared with the server so both
// sides identify the same book identically.
func (m *BookMatcher) Match(rec source.Record) (link.LibraryBook, bool) {
	q := match.Query{
		Title: rec.Title, TitleShort: rec.TitleShort, Author: rec.Author,
		Series: rec.Series, Sequence: rec.Sequence, HasSequence: rec.HasSequence, ASIN: rec.ASIN,
	}
	if rec.Narrator != "" {
		q.Narrators = []string{rec.Narrator}
	}
	i, ok := match.Best(m.cands, q)
	if !ok {
		return link.LibraryBook{}, false
	}
	b := m.books[m.owner[i]]
	m.claims[b.Path]++
	return b, true
}

// Contested reports whether more than one record has matched path since the
// matcher was built - meaning at least one of those matches is wrong, so nothing
// identity-durable (like an ASIN backfill) should be derived from any of them.
func (m *BookMatcher) Contested(path string) bool {
	return m.claims[path] > 1
}

// MatchInBooks is a one-shot match over books. For repeated matching against the
// same library, build a BookMatcher once and reuse it.
func MatchInBooks(books []link.LibraryBook, rec source.Record) (link.LibraryBook, bool) {
	return NewBookMatcher(books).Match(rec)
}

// Credit is a book's author credits as one string ("A, B"), the shape the
// manager's server listing carried and pkg/match's author gate compares.
func Credit(authors []string) string {
	return strings.Join(authors, ", ")
}

// SeriesPosition is b's position in series (matched tolerantly, as siblings are),
// and whether b is in it with a usable number.
func SeriesPosition(b link.LibraryBook, series string) (float64, bool) {
	target := match.NormalizeSeries(series)
	for _, s := range b.Series {
		if match.NormalizeSeries(s.Name) == target {
			return parsePosition(s.Position)
		}
	}
	return 0, false
}

// parsePosition reads a server series position ("3", "2.5"); anything else, and
// "", is no position.
func parsePosition(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func recordFileName(rec source.Record) string {
	if rec.FileName != "" {
		return rec.FileName
	}
	return filepath.Base(rec.SrcPath)
}
