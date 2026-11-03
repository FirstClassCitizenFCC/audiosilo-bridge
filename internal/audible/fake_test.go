//go:build audiblefake

package audible

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible/audiblefake"
)

// startFake serves a fresh fake and returns it with options pointing a client at it.
func startFake(t *testing.T) (*audiblefake.Fake, []Option) {
	t.Helper()
	fake := audiblefake.New(audiblefake.Options{KeyBits: 1024})
	ts := httptest.NewServer(fake.Handler())
	t.Cleanup(ts.Close)
	return fake, []Option{WithEndpoints(Endpoints{Amazon: ts.URL, AmazonAPI: ts.URL, API: ts.URL}), noSleep()}
}

// signIn runs the browser half against the fake (open the authorize URL, land
// on maplanding) and returns the pasted-back redirect URL with the PKCE pair
// and serial the Bridge holds.
func signIn(t *testing.T, opts []Option) (redirect string, pkce PKCE, serial string) {
	t.Helper()
	mp, _ := MarketplaceFor("us")
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if serial, err = NewDeviceSerial(); err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(BuildAuthorizeURL(mp, pkce, serial, opts...))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("sign-in answered %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), pkce, serial
}

// registerWithFake signs in and registers a device against the fake.
func registerWithFake(t *testing.T, opts []Option) Credentials {
	t.Helper()
	mp, _ := MarketplaceFor("us")
	redirect, pkce, serial := signIn(t, opts)
	code, err := ParseAuthCode(redirect)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := Register(context.Background(), mp, code, pkce, serial, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func TestFakeSignInAndRegister(t *testing.T) {
	fake, opts := startFake(t)
	redirect, _, serial := signIn(t, opts)
	if !strings.Contains(redirect, "/ap/maplanding?") {
		t.Fatalf("redirect = %q", redirect)
	}
	code, err := ParseAuthCode(redirect)
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := MarketplaceFor("us")

	// Denied: a verifier that isn't the challenge's.
	if _, err := Register(context.Background(), mp, code, PKCE{Verifier: "wrong"}, serial, opts...); HTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("wrong verifier: err = %v", err)
	}
	// The failed attempt used the code up, as Amazon does: sign in again.
	redirect, pkce, serial := signIn(t, opts)
	code, _ = ParseAuthCode(redirect)
	creds, err := Register(context.Background(), mp, code, pkce, serial, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if creds.ADPToken == "" || creds.RefreshToken == "" || creds.DeviceSerial != serial || creds.Marketplace != "us" || creds.CustomerID == "" {
		t.Fatalf("creds = %+v", creds)
	}
	if _, err := creds.PrivateKey(); err != nil {
		t.Fatal(err)
	}
	// Denied: a code is good once.
	if _, err := Register(context.Background(), mp, code, pkce, serial, opts...); HTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("reused code: err = %v", err)
	}
	reg := fake.Calls().Registered
	if len(reg) != 1 || reg[0].Serial != serial || !strings.HasSuffix(reg[0].DeviceName, "%AudioSilo") {
		t.Fatalf("registered = %+v", reg)
	}
}

func TestFakeLibraryStates(t *testing.T) {
	_, opts := startFake(t)
	c, err := NewClient(registerWithFake(t, opts), opts...)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := c.Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(audiblefake.Fixtures) || len(recs) != 30 {
		t.Fatalf("library = %d titles", len(recs))
	}
	counts := map[string]int{}
	for i, r := range recs {
		want := audiblefake.Fixtures[i]
		if r.ASIN != want.ASIN || r.Title != want.Title {
			t.Fatalf("record %d = %+v", i, r)
		}
		var got string
		switch {
		case r.UnavailableReason == "":
			got = "available"
		case strings.Contains(r.UnavailableReason, "pre-order"):
			got = "preorder"
		default:
			got = "unavailable"
		}
		if got != want.State() {
			t.Errorf("%s: state %s, want %s (%q)", r.ASIN, got, want.State(), r.UnavailableReason)
		}
		counts[got]++
	}
	if counts["available"] != 22 || counts["preorder"] != 4 || counts["unavailable"] != 4 {
		t.Fatalf("states = %v", counts)
	}
	thicket := recs[2]
	if thicket.Series != "Orchard Cycle" || !thicket.HasSequence || thicket.Sequence != 3 || thicket.RuntimeSec != 745*60 || thicket.Narrator != "Iris Fenwick" {
		t.Fatalf("series record = %+v", thicket)
	}
}

// The whole library's places in 25s, split on the fake's 400 over 10, with
// last_updated; a place written back is recorded and read back.
func TestFakeLastPositions(t *testing.T) {
	fake, opts := startFake(t)
	c, err := NewClient(registerWithFake(t, opts), opts...)
	if err != nil {
		t.Fatal(err)
	}
	var asins []string
	for _, f := range audiblefake.Fixtures {
		asins = append(asins, f.ASIN)
	}
	got, err := c.LastPositions(context.Background(), asins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("positions = %d, want the 7 fixtures", len(got))
	}
	p := got["B0FAKE0002"]
	if p.PositionMs != 12_345_678 || p.LastUpdated.IsZero() || time.Since(p.LastUpdated) < 29*24*time.Hour {
		t.Fatalf("B0FAKE0002 = %+v", p)
	}
	sizes := fake.Calls().LastPositionsSizes
	if sizes[0] != 25 || slices.Max(sizes) != 25 || !slices.ContainsFunc(sizes, func(n int) bool { return n <= 10 }) {
		t.Fatalf("request sizes = %v, want 25 refused then split", sizes)
	}

	if err := c.SetLastPosition(context.Background(), "B0FAKE0006", "CR!FAKEACR", 5000); err != nil {
		t.Fatal(err)
	}
	if put := fake.Calls().PutPositions; len(put) != 1 || put[0] != (audiblefake.PutPosition{ASIN: "B0FAKE0006", ACR: "CR!FAKEACR", PositionMs: 5000}) {
		t.Fatalf("put = %+v", put)
	}
	again, err := c.LastPositions(context.Background(), []string{"B0FAKE0006"}, nil)
	if err != nil || again["B0FAKE0006"].PositionMs != 5000 {
		t.Fatalf("read back = %+v, %v", again, err)
	}
}

// Deregister removes the device (the fake records it) and its credentials stop
// working: a signed call is then refused, as is one from an account Audible
// refuses, and one signed with another key.
func TestFakeDeregisterAndRefusals(t *testing.T) {
	fake, opts := startFake(t)
	creds := registerWithFake(t, opts)
	c, err := NewClient(creds, opts...)
	if err != nil {
		t.Fatal(err)
	}

	// Denied: the right token signed with a key the fake never issued.
	other, _ := rsa.GenerateKey(rand.Reader, 1024)
	forged := creds
	forged.DevicePrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)}))
	fc, err := NewClient(forged, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fc.Library(context.Background()); !IsRefused(err) {
		t.Fatalf("forged signature: err = %v", err)
	}

	// The access token from registration is still fresh: no refresh needed.
	if err := Deregister(context.Background(), creds, opts...); err != nil {
		t.Fatal(err)
	}
	if d := fake.Calls().Deregistered; len(d) != 1 || d[0] != creds.DeviceSerial {
		t.Fatalf("deregistered = %v", d)
	}
	if _, err := c.Library(context.Background()); HTTPStatus(err) != http.StatusUnauthorized || !IsRefused(err) {
		t.Fatalf("after deregister: err = %v", err)
	}
	// Denied: deregistering again (the tokens are gone).
	if err := Deregister(context.Background(), creds, opts...); !IsRefused(err) {
		t.Fatalf("second deregister: err = %v", err)
	}

	// A refused account: 403 on every signed call.
	c2, _ := NewClient(registerWithFake(t, opts), opts...)
	fake.Refuse()
	if _, err := c2.Library(context.Background()); HTTPStatus(err) != http.StatusForbidden || !IsRefused(err) {
		t.Fatalf("refused account: err = %v", err)
	}
}

