package registry

import (
	"path/filepath"
	"testing"
)

func TestOverrideStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audible-overrides.json")
	s, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown scope yields empty (non-nil) maps.
	got := s.Get("srvA", 1)
	if got.Matches == nil || got.Dests == nil || len(got.Matches) != 0 || len(got.Dests) != 0 {
		t.Fatalf("unknown scope should be empty non-nil maps: %+v", got)
	}

	if err := s.Set("srvA", 1, Overrides{
		Matches: map[string]string{"B01": "Author/Book"},
		Dests:   map[string]string{"B02": "HALO/Evolutions/x.m4b"},
	}); err != nil {
		t.Fatal(err)
	}
	// Scopes are independent.
	if g := s.Get("srvA", 2); len(g.Matches) != 0 {
		t.Fatalf("a different library must not inherit overrides: %+v", g)
	}

	// Persistence across reopen.
	s2, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	g := s2.Get("srvA", 1)
	if g.Matches["B01"] != "Author/Book" || g.Dests["B02"] != "HALO/Evolutions/x.m4b" {
		t.Fatalf("reopened store lost overrides: %+v", g)
	}

	// The returned maps are copies - mutating them must not corrupt the store.
	g.Matches["B01"] = "tampered"
	if again := s2.Get("srvA", 1); again.Matches["B01"] != "Author/Book" {
		t.Fatalf("Get returned an aliased map: %+v", again)
	}

	// Clearing to empty drops the scope from disk.
	if err := s2.Set("srvA", 1, Overrides{}); err != nil {
		t.Fatal(err)
	}
	s3, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := s3.Get("srvA", 1); len(g.Matches) != 0 || len(g.Dests) != 0 {
		t.Fatalf("cleared scope should be gone after reopen: %+v", g)
	}
}
