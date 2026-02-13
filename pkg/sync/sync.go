package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/api"
)

// Engine handles file synchronization between local and remote.
type Engine struct {
	Client  *api.Client
	SyncDir string
	Verbose bool
	// RootDir is the name of the remote root directory (e.g. "root").
	RootDir string
	// State tracks notes and cursor between syncs.
	State  *State
	// Ignore holds the parsed .izeropignore rules.
	Ignore *IgnoreRules
}

// NewEngine creates a sync engine.
func NewEngine(client *api.Client, syncDir string, state *State) *Engine {
	if state.Notes == nil {
		state.Notes = make(map[string]string)
	}
	return &Engine{
		Client:  client,
		SyncDir: syncDir,
		RootDir: "root",
		State:   state,
		Ignore:  LoadIgnoreFile(syncDir),
	}
}

// SyncResult tracks what happened during a sync.
type SyncResult struct {
	Downloaded int
	Uploaded   int
	Deleted    int
	Skipped    int
	Conflicts  int
	Errors     []string
}

// remoteToLocal converts a remote path to a local path.
// Strips the sync directory prefix so /sync/foo.txt → foo.txt
func (e *Engine) remoteToLocal(remotePath string) string {
	prefix := "/" + e.RootDir
	if strings.HasPrefix(remotePath, prefix+"/") {
		return remotePath[len(prefix)+1:]
	}
	if strings.HasPrefix(remotePath, prefix) && len(remotePath) == len(prefix) {
		return ""
	}
	// For paths not under the sync dir, strip leading slash
	if strings.HasPrefix(remotePath, "/") {
		return remotePath[1:]
	}
	return remotePath
}

// localToRemote converts a local relative path to a remote path.
func (e *Engine) localToRemote(localRel string) string {
	return "/" + e.RootDir + "/" + filepath.ToSlash(localRel)
}

// initRootDir discovers or creates the sync root directory on the server.
// Returns the directory ID.
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

	// Create the sync root directory
	dir, err := e.Client.CreateDirectory(e.RootDir, "")
	if err != nil {
		return "", nil, fmt.Errorf("could not create sync directory %q: %w", e.RootDir, err)
	}
	remoteDirsByPath[rootPath] = *dir
	return dir.ID, remoteDirsByPath, nil
}

// PullSync downloads remote changes to the local sync directory.
func (e *Engine) PullSync(cursor string) (*SyncResult, string, error) {
	result := &SyncResult{}

	changes, err := e.Client.GetChanges(cursor)
	if err != nil {
		return nil, cursor, fmt.Errorf("could not fetch changes: %w", err)
	}

	for _, change := range changes.Changes {
		switch change.Type {
		case "directory":
			e.handleDirectoryChange(change, result)
		case "file":
			e.handleFileChange(change, result)
		}
	}

	// If there are more changes, keep fetching
	if changes.HasMore {
		moreResult, newCursor, err := e.PullSync(changes.Cursor)
		if err != nil {
			return result, changes.Cursor, err
		}
		result.Downloaded += moreResult.Downloaded
		result.Deleted += moreResult.Deleted
		result.Skipped += moreResult.Skipped
		result.Errors = append(result.Errors, moreResult.Errors...)
		return result, newCursor, nil
	}

	return result, changes.Cursor, nil
}

