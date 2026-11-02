// Package source discovers importable audiobooks and normalizes their metadata.
// A Source yields Records that point at local files (a folder walk or drag-drop);
// a Source that must fetch bytes first (Audible) also implements Acquirer.
package source

import "context"

// Record is normalized metadata for one importable audiobook file.
type Record struct {
	SrcPath     string // absolute path on this machine ("" until Acquire for fetch sources)
	FileName    string // destination file name (for fetch sources where SrcPath is empty)
	Title       string
	TitleShort  string
	Author      string
	Series      string
	Narrator    string
	ASIN        string
	Year        string
	Sequence    float64
	HasSequence bool
	// RuntimeSec is the book's total length in seconds (0 = unknown). Fetch sources
	// that know it (Audible) populate it; it lets the stats sync tell an at-the-end
	// position from a mid-book one.
	RuntimeSec float64
	Key        string // dedup/resume key: ASIN if present, else the basename
	// UnavailableReason, when non-empty, says why this record can't be acquired
	// (e.g. an Audible periodical or a title without download rights). File-based
	// sources leave it empty; importjob.Run fails such records with the reason
	// instead of attempting them.
	UnavailableReason string
}

// Source discovers importable audiobooks.
type Source interface {
	Name() string
	Discover(ctx context.Context) ([]Record, error)
}

// ProgressFunc reports sub-progress of an Acquire. stage is "downloading" (copied /
// total bytes; total is -1 when unknown) or "decrypting" (removing DRM; copied/total
// are 0, it has no byte progress). It may be called many times; nil is allowed.
type ProgressFunc func(stage string, copied, total int64)

// Acquirer fetches a record's bytes to a local path before transfer, returning a
// cleanup to remove the temp file afterward. File-based sources don't implement it
// (their Record.SrcPath already points at a local file). onProgress (may be nil)
// reports the download/decrypt sub-stages.
type Acquirer interface {
	Acquire(ctx context.Context, r Record, onProgress ProgressFunc) (localPath string, cleanup func(), err error)
}
