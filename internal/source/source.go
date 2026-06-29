// Package source discovers importable audiobooks and normalizes their metadata.
// A Source yields Records that point at local files (a folder walk or drag-drop);
// a Source that must fetch bytes first (e.g. Audible, M5) also implements Acquirer.
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
	Key         string // dedup/resume key: ASIN if present, else the basename
}

// Source discovers importable audiobooks.
type Source interface {
	Name() string
	Discover(ctx context.Context) ([]Record, error)
}

// Acquirer fetches a record's bytes to a local path before transfer, returning a
// cleanup to remove the temp file afterward. File-based sources don't implement it
// (their Record.SrcPath already points at a local file).
type Acquirer interface {
	Acquire(ctx context.Context, r Record) (localPath string, cleanup func(), err error)
}
