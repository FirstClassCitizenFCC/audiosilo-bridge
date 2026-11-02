// Package audible is a native Go client for backing up an Audible library the user
// owns: it logs in (OAuth + PKCE, via a browser), registers a virtual device,
// lists the library, requests per-book decryption vouchers, downloads the AAXC,
// and strips DRM with ffmpeg into a plain .m4b - the same capability as the
// open-source Libation / OpenAudible / audible-cli. It plugs into the import
// pipeline as a source.Acquirer (download-on-demand), so placement, transfer, and
// the skip-if-already-on-server check are shared with folder/drag-drop imports.
//
// Credentials (full Amazon account tokens + the device RSA key) are stored only in
// the OS keychain. The decryption key for a book is never persisted.
package audible

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE is an RFC 7636 verifier/challenge pair (S256), used in the Audible OAuth
// authorize step.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a fresh PKCE pair.
func NewPKCE() (PKCE, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}
