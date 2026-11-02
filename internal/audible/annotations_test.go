package audible

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLastPositions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/1.0/annotations/lastpositions") {
			http.NotFound(w, r)
			return
		}
		// position_ms as a string for one and a number for another (Audible varies).
		_, _ = io.WriteString(w, `{"asin_last_position_heard_annots":[
			{"asin":"B01","last_position_heard":{"status":"Exists","position_ms":"123456"}},
			{"asin":"B02","last_position_heard":{"status":"Exists","position_ms":7890}},
			{"asin":"B03","last_position_heard":{"status":"DoesNotExist","position_ms":"0"}}
		]}`)
	}))
	defer ts.Close()

	c := testClient(t, ts.URL)
	got, err := c.LastPositions(context.Background(), []string{"B01", "B02", "B03"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["B01"] != 123456 || got["B02"] != 7890 {
		t.Fatalf("positions = %+v", got)
	}
	if _, ok := got["B03"]; ok {
		t.Fatal("non-existent position should be omitted")
	}
}

// TestLastPositionsChunks proves a large library is split across several requests
// (so the query never overflows into an HTTP 414) and the results merge.
func TestLastPositionsChunks(t *testing.T) {
	const n = 250 // > 2 * lastPositionsBatch, so several requests
	asins := make([]string, n)
	for i := range asins {
		asins[i] = fmt.Sprintf("B%09d", i)
	}

	var maxQueryLen int
	srv := lastPositionsServer(t, 0, &maxQueryLen) // 0 = no server-side cap
	defer srv.Close()

	got, err := testClient(t, srv.URL).LastPositions(context.Background(), asins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("merged positions = %d, want %d", len(got), n)
	}
	// Each request must stay well under any reasonable URL limit (the first bug was
	// a single ~all-ASINs query returning HTTP 414, Request-URI Too Long).
	if maxQueryLen > 2048 {
		t.Fatalf("max query length = %d, want <= 2048", maxQueryLen)
	}
}

// TestLastPositionsAdaptiveSplit proves the batch self-tunes below Audible's
// undocumented ASIN-list cap: when the endpoint 400s an over-long list, the client
// splits and retries, converging on the real limit and still returning every
// position (the observed bug: a 50-ASIN batch still returned HTTP 400).
func TestLastPositionsAdaptiveSplit(t *testing.T) {
	const n = 250
	asins := make([]string, n)
	for i := range asins {
		asins[i] = fmt.Sprintf("B%09d", i)
	}

	// Server rejects any request carrying more than 7 ASINs - below the starting
	// batch, so the splitter must recurse to converge.
	const maxAsins = 7
	srv := lastPositionsServer(t, maxAsins, nil)
	defer srv.Close()

	got, err := testClient(t, srv.URL).LastPositions(context.Background(), asins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("merged positions = %d, want %d (adaptive split lost entries)", len(got), n)
	}
}

// lastPositionsServer returns a fake annotations endpoint. If maxAsins > 0 it
// mimics Audible's HTTP 400 when a request carries more than maxAsins ASINs. If
// maxQueryLen is non-nil it records the longest query string seen.
func lastPositionsServer(t *testing.T, maxAsins int, maxQueryLen *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/1.0/annotations/lastpositions") {
			http.NotFound(w, r)
			return
		}
		if maxQueryLen != nil {
			if l := len(r.URL.RawQuery); l > *maxQueryLen {
				*maxQueryLen = l
			}
		}
		reqAsins := strings.Split(r.URL.Query().Get("asins"), ",")
		if maxAsins > 0 && len(reqAsins) > maxAsins {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"1 validation error detected: Value at 'asins' failed to satisfy constraint"}`)
			return
		}
		var b strings.Builder
		b.WriteString(`{"asin_last_position_heard_annots":[`)
		for i, a := range reqAsins {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"asin":%q,"last_position_heard":{"status":"Exists","position_ms":%d}}`, a, i+1)
		}
		b.WriteString(`]}`)
		_, _ = io.WriteString(w, b.String())
	}))
}

func TestSetLastPosition(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := testClient(t, ts.URL)
	if err := c.SetLastPosition(context.Background(), "B01", "ACR123", 555000); err != nil {
		t.Fatal(err)
	}
	// PUT /1.0/lastpositions/{asin} with {acr, asin, position_ms:<number>}.
	if gotPath != "/1.0/lastpositions/B01" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["asin"] != "B01" || gotBody["acr"] != "ACR123" {
		t.Fatalf("body = %+v", gotBody)
	}
	if ms, ok := gotBody["position_ms"].(float64); !ok || ms != 555000 {
		t.Fatalf("position_ms should be a number 555000, got %#v", gotBody["position_ms"])
	}

	// acr is required.
	if err := c.SetLastPosition(context.Background(), "B01", "", 1); err == nil {
		t.Fatal("expected an error when acr is empty")
	}
}
