package audible

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	keyPEM, _ := pemKey(t, false)
	c, err := NewClient(Credentials{
		ADPToken: "ADP", DevicePrivateKey: keyPEM, DeviceType: deviceType,
		DeviceSerial: "SER", CustomerID: "CUST", Marketplace: "us",
	}, WithEndpoints(Endpoints{Amazon: baseURL, AmazonAPI: baseURL, API: baseURL}), noSleep())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// noSleep makes the backoff's waits instant (tests).
func noSleep() Option {
	return withSleep(func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
}

func TestLoginURLAndParse(t *testing.T) {
	mp, _ := MarketplaceFor("us")
	pkce := PKCE{Verifier: "v", Challenge: "chal"}
	u := BuildAuthorizeURL(mp, pkce, "SERIAL")
	for _, want := range []string{"www.amazon.com/ap/signin", "openid.oa2.code_challenge=chal", "marketPlaceId=" + mp.MarketPlaceID, "maplanding"} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize URL missing %q:\n%s", want, u)
		}
	}
	code, err := ParseAuthCode("https://www.amazon.com/ap/maplanding?openid.oa2.authorization_code=ABC123&x=y")
	if err != nil || code != "ABC123" {
		t.Fatalf("ParseAuthCode = %q, %v", code, err)
	}
	if _, err := ParseAuthCode("https://www.amazon.com/ap/maplanding?nope=1"); err == nil {
		t.Fatal("expected error when no code present")
	}
}

func TestMarketplaceDomains(t *testing.T) {
	uk, _ := MarketplaceFor("uk")
	if uk.AmazonAPIDomain() != "api.amazon.co.uk" {
		t.Errorf("AmazonAPIDomain = %q", uk.AmazonAPIDomain())
	}
	if uk.APIDomain() != "api.audible.co.uk" {
		t.Errorf("APIDomain = %q", uk.APIDomain())
	}
	if uk.CookieDomain() != ".amazon.co.uk" {
		t.Errorf("CookieDomain = %q", uk.CookieDomain())
	}
}

func TestClientLibrary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0/library" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-adp-token") != "ADP" || r.Header.Get("x-adp-signature") == "" {
			t.Errorf("request not signed: %v", r.Header)
		}
		if rg := r.URL.Query().Get("response_groups"); !strings.Contains(rg, "customer_rights") {
			t.Errorf("response_groups missing customer_rights: %q", rg)
		}
		_, _ = w.Write([]byte(`{"items":[
			{"asin":"B01","title":"Soulsmith","authors":[{"name":"Will Wight"}],
			 "narrators":[{"name":"Travis Baldree"}],
			 "series":[{"title":"Cradle","sequence":"2"}],"release_date":"2016-08-01",
			 "customer_rights":{"is_consumable_offline":true}},
			{"asin":"B02","title":"Victorian Secrets","content_delivery_type":"Periodical",
			 "customer_rights":{"is_consumable_offline":true}},
			{"asin":"B03","title":"Left the Catalog",
			 "customer_rights":{"is_consumable_offline":false}},
			{"asin":"B04","title":"Future Book","release_date":"2999-01-01"}
		]}`))
	}))
	defer ts.Close()

	recs, err := testClient(t, ts.URL).Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.ASIN != "B01" || r.Author != "Will Wight" || r.Series != "Cradle" || !r.HasSequence || r.Sequence != 2 {
		t.Fatalf("mapped record wrong: %+v", r)
	}
	if r.Year != "2016" || r.FileName != "Soulsmith.m4b" || r.SrcPath != "" {
		t.Fatalf("record fields: year=%q file=%q src=%q", r.Year, r.FileName, r.SrcPath)
	}
	if r.ReleaseDate != "2016-08-01" || r.SeriesPosition != "2" || len(r.Authors) != 1 || r.Authors[0] != "Will Wight" {
		t.Fatalf("record: release=%q position=%q authors=%q", r.ReleaseDate, r.SeriesPosition, r.Authors)
	}
	if recs[1].ReleaseDate != "" || recs[1].Authors != nil {
		t.Fatalf("no date or authors: %+v", recs[1])
	}
	// A normal owned book is downloadable; the rest each carry their reason.
	if r.UnavailableReason != "" {
		t.Fatalf("owned book marked unavailable: %q", r.UnavailableReason)
	}
	if got := recs[1].UnavailableReason; !strings.Contains(got, "periodical") {
		t.Fatalf("periodical reason = %q", got)
	}
	if got := recs[2].UnavailableReason; !strings.Contains(got, "download rights") {
		t.Fatalf("no-rights reason = %q", got)
	}
	if got := recs[3].UnavailableReason; !strings.Contains(got, "pre-order") {
		t.Fatalf("pre-order reason = %q", got)
	}
}

