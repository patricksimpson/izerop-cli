package sync

import (
	"github.com/patricksimpson/izerop-cli/pkg/api"
)

// Engine handles bidirectional file synchronization.
type Engine struct {
	Client  *api.Client
	SyncDir string
}

// NewEngine creates a sync engine.
func NewEngine(client *api.Client, syncDir string) *Engine {
	return &Engine{
		Client:  client,
		SyncDir: syncDir,
	}
}

// ChangeType represents the kind of sync change.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
)

// Change represents a single file change to sync.
type Change struct {
	Type      ChangeType
	Path      string
	LocalHash string
	RemoteHash string
}

// TODO: Implement sync logic
// - Scan local directory for files + checksums
// - Fetch remote file list via API
// - Diff local vs remote to determine changes
// - Upload new/modified local files
// - Download new/modified remote files
// - Handle conflicts (last-write-wins or prompt)
