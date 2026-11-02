package importjob

import (
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

func TestPlan(t *testing.T) {
	// An existing sibling in the Cradle series (drives the C##/width-2 convention),
	// and the existing "Unsouled" the existence check matches by author + title.
	books := []link.LibraryBook{
		{Path: "Will Wight/Cradle/C01 - Unsouled", Title: "Unsouled", Authors: []string{"Will Wight"}, Series: []link.SeriesRef{{Name: "Cradle", Position: "1"}}},
	}
	recs := []source.Record{
		{SrcPath: "/tmp/Soulsmith.m4b", Title: "Soulsmith", Author: "Will Wight", Series: "Cradle", Sequence: 2, HasSequence: true},
		{SrcPath: "/tmp/Unsouled.m4b", Title: "Unsouled", Author: "Will Wight", Series: "Cradle", Sequence: 1, HasSequence: true},
	}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
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
	// Book 1 already exists (author + title).
	if !items[1].ExistsOnServer {
		t.Fatal("Unsouled should be detected on the server")
	}
}

// The real He Who Fights with Monsters case: the server's tags credit both authors
// and spell the series folder "WIth", while the Audible record carries only its
// first author and a title that cleans down to the bare number. The book must land
// in the existing series folder, named like its siblings - not in a near-duplicate
// "He Who Fights with Monsters/HWFWM13 - 13" beside it.
func TestPlanMultiAuthorSiblingsBareTitle(t *testing.T) {
	books := []link.LibraryBook{
		{Path: "Shirtaloon/He Who Fights WIth Monsters/FWM01 - He Who Fights With Monsters - Book 1", Title: "He Who Fights with Monsters", Authors: []string{"Shirtaloon, Travis Deverell"}, Series: []link.SeriesRef{{Name: "He Who Fights WIth Monsters"}}},
		{Path: "Shirtaloon/He Who Fights WIth Monsters/FWM12 - He Who Fights With Monsters - Book 12", Title: "He Who Fights with Monsters 12: A LitRPG Adventure", Authors: []string{"Shirtaloon, Travis Deverell"}, Series: []link.SeriesRef{{Name: "He Who Fights WIth Monsters"}}},
	}
	recs := []source.Record{{
		Key: "B0XXX", FileName: "He Who Fights with Monsters 13 - A LitRPG Adventure.m4b",
		Title:  "He Who Fights with Monsters 13: A LitRPG Adventure",
		Author: "Shirtaloon", Series: "He Who Fights with Monsters", Sequence: 13, HasSequence: true,
	}}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books, SkipExistsCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "Shirtaloon/He Who Fights WIth Monsters/FWM13 - He Who Fights With Monsters - Book 13/He Who Fights with Monsters 13 - A LitRPG Adventure.m4b"
	if items[0].RelPath != want {
		t.Fatalf("placement:\n got: %q\nwant: %q", items[0].RelPath, want)
	}
}

// A book in several series anchors a placement in any of them, at its position
// in that series.
func TestPlanSiblingInSecondSeries(t *testing.T) {
	books := []link.LibraryBook{{
		Path: "Pratchett/Witches/W01 - Equal Rites", Title: "Equal Rites", Authors: []string{"Terry Pratchett"},
		Series: []link.SeriesRef{{Name: "Discworld", Position: "3"}, {Name: "Witches", Position: "1"}},
	}}
	recs := []source.Record{{SrcPath: "/tmp/wa.m4b", Title: "Wyrd Sisters", Author: "Terry Pratchett", Series: "Witches", Sequence: 2, HasSequence: true}}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].RelPath != "Pratchett/Witches/W02 - Wyrd Sisters/wa.m4b" {
		t.Fatalf("placement = %q", items[0].RelPath)
	}
	if items[0].ExistsOnServer {
		t.Fatal("a new book in the series must not match its sibling")
	}
}

