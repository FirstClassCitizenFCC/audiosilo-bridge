//go:build audiblefake

package audiblefake

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	_ "embed" // the fixture download
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// fixtureM4B is the fake's download: 30 s of tone, three chapters, AAC in an
// unencrypted MP4. Every title serves it (tagged with the title's own title
// and authors when ffmpeg is there), as the "AAXC": ffmpeg passes an
// unencrypted MP4 through with -audible_key/-audible_iv set (audible's
// TestDecryptPassesAnUnencryptedMP4Through), so the Bridge's real decrypt
// step runs unchanged against the fake.
//
//go:embed testdata/fixture.m4b
var fixtureM4B []byte

// servedAt is the Last-Modified of every download.
var servedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// CutNextDownload makes the next content GET stop after n bytes, as a dropped
// connection does (the Bridge resumes it with a Range).
func (f *Fake) CutNextDownload(n int64) {
	f.mu.Lock()
	f.cutNext = n
	f.mu.Unlock()
}

// ExpireURLs makes every download URL issued so far answer 403, as Audible's
// presigned URLs do once they expire (the Bridge licenses again once).
func (f *Fake) ExpireURLs() {
	f.mu.Lock()
	f.urls = map[string]string{}
	f.mu.Unlock()
}

// Release releases a preorder today: the library lists it available and a
// license is granted. False for an ASIN that isn't a fixture preorder.
func (f *Fake) Release(asin string) bool {
	for _, t := range Fixtures {
		if t.ASIN == asin && t.ReleaseIn > 0 {
			f.mu.Lock()
			f.released[asin] = true
			f.mu.Unlock()
			return true
		}
	}
	return false
}

// titles is the library as it stands: the fixtures, a released preorder out
// today (with a runtime).
func (f *Fake) titles() []Title {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Title, len(Fixtures))
	for i, t := range Fixtures {
		if f.released[t.ASIN] {
			t.ReleaseIn = 0
			if t.RuntimeMin == 0 {
				t.RuntimeMin = 1
			}
		}
		out[i] = t
	}
	return out
}

// title finds one title of the library as it stands.
func (f *Fake) title(asin string) (Title, bool) {
	for _, t := range f.titles() {
		if t.ASIN == asin {
			return t, true
		}
	}
	return Title{}, false
}

