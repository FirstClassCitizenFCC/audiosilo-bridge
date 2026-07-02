package audible

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kodestar/audiosilo-manager/internal/source"
)

type libraryResp struct {
	Items []libItem `json:"items"`
}

type libItem struct {
	ASIN        string      `json:"asin"`
	Title       string      `json:"title"`
	Subtitle    string      `json:"subtitle"`
	Authors     []nameRef   `json:"authors"`
	Narrators   []nameRef   `json:"narrators"`
	Series      []seriesRef `json:"series"`
	ReleaseDate string      `json:"release_date"`
	IssueDate   string      `json:"issue_date"`
	ContentType string      `json:"content_type"`
}

type nameRef struct {
	Name string `json:"name"`
}

type seriesRef struct {
	Title    string `json:"title"`
	Sequence string `json:"sequence"`
}

// Library lists the account's owned audiobooks as import records (metadata only;
// SrcPath is empty - the file is fetched on demand by Acquire).
func (c *Client) Library(ctx context.Context) ([]source.Record, error) {
	var recs []source.Record
	for page := 1; ; page++ {
		q := url.Values{
			"response_groups": {"contributors,series,product_desc,product_attrs"},
			"num_results":     {"1000"},
			"page":            {strconv.Itoa(page)},
		}
		var resp libraryResp
		if err := c.doSigned(ctx, http.MethodGet, "/1.0/library?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			recs = append(recs, it.toRecord())
		}
		if len(resp.Items) < 1000 {
			break
		}
	}
	return recs, nil
}

func (it libItem) toRecord() source.Record {
	rec := source.Record{
		Title: it.Title, TitleShort: it.Title, ASIN: it.ASIN, Key: it.ASIN,
		FileName: safeFileName(it.Title) + ".m4b",
	}
	if len(it.Authors) > 0 {
		rec.Author = it.Authors[0].Name
	}
	if len(it.Narrators) > 0 {
		rec.Narrator = it.Narrators[0].Name
	}
	if len(it.Series) > 0 {
		rec.Series = it.Series[0].Title
		if seq, ok := parseSeq(it.Series[0].Sequence); ok {
			rec.Sequence, rec.HasSequence = seq, true
		}
	}
	rec.Year = yearFromDate(firstNonEmptyStr(it.ReleaseDate, it.IssueDate))
	return rec
}

// parseSeq parses the free-form Audible series sequence ("1", "0.5", "1-3", "").
// Only a clean single number counts; anything else leaves the book unsequenced.
func parseSeq(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func yearFromDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && isDigit4(s[:4]) {
		return s[:4]
	}
	return ""
}

func isDigit4(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) == 4
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func safeFileName(s string) string {
	s = strings.TrimSpace(s)
	r := strings.NewReplacer("/", "-", "\\", "-", ":", " -", "\x00", "")
	out := strings.TrimRight(r.Replace(s), " .")
	if out == "" {
		out = "audiobook"
	}
	return out
}
