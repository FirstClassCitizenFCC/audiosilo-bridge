package audible

import (
	"context"
	"encoding/json"
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
	got, err := c.LastPositions(context.Background(), []string{"B01", "B02", "B03"})
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
