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
}

// NewEngine creates a sync engine.
func NewEngine(client *api.Client, syncDir string) *Engine {
	return &Engine{
		Client:  client,
		SyncDir: syncDir,
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

// PullSync downloads remote changes to the local sync directory.
// Uses the changes API with a cursor to only fetch what's new.
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

	// Get ALL remote files (across all directories) indexed by path
	remoteFilesByPath := make(map[string]api.FileEntry)
	files, err := e.Client.ListFiles("")
	if err != nil {
		return nil, fmt.Errorf("could not list remote files: %w", err)
	}
	for _, f := range files {
		remoteFilesByPath[f.Path] = f
	}

	// Walk local directory
	err = filepath.Walk(e.SyncDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("walk error: %s: %v", path, err))
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

		// Build the remote-style path
		remotePath := "/" + filepath.ToSlash(relPath)

		if info.IsDir() {
			// Check if directory exists remotely
			if _, exists := remoteDirsByPath[remotePath]; !exists {
				// Find or create parent directory
				parentPath := filepath.Dir(remotePath)
				parentID := ""
				if parentPath != "/" {
					if parent, ok := remoteDirsByPath[parentPath]; ok {
						parentID = parent.ID
					}
				}

				if e.Verbose {
					fmt.Printf("  📁 Creating: %s\n", remotePath)
				}
				dir, err := e.Client.CreateDirectory(info.Name(), parentID)
				if err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("mkdir %s: %v", remotePath, err))
				} else {
					remoteDirsByPath[remotePath] = *dir
				}
			}
			return nil
		}

		// It's a file — check if it needs uploading
		remoteFile, exists := remoteFilesByPath[remotePath]
		if exists {
			// Compare sizes as a quick check
			if remoteFile.Size == info.Size() {
				result.Skipped++
				return nil
			}
		}

		// Find the directory ID for this file
		dirPath := filepath.Dir(remotePath)
		dirID := ""
		if dir, ok := remoteDirsByPath[dirPath]; ok {
			dirID = dir.ID
		}

		if dirID == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("no remote directory for %s (dir: %s)", remotePath, dirPath))
			return nil
		}

		if e.Verbose {
			fmt.Printf("  ⬆ Uploading: %s\n", remotePath)
		}
		_, uploadErr := e.Client.UploadFile(path, dirID, info.Name())
		if uploadErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("upload %s: %v", remotePath, uploadErr))
		} else {
			result.Uploaded++
		}

		return nil
	})

	if err != nil {
		return result, fmt.Errorf("walk failed: %w", err)
	}

	return result, nil
}

func (e *Engine) handleDirectoryChange(change api.Change, result *SyncResult) {
	localPath := filepath.Join(e.SyncDir, change.Path)

	switch change.Action {
	case "created", "modified":
		if err := os.MkdirAll(localPath, 0755); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mkdir %s: %v", localPath, err))
		}
	case "deleted":
		// Only remove if empty
		entries, _ := os.ReadDir(localPath)
		if len(entries) == 0 {
			os.Remove(localPath)
			result.Deleted++
		}
	}
}

func (e *Engine) handleFileChange(change api.Change, result *SyncResult) {
	localPath := filepath.Join(e.SyncDir, change.Path)

	switch change.Action {
	case "created", "modified":
		// Ensure parent directory exists
		os.MkdirAll(filepath.Dir(localPath), 0755)

		// Download the file
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

		if e.Verbose {
			fmt.Printf("  ⬇ %s\n", change.Path)
		}
		result.Downloaded++

	case "deleted":
		if _, err := os.Stat(localPath); err == nil {
			os.Remove(localPath)
			if e.Verbose {
				fmt.Printf("  🗑 %s\n", change.Path)
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
