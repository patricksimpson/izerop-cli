package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// State tracks sync state between runs.
type State struct {
	Cursor string `json:"cursor"`
}

// StateFilePath returns the path to the sync state file within a sync dir.
func StateFilePath(syncDir string) string {
	return filepath.Join(syncDir, ".izerop-sync.json")
}

// LoadState reads the sync state from disk.
func LoadState(syncDir string) (*State, error) {
	path := StateFilePath(syncDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return &State{}, nil // fresh state if no file
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return &State{}, nil
	}
	return &state, nil
}

// SaveState writes the sync state to disk.
func SaveState(syncDir string, state *State) error {
	path := StateFilePath(syncDir)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
