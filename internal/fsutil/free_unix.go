//go:build linux || darwin || freebsd || dragonfly || aix

package fsutil

import (
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
	// Signed on the BSDs, where a filesystem inside its root reserve answers a
	// negative count: no room for an unprivileged writer.
	avail, size := int64(st.Bavail), int64(st.Bsize) //nolint:gosec,unconvert // G115: the field types differ by OS
	switch {
	case avail <= 0 || size <= 0:
		return 0, nil
	case avail > math.MaxInt64/size:
		return math.MaxInt64, nil
	}
	return avail * size, nil
}