// PushSync scans the local sync directory and uploads new/changed files.
func (e *Engine) PushSync() (*SyncResult, error) {
	result := &SyncResult{}

	// Get remote state — directories
	rootID, remoteDirsByPath, err := e.initRootDir()
	if err != nil {
		return nil, fmt.Errorf("could not init sync directory: %w", err)
	}
	rootDir := remoteDirsByPath["/"+e.RootDir]
	_ = rootID

	// Get remote files under the sync root, indexed by path
	remoteFilesByPath := make(map[string]api.FileEntry)
	rootPrefix := "/" + e.RootDir
	for path, dir := range remoteDirsByPath {
		if path == rootPrefix || strings.HasPrefix(path, rootPrefix+"/") {
			files, err := e.Client.ListFiles(dir.ID)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("list files in %s: %v", path, err))
				continue
			}
			for _, f := range files {
				remoteFilesByPath[f.Path] = f
			}
		}
	}

	// Walk local directory
	err = filepath.Walk(e.SyncDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("walk error: %s: %v", path, walkErr))
			return nil
		}

		// Skip hidden files/dirs
		if strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(e.SyncDir, path)
		if relPath == "." {
			return nil
		}

		// Check ignore rules
		if e.Ignore.IsIgnored(relPath, info.IsDir()) {
			if e.Verbose {
				fmt.Printf("  ⏭ Ignored: %s\n", relPath)
			}
			if info.IsDir() {
				return filepath.SkipDir
			}
			result.Skipped++
			return nil
		}

		// Build the remote path (under root dir)
		remotePath := e.localToRemote(relPath)

		if info.IsDir() {
			if _, exists := remoteDirsByPath[remotePath]; !exists {
				// Find parent directory ID
				parentRemotePath := filepath.Dir(remotePath)
				parentRemotePath = filepath.ToSlash(parentRemotePath)
				parentID := ""
				if parent, ok := remoteDirsByPath[parentRemotePath]; ok {
					parentID = parent.ID
				} else {
					// Parent is root
					parentID = rootDir.ID
				}

				if e.Verbose {
					fmt.Printf("  📁 Creating: %s\n", remotePath)
				}
				dir, createErr := e.Client.CreateDirectory(info.Name(), parentID)
				if createErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("mkdir %s: %v", remotePath, createErr))
				} else {
					remoteDirsByPath[remotePath] = *dir
				}
			}
			return nil
		}

		// Check if this is a tracked note file
		if noteID, isNote := e.State.Notes[relPath]; isNote {
			// This is a note — use text API to update
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", relPath, readErr))
				return nil
			}

			// Check the remote version — build the remote path without .txt
			noteRemotePath := remotePath
			if strings.HasSuffix(noteRemotePath, ".txt") {
				noteRemotePath = strings.TrimSuffix(noteRemotePath, ".txt")
			}

			if remoteFile, exists := remoteFilesByPath[noteRemotePath]; exists {
				if remoteFile.Size == info.Size() {
					result.Skipped++
					return nil
				}
			}

			if e.Verbose {
				fmt.Printf("  📝 Updating note: %s\n", relPath)
			}
			_, updateErr := e.Client.UpdateFile(noteID, map[string]string{
				"contents": string(contents),
			})
			if updateErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("update note %s: %v", relPath, updateErr))
			} else {
				noteHash, _ := HashFile(path)
				e.State.Files[relPath] = FileRecord{
					RemoteID: noteID,
					Size:     info.Size(),
					Hash:     noteHash,
					LocalMod: info.ModTime().Unix(),
				}
				result.Uploaded++
			}
			return nil
		}

		// Skip conflict files
		if strings.Contains(info.Name(), ".conflict") {
			result.Skipped++
			return nil
		}

		// It's a regular file — check if it needs uploading
		remoteFile, exists := remoteFilesByPath[remotePath]
		if exists {
			// Use content hash for reliable comparison (size alone causes false positives)
			localHash, hashErr := HashFile(path)
			if hashErr == nil {
				if rec, tracked := e.State.Files[relPath]; tracked && rec.Hash != "" && rec.Hash == localHash && rec.RemoteTime == remoteFile.UpdatedAt {
					// Hash matches what we last synced AND remote hasn't changed — skip
					result.Skipped++
					return nil
				}
			}

			if remoteFile.Size == info.Size() && localHash != "" {
				if rec, tracked := e.State.Files[relPath]; tracked && rec.Hash == localHash {
					// Same hash as last sync, same size — remote metadata might differ but content is same
					e.State.Files[relPath] = FileRecord{
						RemoteID:   remoteFile.ID,
						Size:       info.Size(),
						Hash:       localHash,
						RemoteTime: remoteFile.UpdatedAt,
						LocalMod:   info.ModTime().Unix(),
					}
					result.Skipped++
					return nil
				}
			}

			// File exists but size differs — check for conflict
			if rec, tracked := e.State.Files[relPath]; tracked {
				// Both changed if remote updated_at differs from what we last saw
				if rec.RemoteTime != "" && rec.RemoteTime != remoteFile.UpdatedAt {
					// Remote also changed — conflict
					ext := filepath.Ext(path)
					base := strings.TrimSuffix(path, ext)
					conflictPath := fmt.Sprintf("%s.conflict%s", base, ext)
					if ext == "" {
						conflictPath = path + ".conflict"
					}

					// Download remote version as conflict file
					cf, createErr := os.Create(conflictPath)
					if createErr == nil {
						_, dlErr := e.Client.DownloadFile(remoteFile.ID, cf)
						cf.Close()
						if dlErr != nil {
							os.Remove(conflictPath)
							result.Errors = append(result.Errors, fmt.Sprintf("conflict download %s: %v", relPath, dlErr))
						} else if e.Verbose {
							fmt.Printf("  ⚠ Conflict: %s (remote saved as %s)\n", relPath, filepath.Base(conflictPath))
						}
					}
					result.Conflicts++
					// Still push local version as the winner
				}
			}

			// File exists but size differs — update it
			if remoteFile.HasText {
				// Text file on server: read local contents and update via API
				contents, readErr := os.ReadFile(path)
				if readErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", relPath, readErr))
					return nil
				}
				if e.Verbose {
					fmt.Printf("  📝 Updating text: %s\n", relPath)
				}
				_, updateErr := e.Client.UpdateFile(remoteFile.ID, map[string]string{
					"contents": string(contents),
				})
				if updateErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("update %s: %v", relPath, updateErr))
				} else {
					h, _ := HashFile(path)
					e.State.Files[relPath] = FileRecord{
						RemoteID:   remoteFile.ID,
						Size:       info.Size(),
						Hash:       h,
						RemoteTime: remoteFile.UpdatedAt,
						LocalMod:   info.ModTime().Unix(),
					}
					result.Uploaded++
				}
				return nil
			}
		}

		// Find the directory ID for this file
		dirRemotePath := filepath.ToSlash(filepath.Dir(remotePath))
		dirID := ""
		if dir, ok := remoteDirsByPath[dirRemotePath]; ok {
			dirID = dir.ID
		}

		if dirID == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("no remote directory for %s (dir: %s)", remotePath, dirRemotePath))
			return nil
		}

		// Decide: text file or binary upload?
		if isTextFile(path, info) {
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", relPath, readErr))
				return nil
			}
			if e.Verbose {
				fmt.Printf("  📝 Creating text: %s\n", relPath)
			}
			created, createErr := e.Client.CreateTextFile(info.Name(), string(contents), dirID, "")
			if createErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("create text %s: %v", relPath, createErr))
			} else {
				h, _ := HashFile(path)
				rid := ""
				if created != nil {
					rid = created.ID
				}
				e.State.Files[relPath] = FileRecord{
					RemoteID: rid,
					Size:     info.Size(),
					Hash:     h,
					LocalMod: info.ModTime().Unix(),
				}
				result.Uploaded++
			}
		} else {
			if e.Verbose {
				fmt.Printf("  ⬆ Uploading: %s\n", relPath)
			}
			uploaded, uploadErr := e.Client.UploadFile(path, dirID, info.Name())
			if uploadErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("upload %s: %v", relPath, uploadErr))
			} else {
				h, _ := HashFile(path)
				rid := ""
				if uploaded != nil {
					rid = uploaded.ID
				}
				e.State.Files[relPath] = FileRecord{
					RemoteID: rid,
					Size:     info.Size(),
					Hash:     h,
					LocalMod: info.ModTime().Unix(),
				}
				result.Uploaded++
			}
		}

		return nil
	})

	if err != nil {
		return result, fmt.Errorf("walk failed: %w", err)
	}

	// Detect local deletions: tracked files that no longer exist on disk
	// If a file is in State.Files but missing locally, the user deleted it — propagate to server
	for relPath, rec := range e.State.Files {
		localPath := filepath.Join(e.SyncDir, relPath)
		if _, statErr := os.Stat(localPath); os.IsNotExist(statErr) {
			if rec.RemoteID == "" {
				// No remote ID tracked, just clean up state
				delete(e.State.Files, relPath)
				continue
			}
			if e.Verbose {
				fmt.Printf("  🗑 Deleting (local removed): %s\n", relPath)
			}
			if delErr := e.Client.DeleteFile(rec.RemoteID); delErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", relPath, delErr))
			} else {
				result.Deleted++
			}
			delete(e.State.Files, relPath)
		}
	}

	// Same for tracked notes
	for relPath, noteID := range e.State.Notes {
		localPath := filepath.Join(e.SyncDir, relPath)
		if _, statErr := os.Stat(localPath); os.IsNotExist(statErr) {
			if e.Verbose {
				fmt.Printf("  🗑 Deleting note (local removed): %s\n", relPath)
			}
			if delErr := e.Client.DeleteFile(noteID); delErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("delete note %s: %v", relPath, delErr))
			} else {
				result.Deleted++
			}
			delete(e.State.Notes, relPath)
			// Also clean from Files if tracked there
			delete(e.State.Files, relPath)
		}
	}

	return result, nil
}

