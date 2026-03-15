package sync2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/api"
	syncv1 "github.com/patricksimpson/izerop-cli/pkg/sync"
)

// Action represents what needs to happen for a single file.
type Action struct {
	Type     ActionType
	Path     string // relative path within sync dir
	RemoteID string // server file ID (if known)
	Reason   string // human-readable explanation
}

// ActionType enumerates the possible sync actions.
type ActionType int

const (
	ActionSkip         ActionType = iota
	ActionPush                    // local → remote (new or updated)
	ActionPull                    // remote → local (new or updated)
	ActionDeleteLocal             // removed on server, delete locally
	ActionDeleteRemote            // removed locally, delete on server
	ActionConflict                // both sides changed
)

func (a ActionType) String() string {
	switch a {
	case ActionSkip:
		return "skip"
	case ActionPush:
		return "push"
	case ActionPull:
		return "pull"
	case ActionDeleteLocal:
		return "delete-local"
	case ActionDeleteRemote:
		return "delete-remote"
	case ActionConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// SyncResult tracks what happened during a sync cycle.
type SyncResult struct {
	Pushed    int
	Pulled    int
	Deleted   int
	Skipped   int
	Conflicts int
	Errors    []string
}

// Engine is the v2 three-way sync engine.
type Engine struct {
	Client    *api.Client
	SyncDir   string
	RootDir   string // remote root directory name (e.g. "root")
	Profile   string
	Verbose   bool
	Stability *StabilityTracker // nil = treat all files as stable
	Ignore    *syncv1.IgnoreRules
	log       func(format string, args ...interface{})
}

// NewEngine creates a v2 sync engine.
func NewEngine(client *api.Client, syncDir, profile string) *Engine {
	e := &Engine{
		Client:  client,
		SyncDir: syncDir,
		RootDir: "root",
		Profile: profile,
		Ignore:  syncv1.LoadIgnoreFile(syncDir),
	}
	e.log = func(format string, args ...interface{}) {
		if e.Verbose {
			fmt.Printf(format+"\n", args...)
		}
	}
	return e
}

// remoteFile is a flattened view of a remote file for comparison.
type remoteFile struct {
	ID          string
	Hash        string
	Size        int64
	Path        string // full remote path
	HasText     bool
	UpdatedAt   string
}

// Sync runs a full three-way sync cycle.
func (e *Engine) Sync() (*SyncResult, error) {
	result := &SyncResult{}

	// Load synced tree (the common ancestor)
	tree, err := LoadState(e.Profile)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	// Load conflict queue
	conflicts, _ := LoadConflicts(e.Profile)

	// 1. Build remote tree from manifest
	remoteFiles, remoteDirs, err := e.buildRemoteTree()
	if err != nil {
		return nil, fmt.Errorf("build remote tree: %w", err)
	}

	// 2. Build local tree by walking disk
	localFiles, err := e.buildLocalTree()
	if err != nil {
		return nil, fmt.Errorf("build local tree: %w", err)
	}

	// 3. Compute actions via three-way comparison
	actions := e.computeActions(localFiles, remoteFiles, tree)

	// 4. Execute actions
	for _, action := range actions {
		switch action.Type {
		case ActionSkip:
			result.Skipped++

		case ActionPush:
			if err := e.executePush(action, localFiles, remoteFiles, remoteDirs, tree); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("push %s: %v", action.Path, err))
			} else {
				e.log("  ⬆ %s", action.Path)
				result.Pushed++
			}

		case ActionPull:
			if err := e.executePull(action, remoteFiles, tree); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("pull %s: %v", action.Path, err))
			} else {
				e.log("  ⬇ %s", action.Path)
				result.Pulled++
			}

		case ActionDeleteLocal:
			localPath, pathErr := e.safePath(action.Path)
			if pathErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("delete local %s: %v", action.Path, pathErr))
				continue
			}
			if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
				result.Errors = append(result.Errors, fmt.Sprintf("delete local %s: %v", action.Path, err))
			} else {
				delete(tree.Files, action.Path)
				e.log("  🗑 %s (deleted on server)", action.Path)
				result.Deleted++
			}

		case ActionDeleteRemote:
			if action.RemoteID != "" {
				if err := e.Client.DeleteFile(action.RemoteID); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("delete remote %s: %v", action.Path, err))
				} else {
					delete(tree.Files, action.Path)
					e.log("  🗑 %s (deleted locally)", action.Path)
					result.Deleted++
				}
			} else {
				delete(tree.Files, action.Path)
				result.Deleted++
			}

		case ActionConflict:
			rf := remoteFiles[action.Path]
			lf := localFiles[action.Path]
			conflicts.Add(ConflictEntry{
				Path:       action.Path,
				LocalHash:  lf.hash,
				RemoteHash: rf.Hash,
				RemoteID:   rf.ID,
				DetectedAt: time.Now(),
			})
			e.log("  ⚠ %s (%s)", action.Path, action.Reason)
			result.Conflicts++
		}
	}

	// 5. Save state
	if err := SaveState(e.Profile, tree); err != nil {
		return result, fmt.Errorf("save state: %w", err)
	}
	if err := SaveConflicts(e.Profile, conflicts); err != nil {
		return result, fmt.Errorf("save conflicts: %w", err)
	}

	return result, nil
}

