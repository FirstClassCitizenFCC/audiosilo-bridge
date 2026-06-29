package importjob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kodestar/audiosilo-manager/internal/placement"
	"github.com/kodestar/audiosilo-manager/internal/serverapi"
	"github.com/kodestar/audiosilo-manager/internal/source"
)

func TestPlan(t *testing.T) {
	mux := http.NewServeMux()
	// Existing siblings in the Cradle series (drives the C##/width-2 convention).
	mux.HandleFunc("/api/v1/libraries/1/books", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"books":[{"rel_path":"Will Wight/Cradle/C01 - Unsouled","series":"Cradle","series_index":1}]}`))
	})
	// Search always returns the existing "Unsouled" so the existence check can
	// match book 1 (by author+title) but not the new book 2.
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"books":[{"title":"Unsouled","author":"Will Wight","series":"Cradle"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := serverapi.New(srv.URL, false)

	recs := []source.Record{
		{SrcPath: "/tmp/Soulsmith.m4b", Title: "Soulsmith", Author: "Will Wight", Series: "Cradle", Sequence: 2, HasSequence: true},
		{SrcPath: "/tmp/Unsouled.m4b", Title: "Unsouled", Author: "Will Wight", Series: "Cradle", Sequence: 1, HasSequence: true},
	}
	items, err := Plan(context.Background(), c, 1, recs, PlanOptions{Mode: placement.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}

	// Book 2 (new) anchors on sibling C01 → C02, and is not yet on the server.
	if items[0].RelPath != "Will Wight/Cradle/C02 - Soulsmith/Soulsmith.m4b" {
		t.Fatalf("Soulsmith placement = %q", items[0].RelPath)
	}
	if items[0].ExistsOnServer {
		t.Fatal("Soulsmith should not be detected on the server")
	}
	// Book 1 already exists (search matches author+title).
	if !items[1].ExistsOnServer {
		t.Fatal("Unsouled should be detected on the server")
	}
}

func TestPlanTemplateMode(t *testing.T) {
	// No server calls are needed for a standalone with an explicit template, but
	// Plan still issues a search for the existence check, so stub it empty.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"books":[]}`))
	}))
	defer srv.Close()
	c := serverapi.New(srv.URL, false)

	recs := []source.Record{{SrcPath: "/x/Warbreaker.m4b", Title: "Warbreaker", Author: "Brandon Sanderson"}}
	items, err := Plan(context.Background(), c, 1, recs, PlanOptions{
		Mode: placement.ModeTemplate, Template: "{Author}/{Title}/{OriginalFilename}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].RelPath != "Brandon Sanderson/Warbreaker/Warbreaker.m4b" {
		t.Fatalf("template placement = %q", items[0].RelPath)
	}
}
