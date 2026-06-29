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