// SyncPull runs only the pull half — fetch remote changes and apply locally.
// Uses the cursor-based changes API for efficiency.
func (e *Engine) SyncPull() (*SyncResult, error) {
	result := &SyncResult{}

	tree, err := LoadState(e.Profile)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	conflicts, _ := LoadConflicts(e.Profile)

	changes, err := e.Client.GetChanges(tree.Cursor)
	if err != nil {
		return nil, fmt.Errorf("get changes: %w", err)
	}

	for _, change := range changes.Changes {
		if change.Type == "directory" {
			e.handleDirChange(change)
			continue
		}
		if change.Type != "file" {
			continue
		}

		relPath := e.remoteToLocal(change.Path)
		if relPath == "" {
			continue
		}
		// Block path traversal from server-supplied paths
		if _, pathErr := e.safePath(relPath); pathErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("skip %s: %v", relPath, pathErr))
			continue
		}
		if e.isIgnored(relPath, false) {
			continue
		}

		switch change.Action {
		case "created", "modified":
			// Check stability — don't overwrite files being edited
			if e.Stability != nil && !e.Stability.IsStable(filepath.Join(e.SyncDir, relPath)) {
				e.log("  ⏳ Skipping (not stable): %s", relPath)
				result.Skipped++
				continue
			}

			// Check for conflict: did local change since last sync?
			localPath := filepath.Join(e.SyncDir, relPath)
			if synced, tracked := tree.Files[relPath]; tracked {
				localHash, err := HashFile(localPath)
				if err == nil && localHash != synced.LocalHash {
					// Local changed too — is the remote content actually different?
					if change.ContentHash != "" && localHash == change.ContentHash {
						// Same content, no conflict
						tree.Files[relPath] = SyncedFile{
							RemoteID:   change.ID,
							LocalHash:  localHash,
							RemoteHash: change.ContentHash,
							Size:       change.Size,
						}
						result.Skipped++
						continue
					}
					// Genuine conflict
					conflicts.Add(ConflictEntry{
						Path:       relPath,
						LocalHash:  localHash,
						RemoteHash: change.ContentHash,
						RemoteID:   change.ID,
						DetectedAt: time.Now(),
					})
					e.log("  ⚠ Conflict: %s", relPath)
					result.Conflicts++
					continue
				}
			}

			// Safe to pull — download
			if err := e.downloadFile(change.ID, localPath); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("pull %s: %v", relPath, err))
				continue
			}

			// Update synced tree
			hash, _ := HashFile(localPath)
			info, _ := os.Stat(localPath)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			tree.Files[relPath] = SyncedFile{
				RemoteID:   change.ID,
				LocalHash:  hash,
				RemoteHash: change.ContentHash,
				Size:       size,
				IsNote:     filepath.Ext(change.Path) == "",
			}
			e.log("  ⬇ %s", relPath)
			result.Pulled++

		case "deleted":
			localPath := filepath.Join(e.SyncDir, relPath)
			if _, err := os.Stat(localPath); err == nil {
				// Check if local was modified
				if synced, tracked := tree.Files[relPath]; tracked {
					localHash, hashErr := HashFile(localPath)
					if hashErr == nil && localHash != synced.LocalHash {
						// Local was modified but remote deleted — conflict
						conflicts.Add(ConflictEntry{
							Path:       relPath,
							LocalHash:  localHash,
							RemoteHash: "",
							RemoteID:   change.ID,
							DetectedAt: time.Now(),
						})
						e.log("  ⚠ Conflict (deleted remotely, modified locally): %s", relPath)
						result.Conflicts++
						continue
					}
				}
				os.Remove(localPath)
				e.log("  🗑 %s", relPath)
				result.Deleted++
			}
			delete(tree.Files, relPath)
		}
	}

	// Update cursor
	tree.Cursor = changes.Cursor

	// Fetch more if needed
	if changes.HasMore {
		if err := SaveState(e.Profile, tree); err != nil {
			return result, fmt.Errorf("save state: %w", err)
		}
		moreResult, err := e.SyncPull()
		if err != nil {
			return result, err
		}
		result.Pulled += moreResult.Pulled
		result.Deleted += moreResult.Deleted
		result.Conflicts += moreResult.Conflicts
		result.Skipped += moreResult.Skipped
		result.Errors = append(result.Errors, moreResult.Errors...)
		return result, nil
	}

	if err := SaveState(e.Profile, tree); err != nil {
		return result, fmt.Errorf("save state: %w", err)
	}
	if err := SaveConflicts(e.Profile, conflicts); err != nil {
		return result, fmt.Errorf("save conflicts: %w", err)
	}

	return result, nil
}

