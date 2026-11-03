package importjob

import (
	"strconv"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
)

// audibleRec is an Audible record as the library listing makes it: the title,
// the first author, the series and its position as Audible spells it.
func audibleRec(asin, title, author, series, pos string) source.Record {
	r := source.Record{ASIN: asin, Key: asin, Title: title, TitleShort: title, Author: author, Authors: []string{author},
		Series: series, SeriesPosition: pos}
	if v, err := strconv.ParseFloat(pos, 64); err == nil {
		r.Sequence, r.HasSequence = v, true
	}
	return r
}

func series(name, pos string) []link.SeriesRef { return []link.SeriesRef{{Name: name, Position: pos}} }

// TestMatcherOverMissShapes runs the matcher over a made-up library shaped
// like the misses of a real 747-title Audible account (2026-11-03): bare
// Audible titles against wordy server titles with no position, positions the
// listing fills from folder codes, a title that is only a number, a residual
// with no words, one title filed under two series names, "(Unabridged)" on a
// genre tail, publisher packs with range positions, sub-series volumes that
// all landed on one book, and titles that are not on the server. Every title
// lands where it should, or nowhere.
func TestMatcherOverMissShapes(t *testing.T) {
	books := []link.LibraryBook{
		// Bare Audible title vs a wordy title naming its place after the series
		// (the listing has no position: the "Series, Book N" marker confirms).
		{Path: "Vale/Ember Heart/EH01 - Coal Will", Title: "Coal Will: Ember Heart Series, Book 1", Authors: []string{"Ivo Vale"}, Series: series("Ember Heart", "")},
		{Path: "Vale/Ember Heart/EH02 - Ember Heart", Title: "Ember Heart - Book 2: Land of Ash", Authors: []string{"Ivo Vale"}, Series: series("Ember Heart", "")},
		{Path: "Vale/Ember Heart/EH03 - Ember Heart", Title: "Ember Heart - Book 3: Land of Smoke", Authors: []string{"Ivo Vale"}, Series: series("Ember Heart", "")},
		// The position the listing filled from the folder code (DM02 -> 2).
		{Path: "Rook/Glass Moons/GM01 - Oathbound", Title: "Oathbound Mender - Under the Glass Moons, Book 1", Authors: []string{"Pell Rook"}, Series: series("Under the Glass Moons", "1")},
		{Path: "Rook/Glass Moons/GM02 - Argo", Title: "Voyage on the Argo", Authors: []string{"Pell Rook"}, Series: series("Under the Glass Moons", "2")},
		// A title that is only a number.
		{Path: "Blair/1985", Title: "1985 (Unabridged)", Authors: []string{"Eric Blair"}},
		// The residual after the series has no words ("The").
		{Path: "Rimm/Mayor of Dunmere/MD01 - The Mayor of Dunmere", Title: "The Mayor of Dunmere", Authors: []string{"Ry Rimm"}, Series: series("Mayor of Dunmere", "")},
		// One title, filed under two series names.
		{Path: "Kade/Maze World/MW01 - Seer's Eye", Title: "Seer's Eye: Waking: A Maze World Novel", Authors: []string{"Aro Kade"}, Series: series("Maze World", "")},
		// "(Unabridged)" after the genre tail, the position from the folder code.
		{Path: "Garth/Prime Hunter/PH15 - 15", Title: "The Prime Hunter 15:  A LitRPG Adventure (Unabridged)", Authors: []string{"Zog Garth"}, Series: series("The Prime Hunter", "15")},
		{Path: "Garth/Prime Hunter/PH14 - 14", Title: "The Prime Hunter 14:  A LitRPG Adventure (Unabridged)", Authors: []string{"Zog Garth"}, Series: series("The Prime Hunter", "14")},
		// Publisher packs: the listing reads LAM05-06 as the range "5-6".
		{Path: "Sisk/Arch Mage/AM01-02", Title: "Arch Mage: Publisher's Pack - Arch Mage, Book 1, 2", Authors: []string{"Mo Sisk"}, Series: series("Arch Mage", "1-2")},
		{Path: "Sisk/Arch Mage/AM03-04", Title: "Arch Mage: Publisher's Pack 2 - Arch Mage, Book 3, 4", Authors: []string{"Mo Sisk"}, Series: series("Arch Mage", "3-4")},
		{Path: "Sisk/Arch Mage/AM05-06", Title: "Arch Mage: Publisher's Pack 3 - Arch Mage, Book 5, 6", Authors: []string{"Mo Sisk"}, Series: series("Arch Mage", "5-6")},
		{Path: "Sisk/Arch Mage/AM07-08", Title: "Arch Mage: Publisher's Pack 4 - Books 7-8", Authors: []string{"Mo Sisk"}, Series: series("Arch Mage", "7-8")},
		// Sub-series volumes: only volume 1 is here, as series book 4.
		{Path: "Yrs/Fate Cycle/FC04 - Strands of Fate - Volume 1", Title: "Strands of Fate: Volume 1", Authors: []string{"Yrs Ill"}, Series: series("Fate Cycle", "")},
		// A Dresden-like short story collection: junk title, position from the folder.
		{Path: "Butch/Wizard Files/WF15.5 Short Cases", Title: "The Wizard Files", Authors: []string{"Jim Butch"}, Series: series("The Wizard Files", "15.5")},
	}
	cases := []struct {
		rec  source.Record
		want string // "" = not on this server
	}{
		{audibleRec("B0SYN00001", "Ember Heart", "Ivo Vale", "Ember Heart Series", "1"), "Vale/Ember Heart/EH01 - Coal Will"},
		{audibleRec("B0SYN00002", "Ember Heart", "Ivo Vale", "Ember Heart Series", "2"), "Vale/Ember Heart/EH02 - Ember Heart"},
		{audibleRec("B0SYN00003", "Ember Heart", "Ivo Vale", "Ember Heart Series", "3"), "Vale/Ember Heart/EH03 - Ember Heart"},
		{audibleRec("B0SYN00004", "Ember Heart", "Ivo Vale", "Ember Heart Series", "4"), ""},
		{audibleRec("B0SYN00011", "Under the Glass Moons", "Pell Rook", "Under the Glass Moons", "1"), "Rook/Glass Moons/GM01 - Oathbound"},
		{audibleRec("B0SYN00012", "Under the Glass Moons 2", "Pell Rook", "Under the Glass Moons", "2"), "Rook/Glass Moons/GM02 - Argo"},
		{audibleRec("B0SYN00013", "Under the Glass Moons 3", "Pell Rook", "Under the Glass Moons", "3"), ""},
		{audibleRec("B0SYN00021", "1985", "Eric Blair", "", ""), "Blair/1985"},
		{audibleRec("B0SYN00022", "The Mayor of Dunmere", "Ry Rimm", "Dunmere", "1"), "Rimm/Mayor of Dunmere/MD01 - The Mayor of Dunmere"},
		{audibleRec("B0SYN00023", "Seer's Eye: Waking: A Maze World Novel", "Aro Kade", "Seer's Eye", "1"), "Kade/Maze World/MW01 - Seer's Eye"},
		{audibleRec("B0SYN00024", "The Prime Hunter 15", "Zog Garth", "The Prime Hunter", "15"), "Garth/Prime Hunter/PH15 - 15"},
		{audibleRec("B0SYN00031", "Arch Mage: Publisher's Pack 2", "Mo Sisk", "Arch Mage", "3-4"), "Sisk/Arch Mage/AM03-04"},
		{audibleRec("B0SYN00032", "Arch Mage: Publisher's Pack 3", "Mo Sisk", "Arch Mage", "5-6"), "Sisk/Arch Mage/AM05-06"},
		{audibleRec("B0SYN00033", "Arch Mage: Publisher's Pack 4", "Mo Sisk", "Arch Mage", "7-8"), "Sisk/Arch Mage/AM07-08"},
		// A single volume of the series is in none of the packs.
		{audibleRec("B0SYN00034", "Arch Mage 5", "Mo Sisk", "Arch Mage", "5"), ""},
		{audibleRec("B0SYN00041", "Short Cases", "Jim Butch", "The Wizard Files", "15.5"), "Butch/Wizard Files/WF15.5 Short Cases"},
		{audibleRec("B0SYN00051", "A House at the Edge", "Amy Sparks", "", ""), ""},
	}
	m := NewBookMatcher(books)
	for _, tc := range cases {
		b, ok := m.Match(tc.rec)
		got := ""
		if ok {
			got = b.Path
		}
		if got != tc.want {
			t.Errorf("%q #%s: matched %q, want %q", tc.rec.Title, tc.rec.SeriesPosition, got, tc.want)
		}
	}
}

