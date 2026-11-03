package importjob

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/source"
	"github.com/kodestar/audiosilo-server/pkg/match"
)

// PlannedItem is one import candidate's computed destination. Whether the
// library already holds it is the caller's question (BookMatcher).
type PlannedItem struct {
	RelPath string // suggested destination (folder + filename), library-relative
	// Kind is how the book is placed: beside its series' books in the library
	// (SeriesExisting), as the first of its series there (SeriesNew), or on its
	// own (Standalone); Siblings is how many of its series' books the library
	// has.
	Kind     placement.Kind
	Siblings int
}

// PlanOptions configures Plan.
type PlanOptions struct {
	Mode     placement.Mode
	Template string // used when Mode == placement.ModeTemplate
	// Books is the library's books (the server's paged listing, read whole). Plan
	// finds siblings and author folders locally in it; nil is an empty library.
	Books []link.LibraryBook
}

// Plan computes a destination for each record against a library's books
// (opts.Books). For series books the existing siblings anchor the placement so
// it mirrors the established folder/shortcode convention.
func Plan(recs []source.Record, opts PlanOptions) ([]PlannedItem, error) {
	items := make([]PlannedItem, 0, len(recs))
	p := NewPlanner(opts.Books)
	for _, rec := range recs {
		item, err := p.Plan(rec, opts.Mode, opts.Template)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// Planner is Plan over one library's books for many records: the author
// folders (AuthorFolders) are found once, and the books indexed by series, so
// a record's siblings (SiblingsFromBooks, the same books in the same order)
// cost its series' books only. Not safe for concurrent use.
type Planner struct {
	books         []link.LibraryBook
	authorFolders map[string]string
	bySeries      map[string][]int // match.NormalizeSeries(name) -> books in it, in order
}

// NewPlanner indexes books (nil is an empty library).
func NewPlanner(books []link.LibraryBook) *Planner {
	p := &Planner{books: books, authorFolders: AuthorFolders(books), bySeries: map[string][]int{}}
	for i, b := range books {
		for _, s := range b.Series {
			k := match.NormalizeSeries(s.Name)
			if at := p.bySeries[k]; len(at) == 0 || at[len(at)-1] != i {
				p.bySeries[k] = append(at, i)
			}
		}
	}
	return p
}

// siblings is SiblingsFromBooks(p.books, author, series) through the index.
func (p *Planner) siblings(author, series string) []link.LibraryBook {
	if strings.TrimSpace(series) == "" {
		return nil
	}
	var out []link.LibraryBook
	for _, i := range p.bySeries[match.NormalizeSeries(series)] {
		if b := p.books[i]; sameAuthor(Credit(b.Authors), author) {
			out = append(out, b)
		}
	}
	return out
}

// Plan is rec's destination (mode and template as PlanOptions'). A template
// that can't place it is the error, and the item still carries rec's Kind and
// Siblings.
func (p *Planner) Plan(rec source.Record, mode placement.Mode, template string) (PlannedItem, error) {
	var siblings []placement.Sibling
	for _, b := range p.siblings(rec.Author, rec.Series) {
		idx, _ := SeriesPosition(b, rec.Series)
		siblings = append(siblings, placement.Sibling{RelPath: b.Path, SeriesIndex: idx})
	}
	// Name by the clean book title - strip the series prefix and "(Book N)" /
	// "(Unabridged)" fluff so a series book lands as "SHORTCODE## - The Ugly
	// Truth", not "... - Diary of a Wimpy Kid: The Ugly Truth (Book 5)".
	clean := match.CleanTitle(rec.Title, seriesAsTitled(rec.Title, rec.Series))
	in := placement.Input{
		Author:       rec.Author,
		Title:        clean,
		TitleShort:   clean,
		SeriesName:   rec.Series,
		Sequence:     rec.Sequence,
		HasSequence:  rec.HasSequence,
		Filename:     rec.FileName,
		ASIN:         rec.ASIN,
		Narrator:     rec.Narrator,
		Year:         rec.Year,
		Siblings:     siblings,
		AuthorFolder: p.authorFolders[match.Normalize(rec.Author)],
	}
	// placement's own rule, so a template that fails still says how the book
	// would have been placed.
	item := PlannedItem{Kind: placement.KindFor(in), Siblings: len(siblings)}
	sug, err := placement.SuggestWith(mode, template, in)
	if err != nil {
		return item, err
	}
	item.RelPath, item.Kind = path.Join(sug.RelPath, sug.Filename), sug.Kind
	return item, nil
}

// seriesAsTitled is the series name as the title opens with it, with the
// article before it ("The Orchard Cycle: Books 1-3" in series "Orchard Cycle"
// is "The Orchard Cycle"), so CleanTitle removes the article and the series
// together; given the bare name it leaves the article and the separator
// behind ("The : Books 1-3"). Any other title gets series as it is.
func seriesAsTitled(title, series string) string {
	t, s := strings.TrimSpace(title), strings.TrimSpace(series)
	if s == "" {
		return series
	}
	for _, art := range []string{"the ", "a ", "an "} {
		n := len(art) + len(s)
		if len(t) < n || !strings.EqualFold(t[:len(art)], art) || !strings.EqualFold(t[len(art):n], s) {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(t[n:]); n < len(t) && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			continue // the series name is the start of a longer word
		}
		return t[:n]
	}
	return series
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
// names). A book whose position is a range ("5-6": a pack or an omnibus, the
// listing's reading of a folder code like LAM05-06) is matched by a range title
// only (see MatchScored). Not safe for concurrent use.
type BookMatcher struct {
	books  []link.LibraryBook
	cands  []match.Book
	people []people     // cands[i]'s normalised author and narrators
	owner  []int        // cands[i] is a candidate for books[owner[i]]
	span   []bool       // cands[i]'s book spans books: a range position in any series
	subset []match.Book // reused per Match: the candidates past the person gate
	subIdx []int        // subset[j] is cands[subIdx[j]]
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
	m := &BookMatcher{books: books, opts: match.ImportOptions()}
	for i, b := range books {
		base := match.Book{Title: b.Title, Author: Credit(b.Authors), ASIN: b.ASIN, Narrators: b.Narrators}
		who := normalisedPeople(base.Author, base.Narrators)
		spans := slices.ContainsFunc(b.Series, func(s link.SeriesRef) bool {
			_, ok := rangeStart(s.Position)
			return ok
		})
		add := func(c match.Book) {
			m.cands = append(m.cands, c)
			m.people = append(m.people, who)
			m.owner = append(m.owner, i)
			m.span = append(m.span, spans)
		}
		if len(b.Series) == 0 {
			add(base)
			continue
		}
		for _, s := range b.Series {
			c := base
			c.Series = s.Name
			var ok bool
			if c.SeriesIndex, ok = parsePosition(s.Position); !ok {
				// A range is placed at its start, as a range title is.
				c.SeriesIndex, _ = rangeStart(s.Position)
			}
			add(c)
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
	isRange := false
	if !q.HasSequence {
		q.Sequence, isRange = rangeStart(rec.SeriesPosition)
		q.HasSequence = isRange
	}
	if rec.Narrator != "" {
		q.Narrators = []string{rec.Narrator}
	}
	i, score, ok := m.best(q, isRange)
	if !ok {
		return link.LibraryBook{}, 0, false
	}
	return m.books[m.owner[i]], score, true
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
// (in their order, so ties break the same way), its index mapped back. A book
// that spans books (m.span) is never a single volume's: a title that is no range
// skips it, or a pack's folder would look like its first volume and take that
// volume's ASIN by enrichment. A range title (q at the range's start) tries the
// spanning books starting where it does first, then the single volumes (a
// folder holding the pack under its first volume's position); a spanning book
// starting elsewhere is another pack.
func (m *BookMatcher) best(q match.Query, isRange bool) (int, float64, bool) {
	if q.ASIN != "" {
		for i := range m.cands {
			if m.cands[i].ASIN != "" && strings.EqualFold(q.ASIN, m.cands[i].ASIN) {
				return i, match.ScoreASIN, true
			}
		}
	}
	qp := normalisedPeople(q.Author, q.Narrators)
	pass := func(keep func(i int) bool) (int, float64, bool) {
		m.subset, m.subIdx = m.subset[:0], m.subIdx[:0]
		for i := range m.cands {
			if keep(i) && m.people[i].passes(qp) {
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
	if isRange {
		if i, score, ok := pass(func(i int) bool { return m.span[i] && m.cands[i].SeriesIndex == q.Sequence }); ok {
			return i, score, true
		}
	}
	return pass(func(i int) bool { return !m.span[i] })
}

// Claim is one record's match: the book's path, the match's score, the
// record's work (WorkKey) and whether the record's series position agrees or
// disagrees with the book's (PositionAgrees).
type Claim struct {
	Path      string
	Score     float64
	Work      string
	Agrees    bool
	Disagrees bool
}

// PositionAgrees compares rec's series position with b's: agrees when both are
// one number and the same, disagrees when both are one number and differ, and
// neither when either has none (a range is none: a pack is matched by
// MatchScored's rules). rec's is its SeriesPosition; b's is its position in
// rec's series, else, when b is not in that series, its main series' (the
// listing puts a folder code's number there: "PG02 - ..." is 2 whatever the
// series is called).
func PositionAgrees(rec source.Record, b link.LibraryBook) (agrees, disagrees bool) {
	want, ok := parsePosition(rec.SeriesPosition)
	if !ok || len(b.Series) == 0 {
		return false, false
	}
	pos := b.Series[0].Position
	target := match.NormalizeSeries(rec.Series)
	for _, s := range b.Series {
		if target != "" && match.NormalizeSeries(s.Name) == target {
			pos = s.Position
			break
		}
	}
	got, ok := parsePosition(pos)
	if !ok {
		return false, false
	}
	return got == want, got != want
}

// WorkKey identifies the work a record is: its title (folded), its authors
// (folded, in any order) and its series position. Two records with one key
// are the same book owned twice (two ASINs, an edition each), which share a
// book without contesting it; the position keeps apart the volumes a series
// lists under one bare title. A title that folds to nothing is no identity:
// its key is "", which KeepBest never takes for the same work.
func WorkKey(rec source.Record) string {
	title := match.Fold(rec.Title)
	if title == "" {
		return ""
	}
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
	return title + "\x00" + strings.Join(slices.Compact(folded), "\x00") + "\x00" + strings.TrimSpace(rec.SeriesPosition)
}

// SpanMismatch reports whether rec spans books (its position is a range: a
// pack) and b does not, as when a pack's title fell back to the folder at its
// first volume's position. Such a match may be shown, but the pack's ASIN is
// not the book's: an enrichment would stamp it onto what may be one volume.
func SpanMismatch(rec source.Record, b link.LibraryBook) bool {
	if _, ok := rangeStart(rec.SeriesPosition); !ok || rec.HasSequence {
		return false
	}
	return !slices.ContainsFunc(b.Series, func(s link.SeriesRef) bool {
		_, ok := rangeStart(s.Position)
		return ok
	})
}

// KeepBest settles books several records claimed. Of the claims on one path:
//   - when the best is no ASIN match (match.ScoreASIN) and some claims' series
//     position agrees with the book's (Claim.Agrees), only those compete: the
//     number tells the volumes of a series apart, even against a higher score
//     (the title words of two volumes often match one folder equally);
//   - the single best score keeps its match, and a tie for the best keeps none
//     (the matcher can't tell which is the book, so none is called "on this
//     server"), unless every tied claim is the same work (WorkKey: one book
//     owned twice), which all keep it: dropping it would download it again.
//
// A claim of the same work as the one that keeps a book keeps it too, whatever
// its score (the edition the book's ASIN names scores higher than the other).
// A path one record claimed keeps it. keep[i] reports whether claims[i]
// stands; contested[i] whether it lost a book it may well be (another title
// kept it, or none could be told apart), which is every losing claim but one
// whose position disagrees with the book's: that title is another volume, so
// it is plainly not here. An empty Path is no claim.
func KeepBest(claims []Claim) (keep, contested []bool) {
	byPath := map[string][]int{}
	for i, c := range claims {
		if c.Path != "" {
			byPath[c.Path] = append(byPath[c.Path], i)
		}
	}
	keep, contested = make([]bool, len(claims)), make([]bool, len(claims))
	for _, at := range byPath {
		w := keeper(claims, at)
		for _, i := range at {
			c := claims[i]
			keep[i] = w == i || (w >= 0 && c.Work != "" && c.Work == claims[w].Work)
			contested[i] = !keep[i] && !c.Disagrees
		}
	}
	return keep, contested
}

// keeper is the claim of at (claims on one path) that keeps the book, or -1
// when a tie between two works keeps none (KeepBest).
func keeper(claims []Claim, at []int) int {
	best := func(at []int) int {
		b := at[0]
		for _, i := range at[1:] {
			if claims[i].Score > claims[b].Score {
				b = i
			}
		}
		return b
	}
	if claims[best(at)].Score < match.ScoreASIN {
		var agree []int
		for _, i := range at {
			if claims[i].Agrees {
				agree = append(agree, i)
			}
		}
		if len(agree) > 0 {
			at = agree
		}
	}
	b := best(at)
	for _, i := range at {
		if i != b && claims[i].Score == claims[b].Score && (claims[i].Work == "" || claims[i].Work != claims[b].Work) {
			return -1 // a tie between two works: none keeps it
		}
	}
	return b
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
