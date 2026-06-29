package audible

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Download fetches a (presigned) content URL to dest. The URL carries its own auth,
// so the request is not signed.
func (c *Client) Download(ctx context.Context, downloadURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	// A dedicated client with no overall timeout — a large audiobook can exceed the
	// API client's per-request timeout; cancellation is governed by ctx.
	resp, err := (&http.Client{}).Do(req)
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
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(dest)
		return err
	}
	return f.Close()
}