// TestMatcherContestedVolumes: sub-series volumes land on the one volume the
// library holds; KeepBest leaves it to none of them when they tie, and to the
// best when one scores higher.
func TestMatcherContestedVolumes(t *testing.T) {
	books := []link.LibraryBook{
		{Path: "Yrs/Fate Cycle/FC04 - Strands of Fate - Volume 1", Title: "Strands of Fate - Volume 1", Authors: []string{"Yrs Ill"}, Series: series("Fate Cycle", "4")},
		{Path: "Val/Road to Craft/RC03", Title: "Road to Craft 3", Authors: []string{"Val Erio"}, Series: series("Road to Craft", "3")},
	}
	recs := []source.Record{
		audibleRec("B0SYN00061", "Strands of Fate: Volume 1", "Yrs Ill", "Fate Cycle", "4"),
		audibleRec("B0SYN00062", "Strands of Fate: Volume 3", "Yrs Ill", "Fate Cycle", "6"),
		audibleRec("B0SYN00071", "Road to Craft", "Val Erio", "Road to Craft", "1"),
		audibleRec("B0SYN00073", "Road to Craft 3", "Val Erio", "Road to Craft", "3"),
	}
	m := NewBookMatcher(books)
	claims := make([]Claim, len(recs))
	for i, r := range recs {
		if b, score, ok := m.MatchScored(r); ok {
			claims[i] = Claim{Path: b.Path, Score: score, Work: WorkKey(r)}
		}
	}
	keep := KeepBest(claims)
	for i, want := range []bool{true, false, false, true} {
		if keep[i] != want {
			t.Errorf("%q: kept %v, want %v (claims %+v)", recs[i].Title, keep[i], want, claims)
		}
	}
}

