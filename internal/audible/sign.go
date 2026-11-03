package audible

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"
)

// signatureDate formats the timestamp Audible expects in the x-adp-signature
// (ISO-8601 UTC, millisecond precision).
func signatureDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// signingString builds the canonical string Audible signs:
//
//	METHOD\npath[?query]\ndate\nbody\nadp_token
func signingString(method, pathAndQuery, date string, body []byte, adpToken string) string {
	return method + "\n" + pathAndQuery + "\n" + date + "\n" + string(body) + "\n" + adpToken
}

// signRequest signs req with the device key and sets the x-adp-* headers Audible's
// unrestricted endpoints require (RSA over SHA-256, PKCS#1 v1.5). A signed request
// needs no bearer token. body is the exact bytes sent (nil for GET).
func signRequest(req *http.Request, body []byte, key *rsa.PrivateKey, adpToken string, now time.Time) error {
	pathAndQuery := req.URL.Path
	if req.URL.RawQuery != "" {
		pathAndQuery += "?" + req.URL.RawQuery
	}
	date := signatureDate(now)
	h := sha256.Sum256([]byte(signingString(req.Method, pathAndQuery, date, body, adpToken)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return err
	}
	req.Header.Set("x-adp-token", adpToken)
	req.Header.Set("x-adp-alg", "SHA256withRSA:1.0")
	req.Header.Set("x-adp-signature", base64.StdEncoding.EncodeToString(sig)+":"+date)
	return nil
}
