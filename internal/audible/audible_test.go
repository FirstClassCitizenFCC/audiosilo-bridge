package audible

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPKCE(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); p.Challenge != want {
		t.Fatalf("challenge = %q, want %q", p.Challenge, want)
	}
}

func TestMarketplace(t *testing.T) {
	m, ok := MarketplaceFor("us")
	if !ok || m.MarketPlaceID != "AF2M0KC94RCEA" {
		t.Fatalf("us marketplace = %+v, ok=%v", m, ok)
	}
	if m.AmazonDomain() != "www.amazon.com" || m.APIDomain() != "api.audible.com" {
		t.Fatalf("domains = %q / %q", m.AmazonDomain(), m.APIDomain())
	}
	if _, ok := MarketplaceFor("nope"); ok {
		t.Fatal("unknown marketplace should not resolve")
	}
}

func pemKey(t *testing.T, pkcs8 bool) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var blk *pem.Block
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		blk = &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	} else {
		blk = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	}
	return string(pem.EncodeToMemory(blk)), key
}

func TestCredentialsRoundTripAndKey(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		keyPEM, key := pemKey(t, pkcs8)
		c := Credentials{
			AccessToken: "at", RefreshToken: "rt", ADPToken: "adp",
			DevicePrivateKey: keyPEM, DeviceType: "A2CZJZGLK2JJVM", DeviceSerial: "SER",
			CustomerID: "CUST", Marketplace: "us", ExpiresAt: 123,
		}
		s, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseCredentials(s)
		if err != nil {
			t.Fatal(err)
		}
		if got.ADPToken != "adp" || got.DeviceSerial != "SER" {
			t.Fatalf("round-trip lost fields: %+v", got)
		}
		parsed, err := got.PrivateKey()
		if err != nil {
			t.Fatalf("PrivateKey (pkcs8=%v): %v", pkcs8, err)
		}
		if parsed.N.Cmp(key.N) != 0 {
			t.Fatal("parsed key does not match original")
		}
	}
}

func TestSignRequest(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"q":1}`)
	req, _ := http.NewRequest(http.MethodPost, "https://api.audible.com/1.0/content/X/licenserequest?a=b", nil)
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := signRequest(req, body, key, "ADP", now); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-adp-token") != "ADP" || req.Header.Get("x-adp-alg") != "SHA256withRSA:1.0" {
		t.Fatalf("headers = %v", req.Header)
	}
	sigHdr := req.Header.Get("x-adp-signature")
	parts := strings.SplitN(sigHdr, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("signature header malformed: %q", sigHdr)
	}
	sig, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	want := signingString("POST", "/1.0/content/X/licenserequest?a=b", parts[1], body, "ADP")
	h := sha256.Sum256([]byte(want))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

func TestDecryptVoucher(t *testing.T) {
	const dt, serial, cust, asin = "A2CZJZGLK2JJVM", "SER123", "CUST456", "B07XYZ"
	key, iv := voucherKeyIV(dt, serial, cust, asin)

	plain := []byte(`{"key":"00112233445566778899aabbccddeeff","iv":"ffeeddccbbaa99887766554433221100"}`)
	// pad with spaces to a block boundary (DecryptVoucher trims to the JSON object)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, ' ')
	}
	block, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, plain)
	b64 := base64.StdEncoding.EncodeToString(enc)

	gotKey, gotIV, err := DecryptVoucher(b64, dt, serial, cust, asin)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "00112233445566778899aabbccddeeff" || gotIV != "ffeeddccbbaa99887766554433221100" {
		t.Fatalf("voucher key/iv = %q / %q", gotKey, gotIV)
	}
	// Wrong asin → wrong derived key → no JSON recovered.
	if _, _, err := DecryptVoucher(b64, dt, serial, cust, "WRONG"); err == nil {
		t.Fatal("expected failure with the wrong asin")
	}
}

func TestDecryptArgs(t *testing.T) {
	aaxc := aaxcArgs("in.aaxc", "KEY", "IV", "out.m4b")
	if aaxc[0] != "-audible_key" || aaxc[1] != "KEY" || aaxc[2] != "-audible_iv" || aaxc[3] != "IV" {
		t.Fatalf("aaxc args order wrong: %v", aaxc)
	}
	if idxOf(aaxc, "-i") < 4 {
		t.Fatalf("-i must come after the key/iv options: %v", aaxc)
	}
	aax := aaxArgs("in.aax", "1CEB00DA", "out.m4b")
	if aax[0] != "-activation_bytes" || aax[1] != "1CEB00DA" {
		t.Fatalf("aax args order wrong: %v", aax)
	}
}

func idxOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