// SyncPush scans for stable local changes and pushes them.
// Only pushes files that differ from the synced tree.
func (e *Engine) SyncPush() (*SyncResult, error) {
	result := &SyncResult{}

	tree, err := LoadState(e.Profile)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	// Get remote state for directory lookups
	rootID, remoteDirsByPath, err := e.initRootDir()
	if err != nil {
		return nil, fmt.Errorf("init root dir: %w", err)
	}
	_ = rootID

	// Build remote file index for conflict detection
	remoteFiles, _, err := e.buildRemoteTree()
	if err != nil {
		return nil, fmt.Errorf("build remote tree: %w", err)
	}

	conflicts, _ := LoadConflicts(e.Profile)

	// Walk local files
	localFiles, err := e.buildLocalTree()
	if err != nil {
		return nil, fmt.Errorf("build local tree: %w", err)
	}

	for relPath, lf := range localFiles {
		// Check stability
		if e.Stability != nil && !e.Stability.IsStable(filepath.Join(e.SyncDir, relPath)) {
			e.log("  ⏳ Skipping (not stable): %s", relPath)
			result.Skipped++
			continue
		}

		synced, tracked := tree.Files[relPath]

		if tracked && lf.hash == synced.LocalHash {
			// Local hasn't changed since last sync — skip
			result.Skipped++
			continue
		}

		// Local changed (or is new). Check remote state.
		rf, onRemote := remoteFiles[relPath]

		if tracked && onRemote {
			// File exists everywhere. Check if remote also changed.
			if rf.Hash != synced.RemoteHash {
				// Remote also changed — conflict
				conflicts.Add(ConflictEntry{
					Path:       relPath,
					LocalHash:  lf.hash,
					RemoteHash: rf.Hash,
					RemoteID:   rf.ID,
					DetectedAt: time.Now(),
				})
				e.log("  ⚠ Conflict: %s", relPath)
				result.Conflicts++
				continue
			}
			// Remote unchanged — safe to push update
			if err := e.uploadFile(relPath, lf, rf.ID, "", remoteDirsByPath, tree); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("push %s: %v", relPath, err))
			} else {
				e.log("  ⬆ %s (updated)", relPath)
				result.Pushed++
			}
		} else if !tracked && !onRemote {
			// New file — upload
			if err := e.uploadFile(relPath, lf, "", "", remoteDirsByPath, tree); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("push %s: %v", relPath, err))
			} else {
				e.log("  ⬆ %s (new)", relPath)
				result.Pushed++
			}
		} else if tracked && !onRemote {
			// Was tracked, now missing on remote — deleted on server while we edited
			conflicts.Add(ConflictEntry{
				Path:       relPath,
				LocalHash:  lf.hash,
				RemoteHash: "",
				RemoteID:   synced.RemoteID,
				DetectedAt: time.Now(),
			})
			e.log("  ⚠ Conflict (modified locally, deleted remotely): %s", relPath)
			result.Conflicts++
		} else {
			// !tracked && onRemote — new local file that already exists on remote somehow
			// Could be a name collision. Check content hash.
			if lf.hash == rf.Hash {
				// Same content, just track it
				tree.Files[relPath] = SyncedFile{
					RemoteID:   rf.ID,
					LocalHash:  lf.hash,
					RemoteHash: rf.Hash,
					Size:       lf.size,
				}
				result.Skipped++
			} else {
				// Different content, same name — conflict
				conflicts.Add(ConflictEntry{
					Path:       relPath,
					LocalHash:  lf.hash,
					RemoteHash: rf.Hash,
					RemoteID:   rf.ID,
					DetectedAt: time.Now(),
				})
				e.log("  ⚠ Conflict (name collision): %s", relPath)
				result.Conflicts++
			}
		}
	}

	// Detect local deletions: files in synced tree that are no longer on disk
	for relPath, synced := range tree.Files {
		if _, exists := localFiles[relPath]; exists {
			continue // still on disk
		}
		// File was deleted locally
		if synced.RemoteID != "" {
			// Check if remote also changed before deleting
			if rf, onRemote := remoteFiles[relPath]; onRemote {
				if rf.Hash != synced.RemoteHash {
					// Remote changed since last sync — don't delete, re-download instead
					e.log("  ⬇ %s (deleted locally but changed remotely — re-downloading)", relPath)
					localPath := filepath.Join(e.SyncDir, relPath)
					if err := e.downloadFile(rf.ID, localPath); err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("re-pull %s: %v", relPath, err))
					} else {
						hash, _ := HashFile(localPath)
						info, _ := os.Stat(localPath)
						sz := int64(0)
						if info != nil {
							sz = info.Size()
						}
						tree.Files[relPath] = SyncedFile{
							RemoteID:   rf.ID,
							LocalHash:  hash,
							RemoteHash: rf.Hash,
							Size:       sz,
						}
						result.Pulled++
					}
					continue
				}

				// Remote unchanged — safe to delete
				if err := e.Client.DeleteFile(synced.RemoteID); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("delete remote %s: %v", relPath, err))
					continue
				}
				e.log("  🗑 %s (deleted locally → deleted on server)", relPath)
				result.Deleted++
			}
		}
		delete(tree.Files, relPath)
	}

	if err := SaveState(e.Profile, tree); err != nil {
		return result, fmt.Errorf("save state: %w", err)
	}
	if err := SaveConflicts(e.Profile, conflicts); err != nil {
		return result, fmt.Errorf("save conflicts: %w", err)
	}

	return result, nil
}

