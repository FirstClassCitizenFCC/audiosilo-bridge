package transfer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kodestar/audiosilo-manager/internal/audio"
	"github.com/kodestar/audiosilo-manager/internal/progress"
)

// SafeJoin resolves rel under root and guarantees the result cannot escape root
// (e.g. via ".." segments), since rel ultimately comes from a user-editable
// template. Security-critical: keep allowed and denied regression tests.
func SafeJoin(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(string(filepath.Separator) + rel) // neutralize leading ".."
	abs := filepath.Join(rootAbs, clean)
	relCheck, err := filepath.Rel(rootAbs, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the library root", rel)
	}
	return abs, nil
}

// Local writes into a directory on this machine (a local path, or a mounted
// SMB/NFS share). It is the default transfer backend.
type Local struct{}

// NewLocal returns a Local transfer backend.
func NewLocal() *Local { return &Local{} }

// EnsureRoot verifies the root exists and is a directory, failing fast before any
// copy — this catches an unmounted network share instead of silently creating a
// local stub tree.
func (*Local) EnsureRoot(_ context.Context, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("library root unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("library root %q is not a directory", root)
	}
	return nil
}

// Index walks root and records every audio file by name (paths relative to root).
// A missing root yields an empty index. The walk lists directories only — sizes
// are read lazily for same-name candidates — so it stays cheap on a large tree.
// ".part" temp files and dotfiles are ignored.
func (*Local) Index(_ context.Context, root string) (*Index, error) {
	idx := newIndex(func(rel string) (int64, error) {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	})
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return idx, nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || strings.HasPrefix(name, ".") || !audio.IsAudio(name) {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		idx.add(name, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

// Place copies SrcPath into <Root>/<RelDir>/<Filename>, creating missing
// directories. It is idempotent: a destination that already exists at the source's
// size is reported AlreadyPresent without copying; a same-name file of a different
// size is left untouched unless Overwrite is set.
func (l *Local) Place(ctx context.Context, req PlaceRequest) (PlaceResult, error) {
	if err := l.EnsureRoot(ctx, req.Root); err != nil {
		return PlaceResult{}, err
	}
	dir, err := SafeJoin(req.Root, req.RelDir)
	if err != nil {
		return PlaceResult{}, err
	}
	dst := filepath.Join(dir, req.Filename)

	srcInfo, err := os.Stat(req.SrcPath)
	if err != nil {
		return PlaceResult{}, err
	}
	if !req.Overwrite {
		if dstInfo, err := os.Stat(dst); err == nil {
			if dstInfo.Size() == srcInfo.Size() {
				return PlaceResult{DestPath: dst, AlreadyPresent: true}, nil
			}
			return PlaceResult{}, fmt.Errorf(
				"destination already exists with a different size (%d vs source %d): %s",
				dstInfo.Size(), srcInfo.Size(), dst)
		} else if !os.IsNotExist(err) {
			return PlaceResult{}, err
		}
	}
	// Unlike the SFTP backend, Local doesn't align ownership/mode to the existing
	// library: it runs as the local user writing to a local/mounted path, so new
	// content is already owned by that user (the umask handles the mode). The SFTP
	// case needs matching because it can land content as the SSH login user (root).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return PlaceResult{}, err
	}
	if err := copyVerified(ctx, req.SrcPath, dst, srcInfo.Size(), req.Progress); err != nil {
		return PlaceResult{}, err
	}
	return PlaceResult{DestPath: dst}, nil
}

// Close is a no-op for Local.
func (*Local) Close() error { return nil }

// copyVerified streams src to a temp file in the destination directory, renames it
// onto dst, and confirms the final size matches the source. ctx cancellation is
// honored between read chunks; progress (if set) is reported as bytes are written.
func copyVerified(ctx context.Context, src, dst string, wantSize int64, onProgress func(int64, int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".audiosilo-import-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	var w io.Writer = tmp
	if onProgress != nil {
		w = &progress.Writer{W: tmp, Total: wantSize, CB: onProgress}
	}
	if _, err := io.Copy(w, &ctxReader{ctx: ctx, r: in}); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if info, err := os.Stat(tmpName); err != nil || info.Size() != wantSize {
		cleanup()
		return fmt.Errorf("copy size mismatch: wrote %d, expected %d", sizeOrNeg(tmpName), wantSize)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func sizeOrNeg(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return -1
}
