package transfer

import (
	"path"
	"slices"
	"strings"
)

// audioExtensions are the audio extensions the index records (lowercase, with
// dot), the manager's audio.Extensions: the Bridge's engine keeps its own list in
// internal/audio, which transfer doesn't import (it pulls in the meta module).
var audioExtensions = []string{
	".m4b", ".m4a", ".mp4", ".mp3", ".aac",
	".flac", ".ogg", ".opus", ".wav", ".aif", ".aiff", ".wma",
}

// isAudio reports whether name (a path or filename) has an audio extension.
func isAudio(name string) bool {
	return slices.Contains(audioExtensions, strings.ToLower(path.Ext(name)))
}

// Index records the audio files already under a destination root, keyed by
// lowercased basename. Matching on name (then confirming size) recognizes a book
// that was already placed even after its containing folder was renamed - an
// exact-path check would miss that. Sizes are read lazily via statSize (a backend
// call: os.Stat for Local, sftp.Stat for SFTP), so a non-match costs no extra I/O,
// which keeps the index cheap on a network mount or over SSH.
type Index struct {
	byName   map[string][]string // lowercased basename -> rel paths
	statSize func(rel string) (int64, error)
	count    int
}

// newIndex builds an empty index whose candidate sizes are resolved by statSize.
func newIndex(statSize func(rel string) (int64, error)) *Index {
	return &Index{byName: map[string][]string{}, statSize: statSize}
}

// add records one file by its basename and root-relative path.
func (i *Index) add(name, rel string) {
	key := strings.ToLower(name)
	i.byName[key] = append(i.byName[key], rel)
	i.count++
}

// Count is the number of indexed files.
func (i *Index) Count() int { return i.count }

// Find reports the rel path of an already-present file that is the same book as a
// source with the given basename and size (so a moved/renamed folder is still
// recognized), and whether one was found. Only same-name candidates are stat'd.
func (i *Index) Find(name string, size int64) (string, bool, error) {
	for _, rel := range i.byName[strings.ToLower(name)] {
		sz, err := i.statSize(rel)
		if err != nil {
			continue // candidate vanished or unreadable; treat as not a match
		}
		if sz == size {
			return rel, true, nil
		}
	}
	return "", false, nil
}