// localFile holds computed info about a local file.
type localFile struct {
	hash string
	size int64
	path string // absolute path
}

// buildLocalTree walks the sync directory and hashes every file.
// Symlinks are skipped to prevent exfiltrating files outside the sync directory.
func (e *Engine) buildLocalTree() (map[string]localFile, error) {
	files := make(map[string]localFile)

	err := filepath.Walk(e.SyncDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := info.Name()

		// Skip hidden
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(e.SyncDir, path)
		if relPath == "." {
			return nil
		}

		// Skip symlinks — they could point outside the sync dir and exfiltrate data
		lstat, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return nil
		}
		if lstat.Mode()&os.ModeSymlink != 0 {
			e.log("  ⚠ Skipping symlink: %s", relPath)
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			if e.isIgnored(relPath, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if e.isIgnored(relPath, false) {
			return nil
		}

		// Skip temp/conflict files
		if strings.HasSuffix(name, ".izerop-tmp") || strings.Contains(name, ".conflict") {
			return nil
		}

		hash, err := HashFile(path)
		if err != nil {
			// Can't read file — use empty hash as sentinel so it's not
			// treated as "deleted" and removed from the server
			e.log("  ⚠ Cannot read %s: %v (skipping sync for this file)", relPath, err)
			files[relPath] = localFile{
				hash: "", // empty hash — won't match any remote hash, won't delete
				size: info.Size(),
				path: path,
			}
			return nil
		}

		files[relPath] = localFile{
			hash: hash,
			size: info.Size(),
			path: path,
		}
		return nil
	})

	return files, err
}