// An expired access token is refreshed before deregistering.
func TestFakeDeregisterRefreshes(t *testing.T) {
	fake, opts := startFake(t)
	creds := registerWithFake(t, opts)
	creds.AccessToken, creds.ExpiresAt = "stale", time.Now().Add(-time.Minute).Unix()
	if err := Deregister(context.Background(), creds, opts...); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls().Deregistered) != 1 {
		t.Fatal("not deregistered")
	}
}

// The tag's environment variable points every endpoint at the fake.
func TestFakeURLEnv(t *testing.T) {
	t.Setenv(FakeURLEnv, "http://127.0.0.1:8099/ ")
	mp, _ := MarketplaceFor("de")
	if e := EndpointsFor(mp); e.Amazon != "http://127.0.0.1:8099" || e.AmazonAPI != e.Amazon || e.API != e.Amazon {
		t.Fatalf("endpoints = %+v", e)
	}
	t.Setenv(FakeURLEnv, "")
	if e := EndpointsFor(mp); e.API != "https://api.audible.de" {
		t.Fatalf("endpoints without the fake = %+v", e)
	}
}

// A recorded run (the owner's recording transport) holds the shapes and titles
// but no credential: not the tokens, the device key, the code or verifier, the
// customer or device ids, nor the signature headers.
func TestRecorderScrubsCredentials(t *testing.T) {
	fake := audiblefake.New(audiblefake.Options{KeyBits: 1024})
	ts := httptest.NewServer(fake.Handler())
	defer ts.Close()
	dir := t.TempDir()
	hc := &http.Client{Transport: audiblefake.NewRecorder(http.DefaultTransport, dir)}
	opts := []Option{WithEndpoints(Endpoints{Amazon: ts.URL, AmazonAPI: ts.URL, API: ts.URL}), WithHTTPClient(hc), noSleep()}

	mp, _ := MarketplaceFor("us")
	redirect, pkce, serial := signIn(t, opts)
	code, _ := ParseAuthCode(redirect)
	creds, err := Register(context.Background(), mp, code, pkce, serial, opts...)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClient(creds, opts...)
	if _, err := c.Library(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LastPositions(context.Background(), []string{"B0FAKE0001"}, nil); err != nil {
		t.Fatal(err)
	}
	refreshed, err := RefreshAccessToken(context.Background(), creds, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := Deregister(context.Background(), refreshed, opts...); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "audible-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("recordings = %v", files)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recording mode = %v", info.Mode())
	}
	raw, _ := os.ReadFile(files[0])
	rec := string(raw)
	keyLine := strings.Split(creds.DevicePrivateKey, "\n")[1]
	for name, secret := range map[string]string{
		"access token": creds.AccessToken, "refreshed access token": refreshed.AccessToken,
		"refresh token": creds.RefreshToken, "adp token": creds.ADPToken, "device key": keyLine,
		"customer id": creds.CustomerID, "serial": serial, "code": code, "verifier": pkce.Verifier,
		"challenge": pkce.Challenge, "account name": "Fake Customer",
	} {
		if strings.Contains(rec, secret) {
			t.Errorf("the recording holds the %s", name)
		}
	}
	for _, h := range []string{"x-adp-signature", "X-Adp-Signature", "Authorization"} {
		if strings.Contains(rec, h) {
			t.Errorf("the recording holds the %s header", h)
		}
	}
	for _, kept := range []string{"The Glass Orchard", "/1.0/library", "asin_last_position_heard_annots", "/auth/register", "REDACTED"} {
		if !strings.Contains(rec, kept) {
			t.Errorf("the recording lost %q", kept)
		}
	}
}
