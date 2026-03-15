package sync2

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/patricksimpson/izerop-cli/pkg/config"
)

// SyncLock prevents concurrent sync operations on the same profile.
// Uses flock for cross-process safety.
type SyncLock struct {
	file *os.File
}

// AcquireSyncLock tries to acquire an exclusive lock for the given profile.
// Returns an error if another process already holds the lock.
func AcquireSyncLock(profile string) (*SyncLock, error) {
	dir, err := config.ProfileDir(profile)
	if err != nil {
		return nil, err
	}
	os.MkdirAll(dir, 0700)

	lockPath := filepath.Join(dir, "sync.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// Try non-blocking exclusive lock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("another sync is already running for profile %q", profile)
	}

	return &SyncLock{file: f}, nil
}

// Release releases the sync lock.
func (l *SyncLock) Release() {
	if l.file != nil {
		syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.file.Close()
	}
}
