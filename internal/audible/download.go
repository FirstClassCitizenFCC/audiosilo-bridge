package audible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/kodestar/audiosilo-bridge/internal/progress"
)

// userAgent mimics the Audible iOS app, matching the reference clients
// (audible-cli sets the same on its session). Some Audible/CDN endpoints reject
// the default Go user agent.
const userAgent = "Audible/671 CFNetwork/1240.0.4 Darwin/20.6.0"

// DefaultMaxDownloadBytes caps one book's download when the caller sets no cap.
// The longest high-quality AAXC files are around 2 GB.
const DefaultMaxDownloadBytes int64 = 4 << 30

// ErrTooLarge is a download refused for passing its size cap. Nothing of it is
// kept.
var ErrTooLarge = errors.New("the download is larger than the cap")

// DownloadOptions shape one Download.
type DownloadOptions struct {
	// MaxBytes caps the whole file (resumed bytes included); 0 is
	// DefaultMaxDownloadBytes.
	MaxBytes int64
	// ExpectedSize is the file's size as the license gave it (0: unknown). A
	// partial file resumes only into a file of the size it was started on.
	ExpectedSize int64
	// BeforeWrite (may be nil) runs once the file's size is known (total, -1
	// when the server sent none) and before a byte is written, with the bytes a
	// resumed partial already holds: the caller's free-space check. Its error
	// ends the download with the partial kept.
	BeforeWrite func(total, held int64) error
	// OnProgress (may be nil) reports bytes held so far and the file's total
	// (-1 when the server sends none), throttled.
	OnProgress func(copied, total int64)
}

