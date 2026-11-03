package audible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/execfile"
)

const (
	testKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	testIV  = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// A License never shows its key, iv or URL: not printed, not logged, not
// marshalled.
func TestLicenseNeverShowsItsSecrets(t *testing.T) {
	lic := License{URL: "https://cdn.example/x?Signature=SECRETSIG", Key: testKey, IV: testIV, Size: 9}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("license", "lic", lic, "nested", struct{ L License }{lic})
	shown := []string{fmt.Sprint(lic), fmt.Sprintf("%v %+v %#v %s", lic, lic, lic, lic), logged.String()}
	if b, err := json.Marshal(map[string]any{"lic": lic}); err == nil {
		t.Fatalf("a license marshalled: %s", b)
	}
	for _, s := range shown {
		for _, secret := range []string{testKey, testIV, "SECRETSIG"} {
			if strings.Contains(s, secret) {
				t.Fatalf("a license showed a secret: %s", s)
			}
		}
	}
}

func TestDenialKind(t *testing.T) {
	reason := func(v, r, m string) licenseResp {
		var lr licenseResp
		lr.ContentLicense.DenialReasons = append(lr.ContentLicense.DenialReasons, struct {
			Message         string `json:"message"`
			RejectionReason string `json:"rejectionReason"`
			ValidationType  string `json:"validationType"`
		}{m, r, v})
		return lr
	}
	for _, c := range []struct {
		lr   licenseResp
		want error
	}{
		{reason("Release", "NotReleased", ""), ErrNotReleased},
		{reason("", "", "This title is a pre-order"), ErrNotReleased},
		{reason("Ownership", "ContentEligibility", ""), ErrNoRights},
		{reason("", "NotOwned", ""), ErrNoRights},
		{reason("Consumption", "Throttled", "slow down"), ErrDenied},
		// A throttled device (an older profile): its Ownership and eligibility
		// reasons don't make it no_rights.
		{reason("Ownership", "CustomerThrottled", ""), ErrThrottled},
		{reason("", "", "Customer id [A1B2C3D4E5F6G7] being throttled"), ErrThrottled},
		{throttled(), ErrThrottled},
		{licenseResp{}, ErrDenied},
	} {
		if got := denialKind(c.lr); !errors.Is(got, c.want) {
			t.Errorf("%+v = %v, want %v", c.lr.ContentLicense.DenialReasons, got, c.want)
		}
	}
}

// throttled is the denial Amazon sends a device on the older profile: three
// reasons, one quoting the customer id (edge jobs 594/595, Libation's
// classifier tests).
func throttled() licenseResp {
	var lr licenseResp
	_ = json.Unmarshal([]byte(`{"content_license":{"status_code":"Denied","license_denial_reasons":[
		{"validationType":"Membership","rejectionReason":"RequesterEligibility","message":"Customer is not part of any plans"},
		{"validationType":"Ownership","rejectionReason":"CustomerThrottled","message":"Customer id [A1B2C3D4E5F6G7] being throttled"},
		{"validationType":"Client","rejectionReason":"RequesterEligibility","message":"urn:cdo:AudibleApiExternalRouterService:Prod:Default[AAA], does not has access to asin[B00H3R0B0W]"}]}}`), &lr)
	return lr
}

// ffmpeg echoes an option's value when it refuses it: the error carries
// neither the key nor the iv, in either case.
func TestDecryptRedactsFFmpegsWords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for ffmpeg")
	}
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\necho \"Error setting option audible_key to value $6.\" >&2\n" +
		"echo \"iv was $(echo $8 | tr a-f A-F)\" >&2\nexit 1\n"
	if err := execfile.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Decrypt(context.Background(), fake, "in.aaxc", filepath.Join(t.TempDir(), "out.m4b"), License{Key: testKey, IV: testIV})
	if err == nil {
		t.Fatal("the fake ffmpeg failed")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, testKey) || strings.Contains(msg, testIV) || !strings.Contains(msg, "[redacted]") {
		t.Fatalf("err = %v", err)
	}
	if err := Decrypt(context.Background(), fake, "in", "out", License{}); err == nil {
		t.Fatal("a license with no key must not run ffmpeg")
	}
}

// ffmpeg passes an unencrypted MP4 through with -audible_key/-audible_iv set:
// the audio comes out bit for bit and the chapters are kept (the Audible fake
// serves one as its "AAXC").
func TestDecryptPassesAnUnencryptedMP4Through(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "book.aaxc.part")
	if b, err := os.ReadFile(filepath.Join("audiblefake", "testdata", "fixture.m4b")); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(in, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "book.m4b.tmp") // the muxer is named, not guessed
	if err := Decrypt(context.Background(), ffmpeg, in, out, License{Key: testKey, IV: testIV}); err != nil {
		t.Fatal(err)
	}
	audio := func(p string) []byte {
		b, err := exec.Command(ffmpeg, "-loglevel", "error", "-i", p, "-map", "0:a", "-c", "copy", "-f", "data", "-").Output() //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if !bytes.Equal(audio(in), audio(out)) {
		t.Fatal("the audio changed on the way through")
	}
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		b, _ := exec.Command(ffprobe, "-loglevel", "error", "-show_chapters", "-of", "csv", out).Output() //nolint:gosec // test
		if strings.Count(string(b), "chapter") < 2 {
			t.Fatalf("chapters lost: %s", b)
		}
	}
}

func TestClientTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0/library/B0TITLE001":
			if r.URL.Query().Get("response_groups") != libraryGroups || r.Header.Get("x-adp-signature") == "" {
				http.Error(w, "unsigned or no groups", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"item":{"asin":"B0TITLE001","title":"Soulsmith","authors":[{"name":"Will Wight"}],
				"series":[{"title":"Cradle","sequence":"2"}],"release_date":"2016-01-01","runtime_length_min":600}}`))
		case "/1.0/library/B0OTHER001":
			_, _ = w.Write([]byte(`{"item":{"asin":"B0SOMEELSE"}}`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	defer ts.Close()
	cl := testClient(t, ts.URL)
	rec, err := cl.Title(context.Background(), "B0TITLE001")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Soulsmith" || rec.Author != "Will Wight" || rec.Series != "Cradle" || rec.Sequence != 2 ||
		rec.FileName != "Soulsmith.m4b" || rec.RuntimeSec != 36000 || rec.UnavailableReason != "" {
		t.Fatalf("record = %+v", rec)
	}
	for _, asin := range []string{"B0MISSING1", "B0OTHER001"} {
		if _, err := cl.Title(context.Background(), asin); !errors.Is(err, ErrNotInLibrary) {
			t.Fatalf("%s: err = %v, want ErrNotInLibrary", asin, err)
		}
	}
}
