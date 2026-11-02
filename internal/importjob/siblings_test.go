package importjob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/serverapi"
	"github.com/kodestar/audiosilo-server/pkg/match"
)

func TestAuthorFolders(t *testing.T) {
	m := AuthorFolders([]serverapi.Book{
		{RelPath: "L.A. McBride/Series/B01 - One", Author: "L.A. McBride"},
		{RelPath: "L.A. McBride/Series/B02 - Two", Author: "L.A. McBride"},
		{RelPath: "Will Wight/Cradle/C01 - Unsouled", Author: "Will Wight"},
		{RelPath: "loose-single-file.m4b", Author: "Nobody"}, // no author folder
	})
	// A differently-spaced author resolves to the existing folder spelling.
	if got := m[match.Normalize("L. A. McBride")]; got != "L.A. McBride" {
		t.Errorf("author folder (spacing) = %q, want %q", got, "L.A. McBride")
	}
	if got := m[match.Normalize("Will Wight")]; got != "Will Wight" {
		t.Errorf("author folder = %q, want %q", got, "Will Wight")
	}
	// An unknown author and a root-level loose file (no folder) have no entry.
	if got := m[match.Normalize("Jane New")]; got != "" {
		t.Errorf("unknown author = %q, want empty", got)
	}
	if got := m[match.Normalize("Nobody")]; got != "" {
		t.Errorf("loose-file author = %q, want empty", got)
	}
}

func TestAuthorFoldersPicksMostCommon(t *testing.T) {
	// Two spellings already on the server; the dominant one wins (deterministically).
	m := AuthorFolders([]serverapi.Book{
		{RelPath: "L. A. McBride/A/x", Author: "L. A. McBride"},
		{RelPath: "L.A. McBride/B/y", Author: "L.A. McBride"},
		{RelPath: "L.A. McBride/C/z", Author: "L.A. McBride"},
	})
	if got := m[match.Normalize("L.A. McBride")]; got != "L.A. McBride" {
		t.Errorf("most-common folder = %q, want %q", got, "L.A. McBride")
	}
}

func TestFindSiblings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"books":[` +
			`{"rel_path":"Will Wight/Cradle/C01 - Unsouled","author":"Will Wight","series":"Cradle","series_index":1},` +
			`{"rel_path":"Will Wight/Cradle/C02 - Soulsmith","author":"Will Wight","series":"The Cradle","series_index":2},` +
			`{"rel_path":"Will Wight/Elder Empire/EE01","author":"Will Wight","series":"Elder Empire","series_index":1}` +
			`]}`))
	}))
	defer srv.Close()

	c := serverapi.New(srv.URL, false)
	got, err := FindSiblings(context.Background(), c, 3, "Will Wight", "Cradle")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 Cradle siblings (tolerant of 'The Cradle'), got %d: %+v", len(got), got)
	}

	// A standalone (no series) returns nil without consulting the server.
	if s, err := FindSiblings(context.Background(), c, 3, "Will Wight", ""); err != nil || s != nil {
		t.Fatalf("standalone: got %+v, %v", s, err)
	}
}

func TestSiblingsFromBooksMultiAuthor(t *testing.T) {
	books := []serverapi.Book{
		{RelPath: "Shirtaloon/HWFWM/FWM01", Author: "Shirtaloon, Travis Deverell", Series: "He Who Fights WIth Monsters"},
		{RelPath: "Shirtaloon/HWFWM/FWM02", Author: "Shirtaloon & Travis Deverell", Series: "He Who Fights With Monsters"},
		{RelPath: "Someone/HWFWM/X01", Author: "Someone Else", Series: "He Who Fights With Monsters"},
		{RelPath: "Shirtaloon/Other/O01", Author: "Shirtaloon", Series: "Other Series"},
	}
	// Allowed: the Audible record's single author is one of the credited authors.
	got := SiblingsFromBooks(books, "Shirtaloon", "He Who Fights with Monsters")
	if len(got) != 2 || got[0].RelPath != "Shirtaloon/HWFWM/FWM01" || got[1].RelPath != "Shirtaloon/HWFWM/FWM02" {
		t.Fatalf("multi-author siblings = %+v", got)
	}
	if len(SiblingsFromBooks(books, "Travis Deverell", "He Who Fights With Monsters")) != 2 {
		t.Fatal("second credited author should match too")
	}
	// Denied: a different author in the same series, and a partial-name overlap.
	if s := SiblingsFromBooks(books, "Jane Doe", "He Who Fights With Monsters"); len(s) != 0 {
		t.Fatalf("unrelated author matched: %+v", s)
	}
	if s := SiblingsFromBooks(books, "Travis", "He Who Fights With Monsters"); len(s) != 0 {
		t.Fatalf("partial name matched: %+v", s)
	}
}

func TestSameAuthor(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"L. A. McBride", "L.A. McBride", true},
		{"Shirtaloon, Travis Deverell", "Shirtaloon", true},
		{"Ilona Andrews and Gordon Andrews", "Gordon Andrews", true},
		{"Ilona Andrews; Gordon Andrews", "Ilona Andrews", true},
		{"Brandon Sanderson", "Brandon Anderson", false},
		{"Dan Anderson", "Dan", false},
		{"", "", false},
	} {
		if got := sameAuthor(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAuthor(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
