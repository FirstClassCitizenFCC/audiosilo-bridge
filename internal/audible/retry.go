package audible

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Backoff for an answer that asks the caller to slow down (429 Too Many Requests,
// 503 Service Unavailable): Audible's rate limits are undocumented, so every call
// waits and tries again a few times rather than failing a whole poll.
const (
	// maxAttempts is how many times one call is sent before its 429/503 is the
	// answer.
	maxAttempts = 4
	// backoffBase is the first wait when the answer has no Retry-After; each
	// later wait doubles it.
	backoffBase = 2 * time.Second
	// maxBackoff caps one wait, Retry-After included: a server asking for an hour
	// fails the call instead of holding a job that long.
	maxBackoff = 2 * time.Minute
)

// retryable reports whether status asks the caller to back off and try again.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// errBackoffTooLong is the answer when Retry-After asks for more than maxBackoff.
var errBackoffTooLong = errors.New("audible asked to wait longer than the Bridge will")

// sendWithBackoff sends the request newReq builds (a fresh one per attempt, so a
// signature carries its own date) and returns the first answer that isn't a
// 429/503, or the last one. A 429/503 body is drained and closed before the wait.
func sendWithBackoff(ctx context.Context, hc *http.Client, sleep func(context.Context, time.Duration) error,
	newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if !retryable(resp.StatusCode) || attempt == maxAttempts {
			return resp, nil
		}
		wait, ok := retryAfter(resp.Header.Get("Retry-After"), time.Now())
		if !ok {
			wait = backoffBase << (attempt - 1)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if wait > maxBackoff {
			return nil, &httpError{StatusCode: resp.StatusCode, Method: req.Method, Path: req.URL.Path, Body: errBackoffTooLong.Error()}
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// retryAfter reads a Retry-After value: delay seconds or an HTTP date. A date in
// the past is no wait; anything unreadable is no answer.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// HTTPStatus is the Audible or Amazon status code behind err, or 0 when err
// isn't an HTTP answer (a network failure, a cancelled context).
func HTTPStatus(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	return 0
}

// IsRefused reports whether err is Audible refusing the device's credentials
// (401 or 403 on a signed call): the account must sign in again.
func IsRefused(err error) bool {
	s := HTTPStatus(err)
	return s == http.StatusUnauthorized || s == http.StatusForbidden
}
