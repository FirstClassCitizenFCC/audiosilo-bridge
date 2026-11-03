//go:build unix

package fsutil

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

// FreeBytes is the space an unprivileged writer may still use on the
// filesystem holding path (statfs's available blocks times the block size).
func FreeBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	avail, size := uint64(st.Bavail), uint64(st.Bsize) //nolint:gosec,unconvert // G115: the field types differ by OS; both are non-negative
	if size != 0 && avail > math.MaxInt64/size {
		return math.MaxInt64, nil
	}
	return int64(avail * size), nil //nolint:gosec // G115: bounded by the check above
}

// IsNoSpace reports whether err is a filesystem that ran out of space (ENOSPC)
// or quota (EDQUOT).
func IsNoSpace(err error) bool {
	return errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.EDQUOT)
}
