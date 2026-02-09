package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/patricksimpson/izerop-cli/pkg/api"
)

// Engine handles file synchronization between local and remote.
type Engine struct {
	Client  *api.Client
	SyncDir string
	Verbose bool
	// RootDir is the name of the remote root directory (e.g. "root").
	// Remote paths under this dir map directly to the local sync dir.
	RootDir string
	// State tracks notes and cursor between syncs.
	State *State
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
	}
}

// SyncResult tracks what happened during a sync.
type SyncResult struct {
	Downloaded int
	Uploaded   int
	Deleted    int
	Skipped    int
	Errors     []string
}

// remoteToLocal converts a remote path to a local path.
// Strips the root directory prefix so /root/foo.txt → foo.txt
// and /root/gallery/pic.jpg → gallery/pic.jpg
func (e *Engine) remoteToLocal(remotePath string) string {
	prefix := "/" + e.RootDir
	if strings.HasPrefix(remotePath, prefix+"/") {
		return remotePath[len(prefix)+1:]
	}
	if strings.HasPrefix(remotePath, prefix) && len(remotePath) == len(prefix) {
		return ""
	}
	// For paths not under root (e.g. /photos/), keep as-is minus leading slash
	if strings.HasPrefix(remotePath, "/") {
		return remotePath[1:]
	}
	return remotePath
}

// localToRemote converts a local relative path to a remote path.
// Prepends the root directory so foo.txt → /root/foo.txt
func (e *Engine) localToRemote(localRel string) string {
	return "/" + e.RootDir + "/" + filepath.ToSlash(localRel)
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
	dirs, err := e.Client.ListDirectories()
	if err != nil {
		return nil, fmt.Errorf("could not list remote directories: %w", err)
	}

	// Build remote dir index by path
	remoteDirsByPath := make(map[string]api.Directory)
	for _, d := range dirs {
		remoteDirsByPath[d.Path] = d
	}

	// Find or verify root directory exists
	rootPath := "/" + e.RootDir
	rootDir, rootExists := remoteDirsByPath[rootPath]
	if !rootExists {
		// Create root directory
		dir, err := e.Client.CreateDirectory(e.RootDir, "")
		if err != nil {
			return nil, fmt.Errorf("could not create root directory: %w", err)
		}
		rootDir = *dir
		remoteDirsByPath[rootPath] = rootDir
	}

	// Get ALL remote files indexed by path
	remoteFilesByPath := make(map[string]api.FileEntry)
	files, err := e.Client.ListFiles("")
	if err != nil {
		return nil, fmt.Errorf("could not list remote files: %w", err)
	}
	for _, f := range files {
		remoteFilesByPath[f.Path] = f
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
				result.Uploaded++
			}
			return nil
		}

		// It's a regular file — check if it needs uploading
		remoteFile, exists := remoteFilesByPath[remotePath]
		if exists {
			if remoteFile.Size == info.Size() {
				result.Skipped++
				return nil
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
			_, createErr := e.Client.CreateTextFile(info.Name(), string(contents), dirID, "")
			if createErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("create text %s: %v", relPath, createErr))
			} else {
				result.Uploaded++
			}
		} else {
			if e.Verbose {
				fmt.Printf("  ⬆ Uploading: %s\n", relPath)
			}
			_, uploadErr := e.Client.UploadFile(path, dirID, info.Name())
			if uploadErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("upload %s: %v", relPath, uploadErr))
			} else {
				result.Uploaded++
			}
		}

		return nil
	})

	if err != nil {
		return result, fmt.Errorf("walk failed: %w", err)
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

	localPath := filepath.Join(e.SyncDir, localRel)

	switch change.Action {
	case "created", "modified":
		// Ensure parent directory exists
		os.MkdirAll(filepath.Dir(localPath), 0755)

		f, err := os.Create(localPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("create %s: %v", localPath, err))
			return
		}

		_, err = e.Client.DownloadFile(change.ID, f)
		f.Close()
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("download %s: %v", change.Path, err))
			os.Remove(localPath)
			return
		}

		// Track notes in state
		if isNote {
			e.State.Notes[localRel] = change.ID
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