// buildRemoteTree fetches the full manifest and returns files indexed by relative path.
func (e *Engine) buildRemoteTree() (map[string]remoteFile, map[string]api.ManifestDir, error) {
	manifest, err := e.Client.GetManifest(e.RootDir)
	if err != nil {
		return nil, nil, err
	}

	rootPrefix := "/" + e.RootDir
	files := make(map[string]remoteFile)
	dirs := make(map[string]api.ManifestDir)

	for _, d := range manifest.Directories {
		dirs[d.Path] = d
	}

	for _, f := range manifest.Files {
		relPath := f.Path
		if strings.HasPrefix(relPath, rootPrefix+"/") {
			relPath = relPath[len(rootPrefix)+1:]
		}
		// Notes (no extension on server) get .txt locally
		if filepath.Ext(relPath) == "" {
			relPath = relPath + ".txt"
		}

		if e.isIgnored(relPath, false) {
			continue
		}

		files[relPath] = remoteFile{
			ID:        f.ID,
			Hash:      f.ContentHash,
			Size:      f.Size,
			Path:      f.Path,
			HasText:   f.HasText,
			UpdatedAt: f.UpdatedAt,
		}
	}

	return files, dirs, nil
}

// computeActions compares local, remote, and synced trees to produce a list of actions.
func (e *Engine) computeActions(
	local map[string]localFile,
	remote map[string]remoteFile,
	tree *SyncedTree,
) []Action {
	var actions []Action

	// Collect all known paths
	allPaths := make(map[string]bool)
	for p := range local {
		allPaths[p] = true
	}
	for p := range remote {
		allPaths[p] = true
	}
	for p := range tree.Files {
		allPaths[p] = true
	}

	for path := range allPaths {
		lf, hasLocal := local[path]
		rf, hasRemote := remote[path]
		sf, hasSynced := tree.Files[path]

		localChanged := hasLocal && (!hasSynced || lf.hash != sf.LocalHash)
		remoteChanged := hasRemote && (!hasSynced || rf.Hash != sf.RemoteHash)

		// Check stability
		if hasLocal && e.Stability != nil && !e.Stability.IsStable(filepath.Join(e.SyncDir, path)) {
			actions = append(actions, Action{Type: ActionSkip, Path: path, Reason: "not stable"})
			continue
		}

		switch {
		case hasLocal && hasRemote && hasSynced:
			// File exists everywhere
			if !localChanged && !remoteChanged {
				actions = append(actions, Action{Type: ActionSkip, Path: path})
			} else if localChanged && !remoteChanged {
				actions = append(actions, Action{Type: ActionPush, Path: path, RemoteID: rf.ID, Reason: "local updated"})
			} else if !localChanged && remoteChanged {
				actions = append(actions, Action{Type: ActionPull, Path: path, RemoteID: rf.ID, Reason: "remote updated"})
			} else {
				// Both changed — check if they converged to the same content
				if lf.hash == rf.Hash {
					// Same content, update synced tree and skip
					tree.Files[path] = SyncedFile{
						RemoteID:   rf.ID,
						LocalHash:  lf.hash,
						RemoteHash: rf.Hash,
						Size:       lf.size,
					}
					actions = append(actions, Action{Type: ActionSkip, Path: path, Reason: "converged"})
				} else {
					actions = append(actions, Action{Type: ActionConflict, Path: path, RemoteID: rf.ID, Reason: "both sides changed"})
				}
			}

		case hasLocal && hasRemote && !hasSynced:
			// File exists on both sides but never synced — check content
			if lf.hash == rf.Hash {
				// Same content, just record it
				tree.Files[path] = SyncedFile{
					RemoteID:   rf.ID,
					LocalHash:  lf.hash,
					RemoteHash: rf.Hash,
					Size:       lf.size,
				}
				actions = append(actions, Action{Type: ActionSkip, Path: path, Reason: "already identical"})
			} else {
				actions = append(actions, Action{Type: ActionConflict, Path: path, RemoteID: rf.ID, Reason: "name collision, different content"})
			}

		case hasLocal && !hasRemote && !hasSynced:
			// New local file, not on remote → push
			actions = append(actions, Action{Type: ActionPush, Path: path, Reason: "new local file"})

		case hasLocal && !hasRemote && hasSynced:
			// Was synced, now gone from remote
			if localChanged {
				actions = append(actions, Action{Type: ActionConflict, Path: path, RemoteID: sf.RemoteID, Reason: "modified locally, deleted remotely"})
			} else {
				actions = append(actions, Action{Type: ActionDeleteLocal, Path: path, Reason: "deleted on server"})
			}

		case !hasLocal && hasRemote && !hasSynced:
			// New remote file, not local → pull
			actions = append(actions, Action{Type: ActionPull, Path: path, RemoteID: rf.ID, Reason: "new remote file"})

		case !hasLocal && hasRemote && hasSynced:
			// Was synced, now gone locally
			if remoteChanged {
				// Remote updated after we deleted locally — re-download
				actions = append(actions, Action{Type: ActionPull, Path: path, RemoteID: rf.ID, Reason: "deleted locally but updated remotely"})
			} else {
				actions = append(actions, Action{Type: ActionDeleteRemote, Path: path, RemoteID: sf.RemoteID, Reason: "deleted locally"})
			}

		case !hasLocal && !hasRemote && hasSynced:
			// Deleted on both sides — clean up synced tree
			delete(tree.Files, path)
			actions = append(actions, Action{Type: ActionSkip, Path: path, Reason: "deleted everywhere"})
		}
	}

	return actions
}

