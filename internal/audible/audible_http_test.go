package audible

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestRegisterParse(t *testing.T) {
	keyPEM, _ := pemKey(t, false)
	mp, _ := MarketplaceFor("us")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/register" {
			http.NotFound(w, r)
			return
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
