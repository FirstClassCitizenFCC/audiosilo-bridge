package podcast

import (
	"errors"
	"os"

	"github.com/kodestar/audiosilo-bridge/internal/fsutil"
)

// linkFile is root.Link; tests replace it to stand in for a filesystem without
// hard links.
var linkFile fsutil.LinkFunc = func(root *os.Root, oldname, newname string) error { return root.Link(oldname, newname) }

// unsupportedLinkErrors are link's answers on a filesystem without hard links
// (fsutil.UnsupportedLinkErrors).
var unsupportedLinkErrors = fsutil.UnsupportedLinkErrors

// installAudio puts the staged temp at name and never replaces a file there
// (fsutil.InstallNoReplace: an exclusive hard link, else a rename that refuses
// an existing target). It reports whether temp still exists (it was linked),
// which the caller then removes. The caller holds the show's mutation lock and
// the episode's file lock, so only a writer outside the Bridge can race the
// fallback's absence check.
func installAudio(root *os.Root, temp, name string) (linked bool, err error) {
	linked, err = fsutil.InstallNoReplace(root, temp, name, linkFile)
	return linked, invalidIfExists(err)
}

// renameNoReplace and renameIfAbsent are fsutil's, answering ErrInvalidFile for
// an existing target as the journal's recovery expects.
func renameNoReplace(root *os.Root, oldname, newname string) error {
	return invalidIfExists(fsutil.RenameNoReplace(root, oldname, newname))
}

func renameIfAbsent(root *os.Root, oldname, newname string) error {
	return invalidIfExists(fsutil.RenameIfAbsent(root, oldname, newname))
}

// invalidIfExists maps fsutil.ErrExists (a refused rename) to ErrInvalidFile;
// a refused hard link's EEXIST stays as it is.
func invalidIfExists(err error) error {
	if errors.Is(err, fsutil.ErrExists) {
		return ErrInvalidFile
	}
	return err
}
