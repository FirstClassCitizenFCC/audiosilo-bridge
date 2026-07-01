package audible

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	keyPEM, _ := pemKey(t, false)
	c, err := NewClient(Credentials{
		ADPToken: "ADP", DevicePrivateKey: keyPEM, DeviceType: deviceType,
		DeviceSerial: "SER", CustomerID: "CUST", Marketplace: "us",
	})
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = baseURL
	return c
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
		_, _ = w.Write([]byte(`{"items":[
			{"asin":"B01","title":"Soulsmith","authors":[{"name":"Will Wight"}],
			 "narrators":[{"name":"Travis Baldree"}],
			 "series":[{"title":"Cradle","sequence":"2"}],"release_date":"2016-08-01"}
		]}`))
	}))
	defer ts.Close()

	recs, err := testClient(t, ts.URL).Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.ASIN != "B01" || r.Author != "Will Wight" || r.Series != "Cradle" || !r.HasSequence || r.Sequence != 2 {
		t.Fatalf("mapped record wrong: %+v", r)
	}
	if r.Year != "2016" || r.FileName != "Soulsmith.m4b" || r.SrcPath != "" {
		t.Fatalf("record fields: year=%q file=%q src=%q", r.Year, r.FileName, r.SrcPath)
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
	if err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/content.aaxc", dest, nil); err != nil {
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
	err := testClient(t, ts.URL).Download(context.Background(), ts.URL+"/file", dest,
		func(copied, total int64) {
			lastCopied, lastTotal = copied, total
			calls++
		})
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
		// auth code/verifier — omitting cookies is what triggers Amazon MissingValue.
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
		// partial first registration fails with DuplicateDeviceName.
		if !strings.Contains(sent.RegistrationData["device_name"], "%DUPE_STRATEGY_1ST%") {
			t.Errorf("device_name missing de-dupe macro: %q", sent.RegistrationData["device_name"])
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

	creds, err := register(context.Background(), ts.URL, mp, "CODE", PKCE{Verifier: "v", Challenge: "c"}, "SER")
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
