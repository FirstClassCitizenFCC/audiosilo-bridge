package registry

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
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
//
// It's non-secret UI bookkeeping, so it lives in a plain JSON file beside the
// registry (atomic temp+rename save), like the sync-status and series-watch stores.
type OverrideStore struct {
	mu     sync.Mutex
	path   string
	scopes map[string]*scopeOverrides // scopeKey → this library's overrides
}

// Overrides is one library's manual overrides (also the wire shape the facade
// returns). Nil maps are tolerated on read and normalized to empty.
type Overrides struct {
	Matches map[string]string `json:"matches"`
	Dests   map[string]string `json:"dests"`
}

type scopeOverrides struct {
	ServerID  string `json:"server_id"`
	LibraryID int64  `json:"library_id"`
	Overrides
}

type overrideFile struct {
	Entries []scopeOverrides `json:"entries"`
}

// OpenOverrides loads the store from path (a missing file yields an empty set).
func OpenOverrides(path string) (*OverrideStore, error) {
	s := &OverrideStore{path: path, scopes: map[string]*scopeOverrides{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var f overrideFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	for i := range f.Entries {
		e := f.Entries[i]
		s.scopes[scopeKey(e.ServerID, e.LibraryID)] = &e
	}
	return s, nil
}

// Get returns a (server, library)'s overrides, always with non-nil maps.
func (s *OverrideStore) Get(serverID string, libraryID int64) Overrides {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Overrides{Matches: map[string]string{}, Dests: map[string]string{}}
	if sc := s.scopes[scopeKey(serverID, libraryID)]; sc != nil {
		maps.Copy(out.Matches, sc.Matches)
		maps.Copy(out.Dests, sc.Dests)
	}
	return out
}

// Set replaces a (server, library)'s overrides wholesale and persists atomically.
// Empty maps drop the scope entirely so the file doesn't accrue empty entries.
func (s *OverrideStore) Set(serverID string, libraryID int64, ov Overrides) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(serverID, libraryID)
	if len(ov.Matches) == 0 && len(ov.Dests) == 0 {
		delete(s.scopes, key)
	} else {
		s.scopes[key] = &scopeOverrides{ServerID: serverID, LibraryID: libraryID, Overrides: ov}
	}
	return s.save()
}

// save writes the whole set atomically (caller must hold the lock).
func (s *OverrideStore) save() error {
	f := overrideFile{}
	for _, sc := range s.scopes {
		f.Entries = append(f.Entries, *sc)
	}
	sort.Slice(f.Entries, func(i, j int) bool {
		if f.Entries[i].ServerID != f.Entries[j].ServerID {
			return f.Entries[i].ServerID < f.Entries[j].ServerID
		}
		return f.Entries[i].LibraryID < f.Entries[j].LibraryID
	})
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