// libraryPageJSON renders a /1.0/library page of count items whose ASINs are unique
// within tag - so the same tag twice is a replayed page, and different tags are
// genuinely different pages. Item 0 of every page carries NO asin: the pager must
// stay correct for an item it cannot key on. Called from the test goroutine (never
// the handler's), so a marshal failure can legitimately t.Fatal.
func libraryPageJSON(t *testing.T, tag string, count int) []byte {
	t.Helper()
	items := make([]map[string]string, count)
	for i := range count {
		items[i] = map[string]string{
			"asin":  fmt.Sprintf("B%s-%05d", tag, i),
			"title": fmt.Sprintf("Book %s-%d", tag, i),
		}
		if i == 0 {
			delete(items[i], "asin")
		}
	}
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An endpoint that replays a full page (rather than returning an empty one) must
// stop the pager at the first page contributing nothing new, and never duplicate a
// book. Before the guard, a short page was the only exit; and while the dedupe was
// keyed on the ASIN alone, a single item without one was re-appended every page and
// kept the "nothing new" stop from ever firing.
func TestClientLibraryStopsOnRepeatedPage(t *testing.T) {
	page := libraryPageJSON(t, "same", libraryPageSize) // identical every time
	var requests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(page)
	}))
	defer ts.Close()

	recs, err := testClient(t, ts.URL).Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != libraryPageSize {
		t.Fatalf("got %d records, want %d (the replayed page was appended again)", len(recs), libraryPageSize)
	}
	if requests != 2 {
		t.Fatalf("made %d requests, want 2 (one page, one that proved it repeats)", requests)
	}
}

// The dedupe guard must not stop a genuinely multi-page library - including across
// two consecutive FULL pages, where only the third page ends the walk.
func TestClientLibraryPaginates(t *testing.T) {
	const extra = 3 // a short final page ends the walk
	pages := map[string][]byte{
		"1": libraryPageJSON(t, "1", libraryPageSize),
		"2": libraryPageJSON(t, "2", libraryPageSize),
		"3": libraryPageJSON(t, "3", extra),
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Query().Get("page")]
		if !ok {
			http.Error(w, "unexpected page", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(page)
	}))
	defer ts.Close()

	recs, err := testClient(t, ts.URL).Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*libraryPageSize + extra; len(recs) != want {
		t.Fatalf("got %d records, want %d (pagination dropped a page)", len(recs), want)
	}
}

func TestClientLicense(t *testing.T) {
	const asin = "B07XYZ"
	// Build a voucher encrypted with the same derivation the client will use.
	key, iv := voucherKeyIV(deviceType, "SER", "CUST", asin)
	plain := []byte(`{"key":"aaaa1111bbbb2222cccc3333dddd4444","iv":"4444dddd3333cccc2222bbbb1111aaaa"}`)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, ' ')
	}
	block, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, plain)
	voucherB64 := base64.StdEncoding.EncodeToString(enc)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/licenserequest") {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{"content_license": map[string]any{
			"license_response": voucherB64,
			"content_metadata": map[string]any{
				"content_url": map[string]any{"offline_url": "https://cdn.example/x.aaxc?sig=1"},
			},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	url, k, iv2, err := testClient(t, ts.URL).License(context.Background(), asin)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://cdn.example/x.aaxc?sig=1" {
		t.Fatalf("download url = %q", url)
	}
	if k != "aaaa1111bbbb2222cccc3333dddd4444" || iv2 != "4444dddd3333cccc2222bbbb1111aaaa" {
		t.Fatalf("voucher key/iv = %q / %q", k, iv2)
	}
}

