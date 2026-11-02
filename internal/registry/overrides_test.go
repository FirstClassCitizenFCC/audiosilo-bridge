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

	// A half-nil Set is stored with the nil map as empty (no "null" in the file).
	if err := s2.Set("srvB", 1, Overrides{Dests: map[string]string{"B03": "X/Y.m4b"}}); err != nil {
		t.Fatal(err)
	}
	if g := s2.Get("srvB", 1); g.Matches == nil || len(g.Matches) != 0 || g.Dests["B03"] != "X/Y.m4b" {
		t.Fatalf("nil Matches should round-trip as empty: %+v", g)
	}

	// The ignore set round-trips and coexists with matches/dests.
	if err := s2.Set("srvC", 1, Overrides{Ignored: map[string]bool{"B09": true}}); err != nil {
		t.Fatal(err)
	}
	sIg, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := sIg.Get("srvC", 1); !g.Ignored["B09"] || g.Matches == nil || g.Ignored == nil {
		t.Fatalf("ignore set should round-trip with non-nil maps: %+v", g)
	}
	// An ignore-only scope survives (not treated as empty).
	if g := sIg.Get("srvC", 1); len(g.Ignored) != 1 {
		t.Fatalf("ignore-only scope was dropped: %+v", g)
	}

	// The rejection set round-trips and a rejection-only scope survives.
	if err := s2.Set("srvE", 1, Overrides{Rejected: map[string]bool{"B10": true}}); err != nil {
		t.Fatal(err)
	}
	sRej, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := sRej.Get("srvE", 1); !g.Rejected["B10"] || g.Rejected == nil || len(g.Rejected) != 1 {
		t.Fatalf("rejection set should round-trip with non-nil maps: %+v", g)
	}

	// Update writes one field's owner without clobbering another owner's field: seed
	// matches, then Update only Ignored - matches must survive.
	if err := s2.Set("srvD", 1, Overrides{Matches: map[string]string{"B1": "path/one"}}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Update("srvD", 1, func(ov *Overrides) { ov.Ignored = map[string]bool{"B2": true} }); err != nil {
		t.Fatal(err)
	}
	if g := s2.Get("srvD", 1); g.Matches["B1"] != "path/one" || !g.Ignored["B2"] {
		t.Fatalf("Update clobbered an untouched field: %+v", g)
	}

	// Clearing to empty drops the scope from disk.
	if err := s2.Set("srvA", 1, Overrides{}); err != nil {
		t.Fatal(err)
	}
	s3, err := OpenOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := s3.Get("srvA", 1); len(g.Matches) != 0 || len(g.Dests) != 0 || len(g.Ignored) != 0 {
		t.Fatalf("cleared scope should be gone after reopen: %+v", g)
	}
}