func TestApplyDestinations(t *testing.T) {
	items := []PlannedItem{
		{Record: source.Record{Key: "A1"}, RelPath: "Various/Halo/Evolutions/Halo - Evolutions.m4b"},
		{Record: source.Record{Key: "A2"}, RelPath: "Will Wight/Cradle/C02 - Soulsmith/Soulsmith.m4b"},
		{Record: source.Record{Key: "A3"}, RelPath: "Author/Book/Book.m4b"},
	}
	ApplyDestinations(items, map[string]string{
		"A1": "HALO/Evolutions/Halo - Evolutions.m4b", // normal override
		"A3": "../outside/Book.m4b",                   // traversal is ignored
		"A9": "Nowhere/x.m4b",                         // unknown key is ignored
	})
	if items[0].RelPath != "HALO/Evolutions/Halo - Evolutions.m4b" || items[0].Note != "manually chosen destination" {
		t.Fatalf("override not applied: %+v", items[0])
	}
	if items[1].RelPath != "Will Wight/Cradle/C02 - Soulsmith/Soulsmith.m4b" {
		t.Fatalf("untouched item changed: %q", items[1].RelPath)
	}
	if items[2].RelPath != "Author/Book/Book.m4b" {
		t.Fatalf("unsafe override applied: %q", items[2].RelPath)
	}
	// Leading slashes are stripped, not treated as absolute host paths.
	ApplyDestinations(items, map[string]string{"A2": "//Flat/Soulsmith.m4b"})
	if items[1].RelPath != "Flat/Soulsmith.m4b" {
		t.Fatalf("slash-prefixed override = %q", items[1].RelPath)
	}
}

// A record whose automatic match the user rejected ("Not on server") must be
// planned as not existing, so Run imports it instead of skipping it.
func TestPlanRejectedExistence(t *testing.T) {
	books := []link.LibraryBook{{Path: "Will Wight/Cradle/C01 - Unsouled", Title: "Unsouled", Authors: []string{"Will Wight"}, Series: []link.SeriesRef{{Name: "Cradle", Position: "1"}}}}
	recs := []source.Record{{Key: "A1", SrcPath: "/tmp/Unsouled.m4b", Title: "Unsouled", Author: "Will Wight", Series: "Cradle", Sequence: 1, HasSequence: true}}

	// Sanity: without a rejection the record is detected on the server.
	plain, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
	if err != nil {
		t.Fatal(err)
	}
	if !plain[0].ExistsOnServer {
		t.Fatalf("record should match the server book: %+v", plain[0])
	}

	rej, err := Plan(recs, PlanOptions{
		Mode: placement.ModeAuto, Books: books, Rejected: map[string]bool{"A1": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rej[0].ExistsOnServer {
		t.Fatalf("rejected record still planned as on server: %+v", rej[0])
	}
}

// A path matched by more than one record is contested - at least one of those
// matches is wrong, so identity-durable writes (the ASIN backfill) must skip it.
func TestBookMatcherContested(t *testing.T) {
	m := NewBookMatcher([]link.LibraryBook{
		{Path: "a/one", Title: "One", Authors: []string{"Au"}, ASIN: "B001"},
		{Path: "a/two", Title: "Two", Authors: []string{"Au"}, ASIN: "B002"},
	})
	for _, asin := range []string{"B001", "B001", "B002"} {
		if _, ok := m.Match(source.Record{Title: "x", Author: "Au", ASIN: asin}); !ok {
			t.Fatalf("no match for %s", asin)
		}
	}
	if !m.Contested("a/one") {
		t.Fatal("doubly-claimed path should be contested")
	}
	if m.Contested("a/two") {
		t.Fatal("singly-claimed path should not be contested")
	}
	if m.Contested("a/none") {
		t.Fatal("unclaimed path should not be contested")
	}
}

func TestPlanTemplateMode(t *testing.T) {
	recs := []source.Record{{SrcPath: "/x/Warbreaker.m4b", Title: "Warbreaker", Author: "Brandon Sanderson"}}
	items, err := Plan(recs, PlanOptions{
		Mode: placement.ModeTemplate, Template: "{Author}/{Title}/{OriginalFilename}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].RelPath != "Brandon Sanderson/Warbreaker/Warbreaker.m4b" {
		t.Fatalf("template placement = %q", items[0].RelPath)
	}
}
