//go:build !linux

package fsutil

import "os"

// RenameNoReplace has no portable atomic form here: the absence check
// (RenameIfAbsent) guards the rename.
func RenameNoReplace(root *os.Root, oldname, newname string) error {
	return RenameIfAbsent(root, oldname, newname)
}
