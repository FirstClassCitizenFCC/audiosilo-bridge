// Package placement decides where a new audiobook should live in the library,
// replicating the existing Author/Series/SHORTCODE## - Title convention. It
// anchors its guess on the folders of sibling books already in the same series
// (so the shortcode, padding and folder names match exactly), and degrades to a
// best-effort guess when the series or author is new.
package placement

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Kind describes how confident the suggestion is.
type Kind int

const (
	// SeriesExisting: the series already has books on the server, so the
	// shortcode/padding/folder names are taken from a sibling — high confidence.
	SeriesExisting Kind = iota
	// SeriesNew: the book is in a series with no siblings on the server; the
	// shortcode is guessed from the series name and should be reviewed.
	SeriesNew
	// Standalone: no series; placed at Author/Title.
	Standalone
)

func (k Kind) String() string {
	switch k {
	case SeriesExisting:
		return "series (existing)"
	case SeriesNew:
		return "series (new)"
	default:
		return "standalone"
	}
}

// Sibling is an existing book in the same author+series, used as a template.
type Sibling struct {
	RelPath     string
	SeriesIndex float64
}

// Input is everything Suggest (and the template engine) needs about the book
// being placed. Auto mode uses Author/Title/SeriesName/Sequence/Filename/Siblings;
// the extra metadata (TitleShort/ASIN/Narrator/Year) is consumed only by template
// variables (see SuggestWith).
type Input struct {
	Author      string // from metadata
	Title       string // clean folder title (short title preferred for Auto mode)
	TitleShort  string // optional; template {TitleShort} (falls back to Title)
	SeriesName  string // empty for standalones
	Sequence    float64
	HasSequence bool
	Filename    string    // original audio filename, e.g. "Waybound - Cradle, Book 12.m4b"
	ASIN        string    // template {ASIN}
	Narrator    string    // template {Narrator}
	Year        string    // template {Year}
	Siblings    []Sibling // existing books in this author+series (may be empty)
	// AuthorFolder is an existing author-folder segment on the server for this
	// (normalized) author, e.g. "L.A. McBride". When set, Auto mode places the book
	// under it verbatim instead of sanitizing Author afresh — so "L. A. McBride"
	// doesn't create a second folder alongside "L.A. McBride". Empty when the author
	// is new to the library (or in template mode, where the template owns the path).
	AuthorFolder string
}

// Suggestion is the proposed destination.
type Suggestion struct {
	RelPath  string   // book folder relative to the library root
	Filename string   // m4b filename placed inside that folder
	Kind     Kind     //
	Note     string   // human-readable explanation of the guess
	Hints    []string // nearby sibling folders, shown to help manual edits
}

// FullPath is the book folder plus the file inside it.
func (s Suggestion) FullPath() string {
	return s.RelPath + "/" + s.Filename
}

// Suggest computes the destination for a book.
func Suggest(in Input) Suggestion {
	switch {
	case in.SeriesName == "":
		return standalone(in)
	case len(in.Siblings) > 0:
		return seriesWithSiblings(in)
	default:
		return seriesNew(in)
	}
}

func standalone(in Input) Suggestion {
	return Suggestion{
		RelPath:  authorSegment(in) + "/" + sanitize(in.Title),
		Filename: in.Filename,
		Kind:     Standalone,
		Note:     "no series — placed directly under the author",
	}
}

// authorSegment returns the author folder to place under: an existing author folder
// on the server (verbatim, to land in it rather than beside it) when known,
// otherwise the sanitized record author.
func authorSegment(in Input) string {
	if in.AuthorFolder != "" {
		return in.AuthorFolder
	}
	return sanitize(in.Author)
}

func seriesWithSiblings(in Input) Suggestion {
	tmpl, ok := pickTemplate(in.Siblings)
	authorSeg, middle, _ := splitRelPath(tmpl.RelPath)
	prefix, width, parsed := parseCode(leafCode(lastSegment(tmpl.RelPath)))

	note := "matched existing series folders"
	if !ok || !parsed {
		// Folders exist but the shortcode style is unusual; keep the real
		// author/series folders and guess the code.
		prefix = guessShortcode(in.SeriesName)
		width = 2
		note = "series folders found, but shortcode could not be read from them — please check the code"
	}

	leaf := buildLeaf(prefix, width, in)
	rel := joinSegments(authorSeg, middle, leaf)
	return Suggestion{
		RelPath:  rel,
		Filename: in.Filename,
		Kind:     SeriesExisting,
		Note:     note,
		Hints:    siblingHints(in.Siblings),
	}
}

func seriesNew(in Input) Suggestion {
	prefix := guessShortcode(in.SeriesName)
	leaf := buildLeaf(prefix, 2, in)
	rel := authorSegment(in) + "/" + sanitize(in.SeriesName) + "/" + leaf
	return Suggestion{
		RelPath:  rel,
		Filename: in.Filename,
		Kind:     SeriesNew,
		Note:     fmt.Sprintf("new series — guessed shortcode %q; please verify", prefix),
	}
}

