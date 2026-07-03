package audible

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kodestar/audiosilo-manager/internal/source"
)

type libraryResp struct {
	Items []libItem `json:"items"`
}

type libItem struct {
	ASIN                string      `json:"asin"`
	Title               string      `json:"title"`
	Subtitle            string      `json:"subtitle"`
	Authors             []nameRef   `json:"authors"`
	Narrators           []nameRef   `json:"narrators"`
	Series              []seriesRef `json:"series"`
	ReleaseDate         string      `json:"release_date"`
	IssueDate           string      `json:"issue_date"`
	ContentType         string      `json:"content_type"`
	ContentDeliveryType string      `json:"content_delivery_type"`
	// CustomerRights needs the customer_rights response group. nil when Audible
	// omits it - treat that as downloadable (defensive default).
	CustomerRights *customerRights `json:"customer_rights"`
}

type customerRights struct {
	IsConsumableOffline bool `json:"is_consumable_offline"`
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
			"response_groups": {"contributors,series,product_desc,product_attrs,customer_rights"},
			"num_results":     {"1000"},
			"page":            {strconv.Itoa(page)},
		}
		var resp libraryResp
		if err := c.doSigned(ctx, http.MethodGet, "/1.0/library?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		now := time.Now()
		for _, it := range resp.Items {
			recs = append(recs, it.toRecord(now))
		}
		if len(resp.Items) < 1000 {
			break
		}
	}
	return recs, nil
}

func (it libItem) toRecord(now time.Time) source.Record {
	rec := source.Record{
		Title: it.Title, TitleShort: it.Title, ASIN: it.ASIN, Key: it.ASIN,
		FileName:          safeFileName(it.Title) + ".m4b",
		UnavailableReason: it.unavailableReason(now),
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

// unavailableReason reports why a library item can't be downloaded ("" = it can).
// The license endpoint would fail these with an opaque error (HTTP 400 for
// periodicals, an empty voucher for titles without offline rights), so classifying
// them here lets the UI say why up front. All checks are defensive: a missing
// field never marks a book unavailable.
func (it libItem) unavailableReason(now time.Time) string {
	if d := parseAudibleDate(firstNonEmptyStr(it.ReleaseDate, it.IssueDate)); !d.IsZero() && d.After(now) {
		return "Not released yet (pre-order)"
	}
	switch it.ContentDeliveryType {
	case "Periodical", "PodcastParent":
		// Audible shows/podcasts are containers of episodes, not a single
		// downloadable audiobook file; a license request is refused outright.
		return "An Audible show/periodical - it has no downloadable audiobook file"
	}
	if it.CustomerRights != nil && !it.CustomerRights.IsConsumableOffline {
		return "Audible reports no download rights for this title (often a Plus/included title that left the catalog)"
	}
	return ""
}

// parseAudibleDate parses the date part of Audible's release/issue dates, which
// arrive as "2016-08-01" or a full timestamp. Zero time when unparseable.
func parseAudibleDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return time.Time{}
	}
	return t
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
