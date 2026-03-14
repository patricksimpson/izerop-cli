package sync2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/config"
)

// ConflictEntry records a sync conflict for later resolution.
type ConflictEntry struct {
	Path       string    `json:"path"`
	LocalHash  string    `json:"local_hash"`
	RemoteHash string    `json:"remote_hash"`
	RemoteID   string    `json:"remote_id"`
	DetectedAt time.Time `json:"detected_at"`
	Resolved   bool      `json:"resolved,omitempty"`
}

// ConflictQueue holds unresolved conflicts.
type ConflictQueue struct {
	Conflicts []ConflictEntry `json:"conflicts"`
}

func conflictPath(profile string) (string, error) {
	dir, err := config.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "conflicts.json"), nil
}

// LoadConflicts reads the conflict queue from disk.
func LoadConflicts(profile string) (*ConflictQueue, error) {
	path, err := conflictPath(profile)
	if err != nil {
		return &ConflictQueue{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return &ConflictQueue{}, nil
	}
	var q ConflictQueue
	if err := json.Unmarshal(data, &q); err != nil {
		return &ConflictQueue{}, nil
	}
	return &q, nil
}

// SaveConflicts writes the conflict queue to disk.
func SaveConflicts(profile string, q *ConflictQueue) error {
	path, err := conflictPath(profile)
	if err != nil {
		return err
	}
	// Filter out resolved conflicts before saving
	var active []ConflictEntry
	for _, c := range q.Conflicts {
		if !c.Resolved {
			active = append(active, c)
		}
	}
	q.Conflicts = active

	if len(q.Conflicts) == 0 {
		// No conflicts — remove the file
		os.Remove(path)
		return nil
	}

	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0700)
	return os.WriteFile(path, data, 0600)
}

// Add queues a new conflict.
func (q *ConflictQueue) Add(entry ConflictEntry) {
	// Replace existing conflict for same path
	for i, c := range q.Conflicts {
		if c.Path == entry.Path {
			q.Conflicts[i] = entry
			return
		}
	}
	q.Conflicts = append(q.Conflicts, entry)
}

// Resolve marks a conflict as resolved.
func (q *ConflictQueue) Resolve(path string) {
	for i, c := range q.Conflicts {
		if c.Path == path {
			q.Conflicts[i].Resolved = true
			return
		}
	}
}

// Unresolved returns only unresolved conflicts.
func (q *ConflictQueue) Unresolved() []ConflictEntry {
	var result []ConflictEntry
	for _, c := range q.Conflicts {
		if !c.Resolved {
			result = append(result, c)
		}
	}
	return result
}
