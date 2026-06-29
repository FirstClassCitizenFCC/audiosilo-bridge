package audible

import (
	"context"
	"os"
	"path/filepath"

	"github.com/kodestar/audiosilo-manager/internal/source"
)

// Source adapts an Audible Client to the import pipeline: Discover lists the owned
// library, and Acquire (download-on-demand) fetches + DRM-strips one book to a
// temp .m4b. It satisfies source.Source and source.Acquirer, so importjob.Run
// handles placement, transfer, the skip-if-already-on-server check, and cleanup.
type Source struct {
	client *Client
	ffmpeg string // ffmpeg path ("" → $PATH; must be ≥4.4 for AAXC)
	tmpDir string // base for per-book temp dirs ("" → os default)
}

// NewSource builds an Audible import source.
func NewSource(client *Client, ffmpeg, tmpDir string) *Source {
	return &Source{client: client, ffmpeg: ffmpeg, tmpDir: tmpDir}
}

// Name identifies the source.
func (s *Source) Name() string { return "Audible" }

// Discover lists the account's owned audiobooks as import records.
func (s *Source) Discover(ctx context.Context) ([]source.Record, error) {
	return s.client.Library(ctx)
}

// Acquire downloads and DRM-strips one book to a temp .m4b, returning the path and
// a cleanup that removes the temp dir. The encrypted source and the key are never
// kept (the .aaxc is deleted right after decryption; the m4b is removed by cleanup
// once the transfer has copied it).
func (s *Source) Acquire(ctx context.Context, rec source.Record) (string, func(), error) {
	dlURL, key, iv, err := s.client.License(ctx, rec.ASIN)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp(s.tmpDir, "audible-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	aaxc := filepath.Join(tmp, rec.ASIN+".aaxc")
	if err := s.client.Download(ctx, dlURL, aaxc); err != nil {
		cleanup()
		return "", nil, err
	}
	m4b := filepath.Join(tmp, rec.ASIN+".m4b")
	if err := runFFmpeg(ctx, s.ffmpeg, aaxcArgs(aaxc, key, iv, m4b)); err != nil {
		cleanup()
		return "", nil, err
	}
	_ = os.Remove(aaxc) // drop the encrypted source as soon as it's decrypted
	return m4b, cleanup, nil
}
