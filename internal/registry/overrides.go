package registry

import (
	"maps"
	"sort"
	"sync"
)

// OverrideStore persists the Audible-backup view's per-book manual overrides so they
// survive closing/reopening the view (and restarting the app). Two maps per (server,
// library), both keyed by the Audible ASIN:
//
//   - Matches: ASIN → server rel_path the user pointed the book at (it's already on
//     the server under a name the auto-matcher missed).
//   - Dests: ASIN → full library-relative destination (folder + filename) the user
//     chose for an import instead of the planned placement.
//   - Ignored: ASIN → true for books the user never wants the stats sync to touch
//     (e.g. a shared account's kids' series). Persisted so the choice sticks.
//
// It's non-secret UI bookkeeping, so it lives in a plain JSON file beside the
// registry (atomic temp+rename save), like the sync-status and series-watch stores.
type OverrideStore struct {
	mu     sync.Mutex
	path   string
	scopes map[string]scopeOverrides // scopeKey → this library's overrides
}

// Overrides is one library's manual overrides. Nil maps are tolerated everywhere:
// Get always returns non-nil maps, and Set stores nil as empty.
type Overrides struct {
	Matches map[string]string `json:"matches"`
	Dests   map[string]string `json:"dests"`
	Ignored map[string]bool   `json:"ignored"`
}

type scopeOverrides struct {
	LibraryScope
	Overrides
}

type overrideFile struct {
	Entries []scopeOverrides `json:"entries"`
}

// OpenOverrides loads the store from path (a missing file yields an empty set).
func OpenOverrides(path string) (*OverrideStore, error) {
	s := &OverrideStore{path: path, scopes: map[string]scopeOverrides{}}
	var f overrideFile
	if err := loadJSONFile(path, &f); err != nil {
		return nil, err
	}
	for _, e := range f.Entries {
		s.scopes[scopeKey(e.ServerID, e.LibraryID)] = e
	}
	return s, nil
}

// Get returns a (server, library)'s overrides, always with non-nil maps.
func (s *OverrideStore) Get(serverID string, libraryID int64) Overrides {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Overrides{Matches: map[string]string{}, Dests: map[string]string{}, Ignored: map[string]bool{}}
	if sc, ok := s.scopes[scopeKey(serverID, libraryID)]; ok {
		maps.Copy(out.Matches, sc.Matches)
		maps.Copy(out.Dests, sc.Dests)
		maps.Copy(out.Ignored, sc.Ignored)
	}
	return out
}

// Set replaces a (server, library)'s overrides wholesale and persists atomically.
// Nil maps are stored as empty; clearing both drops the scope entirely so the file
// doesn't accrue empty entries.
func (s *OverrideStore) Set(serverID string, libraryID int64, ov Overrides) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(serverID, libraryID, ov)
}

// Update atomically applies mut to a (server, library)'s overrides and persists. The
// read-modify-write happens without releasing the lock, so two callers that each own a
// different field (SaveOverrides writes matches/dests; SetStatsIgnored writes the
// ignore set) can't lose one another's change - which a caller-side Get-then-Set could.
func (s *OverrideStore) Update(serverID string, libraryID int64, mut func(ov *Overrides)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ov := Overrides{Matches: map[string]string{}, Dests: map[string]string{}, Ignored: map[string]bool{}}
	if sc, ok := s.scopes[scopeKey(serverID, libraryID)]; ok {
		maps.Copy(ov.Matches, sc.Matches)
		maps.Copy(ov.Dests, sc.Dests)
		maps.Copy(ov.Ignored, sc.Ignored)
	}
	mut(&ov)
	return s.setLocked(serverID, libraryID, ov)
}

// setLocked is Set's body; the caller must hold s.mu.
func (s *OverrideStore) setLocked(serverID string, libraryID int64, ov Overrides) error {
	key := scopeKey(serverID, libraryID)
	if len(ov.Matches) == 0 && len(ov.Dests) == 0 && len(ov.Ignored) == 0 {
		delete(s.scopes, key)
		return s.save()
	}
	if ov.Matches == nil {
		ov.Matches = map[string]string{}
	}
	if ov.Dests == nil {
		ov.Dests = map[string]string{}
	}
	if ov.Ignored == nil {
		ov.Ignored = map[string]bool{}
	}
	s.scopes[key] = scopeOverrides{
		LibraryScope: LibraryScope{ServerID: serverID, LibraryID: libraryID}, Overrides: ov,
	}
	return s.save()
}

// save writes the whole set atomically (caller must hold the lock).
func (s *OverrideStore) save() error {
	f := overrideFile{}
	for _, sc := range s.scopes {
		f.Entries = append(f.Entries, sc)
	}
	sort.Slice(f.Entries, func(i, j int) bool {
		return lessScope(f.Entries[i].LibraryScope, f.Entries[j].LibraryScope)
	})
	return saveJSONFileAtomic(s.path, f)
}