// A denied license (no voucher) must surface Audible's denial reason, not just
// "no license voucher returned" - the reason is the only clue the user gets.
func TestClientLicenseDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content_license":{
			"status_code":"Denied",
			"license_denial_reasons":[{"rejectionReason":"ContentEligibility","validationType":"Ownership"}]
		}}`))
	}))
	defer ts.Close()

	_, _, _, err := testClient(t, ts.URL).License(context.Background(), "B00")
	if err == nil {
		t.Fatal("expected an error for a denied license")
	}
	if !strings.Contains(err.Error(), "ContentEligibility") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("denial detail missing from error: %v", err)
	}
}

func TestDownloadIsSigned(t *testing.T) {
	// The content-delivery GET must be RSA-signed and carry the Audible user agent,
	// or Audible's CDS returns HTTP 403 (the bug an unsigned plain GET hit). Also
	// verify a redirect is followed and re-signed for the new path.
	var gotSig, gotUA, redirectSig bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/content.aaxc":
			if r.Header.Get("x-adp-token") == "ADP" && r.Header.Get("x-adp-signature") != "" {
				gotSig = true
			}
			if r.Header.Get("User-Agent") == userAgent {
				gotUA = true
			}
			http.Redirect(w, r, "/cdn/file.aaxc", http.StatusFound)
		case "/cdn/file.aaxc":
			// The redirected request must be re-signed for the new path.
			if r.Header.Get("x-adp-signature") != "" {
				redirectSig = true
			}
			_, _ = w.Write([]byte("AUDIO-BYTES"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	dest := t.TempDir() + "/out.m4b"
	if err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/content.aaxc", dest, DownloadOptions{}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !gotSig {
		t.Error("download request was not RSA-signed (would 403 against Audible CDS)")
	}
	if !gotUA {
		t.Error("download request did not carry the Audible user agent")
	}
	if !redirectSig {
		t.Error("redirected request was not re-signed")
	}
	if b, _ := os.ReadFile(dest); string(b) != "AUDIO-BYTES" {
		t.Fatalf("downloaded content = %q", b)
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	body := strings.Repeat("A", 2500)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	dest := t.TempDir() + "/out.m4b"
	var lastCopied, lastTotal int64
	calls := 0
	err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/file", dest, DownloadOptions{
		OnProgress: func(copied, total int64) {
			lastCopied, lastTotal = copied, total
			calls++
		}})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if calls == 0 {
		t.Fatal("progress callback was never called")
	}
	// The reader always fires a final update at EOF, so it lands on the exact size
	// and the Content-Length total.
	if lastCopied != int64(len(body)) {
		t.Errorf("final copied = %d, want %d", lastCopied, len(body))
	}
	if lastTotal != int64(len(body)) {
		t.Errorf("final total = %d, want %d", lastTotal, len(body))
	}
}

func TestRegisterParse(t *testing.T) {
	keyPEM, _ := pemKey(t, false)
	mp, _ := MarketplaceFor("us")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/register" {
			http.NotFound(w, r)
			return
		}
		// Assert the registration body carries the required cookies object and the
		// auth code/verifier - omitting cookies is what triggers Amazon MissingValue.
		var sent registerBody
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode register body: %v", err)
		}
		if sent.Cookies.Domain != ".amazon.com" {
			t.Errorf("cookies.domain = %q, want .amazon.com", sent.Cookies.Domain)
		}
		if sent.Cookies.WebsiteCookies == nil {
			t.Error("cookies.website_cookies must be [] not null")
		}
		if sent.AuthData["authorization_code"] != "CODE" || sent.AuthData["code_verifier"] != "v" {
			t.Errorf("auth_data not carried through: %+v", sent.AuthData)
		}
		// The device name must carry the de-dupe macro, else a retry after a
		// partial first registration fails with DuplicateDeviceName, and end in
		// the product name the owner sees in Amazon's device list.
		if !strings.Contains(sent.RegistrationData["device_name"], "%DUPE_STRATEGY_1ST%") {
			t.Errorf("device_name missing de-dupe macro: %q", sent.RegistrationData["device_name"])
		}
		if !strings.HasSuffix(sent.RegistrationData["device_name"], "%AudioSilo") {
			t.Errorf("device_name = %q, want the AudioSilo name", sent.RegistrationData["device_name"])
		}
		resp := map[string]any{"response": map[string]any{"success": map[string]any{
			"tokens": map[string]any{
				"bearer":  map[string]any{"access_token": "AT", "refresh_token": "RT", "expires_in": "3600"},
				"mac_dms": map[string]any{"adp_token": "ADP", "device_private_key": keyPEM},
			},
			"extensions": map[string]any{
				"device_info":   map[string]any{"device_serial_number": "DS"},
				"customer_info": map[string]any{"user_id": "amzn1.account.X"},
			},
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	creds, err := Register(context.Background(), mp, "CODE", PKCE{Verifier: "v", Challenge: "c"}, "SER",
		WithEndpoints(Endpoints{AmazonAPI: ts.URL}))
	if err != nil {
		t.Fatal(err)
	}
	if creds.ADPToken != "ADP" || creds.RefreshToken != "RT" || creds.CustomerID != "amzn1.account.X" {
		t.Fatalf("creds = %+v", creds)
	}
	if creds.DeviceSerial != "DS" || creds.Marketplace != "us" {
		t.Fatalf("creds device/marketplace: %+v", creds)
	}
	if _, err := creds.PrivateKey(); err != nil {
		t.Fatalf("registered key not parseable: %v", err)
	}
}
