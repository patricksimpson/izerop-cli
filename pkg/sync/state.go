package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// FileRecord tracks the last-known state of a synced file.
type FileRecord struct {
	RemoteID   string `json:"remote_id"`
	Size       int64  `json:"size"`
	Hash       string `json:"hash,omitempty"`
	RemoteTime string `json:"remote_time,omitempty"`
	LocalMod   int64  `json:"local_mod,omitempty"` // unix timestamp
}

// State tracks sync state between runs.
type State struct {
	Cursor string            `json:"cursor"`
	// Notes maps local relative paths to remote file IDs for note/text files.
	Notes  map[string]string `json:"notes,omitempty"`
	// Files maps local relative paths to their last-synced state.
	Files  map[string]FileRecord `json:"files,omitempty"`
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
		return &State{Files: make(map[string]FileRecord)}, nil
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return &State{Files: make(map[string]FileRecord)}, nil
	}
	if state.Files == nil {
		state.Files = make(map[string]FileRecord)
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