// libraryTitle is GET /1.0/library/{asin}: one title, 404 when the library
// doesn't hold it.
func (f *Fake) libraryTitle(w http.ResponseWriter, r *http.Request, _ []byte) {
	t, ok := f.title(r.PathValue("asin"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "the title is not in the customer's library"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": libraryItem(t, f.opts.Now()), "response_groups": strings.Split(r.URL.Query().Get("response_groups"), ",")})
}

// FakeKey is the AES key and iv (hex) the fake's voucher for asin carries:
// what the Bridge hands ffmpeg (tests look for them in logs and results).
func FakeKey(asin string) (key, iv string) {
	sum := sha256.Sum256([]byte("audiblefake/content-key/" + asin))
	return hex.EncodeToString(sum[:16]), hex.EncodeToString(sum[16:])
}

// license is POST /1.0/content/{asin}/licenserequest: Denied for a title not
// in the library, a preorder not yet out or one without download rights;
// else Granted with a voucher only this device can open, a download URL and
// the file's size and ACR.
func (f *Fake) license(w http.ResponseWriter, r *http.Request, _ []byte) {
	asin := r.PathValue("asin")
	f.mu.Lock()
	d := f.devices[r.Header.Get("x-adp-token")]
	f.mu.Unlock()
	t, ok := f.title(asin)
	deny := func(validation, rejection, message string) {
		f.record(LicenseCall{ASIN: asin, Status: "Denied"})
		writeJSON(w, http.StatusOK, map[string]any{"content_license": map[string]any{
			"asin": asin, "status_code": "Denied",
			"license_denial_reasons": []map[string]string{{"validationType": validation, "rejectionReason": rejection, "message": message}},
		}})
	}
	switch {
	case !ok:
		deny("Ownership", "NotOwned", "the title is not in the customer's library")
		return
	case t.ReleaseIn > 0:
		deny("Release", "NotReleased", "the title is not released yet")
		return
	case t.State() != "available":
		deny("Ownership", "ContentEligibility", "the customer has no download rights for the title")
		return
	}
	key, iv := FakeKey(asin)
	voucher, err := sealVoucher(d.devType+d.serial+f.opts.CustomerID+asin, key, iv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := f.contentOf(r.Context(), t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := "T" + randHex(12)
	f.mu.Lock()
	f.urls[token] = asin
	f.mu.Unlock()
	f.record(LicenseCall{ASIN: asin, Status: "Granted"})
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	writeJSON(w, http.StatusOK, map[string]any{"content_license": map[string]any{
		"asin": asin, "status_code": "Granted", "license_response": voucher,
		"content_metadata": map[string]any{
			"content_url":       map[string]any{"offline_url": scheme + "://" + r.Host + "/_fake/content/" + asin + ".aaxc?token=" + token},
			"content_reference": map[string]any{"acr": "FAKEACR-" + asin, "asin": asin, "content_size_in_bytes": len(body)},
		},
	}})
}

// record appends a license call.
func (f *Fake) record(c LicenseCall) {
	f.mu.Lock()
	f.calls.Licenses = append(f.calls.Licenses, c)
	f.mu.Unlock()
}

// sealVoucher is Audible's voucher: the JSON {key, iv} under AES-128-CBC with
// the key and iv SHA-256(device type + serial + customer id + asin) gives.
func sealVoucher(material, key, iv string) (string, error) {
	plain, err := json.Marshal(map[string]any{"key": key, "iv": iv, "rules": []any{}})
	if err != nil {
		return "", err
	}
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, ' ')
	}
	sum := sha256.Sum256([]byte(material))
	block, err := aes.NewCipher(sum[:16])
	if err != nil {
		return "", err
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, sum[16:32]).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out), nil
}

// download is GET /_fake/content/{asin}.aaxc?token=: the title's file with
// Range and If-Range (http.ServeContent against its ETag), 403 once the
// token's license has expired. A CutNextDownload stops it part way.
func (f *Fake) download(w http.ResponseWriter, r *http.Request, _ []byte) {
	asin := strings.TrimSuffix(r.PathValue("file"), ".aaxc")
	f.mu.Lock()
	issued := f.urls[r.URL.Query().Get("token")]
	cut := f.cutNext
	expired := issued != asin
	if !expired {
		f.cutNext = 0
	}
	f.calls.Downloads = append(f.calls.Downloads, DownloadCall{ASIN: asin, Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"), Expired: expired})
	f.mu.Unlock()
	if expired {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Request has expired"})
		return
	}
	t, _ := f.title(asin)
	body, err := f.contentOf(r.Context(), t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Content-Type", "audio/vnd.audible.aaxc")
	out := http.ResponseWriter(w)
	if cut > 0 {
		out = &cutWriter{ResponseWriter: w, left: cut}
	}
	http.ServeContent(out, r, asin+".aaxc", servedAt, bytes.NewReader(body))
}

// cutWriter aborts the response after left bytes (the client sees the
// connection drop before Content-Length).
type cutWriter struct {
	http.ResponseWriter
	left int64
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if int64(len(p)) >= c.left {
		_, _ = c.ResponseWriter.Write(p[:c.left])
		if fl, ok := c.ResponseWriter.(http.Flusher); ok {
			fl.Flush()
		}
		panic(http.ErrAbortHandler)
	}
	c.left -= int64(len(p))
	return c.ResponseWriter.Write(p)
}

// contentOf is the title's download: the fixture tagged with the title's own
// title, authors and narrators by ffmpeg (-c copy), made once per title; the
// fixture as it is when there is no ffmpeg.
func (f *Fake) contentOf(ctx context.Context, t Title) ([]byte, error) {
	f.mu.Lock()
	b, ok := f.content[t.ASIN]
	f.mu.Unlock()
	if ok {
		return b, nil
	}
	b = fixtureM4B
	if tagged, err := tag(ctx, f.opts.FFmpeg, t); err == nil {
		b = tagged
	}
	f.mu.Lock()
	if cached, ok := f.content[t.ASIN]; ok {
		b = cached // another request made it first: serve one version only
	} else {
		f.content[t.ASIN] = b
	}
	f.mu.Unlock()
	return b, nil
}

// tag writes t's metadata into a copy of the fixture with ffmpeg.
func tag(ctx context.Context, ffmpeg string, t Title) ([]byte, error) {
	if ffmpeg == "-" {
		return nil, os.ErrNotExist
	}
	if ffmpeg == "" {
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil, err
		}
		ffmpeg = p
	}
	dir, err := os.MkdirTemp("", "audiblefake-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in, out := filepath.Join(dir, "in.m4b"), filepath.Join(dir, "out.m4b")
	if err := os.WriteFile(in, fixtureM4B, 0o600); err != nil {
		return nil, err
	}
	year := time.Now().AddDate(0, 0, t.ReleaseIn).Format("2006")
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", in, "-map", "0", "-map_metadata", "0", "-c", "copy",
		"-metadata", "title=" + t.Title, "-metadata", "album=" + t.Title,
		"-metadata", "artist=" + strings.Join(t.Authors, ", "), "-metadata", "album_artist=" + strings.Join(t.Authors, ", "),
		"-metadata", "composer=" + strings.Join(t.Narrators, ", "), "-metadata", "date=" + year,
		"-metadata", "genre=Audiobook", "-f", "ipod", "-y", out}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, ffmpeg, args...).Run(); err != nil { //nolint:gosec // G204: the fake's own ffmpeg and fixture
		return nil, err
	}
	return os.ReadFile(out) //nolint:gosec // G304: the fake's own temp file
}
