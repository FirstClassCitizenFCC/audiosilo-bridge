package importjob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kodestar/audiosilo-manager/internal/serverapi"
)

func TestNormalizeSeries(t *testing.T) {
	cases := map[string]string{
		"The Primal Hunter": "primalhunter",
		"primal hunter":     "primalhunter",
		"A Court of Thorns": "courtofthorns",
		"Theory of Magic":   "theoryofmagic", // "the" only stripped as a whole word
		"  Cradle  ":        "cradle",
	}
	for in, want := range cases {
		if got := normalizeSeries(in); got != want {
			t.Errorf("normalizeSeries(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthorFolders(t *testing.T) {
	m := AuthorFolders([]serverapi.Book{
		{RelPath: "L.A. McBride/Series/B01 - One", Author: "L.A. McBride"},
		{RelPath: "L.A. McBride/Series/B02 - Two", Author: "L.A. McBride"},
		{RelPath: "Will Wight/Cradle/C01 - Unsouled", Author: "Will Wight"},
		{RelPath: "loose-single-file.m4b", Author: "Nobody"}, // no author folder
	})
	// A differently-spaced author resolves to the existing folder spelling.
	if got := m[normalize("L. A. McBride")]; got != "L.A. McBride" {
		t.Errorf("author folder (spacing) = %q, want %q", got, "L.A. McBride")
	}
	if got := m[normalize("Will Wight")]; got != "Will Wight" {
		t.Errorf("author folder = %q, want %q", got, "Will Wight")
	}
	// An unknown author and a root-level loose file (no folder) have no entry.
	if got := m[normalize("Jane New")]; got != "" {
		t.Errorf("unknown author = %q, want empty", got)
	}
	if got := m[normalize("Nobody")]; got != "" {
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
	if got := m[normalize("L.A. McBride")]; got != "L.A. McBride" {
		t.Errorf("most-common folder = %q, want %q", got, "L.A. McBride")
	}
}

func TestFindSiblings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"books":[` +
			`{"rel_path":"Will Wight/Cradle/C01 - Unsouled","series":"Cradle","series_index":1},` +
			`{"rel_path":"Will Wight/Cradle/C02 - Soulsmith","series":"The Cradle","series_index":2},` +
			`{"rel_path":"Will Wight/Elder Empire/EE01","series":"Elder Empire","series_index":1}` +
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