// isTextFile determines if a file should be treated as a text file.
// Files without extensions or with known text extensions are text files.
func isTextFile(path string, info os.FileInfo) bool {
	ext := strings.ToLower(filepath.Ext(info.Name()))

	// No extension = text file
	if ext == "" {
		return true
	}

	// Known text extensions
	textExts := map[string]bool{
		".txt": true, ".md": true, ".json": true, ".yml": true,
		".yaml": true, ".xml": true, ".html": true, ".css": true,
		".js": true, ".ts": true, ".rb": true, ".py": true,
		".go": true, ".sh": true, ".bash": true, ".toml": true,
		".csv": true, ".log": true, ".env": true, ".conf": true,
		".cfg": true, ".ini": true, ".sql": true, ".svg": true,
	}

	if textExts[ext] {
		return true
	}

	// Small files without binary content are likely text
	if info.Size() < 1024*100 { // < 100KB
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		// Check for null bytes (binary indicator)
		for _, b := range data {
			if b == 0 {
				return false
			}
		}
		return true
	}

	return false
}

func (e *Engine) handleDirectoryChange(change api.Change, result *SyncResult) {
	localRel := e.remoteToLocal(change.Path)
	if localRel == "" {
		return // root dir itself, skip
	}
	if e.Ignore.IsIgnored(localRel, true) {
		return
	}
	localPath := filepath.Join(e.SyncDir, localRel)

	switch change.Action {
	case "created", "modified":
		if err := os.MkdirAll(localPath, 0755); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mkdir %s: %v", localPath, err))
		}
	case "deleted":
		entries, _ := os.ReadDir(localPath)
		if len(entries) == 0 {
			os.Remove(localPath)
			result.Deleted++
		}
	}
}

