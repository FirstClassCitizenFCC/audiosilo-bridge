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