func TestKeepBest(t *testing.T) {
	cases := []struct {
		name   string
		claims []Claim
		want   []bool
	}{
		{"one claim each", []Claim{{Path: "a", Score: 1}, {Path: "b", Score: 1.2}}, []bool{true, true}},
		{"best score keeps", []Claim{{Path: "a", Score: 1.5, Work: "x"}, {Path: "a", Score: 2.5, Work: "y"}, {Path: "a", Score: 1, Work: "z"}}, []bool{false, true, false}},
		{"a tie keeps none", []Claim{{Path: "a", Score: 2.5, Work: "x"}, {Path: "a", Score: 2.5, Work: "y"}}, []bool{false, false}},
		{"a tie below the best is no matter", []Claim{{Path: "a", Score: 1, Work: "x"}, {Path: "a", Score: 1, Work: "y"}, {Path: "a", Score: 3, Work: "z"}}, []bool{false, false, true}},
		{"the same work twice keeps both", []Claim{{Path: "a", Score: 3, Work: "w"}, {Path: "a", Score: 3, Work: "w"}}, []bool{true, true}},
		{"the same work tied with another keeps none", []Claim{{Path: "a", Score: 3, Work: "w"}, {Path: "a", Score: 3, Work: "w"}, {Path: "a", Score: 3, Work: "v"}}, []bool{false, false, false}},
		{"no work key is never a duplicate", []Claim{{Path: "a", Score: 3}, {Path: "a", Score: 3}}, []bool{false, false}},
		{"an ASIN match beats a title match", []Claim{{Path: "a", Score: 3, Work: "x"}, {Path: "a", Score: 10, Work: "y"}}, []bool{false, true}},
		{"no claim", []Claim{{}, {Path: "a", Score: 1}}, []bool{false, true}},
	}
	for _, tc := range cases {
		got := KeepBest(tc.claims)
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: keep = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestMatcherRangeBooks: a book whose position is a range (a pack's folder,
// "LAM05-06" listed as "5-6") is matched by the pack starting where it does,
// before a single volume's folder at that position; never by a single volume
// (by position or by title), so it never takes a volume's ASIN by enrichment;
// and never by a pack starting elsewhere.
func TestMatcherRangeBooks(t *testing.T) {
	pack := audibleRec("B0SYN00081", "Iron Vale: Books 5-6", "Ona Fell", "Iron Vale", "5-6")
	vol5 := audibleRec("B0SYN00082", "Iron Vale 5", "Ona Fell", "Iron Vale", "5")
	ash := audibleRec("B0SYN00083", "Ash Road", "Ona Fell", "Iron Vale", "5")
	cases := []struct {
		name  string
		books []link.LibraryBook
		rec   source.Record
		want  string
	}{
		{"a single volume by position", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5-6")},
		}, vol5, ""},
		{"a single volume by title", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05-06", Title: "Ash Road and Ember Road", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5-6")},
		}, ash, ""},
		{"a range in another series too", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"},
				Series: []link.SeriesRef{{Name: "Fell Worlds", Position: "5"}, {Name: "Iron Vale", Position: "5-6"}}},
		}, vol5, ""},
		{"the pack on its range", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5-6")},
		}, pack, "Fell/Iron Vale/IV05-06"},
		{"the pack on its range before volume 5", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05 - Iron Vale", Title: "Iron Vale 5", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5")},
			{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5-6")},
		}, pack, "Fell/Iron Vale/IV05-06"},
		{"the pack on a folder at its start", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05 - Books 5-6", Title: "Iron Vale", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5")},
		}, pack, "Fell/Iron Vale/IV05 - Books 5-6"},
		{"not another pack", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV03-04", Title: "Iron Vale: Books 3-4", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "3-4")},
		}, pack, ""},
		{"a range's own ASIN", []link.LibraryBook{
			{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"}, ASIN: "B0SYN00082", Series: series("Iron Vale", "5-6")},
		}, vol5, "Fell/Iron Vale/IV05-06"},
	}
	for _, tc := range cases {
		got := ""
		if b, ok := NewBookMatcher(tc.books).Match(tc.rec); ok {
			got = b.Path
		}
		if got != tc.want {
			t.Errorf("%s: %q #%s matched %q, want %q", tc.name, tc.rec.Title, tc.rec.SeriesPosition, got, tc.want)
		}
	}
	// The folder the old listing gave as "5" was volume 5's by position: the
	// range is what keeps them apart.
	old := []link.LibraryBook{{Path: "Fell/Iron Vale/IV05-06", Title: "Iron Vale", Authors: []string{"Ona Fell"}, Series: series("Iron Vale", "5")}}
	if _, ok := NewBookMatcher(old).Match(vol5); !ok {
		t.Error("a single position no longer matches its volume")
	}
}

