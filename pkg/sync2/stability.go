package sync2

import (
	gosync "sync"
	"time"
)

// StabilityTracker determines whether a file is "at rest" — i.e., not currently
// being edited. Files are touched on every fsnotify event and are only considered
// stable once their cooldown has expired.
type StabilityTracker struct {
	mu       gosync.Mutex
	pending  map[string]time.Time // path → time of last fs event
	cooldown time.Duration
}

// NewStabilityTracker creates a tracker with the given cooldown duration.
// A file must have no fs events for this long before it's considered stable.
func NewStabilityTracker(cooldown time.Duration) *StabilityTracker {
	return &StabilityTracker{
		pending:  make(map[string]time.Time),
		cooldown: cooldown,
	}
}

// Touch records a filesystem event for a path, resetting its cooldown.
func (s *StabilityTracker) Touch(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[path] = time.Now()
}

// IsStable returns true if the file's cooldown has expired (no recent events).
// Returns false if the file has never been touched (not tracked).
func (s *StabilityTracker) IsStable(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastEvent, exists := s.pending[path]
	if !exists {
		// Never touched by fsnotify — it's stable (e.g. was already on disk before watcher started)
		return true
	}
	return time.Since(lastEvent) >= s.cooldown
}

// StableFiles returns all paths that have passed their cooldown.
func (s *StabilityTracker) StableFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var stable []string
	for path, lastEvent := range s.pending {
		if now.Sub(lastEvent) >= s.cooldown {
			stable = append(stable, path)
		}
	}
	return stable
}

// Remove stops tracking a path (call after successful sync).
func (s *StabilityTracker) Remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, path)
}

// HasPending returns true if any files are still in their cooldown period.
func (s *StabilityTracker) HasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, lastEvent := range s.pending {
		if now.Sub(lastEvent) < s.cooldown {
			return true
		}
	}
	return false
}

// PendingCount returns the number of files still in cooldown.
func (s *StabilityTracker) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	count := 0
	for _, lastEvent := range s.pending {
		if now.Sub(lastEvent) < s.cooldown {
			count++
		}
	}
	return count
}

// AllPending returns all tracked paths (both stable and still in cooldown).
func (s *StabilityTracker) AllPending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.pending))
	for p := range s.pending {
		paths = append(paths, p)
	}
	return paths
}

// MarkStable forces a path's cooldown to expire so it's picked up on next push.
func (s *StabilityTracker) MarkStable(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[path]; exists {
		s.pending[path] = time.Now().Add(-s.cooldown - time.Second)
	}
}
