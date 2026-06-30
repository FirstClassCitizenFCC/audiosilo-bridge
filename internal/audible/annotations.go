package audible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// flexMS unmarshals a millisecond value that Audible may encode as either a JSON
// number or a quoted string.
type flexMS int64

func (f *flexMS) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexMS(n)
		return nil
	}
	fl, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("position_ms %q: %w", s, err)
	}
	*f = flexMS(int64(fl))
	return nil
}

// LastPositions fetches the last-heard position (in milliseconds) for each ASIN
// from Audible's annotations service. ASINs with no recorded position are omitted.
func (c *Client) LastPositions(ctx context.Context, asins []string) (map[string]int64, error) {
	if len(asins) == 0 {
		return map[string]int64{}, nil
	}
	path := "/1.0/annotations/lastpositions?" + url.Values{"asins": {strings.Join(asins, ",")}}.Encode()
	var resp struct {
		Annots []struct {
			ASIN string `json:"asin"`
			Last struct {
				Status     string `json:"status"`
				PositionMs flexMS `json:"position_ms"`
			} `json:"last_position_heard"`
		} `json:"asin_last_position_heard_annots"`
	}
	if err := c.doSigned(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(resp.Annots))
	for _, a := range resp.Annots {
		if a.Last.Status != "Exists" {
			continue
		}
		out[a.ASIN] = int64(a.Last.PositionMs)
	}
	return out, nil
}

// SetLastPosition writes a last-heard position (milliseconds) back to Audible so
// other devices reflect server-side progress, via PUT /1.0/lastpositions/{asin}
// with {acr, asin, position_ms} (per the mkb79/Audible API reference). The acr is
// the Audible Content Reference from a license request (see ContentACR). Gated
// behind an explicit confirmation in the UI; a non-2xx is a per-book failure.
func (c *Client) SetLastPosition(ctx context.Context, asin, acr string, positionMs int64) error {
	if asin == "" || acr == "" {
		return fmt.Errorf("asin and acr are required to set a position")
	}
	body, err := json.Marshal(map[string]any{
		"acr":         acr,
		"asin":        asin,
		"position_ms": positionMs, // numeric milliseconds
	})
	if err != nil {
		return err
	}
	return c.doSigned(ctx, http.MethodPut, "/1.0/lastpositions/"+url.PathEscape(asin), body, nil)
}
