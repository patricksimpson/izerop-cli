package sync2

import (
	"fmt"

	syncv1 "github.com/patricksimpson/izerop-cli/pkg/sync"
)

// MigrateFromV1 converts a v1 sync state to v2 format.
// Preserves remote IDs and hashes where available.
// The cursor is carried over so incremental pull continues working.
func MigrateFromV1(profile string) (*SyncedTree, error) {
	v1, err := syncv1.LoadState(profile)
	if err != nil {
		return nil, fmt.Errorf("load v1 state: %w", err)
	}

	tree := NewSyncedTree()
	tree.Cursor = v1.Cursor

	for relPath, rec := range v1.Files {
		sf := SyncedFile{
			RemoteID: rec.RemoteID,
			Size:     rec.Size,
		}
		// v1 stored a single hash — use it as both local and remote
		// since at sync time they were in agreement
		if rec.Hash != "" {
			sf.LocalHash = rec.Hash
			sf.RemoteHash = rec.Hash
		}
		tree.Files[relPath] = sf
	}

	// Also migrate notes
	for relPath, noteID := range v1.Notes {
		if _, exists := tree.Files[relPath]; !exists {
			tree.Files[relPath] = SyncedFile{
				RemoteID: noteID,
				IsNote:   true,
			}
		} else {
			sf := tree.Files[relPath]
			sf.IsNote = true
			tree.Files[relPath] = sf
		}
	}

	return tree, nil
}

// MigrateIfNeeded checks for v2 state; if missing but v1 exists, migrates.
// Returns the v2 state (migrated or loaded).
func MigrateIfNeeded(profile string) (*SyncedTree, bool, error) {
	// Try loading v2 first
	tree, err := LoadState(profile)
	if err == nil && len(tree.Files) > 0 {
		return tree, false, nil // already v2
	}

	// Check if v1 state exists
	v1, err := syncv1.LoadState(profile)
	if err != nil || (len(v1.Files) == 0 && len(v1.Notes) == 0) {
		// No v1 state either — start fresh
		return NewSyncedTree(), false, nil
	}

	// Migrate v1 → v2
	tree, err = MigrateFromV1(profile)
	if err != nil {
		return nil, false, fmt.Errorf("migration failed: %w", err)
	}

	// Save v2 state
	if err := SaveState(profile, tree); err != nil {
		return nil, false, fmt.Errorf("save migrated state: %w", err)
	}

	return tree, true, nil
}