// executePush uploads a local file to the server and updates the synced tree.
func (e *Engine) executePush(action Action, local map[string]localFile, remote map[string]remoteFile, remoteDirs map[string]api.ManifestDir, tree *SyncedTree) error {
	lf := local[action.Path]
	rf, onRemote := remote[action.Path]

	if onRemote {
		return e.uploadFile(action.Path, lf, rf.ID, "", nil, tree)
	}

	// New file — need to find/create the parent directory
	_, remoteDirsByPath, err := e.initRootDir()
	if err != nil {
		return fmt.Errorf("init root dir: %w", err)
	}
	return e.uploadFile(action.Path, lf, "", "", remoteDirsByPath, tree)
}

// safePath validates that a relative path doesn't escape the sync directory.
// Returns the cleaned absolute path or an error if it would escape.
func (e *Engine) safePath(relPath string) (string, error) {
	// Clean the path to resolve any .. components
	cleaned := filepath.Clean(relPath)

	// Reject paths that start with .. or are absolute
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path traversal blocked: %q resolves outside sync directory", relPath)
	}

	absPath := filepath.Join(e.SyncDir, cleaned)

	// Double-check: resolved path must be inside sync dir
	if !strings.HasPrefix(absPath, e.SyncDir+string(filepath.Separator)) && absPath != e.SyncDir {
		return "", fmt.Errorf("path traversal blocked: %q resolves to %q (outside %q)", relPath, absPath, e.SyncDir)
	}

	return absPath, nil
}

// executePull downloads a remote file and updates the synced tree.
func (e *Engine) executePull(action Action, remote map[string]remoteFile, tree *SyncedTree) error {
	rf, ok := remote[action.Path]
	if !ok {
		return fmt.Errorf("file not in remote tree")
	}

	localPath, err := e.safePath(action.Path)
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(localPath), 0755)

	if err := e.downloadFile(rf.ID, localPath); err != nil {
		return err
	}

	hash, _ := HashFile(localPath)
	info, _ := os.Stat(localPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	tree.Files[action.Path] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: rf.Hash,
		Size:       size,
		IsNote:     filepath.Ext(rf.Path) == "",
	}
	return nil
}

