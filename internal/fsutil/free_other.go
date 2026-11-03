//go:build unix && !(linux || darwin || freebsd || dragonfly || aix)

package fsutil

import "errors"

// FreeBytes can't read the free space on this system (its statfs differs):
// callers treat the error as "unknown" and go ahead.
func FreeBytes(string) (int64, error) { return 0, errors.ErrUnsupported }
