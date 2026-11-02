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
	// RuntimeLengthMin comes from the product_attrs response group (minute-rounded,
	// 0/absent for shows and some titles).
	RuntimeLengthMin int `json:"runtime_length_min"`
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

const (
	// libraryPageSize is the per-request item count; a short page means the last one.
	libraryPageSize = 1000
	// maxLibraryPages bounds the pager. At libraryPageSize that allows a 250,000-title
	// account - far beyond any real library - while keeping a misbehaving endpoint
	// finite: the only exit used to be a short page, so an endpoint that clamped `page`
	// or replayed the last one for an out-of-range value appended 1000 records per
	// request until the caller's deadline, with nothing bounding the memory.
	maxLibraryPages = 250
)

// dedupeKey identifies a library item for the pager's dedupe. The ASIN is the real
// identity, but it MUST fall back to something non-empty: an item keyed on "" is
// never recorded as seen, so a replayed page re-appends it every time and the
// "contributed nothing new" stop never fires - one blank-ASIN item was enough to
// spin the pager to its page cap. The API always sends an ASIN; this is the
// defensive path, and collapsing two same-titled ASIN-less items loses nothing
// (Record.Key is the ASIN, so downstream can't tell them apart anyway).
func (it libItem) dedupeKey() string {
	if it.ASIN != "" {
		return it.ASIN
	}
	return "title:" + it.Title
}

// Library lists the account's owned audiobooks as import records (metadata only;
// SrcPath is empty - the file is fetched on demand by Acquire). Items are deduped
// by dedupeKey, so a repeated page can neither duplicate a book nor spin the pager.
func (c *Client) Library(ctx context.Context) ([]source.Record, error) {
	var recs []source.Record
	seen := make(map[string]bool, libraryPageSize) // one page is the common case
	for page := 1; page <= maxLibraryPages; page++ {
		q := url.Values{
			"response_groups": {"contributors,series,product_desc,product_attrs,customer_rights"},
			"num_results":     {strconv.Itoa(libraryPageSize)},
			"page":            {strconv.Itoa(page)},
		}
		var resp libraryResp
		if err := c.doSigned(ctx, http.MethodGet, "/1.0/library?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		now := time.Now()
		before := len(recs)
		for _, it := range resp.Items {
			k := it.dedupeKey()
			if seen[k] {
				continue
			}
			seen[k] = true
			recs = append(recs, it.toRecord(now))
		}
		// A short page is the last one. A full page that contributed nothing new means
		// the endpoint is replaying a page instead of advancing - stop either way.
		if len(resp.Items) < libraryPageSize || len(recs) == before {
			break
		}
	}
	return recs, nil
}

func (it libItem) toRecord(now time.Time) source.Record {
	rec := source.Record{
		Title: it.Title, TitleShort: it.Title, ASIN: it.ASIN, Key: it.ASIN,
		FileName:          safeFileName(it.Title) + ".m4b",
		RuntimeSec:        float64(it.RuntimeLengthMin) * 60,
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
	if ReleaseIsUpcoming(firstNonEmptyStr(it.ReleaseDate, it.IssueDate), now) {
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

// ReleaseIsUpcoming reports whether an Audible release/issue date falls after now -
// a pre-order / not-yet-released title. A zero now disables the check; an empty or
// unparseable date reads as already released (never withhold a real title). It is
// the one definition of "upcoming" - seriesgap uses it too.
func ReleaseIsUpcoming(date string, now time.Time) bool {
	if now.IsZero() {
		return false
	}
	d := parseAudibleDate(date)
	return !d.IsZero() && d.After(now)
}

// parseAudibleDate parses Audible's release/issue dates, which arrive as
// "2016-08-01" (sometimes with a time suffix), "2016-08", or "2016" depending on
// the endpoint. Zero time when unparseable.
func parseAudibleDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if len(s) > 10 {
		s = s[:10] // a full timestamp leads with the date
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
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
