package audible

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSeriesMembers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sims") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-adp-token") != "ADP" || r.Header.Get("x-adp-signature") == "" {
			t.Errorf("sims request not RSA-signed: %v", r.Header)
		}
		if got := r.URL.Query().Get("similarity_type"); got != "InTheSameSeries" {
			t.Errorf("similarity_type = %q", got)
		}
		_, _ = w.Write([]byte(`{"similar_products":[
			{"asin":"C3","title":"Blackflame","authors":[{"name":"Will Wight"}],
			 "series":[{"title":"Cradle","sequence":"3"}],"release_date":"2017-01-01","content_type":"Product"},
			{"asin":"C4","title":"Skysworn",
			 "series":[{"title":"A Cradle Omnibus","sequence":"1"},{"title":"Cradle","sequence":"4"}]},
			{"asin":"CX","title":"Unrelated Standalone"}
		]}`))
	}))
	defer ts.Close()

	prods, err := testClient(t, ts.URL).SeriesMembers(context.Background(), "C1", "Cradle")
	if err != nil {
		t.Fatal(err)
	}
	if len(prods) != 3 {
		t.Fatalf("got %d products, want 3", len(prods))
	}

	byASIN := map[string]CatalogProduct{}
	for _, p := range prods {
		byASIN[p.ASIN] = p
	}
	if c3 := byASIN["C3"]; c3.Sequence != 3 || !c3.HasSequence || c3.ReleaseDate != "2017-01-01" || len(c3.Authors) != 1 {
		t.Fatalf("C3 mapped wrong: %+v", c3)
	}
	// The queried series block (Cradle) must be chosen over the other (Omnibus) one.
	if c4 := byASIN["C4"]; c4.Sequence != 4 || !c4.HasSequence || c4.SeriesName != "Cradle" {
		t.Fatalf("C4 picked the wrong series block: %+v", c4)
	}
	// A product with no matching series block is kept (not dropped), just unsequenced.
	if cx := byASIN["CX"]; cx.HasSequence {
		t.Fatalf("CX should be unsequenced: %+v", cx)
	}
}

func TestSeriesMembersProductsFallback(t *testing.T) {
	// Some marketplaces key the list "products" rather than "similar_products".
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"products":[{"asin":"P1","title":"One","series":[{"title":"S","sequence":"1"}]}]}`))
	}))
	defer ts.Close()

	prods, err := testClient(t, ts.URL).SeriesMembers(context.Background(), "seed", "S")
	if err != nil {
		t.Fatal(err)
	}
	if len(prods) != 1 || prods[0].ASIN != "P1" || prods[0].Sequence != 1 {
		t.Fatalf("fallback parse failed: %+v", prods)
	}
}
