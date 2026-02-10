package sync

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/patricksimpson/izerop-cli/pkg/config"
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

// StatePath returns the path to the sync state file for a profile.
// State is stored in the profile config dir (~/.config/izerop/profiles/<name>/sync-state.json).
func StatePath(profile string) (string, error) {
	return config.ProfileStatePath(profile)
}

// MigrateState moves the legacy .izerop-sync.json from the sync dir to the profile config dir.
func MigrateState(profile string, syncDir string) {
	if syncDir == "" {
		return
	}
	legacyPath := filepath.Join(syncDir, ".izerop-sync.json")
	newPath, err := StatePath(profile)
	if err != nil {
		return
	}
	// Only migrate if legacy exists and new doesn't
	if _, err := os.Stat(legacyPath); err != nil {
		return // no legacy file
	}
	if _, err := os.Stat(newPath); err == nil {
		// New file already exists — just remove legacy
		os.Remove(legacyPath)
		return
	}
	// Move legacy to new location
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(newPath), 0700)
	if err := os.WriteFile(newPath, data, 0600); err != nil {
		return
	}
	os.Remove(legacyPath)
}

// LoadState reads the sync state from the profile config dir.
func LoadState(profile string) (*State, error) {
	path, err := StatePath(profile)
	if err != nil {
		return &State{Files: make(map[string]FileRecord)}, nil
	}
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

// SaveState writes the sync state to the profile config dir.
func SaveState(profile string, state *State) error {
	path, err := StatePath(profile)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
