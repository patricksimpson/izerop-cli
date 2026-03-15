package sync2

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/config"
)

// SyncLock prevents concurrent sync operations on the same profile.
// On Windows, uses exclusive file creation as a lock (no flock available).
type SyncLock struct {
	file *os.File
}

// AcquireSyncLock tries to acquire an exclusive lock for the given profile.
func AcquireSyncLock(profile string) (*SyncLock, error) {
	dir, err := config.ProfileDir(profile)
	if err != nil {
		return nil, err
	}
	os.MkdirAll(dir, 0700)

	lockPath := filepath.Join(dir, "sync.lock")

	// If lock file exists and is older than 30 min, treat as stale
	if info, statErr := os.Stat(lockPath); statErr == nil {
		if time.Since(info.ModTime()) > 30*time.Minute {
			os.Remove(lockPath)
		}
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("another sync is already running for profile %q", profile)
	}

	return &SyncLock{file: f}, nil
}

// Release releases the sync lock.
func (l *SyncLock) Release() {
	if l.file != nil {
		path := l.file.Name()
		l.file.Close()
		os.Remove(path)
	}
}
