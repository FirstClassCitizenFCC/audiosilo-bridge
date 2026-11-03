package audiblejob

import (
	"errors"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

// The placement goldens: where a title lands (Auto: series siblings' folder
// and shortcode, an existing author folder's spelling, Author/Title; a
// template; the console's dest_path), and the destinations refused.
func TestDestinationGoldens(t *testing.T) {
	books := goldenBooks()
	for _, c := range goldenCases() {
		got, err := Destination(c.p, c.rec, books)
		var f *Failure
		switch {
		case c.code != "":
			if !errors.As(err, &f) || f.Code != c.code || got != "" {
				t.Errorf("%s: %q, %v; want code %s", c.name, got, err, c.code)
			}
		case err != nil || got != c.want:
			t.Errorf("%s:\n got %q, %v\nwant %q", c.name, got, err, c.want)
		}
	}
}

// goldenBooks is a library the placement goldens place into: a series with
// shortcoded siblings, an author folder spelled its own way, a standalone.
func goldenBooks() []link.LibraryBook {
	return []link.LibraryBook{
		{Path: "Mara Quill/Orchard Cycle/OC01 - The Glass Orchard", Title: "The Glass Orchard", Authors: []string{"Mara Quill"},
			Series: []link.SeriesRef{{Name: "Orchard Cycle", Position: "1"}}},
		{Path: "Mara Quill/Orchard Cycle/OC03 - The Iron Thicket", Title: "The Iron Thicket", Authors: []string{"Mara Quill"},
			Series: []link.SeriesRef{{Name: "Orchard Cycle", Position: "3"}}},
		{Path: "R.J. Okonkwo/Ninefold/Ninefold 1 - Ninefold Gate", Title: "Ninefold Gate", Authors: []string{"R.J. Okonkwo"},
			Series: []link.SeriesRef{{Name: "Ninefold", Position: "1"}}},
		{Path: "Theo Marchetti/A Small Hour in Varrow", Title: "A Small Hour in Varrow", Authors: []string{"Theo Marchetti"}},
	}
}

func rec(asin, title, author, series string, seq float64) source.Record {
	r := source.Record{ASIN: asin, Title: title, TitleShort: title, Author: author, Authors: []string{author},
		FileName: title + ".m4b", Year: "2024"}
	if series != "" {
		r.Series, r.Sequence, r.HasSequence = series, seq, seq > 0
	}
	return r
}

type goldenCase struct {
	name string
	p    ImportParams
	rec  source.Record
	want string // "" with code: refused
	code string
}

func goldenCases() []goldenCase {
	p := ImportParams{Account: "acct0001", ASIN: "B0FAKE0002", LibraryID: 1}
	with := func(f func(*ImportParams)) ImportParams { q := p; f(&q); return q }
	return []goldenCase{
		{name: "series, siblings", p: p, rec: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2), want: "Mara Quill/Orchard Cycle/OC02 - The Copper Grove/The Copper Grove.m4b"},
		{name: "series, author spelling", p: p, rec: rec("B0FAKE0016", "Ninefold Crown", "R. J. Okonkwo", "Ninefold", 2), want: "R.J. Okonkwo/Ninefold/Ninefold 2 - Crown/Ninefold Crown.m4b"},
		{name: "series, new", p: p, rec: rec("B0FAKE0011", "The Cartographer's Apprentice", "Wen Liang", "Mapmakers", 1), want: "Wen Liang/Mapmakers/M01 - The Cartographer's Apprentice/The Cartographer's Apprentice.m4b"},
		{name: "standalone, author known", p: p, rec: rec("B0FAKE0006", "Lanterns Over Hollow Bay", "Theo Marchetti", "", 0), want: "Theo Marchetti/Lanterns Over Hollow Bay/Lanterns Over Hollow Bay.m4b"},
		{name: "standalone, new author", p: p, rec: rec("B0FAKE0018", "The Quiet Engineer", "Hannah Voss", "", 0), want: "Hannah Voss/The Quiet Engineer/The Quiet Engineer.m4b"},
		{name: "template folder", p: with(func(q *ImportParams) { q.Template = "{Author}/{Title}" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Callum Dree/Salt and Ember/Salt and Ember.m4b"},
		{name: "template file", p: with(func(q *ImportParams) { q.Template = "Audible/{Author}/{Title}.m4b" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Audible/Callum Dree/Salt and Ember.m4b"},
		{name: "dest folder", p: with(func(q *ImportParams) { q.DestPath = "Imports/Callum" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Imports/Callum/Salt and Ember.m4b"},
		{name: "dest file", p: with(func(q *ImportParams) { q.DestPath = "Imports/Salt.m4b"; q.Template = "{Title}" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Imports/Salt.m4b"},
		{name: "dest escapes", p: with(func(q *ImportParams) { q.DestPath = "../outside" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "dest absolute", p: with(func(q *ImportParams) { q.DestPath = "/etc/x.m4b" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "dest hidden", p: with(func(q *ImportParams) { q.DestPath = "A/.x.m4b" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "template bad", p: with(func(q *ImportParams) { q.Template = "{Author/{" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		// The console's dest_path wins: the account's template isn't consulted.
		{name: "dest over a template that can't place", p: with(func(q *ImportParams) { q.Template = "{Author/{"; q.DestPath = "Imports/Salt" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Imports/Salt/Salt and Ember.m4b"},
		// A pack with no sequence, its title opening "The <series>": named by
		// what is left, never "The  - Books 1-3".
		{name: "pack, article before the series", p: p, rec: pack(), want: "Mara Quill/Orchard Cycle/Books 1-3/The Orchard Cycle: Books 1-3.m4b"},
		// No author: Auto leaves out the empty folder (never a leading "/").
		{name: "no author", p: p, rec: noAuthor(""), want: "Lone Title/Lone Title.m4b"},
		{name: "no author, series", p: p, rec: noAuthor("Saga"), want: "Saga/S01 - Lone Title/Lone Title.m4b"},
		// A hidden name is never indexed by the server: generated names lose
		// their leading dots, and a dest_path naming one is refused.
		{name: "author with a leading dot", p: p, rec: rec("B0FAKE0019", "Salt and Ember", ".hack Team", "", 0), want: "hack Team/Salt and Ember/Salt and Ember.m4b"},
		{name: "template hidden folder", p: with(func(q *ImportParams) { q.Template = ".audible/{Author}/{Title}" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "audible/Callum Dree/Salt and Ember/Salt and Ember.m4b"},
		{name: "dest hidden folder", p: with(func(q *ImportParams) { q.DestPath = "Imports/.audible/Salt" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		// The backup is an MP4: a name may end .m4b, .m4a or .mp4, never
		// another audio type; any other dot is the folder's.
		{name: "dest m4a", p: with(func(q *ImportParams) { q.DestPath = "Imports/Salt.m4a" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Imports/Salt.m4a"},
		{name: "dest mp3", p: with(func(q *ImportParams) { q.DestPath = "Imports/Salt.mp3" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "dest AAC", p: with(func(q *ImportParams) { q.DestPath = "Imports/Salt.AAC" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "template mp3", p: with(func(q *ImportParams) { q.Template = "Audible/{Author}/{Title}.mp3" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), code: CodeNotWritable},
		{name: "dest folder with a dot", p: with(func(q *ImportParams) { q.DestPath = "Imports/Vol. 2" }), rec: rec("B0FAKE0019", "Salt and Ember", "Callum Dree", "", 0), want: "Imports/Vol. 2/Salt and Ember.m4b"},
	}
}

// noAuthor is a title Audible lists with no author, in series (no series
// when "").
func noAuthor(series string) source.Record {
	r := source.Record{ASIN: "B0FAKE0021", Title: "Lone Title", TitleShort: "Lone Title", FileName: "Lone Title.m4b", Year: "2024"}
	if series != "" {
		r.Series, r.Sequence, r.HasSequence = series, 1, true
	}
	return r
}
