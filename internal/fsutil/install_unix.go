//go:build !windows

package fsutil

import "syscall"

// UnsupportedLinkErrors are link(2)'s answers on a filesystem without hard links:
// EPERM (Linux, e.g. vfat, CIFS without unix extensions), ENOTSUP/EOPNOTSUPP
// (macOS SMB and many FUSE shares), EMLINK and ENOSYS.
var UnsupportedLinkErrors = []error{syscall.EPERM, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EMLINK, syscall.ENOSYS}
