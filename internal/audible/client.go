package audible

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an authenticated Audible API client for one registered device. Calls
// to the unrestricted endpoints (library, license, stats) are RSA-signed with the
// device key — see signRequest.
type Client struct {
	creds   Credentials
	key     *rsa.PrivateKey
	mp      Marketplace
	http    *http.Client
	baseURL string // https://api.audible.<tld>; overridable in tests
	now     func() time.Time
}

// NewClient builds a client from stored credentials.
func NewClient(creds Credentials) (*Client, error) {
	key, err := creds.PrivateKey()
	if err != nil {
		return nil, err
	}
	mp, ok := MarketplaceFor(creds.Marketplace)
	if !ok {
		return nil, fmt.Errorf("unknown marketplace %q", creds.Marketplace)
	}
	return &Client{
		creds:   creds,
		key:     key,
		mp:      mp,
		http:    &http.Client{Timeout: 90 * time.Second},
		baseURL: "https://" + mp.APIDomain(),
		now:     time.Now,
	}, nil
}

// doSigned issues an RSA-signed request and decodes a JSON response into out.
func (c *Client) doSigned(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := signRequest(req, body, c.key, c.creds.ADPToken, c.now()); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, snippet(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
