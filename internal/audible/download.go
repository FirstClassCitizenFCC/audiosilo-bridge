package audible

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/kodestar/audiosilo-manager/internal/progress"
)

// userAgent mimics the Audible iOS app, matching the reference clients
// (audible-cli sets the same on its session). Some Audible/CDN endpoints reject
// the default Go user agent.
const userAgent = "Audible/671 CFNetwork/1240.0.4 Darwin/20.6.0"

// Download fetches a content URL to dest. The content-delivery URL is served by
// Audible's CDS and validates the same device RSA signature as the API — audible-cli
// downloads it with the authenticated session, not a plain GET, so an unsigned
// request returns HTTP 403. We sign the request (and re-sign on each redirect, since
// a redirect to a presigned CDN ignores the headers but a redirect to another CDS
// path needs a fresh signature for that path).
// onProgress (may be nil) reports bytes copied and the total (Content-Length, or -1
// when the server sends none), throttled so a large audiobook doesn't flood callers.
func (c *Client) Download(ctx context.Context, downloadURL, dest string, onProgress func(copied, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	if err := c.signGet(req); err != nil {
		return err
	}
	// A dedicated client with no overall timeout — a large audiobook can exceed the
	// API client's per-request timeout; cancellation is governed by ctx. Redirects
	// (CDS → CDN) are followed, re-signed for the new URL.
	client := &http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return c.signGet(r)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	var src io.Reader = resp.Body
	if onProgress != nil {
		src = &progress.Reader{R: resp.Body, Total: resp.ContentLength, CB: onProgress}
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		_ = os.Remove(dest)
		return err
	}
	return f.Close()
}

// signGet sets the Audible user agent and an RSA signature for a GET request's URL.
func (c *Client) signGet(req *http.Request) error {
	req.Header.Set("User-Agent", userAgent)
	return signRequest(req, nil, c.key, c.creds.ADPToken, c.now())
}
