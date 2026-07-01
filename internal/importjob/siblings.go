// Package importjob orchestrates getting books onto a server: discovering import
// candidates, computing their placement, transferring them, and triggering a
// rescan. This file holds series-sibling detection — finding the existing books a
// new book's placement should anchor on. Transfer/runner orchestration lands in M2.
package importjob

import (
	"context"
	"strings"

	"github.com/kodestar/audiosilo-manager/internal/serverapi"
)

// FindSiblings returns the books in a library that belong to the same author and
// (tolerantly-normalized) series as the target — the existing entries a placement
// anchors on. It fetches the whole author and matches the series client-side,
// because the server's series filter is an exact match and would miss folders like
// "The Primal Hunter" vs "Primal Hunter". Returns nil for a standalone (no series).
func FindSiblings(ctx context.Context, c *serverapi.Client, libraryID int64, author, series string) ([]serverapi.Book, error) {
	if strings.TrimSpace(series) == "" {
		return nil, nil
	}
	books, err := c.ListBooks(ctx, libraryID, author, "", 1000)
	if err != nil {
		return nil, err
	}
	target := normalizeSeries(series)
	var out []serverapi.Book
	for _, b := range books {
		if normalizeSeries(b.Series) == target {
			out = append(out, b)
		}
	}
	return out, nil
}

// SiblingsFromBooks is the local equivalent of FindSiblings: it filters an
// already-fetched library book list to the same author + (tolerantly-normalized)
// series, instead of issuing a query. The Audible pre-flight (and bulk import) hold
// the whole library in memory, so matching locally avoids one request per book —
// which otherwise trips the server's rate limiter (HTTP 429) on a large library.
func SiblingsFromBooks(books []serverapi.Book, author, series string) []serverapi.Book {
	if strings.TrimSpace(series) == "" {
		return nil
	}
	target := normalizeSeries(series)
	var out []serverapi.Book
	for _, b := range books {
		if sameNorm(b.Author, author) && normalizeSeries(b.Series) == target {
			out = append(out, b)
		}
	}
	return out
}

// AuthorFolders maps each author (normalized via normalize) to the author-folder
// spelling already used on the server for their books, so a new book reuses the
// existing folder instead of a near-duplicate — e.g. an Audible "L. A. McBride"
// placed under an existing "L.A. McBride" rather than beside it. When an author has
// more than one spelling on the server the most common wins (first-seen breaks ties,
// deterministically). Books at the library root (no author folder) and blank authors
// are skipped. Built once per plan (one pass) so the per-record lookup is O(1);
// unlike siblings it keys on author alone (any series), so standalones and
// first-in-series books reuse the folder too. Look up with authorFolders[normalize(author)].
func AuthorFolders(books []serverapi.Book) map[string]string {
	type tally struct {
		counts map[string]int
		best   string
		bestN  int
	}
	byAuthor := map[string]*tally{}
	for _, b := range books {
		seg := authorFolderSeg(b.RelPath)
		key := normalize(b.Author)
		if seg == "" || key == "" {
			continue
		}
		t := byAuthor[key]
		if t == nil {
			t = &tally{counts: map[string]int{}}
			byAuthor[key] = t
		}
		t.counts[seg]++
		if t.counts[seg] > t.bestN { // strictly-greater keeps the first-seen spelling on ties
			t.best, t.bestN = seg, t.counts[seg]
		}
	}
	out := make(map[string]string, len(byAuthor))
	for k, t := range byAuthor {
		out[k] = t.best
	}
	return out
}

// authorFolderSeg is the first path segment of a book rel path (its author folder),
// or "" for a root-level loose file that has no author folder.
func authorFolderSeg(rel string) string {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// normalizeSeries normalizes a series name for tolerant matching, additionally
// dropping a leading article so "The Primal Hunter" and "Primal Hunter" compare
// equal. The article is stripped as a whole word before punctuation/spaces are
// removed, so names like "Theory..." are left intact.
func normalizeSeries(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, art) {
			t = t[len(art):]
			break
		}
	}
	return normalize(t)
}

// sameNorm reports whether two strings are equal after tolerant normalization.
func sameNorm(a, b string) bool { return normalize(a) == normalize(b) && normalize(a) != "" }

// normalize lowercases and strips non-alphanumeric runes for tolerant matching.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
