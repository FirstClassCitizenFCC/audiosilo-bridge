package fsutil

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// FreeBytes is the space the calling user may still use on the volume holding
// path (GetDiskFreeSpaceEx's caller-available bytes).
func FreeBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	if avail > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(avail), nil
}

// IsNoSpace reports whether err is a volume that ran out of space
// (ERROR_DISK_FULL, ERROR_HANDLE_DISK_FULL).
func IsNoSpace(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}

// IsReadOnly reports whether err is a write refused by a write-protected
// volume (ERROR_WRITE_PROTECT).
func IsReadOnly(err error) bool {
	return errors.Is(err, windows.ERROR_WRITE_PROTECT)
}
