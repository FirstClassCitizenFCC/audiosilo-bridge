// Package transfer places an audiobook into the library root. The server never
// writes — this is the only component that touches the destination tree. Copies
// go to a temp file in the destination directory and are renamed into place, so
// a partially copied file is never visible to a scan, and the copy is verified
// by size before it is considered done.
package transfer

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SafeJoin resolves rel under root and guarantees the result cannot escape root
// (e.g. via ".." segments), since rel ultimately comes from user-editable input.
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

// EnsureRoot verifies the library root exists and is a directory, failing fast
// before any copy. This catches an unmounted network share (the destination is
// typically an SMB/NFS mount) instead of silently creating a local stub tree.
func EnsureRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("library root unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("library root %q is not a directory", root)
	}
	return nil
}

// Index records the .m4b files already under a destination root, keyed by file
// name. Matching on the name (then confirming size) recognizes a book that was
// already copied even after its containing folder was renamed, which an
// exact-path check would miss.
//
// The walk lists directories only — it does not stat every file — so it stays
// cheap on a network-mounted library, where a stat-per-file walk of a large tree
// can take minutes. Sizes are read lazily, only for the handful of files whose
// name matches a book about to be placed.
type Index struct {
	root   string
	byName map[string][]string // lower-cased base name -> relative paths
	count  int
}

// IndexDest walks root and records every .m4b file by name, with paths relative
// to root. A missing root yields an empty index (nothing copied yet). Temporary
// ".part" files from an interrupted copy, and other dotfiles, are ignored.
func IndexDest(root string) (*Index, error) {
	idx := &Index{root: root, byName: map[string][]string{}}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return idx, nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".m4b") {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		key := strings.ToLower(name)
		idx.byName[key] = append(idx.byName[key], rel)
		idx.count++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

// Count is the number of indexed files.
func (i *Index) Count() int { return i.count }

// Find reports the relative path of a file already present that is the same book
// as src — same file name and same size, so a book whose folder was moved or
// renamed is still recognized — and whether one was found. Only candidates that
// share src's name are stat'd, so an unplaced book costs no extra I/O.
func (i *Index) Find(src string) (string, bool, error) {
	candidates := i.byName[strings.ToLower(filepath.Base(src))]
	if len(candidates) == 0 {
		return "", false, nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", false, err
	}
	for _, rel := range candidates {
		ci, err := os.Stat(filepath.Join(i.root, rel))
		if err != nil {
			continue // candidate vanished or unreadable; treat as not a match
		}
		if ci.Size() == info.Size() {
			return rel, true, nil
		}
	}
	return "", false, nil
}

// DestExists reports whether the destination file is already present.
func DestExists(root, relDir, filename string) (bool, error) {
	dir, err := SafeJoin(root, relDir)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, filename))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Place copies src into <root>/<relDir>/<filename>, creating any missing
// directories. It returns the absolute destination path.
//
// Placement is idempotent: if the destination already exists with the same size
// as the source, it is treated as already placed and the copy is skipped (so a
// retry after a partial run — e.g. the copy succeeded but verification later
// failed — does not error or duplicate work). A same-name file with a different
// size is left untouched unless overwrite is set.
func Place(root, relDir, filename, src string, overwrite bool) (string, error) {
	if err := EnsureRoot(root); err != nil {
		return "", err
	}
	dir, err := SafeJoin(root, relDir)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filename)

	srcInfo, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !overwrite {
		if dstInfo, err := os.Stat(dst); err == nil {
			if dstInfo.Size() == srcInfo.Size() {
				return dst, nil // already placed; nothing to do
			}
			return "", fmt.Errorf("destination already exists with a different size (%d vs source %d): %s",
				dstInfo.Size(), srcInfo.Size(), dst)
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := copyVerified(src, dst, srcInfo.Size()); err != nil {
		return "", err
	}
	return dst, nil
}

// copyVerified streams src to a temp file in the destination directory, renames
// it onto dst, and confirms the final size matches the source.
func copyVerified(src, dst string, wantSize int64) error {
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

	if _, err := io.Copy(tmp, in); err != nil {
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

func sizeOrNeg(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return -1
}
