package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ErrExists is a no-replace install or rename that found a file at its
// target. It wraps fs.ErrExist, so a refused hard link (EEXIST) and a refused
// rename both satisfy errors.Is(err, fs.ErrExist).
var ErrExists = fmt.Errorf("a file is already at the target: %w", fs.ErrExist)

// LinkFunc makes newname a hard link to oldname inside root (root.Link; tests
// stand in a filesystem without hard links).
type LinkFunc func(root *os.Root, oldname, newname string) error

// InstallNoReplace puts the staged temp at name inside root and never replaces
// a file there (the podcast episode install and the library transfer share it).
// An exclusive hard link is the first choice everywhere: it fails with EEXIST
// (fs.ErrExist) when name exists. A filesystem without hard links (SMB/CIFS,
// some NAS or FUSE shares, bind mounts of those) falls back to RenameNoReplace,
// which answers ErrExists. It reports whether temp still exists (it was
// linked), which the caller then removes. link nil is root.Link.
func InstallNoReplace(root *os.Root, temp, name string, link LinkFunc) (linked bool, err error) {
	if link == nil {
		link = func(r *os.Root, oldname, newname string) error { return r.Link(oldname, newname) }
	}
	err = link(root, temp, name)
	if err == nil {
		return true, nil
	}
	if !LinkUnsupported(err) {
		return false, err
	}
	return false, RenameNoReplace(root, temp, name)
}

// LinkUnsupported reports whether a failed link means the filesystem has no
// hard links (as opposed to a target that exists, or a real permission problem
// the rename would meet too).
func LinkUnsupported(err error) bool {
	for _, unsupported := range UnsupportedLinkErrors {
		if errors.Is(err, unsupported) {
			return true
		}
	}
	return false
}

// RenameIfAbsent is the portable no-replace rename: the absence check runs
// immediately before the rename, so only a writer outside the caller's own
// locks can race it (RenameNoReplace is atomic where the kernel offers it).
func RenameIfAbsent(root *os.Root, oldname, newname string) error {
	if _, err := root.Lstat(newname); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(oldname, newname)
}