// partialState is what Download records beside a file it starts (StatePath):
// the size the server announced and its validators. A partial file resumes only
// with it, so a Range never appends another file's bytes, even under a new
// license's URL: the validator goes out as If-Range (a changed file answers 200,
// the whole of it), and the 206's total must be the size recorded (or the
// license's).
type partialState struct {
	Size         int64  `json:"size,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// StatePath is where Download records what it knows of the partial file at
// dest (dest + ".state").
func StatePath(dest string) string { return dest + ".state" }

// RemovePartial deletes a partial download and its state file (a cancel, or a
// job's terminal end).
func RemovePartial(dest string) {
	_ = os.Remove(dest)
	_ = os.Remove(StatePath(dest))
}

func readState(dest string) (partialState, bool) {
	b, err := os.ReadFile(StatePath(dest)) //nolint:gosec // G304: the caller's own temp path
	if err != nil {
		return partialState{}, false
	}
	var st partialState
	if json.Unmarshal(b, &st) != nil {
		return partialState{}, false
	}
	return st, true
}

// ifRange is the state's validator for If-Range: a strong ETag, else the
// Last-Modified date ("" for none).
func (st partialState) ifRange() string {
	if st.ETag != "" && !strings.HasPrefix(st.ETag, "W/") {
		return st.ETag
	}
	return st.LastModified
}

// Download fetches a content URL to dest, resuming a partial dest left by an
// earlier attempt, under this URL or another (a new license's), only when it
// can tell it is the same file: the state Download recorded when it started
// the file (StatePath) must exist, agree with opts.ExpectedSize when both
// know a size, and hold a validator (sent as If-Range) or a size. A 206 that
// starts where dest ends, for a file of that size, is appended; a 200 (If-Range
// found another file) starts dest again from its body, and anything else (a
// 206 from elsewhere, a 416 for another size) starts dest again with a new
// request. A failure part way leaves what arrived in dest (a refusal for size
// removes it, and its state), for the next attempt to resume.
//
// The content-delivery URL is served by Audible's CDS and validates the same
// device RSA signature as the API - audible-cli downloads it with the
// authenticated session, not a plain GET, so an unsigned request returns HTTP
// 403. We sign the request (and re-sign on each redirect, since a redirect to a
// presigned CDN ignores the headers but a redirect to another CDS path needs a
// fresh signature for that path).
func (c *Client) Download(ctx context.Context, downloadURL, dest string, opts DownloadOptions) error {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDownloadBytes
	}
	var have int64
	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
		have = info.Size()
	}
	st, known := readState(dest)
	want := st.Size
	if want <= 0 {
		want = opts.ExpectedSize
	}
	if have > maxBytes {
		RemovePartial(dest)
		return ErrTooLarge
	}
	if have > 0 && (!known || (opts.ExpectedSize > 0 && st.Size > 0 && st.Size != opts.ExpectedSize) ||
		(want <= 0 && st.ifRange() == "") || (want > 0 && have > want)) {
		// Nothing proves this partial is the file the URL serves.
		RemovePartial(dest)
		have = 0
	}
	// A dedicated client with no overall timeout - a large audiobook can exceed the
	// API client's per-request timeout; cancellation is governed by ctx. Redirects
	// (CDS -> CDN) are followed, re-signed for the new URL.
	client := &http.Client{
		Transport: c.set.http.Transport,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return c.signGet(r)
		},
	}
	resp, err := sendWithBackoff(ctx, client, c.set.sleep, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, err
		}
		if have > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
			if v := st.ifRange(); v != "" {
				req.Header.Set("If-Range", v)
			}
		}
		return req, c.signGet(req)
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// restart drops dest and downloads it again from the start, without a Range.
	restart := func() error {
		_ = resp.Body.Close()
		RemovePartial(dest)
		return c.Download(ctx, downloadURL, dest, opts)
	}

	var (
		appendTo bool
		total    int64
	)
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		start, size, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != have || (want > 0 && size != want) {
			return restart() // not the rest of our file
		}
		appendTo, total = true, size
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
		// dest may already be whole: the server says how long the file is.
		if _, size, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && size == have && (want <= 0 || want == have) {
			return nil
		}
		return restart()
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		total = resp.ContentLength
	default:
		return &httpError{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: "download"}
	}
	if total > maxBytes {
		RemovePartial(dest)
		return ErrTooLarge
	}
	held := int64(0)
	if appendTo {
		held = have
	}
	if opts.BeforeWrite != nil {
		if err := opts.BeforeWrite(total, held); err != nil {
			return err
		}
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if appendTo {
		flags = os.O_WRONLY | os.O_APPEND
	} else {
		fresh := partialState{Size: max(total, 0), ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
		b, _ := json.Marshal(fresh)
		if err := os.WriteFile(StatePath(dest), b, 0o600); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(dest, flags, 0o600) //nolint:gosec // the caller's own temp path
	if err != nil {
		return err
	}
	// Read at most one byte past the cap, to tell a file at the cap from one over it.
	src := io.LimitReader(resp.Body, maxBytes-held+1)
	if opts.OnProgress != nil {
		cb := opts.OnProgress
		src = &progress.Reader{R: src, Total: total, CB: func(copied, t int64) { cb(held+copied, t) }}
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if held+n > maxBytes {
		RemovePartial(dest)
		return ErrTooLarge
	}
	if err != nil {
		return err
	}
	if total >= 0 && held+n != total {
		return fmt.Errorf("download ended at %d of %d bytes: %w", held+n, total, io.ErrUnexpectedEOF)
	}
	return nil
}

// parseContentRange reads "bytes start-end/size" (size "*" is unknown, -1).
func parseContentRange(v string) (start, size int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, false
	}
	span, sizeStr, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	size = -1
	if sizeStr != "*" {
		n, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		size = n
	}
	if span == "*" {
		return 0, size, size >= 0
	}
	first, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	return start, size, true
}

// signGet sets the Audible user agent and an RSA signature for a GET request's URL.
func (c *Client) signGet(req *http.Request) error {
	req.Header.Set("User-Agent", userAgent)
	return signRequest(req, nil, c.key, c.creds.ADPToken, c.now())
}