// downloadFile atomically downloads a file to the local path.
func (e *Engine) downloadFile(remoteID, localPath string) error {
	os.MkdirAll(filepath.Dir(localPath), 0755)
	tmpPath := localPath + ".izerop-tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	_, err = e.Client.DownloadFile(remoteID, f)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("download: %w", err)
	}

	if err := os.Rename(tmpPath, localPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// uploadFile pushes a local file to the server and updates the synced tree.
func (e *Engine) uploadFile(relPath string, lf localFile, existingID string, dirID string, remoteDirsByPath map[string]api.Directory, tree *SyncedTree) error {
	localPath := filepath.Join(e.SyncDir, relPath)

	if existingID != "" {
		// Update existing file — use If-Match for optimistic concurrency
		// Look up the expected remote hash from the synced tree
		expectedHash := ""
		if sf, ok := tree.Files[relPath]; ok {
			expectedHash = sf.RemoteHash
		}

		if isTextFile(localPath) {
			contents, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			updated, err := e.Client.UpdateFileWithETag(existingID, map[string]string{
				"contents": string(contents),
			}, expectedHash)
			if err != nil {
				var conflictErr *api.ErrConflict
				if errors.As(err, &conflictErr) {
					return fmt.Errorf("conflict: server hash changed to %s (use 'izerop conflicts' to resolve)", conflictErr.CurrentHash)
				}
				return err
			}
			tree.Files[relPath] = SyncedFile{
				RemoteID:   updated.ID,
				LocalHash:  lf.hash,
				RemoteHash: updated.ContentHash,
				Size:       lf.size,
			}
			return nil
		}
		// Binary update — re-upload (API doesn't support binary PATCH, delete + create)
		if err := e.Client.DeleteFile(existingID); err != nil {
			return fmt.Errorf("delete before re-upload: %w", err)
		}
		existingID = "" // fall through to create
	}

	// New file — find directory
	if dirID == "" && remoteDirsByPath != nil {
		remotePath := "/" + e.RootDir + "/" + filepath.ToSlash(filepath.Dir(relPath))
		if remotePath == "/"+e.RootDir+"/." {
			remotePath = "/" + e.RootDir
		}
		if dir, ok := remoteDirsByPath[remotePath]; ok {
			dirID = dir.ID
		} else {
			// Create parent directories as needed
			var err error
			dirID, err = e.ensureRemoteDir(filepath.Dir(relPath), remoteDirsByPath)
			if err != nil {
				return fmt.Errorf("ensure dir: %w", err)
			}
		}
	}

	if dirID == "" {
		// Last resort: get root dir ID
		rootID, _, err := e.initRootDir()
		if err != nil {
			return fmt.Errorf("init root: %w", err)
		}
		dirID = rootID
	}

	name := filepath.Base(relPath)

	// If this file was tracked as a note (no extension on server),
	// strip the .txt we added locally so it doesn't create a duplicate
	if sf, ok := tree.Files[relPath]; ok && sf.IsNote {
		name = strings.TrimSuffix(name, ".txt")
	}

	if isTextFile(localPath) {
		contents, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		created, err := e.Client.CreateTextFile(name, string(contents), dirID, "")
		if err != nil {
			return err
		}
		remoteHash := ""
		remoteID := ""
		if created != nil {
			remoteID = created.ID
			remoteHash = created.ContentHash
		}
		tree.Files[relPath] = SyncedFile{
			RemoteID:   remoteID,
			LocalHash:  lf.hash,
			RemoteHash: remoteHash,
			Size:       lf.size,
		}
		return nil
	}

	uploaded, err := e.Client.UploadFile(localPath, dirID, name)
	if err != nil {
		return err
	}
	remoteHash := ""
	remoteID := ""
	if uploaded != nil {
		remoteID = uploaded.ID
		remoteHash = uploaded.ContentHash
	}
	tree.Files[relPath] = SyncedFile{
		RemoteID:   remoteID,
		LocalHash:  lf.hash,
		RemoteHash: remoteHash,
		Size:       lf.size,
	}
	return nil
}

// ensureRemoteDir creates parent directories on the server as needed.
func (e *Engine) ensureRemoteDir(relDir string, remoteDirsByPath map[string]api.Directory) (string, error) {
	relDir = filepath.ToSlash(relDir)
	if relDir == "." || relDir == "" {
		rootID, _, err := e.initRootDir()
		return rootID, err
	}

	remotePath := "/" + e.RootDir + "/" + relDir
	if dir, ok := remoteDirsByPath[remotePath]; ok {
		return dir.ID, nil
	}

	// Ensure parent exists first
	parentRel := filepath.Dir(relDir)
	parentID, err := e.ensureRemoteDir(parentRel, remoteDirsByPath)
	if err != nil {
		return "", err
	}

	name := filepath.Base(relDir)
	dir, err := e.Client.CreateDirectory(name, parentID)
	if err != nil {
		return "", err
	}
	remoteDirsByPath[remotePath] = api.Directory{
		ID:   dir.ID,
		Name: dir.Name,
		Path: dir.Path,
	}
	return dir.ID, nil
}

// initRootDir discovers or creates the sync root directory on the server.
func (e *Engine) initRootDir() (string, map[string]api.Directory, error) {
	dirs, err := e.Client.ListDirectories()
	if err != nil {
		return "", nil, err
	}

	remoteDirsByPath := make(map[string]api.Directory)
	for _, d := range dirs {
		remoteDirsByPath[d.Path] = d
	}

	rootPath := "/" + e.RootDir
	if rootDir, exists := remoteDirsByPath[rootPath]; exists {
		return rootDir.ID, remoteDirsByPath, nil
	}

	dir, err := e.Client.CreateDirectory(e.RootDir, "")
	if err != nil {
		return "", nil, fmt.Errorf("create sync dir %q: %w", e.RootDir, err)
	}
	remoteDirsByPath[rootPath] = *dir
	return dir.ID, remoteDirsByPath, nil
}

// handleDirChange processes a directory change from the changes API.
func (e *Engine) handleDirChange(change api.Change) {
	relPath := e.remoteToLocal(change.Path)
	if relPath == "" {
		return
	}
	localPath, err := e.safePath(relPath)
	if err != nil {
		return // silently skip path-traversal directory changes
	}

	switch change.Action {
	case "created", "modified":
		os.MkdirAll(localPath, 0755)
	case "deleted":
		entries, _ := os.ReadDir(localPath)
		if len(entries) == 0 {
			os.Remove(localPath)
		}
	}
}

// remoteToLocal converts a remote path to a local relative path.
func (e *Engine) remoteToLocal(remotePath string) string {
	prefix := "/" + e.RootDir
	if strings.HasPrefix(remotePath, prefix+"/") {
		rel := remotePath[len(prefix)+1:]
		// Notes get .txt extension locally
		if filepath.Ext(rel) == "" {
			rel = rel + ".txt"
		}
		return rel
	}
	if remotePath == prefix {
		return ""
	}
	if strings.HasPrefix(remotePath, "/") {
		return remotePath[1:]
	}
	return remotePath
}

// isIgnored checks if a path should be skipped.
func (e *Engine) isIgnored(relPath string, isDir bool) bool {
	if e.Ignore == nil {
		return false
	}
	return e.Ignore.IsIgnored(relPath, isDir)
}

// isTextFile checks if a file should be treated as text for API purposes.
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return true
	}
	textExts := map[string]bool{
		".txt": true, ".md": true, ".json": true, ".yml": true,
		".yaml": true, ".xml": true, ".html": true, ".css": true,
		".js": true, ".ts": true, ".rb": true, ".py": true,
		".go": true, ".sh": true, ".bash": true, ".toml": true,
		".csv": true, ".log": true, ".env": true, ".conf": true,
		".cfg": true, ".ini": true, ".sql": true, ".svg": true,
	}
	return textExts[ext]
}

