package sync2

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/patricksimpson/izerop-cli/pkg/config"
)

const stateVersion = 1

// SyncedTree is the "synced" tree — the last agreed-upon state between local and remote.
// This is the common ancestor used for three-way comparison.
type SyncedTree struct {
	Version int                   `json:"version"`
	Cursor  string                `json:"cursor"`
	Files   map[string]SyncedFile `json:"files"`
}

// SyncedFile records what a file looked like the last time local and remote agreed.
type SyncedFile struct {
	RemoteID   string `json:"id"`
	LocalHash  string `json:"local_hash"`  // content hash of local file at last sync
	RemoteHash string `json:"remote_hash"` // content hash from server at last sync
	Size       int64  `json:"size"`
	IsNote     bool   `json:"is_note,omitempty"`
}

// NewSyncedTree creates an empty synced tree.
func NewSyncedTree() *SyncedTree {
	return &SyncedTree{
		Version: stateVersion,
		Files:   make(map[string]SyncedFile),
	}
}

// statePath returns the path to the v2 sync state file for a profile.
func statePath(profile string) (string, error) {
	dir, err := config.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sync-state-v2.json"), nil
}

// LoadState reads the v2 sync state from the profile config dir.
func LoadState(profile string) (*SyncedTree, error) {
	path, err := statePath(profile)
	if err != nil {
		return NewSyncedTree(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return NewSyncedTree(), nil
	}

	var tree SyncedTree
	if err := json.Unmarshal(data, &tree); err != nil {
		return NewSyncedTree(), nil
	}
	if tree.Files == nil {
		tree.Files = make(map[string]SyncedFile)
	}
	return &tree, nil
}

// SaveState writes the v2 sync state to the profile config dir.
func SaveState(profile string, tree *SyncedTree) error {
	path, err := statePath(profile)
	if err != nil {
		return err
	}
	tree.Version = stateVersion
	data, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0700)
	return os.WriteFile(path, data, 0600)
}
