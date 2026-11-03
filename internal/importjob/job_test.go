package importjob

import (
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

func TestPlan(t *testing.T) {
	// An existing sibling in the Cradle series (drives the C##/width-2 convention).
	books := []link.LibraryBook{
		{Path: "Will Wight/Cradle/C01 - Unsouled", Title: "Unsouled", Authors: []string{"Will Wight"}, Series: []link.SeriesRef{{Name: "Cradle", Position: "1"}}},
	}
	recs := []source.Record{
		{FileName: "Soulsmith.m4b", Title: "Soulsmith", Author: "Will Wight", Series: "Cradle", Sequence: 2, HasSequence: true},
		{FileName: "Unsouled.m4b", Title: "Unsouled", Author: "Will Wight", Series: "Cradle", Sequence: 1, HasSequence: true},
	}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}

	// Book 2 (new) anchors on sibling C01 → C02.
	if items[0].RelPath != "Will Wight/Cradle/C02 - Soulsmith/Soulsmith.m4b" {
		t.Fatalf("Soulsmith placement = %q", items[0].RelPath)
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
		FileName: "He Who Fights with Monsters 13 - A LitRPG Adventure.m4b",
		Title:    "He Who Fights with Monsters 13: A LitRPG Adventure",
		Author:   "Shirtaloon", Series: "He Who Fights with Monsters", Sequence: 13, HasSequence: true,
	}}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
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
	recs := []source.Record{{FileName: "wa.m4b", Title: "Wyrd Sisters", Author: "Terry Pratchett", Series: "Witches", Sequence: 2, HasSequence: true}}
	items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: books})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].RelPath != "Pratchett/Witches/W02 - Wyrd Sisters/wa.m4b" {
		t.Fatalf("placement = %q", items[0].RelPath)
	}
}

func TestPlanTemplateMode(t *testing.T) {
	recs := []source.Record{{FileName: "Warbreaker.m4b", Title: "Warbreaker", Author: "Brandon Sanderson"}}
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

// A title that opens with an article before its series name is cleaned of
// both: never "The : Books 1-3" or "The 3" (a bare number keeps its series
// words, as CleanTitle gives any title that is only its series and number).
func TestPlanArticleBeforeSeries(t *testing.T) {
	for _, c := range []struct{ title, series, want string }{
		{"The Orchard Cycle: Books 1-3", "Orchard Cycle", "Mara Quill/Orchard Cycle/Books 1-3/x.m4b"},
		{"The Mayor of Noobtown 3", "Mayor of Noobtown", "Mara Quill/Mayor of Noobtown/Mayor of Noobtown 3/x.m4b"},
		{"Orchard Cycle: Books 1-3", "Orchard Cycle", "Mara Quill/Orchard Cycle/Books 1-3/x.m4b"},
		{"The Orchard Cycles Reborn", "Orchard Cycle", "Mara Quill/Orchard Cycle/The Orchard Cycles Reborn/x.m4b"},
	} {
		items, err := Plan([]source.Record{{FileName: "x.m4b", Title: c.title, Author: "Mara Quill", Series: c.series}},
			PlanOptions{Mode: placement.ModeAuto})
		if err != nil {
			t.Fatal(err)
		}
		if items[0].RelPath != c.want {
			t.Errorf("%q: got %q, want %q", c.title, items[0].RelPath, c.want)
		}
	}
}

// "<Series> N: Subtitle" cleans to "N: Subtitle" once the series is stripped;
// the folder code carries the number already, so the folder is named by the
// subtitle alone ("2.5 - Margins", never "2.5 - 2.5 - Margins"). The file keeps
// its original name, as every placement does. A number that is not the
// record's position, a title of its own and a bare number stay as they were.
func TestPlanSeriesNumberTitle(t *testing.T) {
	plain := []link.LibraryBook{{
		Path: "Wen Liang/Mapmakers/1 - The Cartographer's Apprentice", Title: "The Cartographer's Apprentice",
		Authors: []string{"Wen Liang"}, Series: []link.SeriesRef{{Name: "Mapmakers", Position: "1"}},
	}}
	coded := []link.LibraryBook{{
		Path: "Wen Liang/Mapmakers/MM01 - The Cartographer's Apprentice", Title: "The Cartographer's Apprentice",
		Authors: []string{"Wen Liang"}, Series: []link.SeriesRef{{Name: "Mapmakers", Position: "1"}},
	}}
	for _, c := range []struct {
		name  string
		books []link.LibraryBook
		title string
		seq   float64
		want  string
	}{
		{"decimal", plain, "Mapmakers 2.5: Margins", 2.5, "Wen Liang/Mapmakers/2.5 - Margins/x.m4b"},
		{"integer", plain, "Mapmakers 3: The Coast", 3, "Wen Liang/Mapmakers/3 - The Coast/x.m4b"},
		{"hyphen", plain, "Mapmakers 3 - The Coast", 3, "Wen Liang/Mapmakers/3 - The Coast/x.m4b"},
		{"en dash", plain, "Mapmakers 3 \u2013 The Coast", 3, "Wen Liang/Mapmakers/3 - The Coast/x.m4b"},
		{"article", plain, "The Mapmakers 3: The Coast", 3, "Wen Liang/Mapmakers/3 - The Coast/x.m4b"},
		{"shortcode decimal", coded, "Mapmakers 2.5: Margins", 2.5, "Wen Liang/Mapmakers/MM02.5 - Margins/x.m4b"},
		{"shortcode integer", coded, "Mapmakers 3: The Coast", 3, "Wen Liang/Mapmakers/MM03 - The Coast/x.m4b"},
		{"new series", nil, "Mapmakers 3: The Coast", 3, "Wen Liang/Mapmakers/M03 - The Coast/x.m4b"},
		{"marker already handled", plain, "Mapmakers, Book 3: The Coast", 3, "Wen Liang/Mapmakers/3 - The Coast/x.m4b"},
		{"not this position", plain, "Mapmakers 7: The Coast", 3, "Wen Liang/Mapmakers/3 - 7 - The Coast/x.m4b"},
		{"title of its own", plain, "3 Days of Rain", 3, "Wen Liang/Mapmakers/3 - 3 Days of Rain/x.m4b"},
	} {
		recs := []source.Record{{FileName: "x.m4b", Title: c.title, Author: "Wen Liang", Series: "Mapmakers", Sequence: c.seq, HasSequence: true}}
		items, err := Plan(recs, PlanOptions{Mode: placement.ModeAuto, Books: c.books})
		if err != nil {
			t.Fatal(err)
		}
		if items[0].RelPath != c.want {
			t.Errorf("%s (%q): got %q, want %q", c.name, c.title, items[0].RelPath, c.want)
		}
	}
}
