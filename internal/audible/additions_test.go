package audible

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recordSleeps is a backoff wait that records each wait instead of sleeping.
func recordSleeps(waits *[]time.Duration) Option {
	return withSleep(func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return ctx.Err()
	})
}

func backoffClient(t *testing.T, baseURL string, waits *[]time.Duration) *Client {
	t.Helper()
	keyPEM, _ := pemKey(t, false)
	c, err := NewClient(Credentials{
		ADPToken: "ADP", DevicePrivateKey: keyPEM, DeviceType: deviceType,
		DeviceSerial: "SER", CustomerID: "CUST", Marketplace: "us",
	}, WithEndpoints(Endpoints{API: baseURL}), recordSleeps(waits))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A 429 with Retry-After waits that long and tries again, signing afresh; a 503
// with none backs off exponentially.
func TestBackoffHonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var sigs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigs = append(sigs, r.Header.Get("x-adp-signature"))
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	defer ts.Close()
	var waits []time.Duration
	if _, err := backoffClient(t, ts.URL, &waits).Library(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	if len(waits) != 2 || waits[0] != 7*time.Second || waits[1] != 2*backoffBase {
		t.Fatalf("waits = %v, want [7s %v]", waits, 2*backoffBase)
	}
	for _, s := range sigs {
		if s == "" {
			t.Fatal("a retried request was not signed")
		}
	}
}

// Denied: a call that keeps answering 429 fails after maxAttempts with the
// status (no endless retry), and a Retry-After past the cap fails at once.
func TestBackoffGivesUp(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()
	var waits []time.Duration
	_, err := backoffClient(t, ts.URL, &waits).Library(context.Background())
	if HTTPStatus(err) != http.StatusTooManyRequests || int(calls.Load()) != maxAttempts {
		t.Fatalf("err = %v (status %d), calls = %d", err, HTTPStatus(err), calls.Load())
	}

	calls.Store(0)
	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer long.Close()
	waits = nil
	_, err = backoffClient(t, long.URL, &waits).Library(context.Background())
	if HTTPStatus(err) != http.StatusServiceUnavailable || calls.Load() != 1 || len(waits) != 0 {
		t.Fatalf("err = %v, calls = %d, waits = %v", err, calls.Load(), waits)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"0":                             0,
		"30":                            30 * time.Second,
		"Mon, 02 Nov 2026 12:01:00 GMT": time.Minute,
		"Mon, 02 Nov 2026 11:00:00 GMT": 0, // in the past
	} {
		if got, ok := retryAfter(in, now); !ok || got != want {
			t.Errorf("retryAfter(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "-5", "soon"} {
		if _, ok := retryAfter(in, now); ok {
			t.Errorf("retryAfter(%q) read as a wait", in)
		}
	}
	// A huge value is longer than the Bridge waits, never an overflow into a
	// short or negative wait.
	for _, in := range []string{"9999999999", "18446744074", "9223372036854775807"} {
		if got, ok := retryAfter(in, now); !ok || got <= maxBackoff {
			t.Errorf("retryAfter(%q) = %v, %v; want more than %v", in, got, ok, maxBackoff)
		}
	}
}

func TestRefusedStatus(t *testing.T) {
	for status, want := range map[int]bool{401: true, 403: true, 400: false, 404: false, 500: false} {
		err := error(&httpError{StatusCode: status})
		if IsRefused(err) != want {
			t.Errorf("IsRefused(%d) = %v", status, !want)
		}
	}
	if IsRefused(errors.New("dial tcp: refused")) || HTTPStatus(context.Canceled) != 0 {
		t.Error("a non-HTTP error read as an answer")
	}
}

// The authorize URL follows the endpoints: a fake's sign-in page and its
// maplanding, never Amazon's.
func TestAuthorizeURLEndpoints(t *testing.T) {
	mp, _ := MarketplaceFor("uk")
	u := BuildAuthorizeURL(mp, PKCE{Challenge: "c"}, "SER", WithEndpoints(Endpoints{Amazon: "http://127.0.0.1:9/"}))
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "127.0.0.1:9" || parsed.Path != "/ap/signin" {
		t.Fatalf("authorize URL = %s", u)
	}
	if rt := parsed.Query().Get("openid.return_to"); rt != "http://127.0.0.1:9/ap/maplanding" {
		t.Fatalf("return_to = %q", rt)
	}
	if def := EndpointsFor(mp); def.Amazon != "https://www.amazon.co.uk" || def.AmazonAPI != "https://api.amazon.co.uk" || def.API != "https://api.audible.co.uk" {
		t.Fatalf("EndpointsFor(uk) = %+v", def)
	}
}

// rangeServer serves body, honouring a Range from its start offset when resume
// is on, else always answering 200 with the whole body.
func rangeServer(t *testing.T, body string, resume bool, ranges *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rg := r.Header.Get("Range")
		*ranges = append(*ranges, rg)
		if rg != "" && resume {
			start, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(len(body)-1)+"/"+strconv.Itoa(len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, body[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
}

// A partial file resumes with a Range and is appended; progress counts the bytes
// already held.
func TestDownloadResumes(t *testing.T) {
	body := strings.Repeat("0123456789", 300)
	var ranges []string
	ts := rangeServer(t, body, true, &ranges)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "book.aaxc")
	if err := os.WriteFile(dest, []byte(body[:1000]), 0o600); err != nil {
		t.Fatal(err)
	}
	var last int64
	err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f", dest, DownloadOptions{
		OnProgress: func(copied, _ int64) { last = copied },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != body {
		t.Fatalf("resumed file has %d bytes, want the whole %d", len(got), len(body))
	}
	if len(ranges) != 1 || ranges[0] != "bytes=1000-" || last != int64(len(body)) {
		t.Fatalf("ranges = %v, last progress = %d", ranges, last)
	}
}

// A server that ignores the Range (200, the whole body) starts the file again
// instead of appending the whole body to the partial one.
func TestDownloadRestartsOnFullAnswer(t *testing.T) {
	body := strings.Repeat("ab", 700)
	var ranges []string
	ts := rangeServer(t, body, false, &ranges)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "book.aaxc")
	if err := os.WriteFile(dest, []byte("stale-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f", dest, DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != body {
		t.Fatalf("file = %d bytes, want %d", len(got), len(body))
	}
}

// A 206 that doesn't start where the file ends is not appended.
func TestDownloadRejectsMisplacedRange(t *testing.T) {
	body := strings.Repeat("x", 500)
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-499/500")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "book.aaxc")
	if err := os.WriteFile(dest, []byte("yyyy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f", dest, DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != body || calls != 2 {
		t.Fatalf("file = %q (%d calls)", got, calls)
	}
}

// A whole file answered 416 with its own size is done, not fetched again.
func TestDownloadAlreadyWhole(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes */4")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "book.aaxc")
	if err := os.WriteFile(dest, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f", dest, DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "done" {
		t.Fatalf("file = %q", got)
	}
}

// Denied: a file past the cap is refused, by its Content-Length before a byte
// is written, or by counting when the server sends none; nothing is kept.
func TestDownloadSizeCap(t *testing.T) {
	body := strings.Repeat("z", 2000)
	withLength := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer withLength.Close()
	chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 4; i++ {
			_, _ = io.WriteString(w, body[:500])
			w.(http.Flusher).Flush()
		}
	}))
	defer chunked.Close()
	for name, ts := range map[string]*httptest.Server{"length": withLength, "chunked": chunked} {
		dest := filepath.Join(t.TempDir(), "book.aaxc")
		err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f", dest, DownloadOptions{MaxBytes: 1999})
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%s: err = %v, want ErrTooLarge", name, err)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("%s: an oversized download was kept", name)
		}
	}
	// Allowed: exactly at the cap.
	dest := filepath.Join(t.TempDir(), "book.aaxc")
	if err := testClient(t, chunked.URL).Download(context.Background(), chunked.URL+"/f", dest, DownloadOptions{MaxBytes: 2000}); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}

// A download error names neither the presigned URL nor its signature.
func TestDownloadErrorHidesURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()
	err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/f?X-Amz-Signature=SECRETSIG", filepath.Join(t.TempDir(), "x"), DownloadOptions{})
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") || HTTPStatus(err) != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	// Nor does a request that got no answer (Go's error names the URL).
	gone := httptest.NewServer(http.NotFoundHandler())
	base := gone.URL
	gone.Close()
	err = testClient(t, base).Download(context.Background(), base+"/f.aaxc?Policy=POL&Signature=SECRETSIG&Key-Pair-Id=KP",
		filepath.Join(t.TempDir(), "y"), DownloadOptions{})
	var ue *url.Error
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") || strings.Contains(err.Error(), "Policy") ||
		!errors.As(err, &ue) || !strings.HasSuffix(ue.URL, "/f.aaxc") {
		t.Fatalf("a transport failure = %v", err)
	}
}

// Deregister refreshes an expired access token, then deregisters this device
// only, with the fresh bearer token; neither error nor request leaks a token.
func TestDeregisterRefreshesFirst(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/auth/token":
			_ = r.ParseForm()
			if r.PostForm.Get("source_token") != "RT" || r.PostForm.Get("requested_token_type") != "access_token" {
				t.Errorf("refresh form = %v", r.PostForm)
			}
			_, _ = io.WriteString(w, `{"access_token":"AT2","expires_in":3600}`)
		case "/auth/deregister":
			if got := r.Header.Get("Authorization"); got != "Bearer AT2" {
				t.Errorf("Authorization = %q", got)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["deregister_all_existing_accounts"] != false {
				t.Errorf("deregister body = %v (must never deregister the account's other devices)", body)
			}
			_, _ = io.WriteString(w, `{"response":{"success":{}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	creds := Credentials{AccessToken: "AT-OLD", RefreshToken: "RT", Marketplace: "us", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if err := Deregister(context.Background(), creds, WithEndpoints(Endpoints{AmazonAPI: ts.URL}), noSleep()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/auth/token,/auth/deregister" {
		t.Fatalf("paths = %v", paths)
	}
}

// Denied: an identity refusal is an HTTP error that carries the status and
// Amazon's answer, never the token sent.
func TestDeregisterRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"response":{"error":{"code":"InvalidToken"}}}`)
	}))
	defer ts.Close()
	creds := Credentials{AccessToken: "SECRET-AT", RefreshToken: "SECRET-RT", Marketplace: "us", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	err := Deregister(context.Background(), creds, WithEndpoints(Endpoints{AmazonAPI: ts.URL}), noSleep())
	if HTTPStatus(err) != http.StatusUnauthorized || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	if _, err := RefreshAccessToken(context.Background(), Credentials{Marketplace: "us"}); err == nil {
		t.Fatal("a refresh with no refresh token should fail before any request")
	}
}

func TestReleaseDay(t *testing.T) {
	for in, want := range map[string]string{"2016-08-01": "2016-08-01", "2016-08-01T00:00:00Z": "2016-08-01",
		"2016-08": "", "2016": "", "": "", "soon": ""} {
		if got := releaseDay(in); got != want {
			t.Errorf("releaseDay(%q) = %q, want %q", in, got, want)
		}
	}
}