func (e *Engine) handleFileChange(change api.Change, result *SyncResult) {
	localRel := e.remoteToLocal(change.Path)
	if localRel == "" {
		return
	}

	// If the file has no extension, it's a note — add .txt locally
	isNote := filepath.Ext(localRel) == ""
	if isNote {
		localRel = localRel + ".txt"
	}

	// Check ignore rules
	if e.Ignore.IsIgnored(localRel, false) {
		result.Skipped++
		return
	}

	localPath := filepath.Join(e.SyncDir, localRel)

	switch change.Action {
	case "created", "modified":
		// Ensure parent directory exists
		os.MkdirAll(filepath.Dir(localPath), 0755)

		// Skip files actively being edited (modified in last 30 seconds)
		if info, statErr := os.Stat(localPath); statErr == nil {
			secsSinceMod := time.Now().Unix() - info.ModTime().Unix()
			if secsSinceMod < 30 {
				if e.Verbose {
					fmt.Printf("  ⏳ Skipping (actively edited): %s\n", localRel)
				}
				result.Skipped++
				return
			}
		}

		// Conflict detection: if local file exists and has changed since last sync
		if info, statErr := os.Stat(localPath); statErr == nil {
			if rec, tracked := e.State.Files[localRel]; tracked {
				// File was previously synced — check if local modified it
				localModTime := info.ModTime().Unix()
				if localModTime != rec.LocalMod || info.Size() != rec.Size {
					// Local changed too — conflict!
					ext := filepath.Ext(localPath)
					base := strings.TrimSuffix(localPath, ext)
					conflictPath := fmt.Sprintf("%s.conflict%s", base, ext)
					if ext == "" {
						conflictPath = localPath + ".conflict"
					}

					// Copy current local to conflict file
					if copyErr := copyFile(localPath, conflictPath); copyErr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("conflict backup %s: %v", localRel, copyErr))
					} else if e.Verbose {
						fmt.Printf("  ⚠ Conflict: %s (local saved as %s)\n", localRel, filepath.Base(conflictPath))
					}
					result.Conflicts++
				}
			}
		}

		// Atomic write: download to temp file, then rename to avoid partial reads
		tmpPath := localPath + ".izerop-tmp"
		f, err := os.Create(tmpPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("create %s: %v", localPath, err))
			return
		}

		_, err = e.Client.DownloadFile(change.ID, f)
		f.Close()
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("download %s: %v", change.Path, err))
			os.Remove(tmpPath)
			return
		}

		if err := os.Rename(tmpPath, localPath); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("rename %s: %v", localPath, err))
			os.Remove(tmpPath)
			return
		}

		// Track notes in state
		if isNote {
			e.State.Notes[localRel] = change.ID
		}

		// Update file record with content hash
		if newInfo, statErr := os.Stat(localPath); statErr == nil {
			hash, _ := HashFile(localPath)
			e.State.Files[localRel] = FileRecord{
				RemoteID:   change.ID,
				Size:       newInfo.Size(),
				Hash:       hash,
				RemoteTime: change.UpdatedAt,
				LocalMod:   newInfo.ModTime().Unix(),
			}
		}

		if e.Verbose {
			label := "⬇"
			if isNote {
				label = "📝"
			}
			fmt.Printf("  %s %s\n", label, localRel)
		}
		result.Downloaded++

	case "deleted":
		if _, err := os.Stat(localPath); err == nil {
			os.Remove(localPath)
			delete(e.State.Notes, localRel)
			if e.Verbose {
				fmt.Printf("  🗑 %s\n", localRel)
			}
			result.Deleted++
		}
	}
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	_, err = io.Copy(d, s)
	return err
}

// HashFile computes SHA256 of a local file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