// buildLeaf assembles "<prefix><number> - <Title>". When there is no usable
// sequence it falls back to just the title (still inside its own folder).
func buildLeaf(prefix string, width int, in Input) string {
	if !in.HasSequence {
		return sanitize(in.Title)
	}
	num := formatSequence(in.Sequence, width)
	return sanitize(prefix + num + " - " + in.Title)
}

// formatSequence renders a series position zero-padded to width, preserving a
// fractional novella position (2.5 with width 2 -> "02.5").
func formatSequence(seq float64, width int) string {
	if width < 1 {
		width = 2
	}
	intPart := int(math.Floor(seq + 1e-9))
	intStr := fmt.Sprintf("%0*d", width, intPart)
	frac := seq - float64(intPart)
	if frac < 1e-9 {
		return intStr
	}
	// strconv gives "0.5"/"0.25"; keep the digits after the point.
	fracStr := strconv.FormatFloat(frac, 'f', -1, 64)
	if dot := strings.IndexByte(fracStr, '.'); dot >= 0 {
		return intStr + "." + fracStr[dot+1:]
	}
	return intStr
}

// pickTemplate chooses the sibling with the highest series index whose folder
// code can be parsed, so padding reflects the largest entry in the series.
func pickTemplate(siblings []Sibling) (Sibling, bool) {
	var best Sibling
	found := false
	for _, s := range siblings {
		if _, _, ok := parseCode(leafCode(lastSegment(s.RelPath))); !ok {
			continue
		}
		if !found || s.SeriesIndex > best.SeriesIndex {
			best, found = s, true
		}
	}
	if found {
		return best, true
	}
	// Nothing parseable; return the highest-indexed sibling for its folder names.
	for _, s := range siblings {
		if !found || s.SeriesIndex > best.SeriesIndex {
			best, found = s, true
		}
	}
	return best, false
}

// leafCode is the part of a book-folder name before the " - " title separator,
// e.g. "C08 - Wintersteel" -> "C08", "Vorkosigan Saga 01 - Shards" ->
// "Vorkosigan Saga 01". Folders without the separator return the whole name.
func leafCode(leaf string) string {
	if i := strings.Index(leaf, " - "); i >= 0 {
		return leaf[:i]
	}
	return leaf
}

// parseCode splits a code segment into its alphabetic prefix and the width of
// its zero-padded number (counting only the integer digits): "C08" -> ("C", 2),
// "FWM01" -> ("FWM", 2), "SA02.5" -> ("SA", 2), "Vorkosigan Saga 01" ->
// ("Vorkosigan Saga ", 2). ok is false when there is no trailing number to learn
// padding from.
func parseCode(code string) (prefix string, width int, ok bool) {
	i := len(code)
	for i > 0 && (isDigit(code[i-1]) || code[i-1] == '.') {
		i--
	}
	prefix, numeric := code[:i], code[i:]
	// width = leading integer digits (before any decimal point).
	w := 0
	for w < len(numeric) && isDigit(numeric[w]) {
		w++
	}
	if w == 0 {
		return "", 0, false
	}
	return prefix, w, true
}

// splitRelPath breaks a book-folder rel path into the author segment, the middle
// series segments (which may be several levels deep for "universe" series), and
// the leaf book folder.
func splitRelPath(rel string) (author, middle, leaf string) {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return parts[0], "", ""
	case 2:
		return parts[0], "", parts[1]
	default:
		return parts[0], strings.Join(parts[1:len(parts)-1], "/"), parts[len(parts)-1]
	}
}

func lastSegment(rel string) string {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	return parts[len(parts)-1]
}

func joinSegments(author, middle, leaf string) string {
	out := author
	if middle != "" {
		out += "/" + middle
	}
	if leaf != "" {
		out += "/" + leaf
	}
	return out
}

func siblingHints(siblings []Sibling) []string {
	hints := make([]string, 0, len(siblings))
	for _, s := range siblings {
		hints = append(hints, s.RelPath)
	}
	return hints
}

// guessShortcode builds an initials-based shortcode for a brand-new series:
// initials of the significant words (multi-word), or the leading letter of a
// single-word name. Always uppercase.
func guessShortcode(series string) string {
	words := strings.Fields(series)
	var initials []rune
	for _, w := range words {
		lw := strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !isLetterRune(r) }))
		if stopwords[lw] {
			continue
		}
		for _, r := range w {
			if isLetterRune(r) {
				initials = append(initials, []rune(strings.ToUpper(string(r)))[0])
				break
			}
		}
	}
	switch {
	case len(initials) >= 2:
		return string(initials)
	case len(words) == 1:
		// Single significant word: take its first letter.
		for _, r := range words[0] {
			if isLetterRune(r) {
				return strings.ToUpper(string(r))
			}
		}
	case len(initials) == 1:
		return string(initials)
	}
	return "X"
}

var stopwords = map[string]bool{
	"the": true, "of": true, "and": true, "a": true, "an": true,
	"to": true, "in": true,
}

// sanitize makes a path segment safe to create on disk without mangling the
// punctuation that real library folders use (apostrophes, commas, periods).
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", " -", "\x00", "")
	s = replacer.Replace(s)
	return strings.TrimRight(s, " .")
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isLetterRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
