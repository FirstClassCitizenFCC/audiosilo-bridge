// Package transfer writes audiobook files into a destination library tree. It is
// the only component that writes content — the server is read-only over the
// network. Two backends share one interface: Local (this host or a mounted share)
// and SFTP (over SSH, M3). Every placement is atomic (temp file + rename),
// verified by size, and idempotent.
package transfer

import "context"

// PlaceRequest describes a single file placement.
type PlaceRequest struct {
	Root      string                    // destination library root
	RelDir    string                    // library-relative destination folder
	Filename  string                    // destination file name
	SrcPath   string                    // absolute source path on this machine
	Overwrite bool                      // replace a same-name file of a different size
	Progress  func(copied, total int64) // optional byte-progress callback
}

// PlaceResult is the outcome of a Place.
type PlaceResult struct {
	DestPath       string // where the file now lives
	AlreadyPresent bool   // destination already held this file at the right size (no copy)
}

// Transfer is a destination backend.
type Transfer interface {
	// EnsureRoot fails fast if the root is missing or not a directory (e.g. an
	// unmounted SMB/NFS share, or a wrong remote path).
	EnsureRoot(ctx context.Context, root string) error
	// Index lists existing audio files under root for idempotency (basename+size).
	Index(ctx context.Context, root string) (*Index, error)
	// Place copies SrcPath to <Root>/<RelDir>/<Filename> atomically and verified.
	Place(ctx context.Context, req PlaceRequest) (PlaceResult, error)
	// Close releases backend resources (e.g. an SSH connection). Local is a no-op.
	Close() error
}
