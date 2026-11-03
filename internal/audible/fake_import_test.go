//go:build audiblefake

package audible

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/audible/audiblefake"
)

// The 4b path against the fake: a title read, a license whose voucher only
// this device opens, a download cut part way and resumed under a second
// license's URL (Range + If-Range), an expired URL refused, and the
// "AAXC" decrypted by ffmpeg with the key set.
func TestFakeLicenseDownloadResumeDecrypt(t *testing.T) {
	fake, opts := startFake(t)
	cl, err := NewClient(registerWithFake(t, opts), opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const asin = "B0FAKE0002"
	rec, err := cl.Title(ctx, asin)
	if err != nil || rec.Title != "The Copper Grove" || rec.Series != "Orchard Cycle" || rec.Sequence != 2 {
		t.Fatalf("title = %+v, %v", rec, err)
	}
	lic, err := cl.License(ctx, asin)
	if err != nil {
		t.Fatal(err)
	}
	if key, iv := audiblefake.FakeKey(asin); lic.Key != key || lic.IV != iv || lic.Size <= 0 {
		t.Fatalf("license: size %d, key/iv as issued: %v", lic.Size, lic.Key == key && lic.IV == iv)
	}
	dest := filepath.Join(t.TempDir(), "acct-"+asin+".aaxc.part")
	fake.CutNextDownload(lic.Size / 3)
	if err := cl.Download(ctx, lic.URL, dest, DownloadOptions{ExpectedSize: lic.Size}); err == nil {
		t.Fatal("the cut download must fail")
	}
	fake.ExpireURLs()
	if err := cl.Download(ctx, lic.URL, dest, DownloadOptions{ExpectedSize: lic.Size}); HTTPStatus(err) != http.StatusForbidden {
		t.Fatalf("an expired URL: %v", err)
	}
	lic2, err := cl.License(ctx, asin)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Download(ctx, lic2.URL, dest, DownloadOptions{ExpectedSize: lic2.Size}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dest); st.Size() != lic.Size {
		t.Fatalf("resumed file is %d bytes, want %d", st.Size(), lic.Size)
	}
	dl := fake.Calls().Downloads
	if len(dl) != 3 || dl[0].Range != "" || !dl[1].Expired || dl[2].Range == "" || dl[2].IfRange == "" {
		t.Fatalf("downloads = %+v", dl)
	}
	if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
		out := filepath.Join(filepath.Dir(dest), "out.m4b")
		if err := Decrypt(ctx, ffmpeg, dest, out, lic2); err != nil {
			t.Fatal(err)
		}
	}
	// Denials say why.
	for asin, want := range map[string]error{"B0FAKE0005": ErrNotReleased, "B0FAKE0029": ErrNoRights, "B0NOTOWNED": ErrNoRights} {
		if _, err := cl.License(ctx, asin); !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", asin, err, want)
		}
	}
	if !fake.Release("B0FAKE0005") {
		t.Fatal("release")
	}
	if _, err := cl.License(ctx, "B0FAKE0005"); err != nil {
		t.Fatalf("a released preorder: %v", err)
	}
	if _, err := cl.Title(ctx, "B0NOTOWNED"); !errors.Is(err, ErrNotInLibrary) {
		t.Fatalf("title not owned: %v", err)
	}
}