// ResolveConflict resolves a conflict by keeping either the local or remote version.
func (e *Engine) ResolveConflict(entry ConflictEntry, keepLocal bool) error {
	tree, err := LoadState(e.Profile)
	if err != nil {
		return err
	}

	localPath := filepath.Join(e.SyncDir, entry.Path)

	if keepLocal {
		// Push local version to server, overwriting remote
		lf := localFile{hash: entry.LocalHash}
		info, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("local file missing: %w", err)
		}
		lf.size = info.Size()
		lf.path = localPath

		if entry.RemoteID != "" {
			return e.uploadFile(entry.Path, lf, entry.RemoteID, "", nil, tree)
		}
		// File was deleted remotely — re-upload as new
		_, remoteDirsByPath, err := e.initRootDir()
		if err != nil {
			return err
		}
		if err := e.uploadFile(entry.Path, lf, "", "", remoteDirsByPath, tree); err != nil {
			return err
		}
	} else {
		// Pull remote version, overwriting local
		if entry.RemoteID == "" || entry.RemoteHash == "" {
			// Remote was deleted — delete local too
			os.Remove(localPath)
			delete(tree.Files, entry.Path)
		} else {
			if err := e.downloadFile(entry.RemoteID, localPath); err != nil {
				return err
			}
			hash, _ := HashFile(localPath)
			info, _ := os.Stat(localPath)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			tree.Files[entry.Path] = SyncedFile{
				RemoteID:   entry.RemoteID,
				LocalHash:  hash,
				RemoteHash: entry.RemoteHash,
				Size:       size,
			}
		}
	}

	return SaveState(e.Profile, tree)
}