func TestRangeStart(t *testing.T) {
	for pos, want := range map[string]float64{"5-6": 5, "0.5-4": 0.5, " 7 - 9 ": 7, "1–3": 1} {
		if v, ok := rangeStart(pos); !ok || v != want {
			t.Errorf("rangeStart(%q) = %v %v, want %v", pos, v, ok, want)
		}
	}
	for _, pos := range []string{"", "5", "5-", "a-b", "Book 1-2", "5-6-7"} {
		if _, ok := rangeStart(pos); ok {
			t.Errorf("rangeStart(%q) is a range", pos)
		}
	}
}

// WorkKey: one book owned twice (title and authors alike, case, spacing and
// author order aside) is one work; another author, title or position is not.
func TestWorkKey(t *testing.T) {
	base := source.Record{Title: "Equal Measures", Authors: []string{"Tess Prat", "Ann Co"}, SeriesPosition: "3"}
	same := []source.Record{
		{Title: "equal  measures", Authors: []string{"Ann Co", "tess prat"}, SeriesPosition: "3"},
		{Title: "Equal Measures", Authors: []string{"Tess Prat", "Ann Co", "Tess Prat"}, SeriesPosition: " 3 "},
	}
	for _, r := range same {
		if WorkKey(r) != WorkKey(base) {
			t.Errorf("%+v is not the same work as %+v", r, base)
		}
	}
	other := []source.Record{
		{Title: "Equal Measures", Authors: []string{"Tess Prat"}, SeriesPosition: "3"},
		{Title: "Equal Measures 2", Authors: []string{"Tess Prat", "Ann Co"}, SeriesPosition: "3"},
		{Title: "Equal Measures", Authors: []string{"Tess Prat", "Ann Co"}, SeriesPosition: "4"},
	}
	for _, r := range other {
		if WorkKey(r) == WorkKey(base) {
			t.Errorf("%+v is the same work as %+v", r, base)
		}
	}
	if WorkKey(source.Record{Title: "X", Author: "A"}) != WorkKey(source.Record{Title: "X", Authors: []string{"A"}}) {
		t.Error("the first author alone is not the author set")
	}
}
