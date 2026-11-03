//go:build unix

package fsutil

import (
	"errors"

	"golang.org/x/sys/unix"
)

// IsNoSpace reports whether err is a filesystem that ran out of space (ENOSPC)
// or quota (EDQUOT).
func IsNoSpace(err error) bool {
	return errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.EDQUOT)
}

// IsReadOnly reports whether err is a write refused by a read-only filesystem
// (EROFS: a share mounted or remounted read-only), which fs.ErrPermission
// doesn't match.
func IsReadOnly(err error) bool {
	return errors.Is(err, unix.EROFS)
}
