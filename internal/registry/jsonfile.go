package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// This package's stores (registry, settings, sync-status, series-watch, Audible
// overrides) all persist non-secret JSON the same way; the load and atomic-save
// contracts live here exactly once.

// loadJSONFile reads a JSON file into v, creating the parent directory as needed
// so a later save can't fail on a missing dir. A missing file is not an error -
// v is left untouched and the store starts empty.
func loadJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, v)
}

// saveJSONFileAtomic writes v as indented JSON via a temp file + rename, so a
// crash mid-write can never truncate the store.
func saveJSONFileAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // don't leave a stray temp file behind on a failed rename
		return err
	}
	return nil
}
