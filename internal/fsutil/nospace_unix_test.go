//go:build unix

package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// The write refusals the import names: out of space or quota, and a read-only
// filesystem (which fs.ErrPermission doesn't match), wrapped as the os
// package wraps them.
func TestWriteRefusals(t *testing.T) {
	wrap := func(errno error) error {
		return fmt.Errorf("install: %w", &os.PathError{Op: "open", Path: "x", Err: errno})
	}
	if !IsNoSpace(wrap(unix.ENOSPC)) || !IsNoSpace(wrap(unix.EDQUOT)) || IsNoSpace(wrap(unix.EROFS)) {
		t.Fatal("IsNoSpace: ENOSPC and EDQUOT only")
	}
	if !IsReadOnly(wrap(unix.EROFS)) || IsReadOnly(wrap(unix.EACCES)) || IsReadOnly(fs.ErrPermission) {
		t.Fatal("IsReadOnly: EROFS only")
	}
}
