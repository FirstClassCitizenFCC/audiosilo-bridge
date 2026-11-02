package audible

import (
	"context"
	"net/http"
	"net/url"

	"github.com/kodestar/audiosilo-server/pkg/match"
)

// CatalogProduct is one Audible catalog entry (a book in a series), read from the
// "same series" similarity endpoint. It is metadata only - enough to tell whether
// the user is missing an entry and to display it.
type CatalogProduct struct {
	ASIN        string
	Title       string
	Subtitle    string
	Authors     []string
	SeriesName  string
	Sequence    float64
	HasSequence bool
	ReleaseDate string
	ContentType string
}

type simsResp struct {
	// The sims endpoint returns members under "similar_products"; some marketplaces
	// have historically used "products", so accept both.
	SimilarProducts []catalogItem `json:"similar_products"`
	Products        []catalogItem `json:"products"`
}

type catalogItem struct {
	ASIN        string      `json:"asin"`
	Title       string      `json:"title"`
	Subtitle    string      `json:"subtitle"`
	Authors     []nameRef   `json:"authors"`
	Series      []seriesRef `json:"series"`
	ReleaseDate string      `json:"release_date"`
	IssueDate   string      `json:"issue_date"`
	ContentType string      `json:"content_type"`
}

// SeriesMembers returns the other books in seedASIN's series via the Audible
// "InTheSameSeries" similarity endpoint (RSA-signed, like every other call). It
// asks for the series response group so each member carries its own sequence, then
// picks the series block matching seriesName (a book can belong to several series).
// The endpoint caps at ~50 results and does not guarantee order, so callers seed it
// from several owned books and sort the union by sequence themselves.
func (c *Client) SeriesMembers(ctx context.Context, seedASIN, seriesName string) ([]CatalogProduct, error) {
	q := url.Values{
		"similarity_type": {"InTheSameSeries"},
		"num_results":     {"50"},
		"response_groups": {"media,product_desc,series,contributors"},
	}
	path := "/1.0/catalog/products/" + url.PathEscape(seedASIN) + "/sims?" + q.Encode()
	var resp simsResp
	if err := c.doSigned(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	items := resp.SimilarProducts
	if len(items) == 0 {
		items = resp.Products
	}
	out := make([]CatalogProduct, 0, len(items))
	for _, it := range items {
		out = append(out, it.toProduct(seriesName))
	}
	return out, nil
}

func (it catalogItem) toProduct(seriesName string) CatalogProduct {
	p := CatalogProduct{
		ASIN:        it.ASIN,
		Title:       it.Title,
		Subtitle:    it.Subtitle,
		ReleaseDate: firstNonEmptyStr(it.ReleaseDate, it.IssueDate),
		ContentType: it.ContentType,
	}
	for _, a := range it.Authors {
		if a.Name != "" {
			p.Authors = append(p.Authors, a.Name)
		}
	}
	p.SeriesName, p.Sequence, p.HasSequence = pickSeries(it.Series, seriesName)
	return p
}

// pickSeries chooses the series block matching want (so a book in multiple series
// reports the sequence for the series being examined), falling back to the first.
func pickSeries(refs []seriesRef, want string) (string, float64, bool) {
	if target := match.NormalizeSeries(want); target != "" {
		for _, r := range refs {
			if match.NormalizeSeries(r.Title) == target {
				seq, ok := parseSeq(r.Sequence)
				return r.Title, seq, ok
			}
		}
	}
	if len(refs) > 0 {
		seq, ok := parseSeq(refs[0].Sequence)
		return refs[0].Title, seq, ok
	}
	return "", 0, false
}
