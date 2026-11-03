package importjob

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
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
// pkg/match matcher, under its import rules (match.ImportOptions: the rules
// for mapping a store's catalog onto a library's books, which the server's own
// callers don't use). It converts the candidate slice and normalises each
// candidate's people once, and each match scores only the candidates whose
// people pass pkg/match's person gate (match.BestWith skips the rest after its
// ASIN step anyway), so a poll matching a whole Audible library doesn't
// normalise every author of the library for every title. A book in several
// series is a candidate under each of them (any one may be the series Audible
// names). It also counts how many records have claimed each matched path (see
// Contested). Not safe for concurrent use.
type BookMatcher struct {
	books  []link.LibraryBook
	cands  []match.Book
	people []people       // cands[i]'s normalised author and narrators
	owner  []int          // cands[i] is a candidate for books[owner[i]]
	claims map[string]int // matched path -> how many records claimed it
	subset []match.Book   // reused per Match: the candidates past the person gate
	subIdx []int          // subset[j] is cands[subIdx[j]]
	opts   match.Options
}

// people is a candidate's (or a query's) author and narrators, normalised as
// pkg/match's person gate compares them.
type people struct {
	author    string
	narrators []string
}

func normalisedPeople(author string, narrators []string) people {
	p := people{author: match.Normalize(author)}
	for _, n := range narrators {
		p.narrators = append(p.narrators, match.Normalize(n))
	}
	return p
}

// passes is pkg/match's personMatch(b, q) over normalised people: b's author or
// a narrator of b is q's author, or b's author is one of q's narrators.
func (b people) passes(q people) bool {
	if match.NamesMatch(b.author, q.author) {
		return true
	}
	for _, n := range b.narrators {
		if match.NamesMatch(n, q.author) {
			return true
		}
	}
	for _, n := range q.narrators {
		if match.NamesMatch(b.author, n) {
			return true
		}
	}
	return false
}

// NewBookMatcher builds a matcher over an already-fetched library book list.
func NewBookMatcher(books []link.LibraryBook) *BookMatcher {
	m := &BookMatcher{books: books, claims: map[string]int{}, opts: match.ImportOptions()}
	for i, b := range books {
		base := match.Book{Title: b.Title, Author: Credit(b.Authors), ASIN: b.ASIN, Narrators: b.Narrators}
		who := normalisedPeople(base.Author, base.Narrators)
		if len(b.Series) == 0 {
			m.cands = append(m.cands, base)
			m.people = append(m.people, who)
			m.owner = append(m.owner, i)
			continue
		}
		for _, s := range b.Series {
			c := base
			c.Series = s.Name
			c.SeriesIndex, _ = parsePosition(s.Position)
			m.cands = append(m.cands, c)
			m.people = append(m.people, who)
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
	b, _, ok := m.MatchScored(rec)
	return b, ok
}

// MatchScored is Match with the match's score (match.ScoreASIN for an ASIN
// match), so a caller can compare two records that claimed one book (KeepBest).
func (m *BookMatcher) MatchScored(rec source.Record) (link.LibraryBook, float64, bool) {
	q := match.Query{
		Title: rec.Title, TitleShort: rec.TitleShort, Author: rec.Author,
		Series: rec.Series, Sequence: rec.Sequence, HasSequence: rec.HasSequence, ASIN: rec.ASIN,
	}
	// A range position ("5-6": a pack of two books) is placed at its start, so a
	// number in the title ("Pack 3") never stands in for the position the store
	// gave.
	if !q.HasSequence {
		q.Sequence, q.HasSequence = rangeStart(rec.SeriesPosition)
	}
	if rec.Narrator != "" {
		q.Narrators = []string{rec.Narrator}
	}
	i, score, ok := m.best(q)
	if !ok {
		return link.LibraryBook{}, 0, false
	}
	b := m.books[m.owner[i]]
	m.claims[b.Path]++
	return b, score, true
}

// rangePosition is a series position spanning books: "5-6", "0.5-4", "1 - 3".
var rangePosition = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*[-\x{2013}]\s*\d+(?:\.\d+)?\s*$`)

// rangeStart is a range position's first number.
func rangeStart(pos string) (float64, bool) {
	sm := rangePosition.FindStringSubmatch(pos)
	if sm == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(sm[1], 64)
	return v, err == nil
}

// best is match.BestWith(m.cands, q, m.opts): its ASIN step over every
// candidate, then match.BestWith over the candidates past the person gate only
// (in their order, so ties break the same way), its index mapped back.
func (m *BookMatcher) best(q match.Query) (int, float64, bool) {
	if q.ASIN != "" {
		for i := range m.cands {
			if m.cands[i].ASIN != "" && strings.EqualFold(q.ASIN, m.cands[i].ASIN) {
				return i, match.ScoreASIN, true
			}
		}
	}
	qp := normalisedPeople(q.Author, q.Narrators)
	m.subset, m.subIdx = m.subset[:0], m.subIdx[:0]
	for i := range m.cands {
		if m.people[i].passes(qp) {
			m.subset = append(m.subset, m.cands[i])
			m.subIdx = append(m.subIdx, i)
		}
	}
	j, score, ok := match.BestWith(m.subset, q, m.opts)
	if !ok {
		return -1, 0, false
	}
	return m.subIdx[j], score, true
}

// Contested reports whether more than one record has matched path since the
// matcher was built - meaning at least one of those matches is wrong, so nothing
// identity-durable (like an ASIN backfill) should be derived from any of them.
func (m *BookMatcher) Contested(path string) bool {
	return m.claims[path] > 1
}

// Claim is one record's match: the book's path, the match's score and the
// record's work (WorkKey).
type Claim struct {
	Path  string
	Score float64
	Work  string
}

// WorkKey identifies the work a record is: its title (folded), its authors
// (folded, in any order) and its series position. Two records with one key
// are the same book owned twice (two ASINs, an edition each), which share a
// book without contesting it; the position keeps apart the volumes a series
// lists under one bare title.
func WorkKey(rec source.Record) string {
	authors := rec.Authors
	if len(authors) == 0 && rec.Author != "" {
		authors = []string{rec.Author}
	}
	folded := make([]string, 0, len(authors))
	for _, a := range authors {
		if f := match.Fold(a); f != "" {
			folded = append(folded, f)
		}
	}
	slices.Sort(folded)
	return match.Fold(rec.Title) + "\x00" + strings.Join(slices.Compact(folded), "\x00") + "\x00" + strings.TrimSpace(rec.SeriesPosition)
}

// KeepBest settles books several records claimed: of the claims on one path,
// only the single best score keeps its match, and a tie for the best keeps
// none (the matcher can't tell which is the book, so none is called "on this
// server"), unless every tied claim is the same work (WorkKey: one book owned
// twice), which all keep it: dropping it would download it again. A path one record claimed keeps it.
// keep[i] reports whether claims[i] stands; an empty Path is no claim.
func KeepBest(claims []Claim) []bool {
	top := map[string][]int{} // path -> the claims at its best score
	for i, c := range claims {
		if c.Path == "" {
			continue
		}
		at := top[c.Path]
		switch {
		case len(at) == 0 || c.Score > claims[at[0]].Score:
			top[c.Path] = []int{i}
		case c.Score == claims[at[0]].Score:
			top[c.Path] = append(at, i)
		}
	}
	keep := make([]bool, len(claims))
	for _, at := range top {
		for _, i := range at[1:] {
			if claims[i].Work == "" || claims[i].Work != claims[at[0]].Work {
				at = nil // a tie between two works: none keeps it
				break
			}
		}
		for _, i := range at {
			keep[i] = true
		}
	}
	return keep
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
