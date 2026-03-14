package sync2

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	gosync "sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/patricksimpson/izerop-cli/pkg/api"
)

// WatcherConfig holds watcher configuration.
type WatcherConfig struct {
	Profile      string
	SyncDir      string
	ServerURL    string
	Client       *api.Client
	PollInterval time.Duration // how often to poll server for remote changes
	SettleTime   time.Duration // cooldown before a file is considered stable (default 5s)
	Verbose      bool
	Logger       *log.Logger
}

// Watcher monitors a directory and syncs changes using the v2 engine.
type Watcher struct {
	cfg       WatcherConfig
	fsw       *fsnotify.Watcher
	stability *StabilityTracker
	syncMu    gosync.Mutex // prevents overlapping sync cycles
	pushCh    chan struct{} // signal to trigger a push
	stopCh    chan struct{}
	pulling   bool // true during pull — suppresses fsnotify processing
}

// NewWatcher creates a new v2 Watcher.
func NewWatcher(cfg WatcherConfig) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify init: %w", err)
	}

	if cfg.SettleTime == 0 {
		cfg.SettleTime = 5 * time.Second
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}

	return &Watcher{
		cfg:       cfg,
		fsw:       fsw,
		stability: NewStabilityTracker(cfg.SettleTime),
		pushCh:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}, nil
}

// Run starts the watcher. Blocks until stopped.
func (w *Watcher) Run() error {
	w.cfg.Logger.Printf("Watching: %s ↔ %s", w.cfg.SyncDir, w.cfg.ServerURL)
	w.cfg.Logger.Printf("Poll: %s, settle: %s, engine: sync2", w.cfg.PollInterval, w.cfg.SettleTime)

	// Auto-migrate v1 state if needed
	if _, migrated, err := MigrateIfNeeded(w.cfg.Profile); err != nil {
		w.cfg.Logger.Printf("Warning: state migration failed: %v", err)
	} else if migrated {
		w.cfg.Logger.Printf("Migrated v1 sync state → v2")
	}

	// Watch the sync dir recursively
	if err := w.addWatchRecursive(w.cfg.SyncDir); err != nil {
		return fmt.Errorf("watch directory: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Initial full sync
	w.runFullSync("startup")

	// Server poll ticker
	pollTicker := time.NewTicker(w.cfg.PollInterval)
	defer pollTicker.Stop()

	// Stability check ticker — periodically check if pending files are now stable
	stabilityTicker := time.NewTicker(2 * time.Second)
	defer stabilityTicker.Stop()

	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handleFSEvent(event)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.cfg.Logger.Printf("fsnotify error: %v", err)

		case <-stabilityTicker.C:
			// Check if any pending files are now stable → trigger push
			if stableFiles := w.stability.StableFiles(); len(stableFiles) > 0 {
				w.triggerPush()
			}

		case <-w.pushCh:
			w.runPush()

		case <-pollTicker.C:
			w.runPull()

		case <-sigCh:
			w.cfg.Logger.Println("Shutting down...")
			w.fsw.Close()
			w.cfg.Logger.Println("Goodbye!")
			return nil

		case <-w.stopCh:
			w.fsw.Close()
			return nil
		}
	}
}

// Stop signals the watcher to stop.
func (w *Watcher) Stop() {
	close(w.stopCh)
}

// handleFSEvent processes a single fsnotify event.
func (w *Watcher) handleFSEvent(event fsnotify.Event) {
	if w.pulling {
		return // ignore events caused by our own downloads
	}

	name := filepath.Base(event.Name)

	// Ignore hidden files, temp files, conflict files
	if strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".izerop-tmp") ||
		strings.HasSuffix(name, "~") ||
		strings.HasSuffix(name, ".swp") ||
		strings.Contains(name, ".conflict") {
		return
	}

	if w.cfg.Verbose {
		w.cfg.Logger.Printf("fs: %s %s", event.Op, event.Name)
	}

	// Watch new directories
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			w.addWatchRecursive(event.Name)
		}
	}

	// Touch the stability tracker — resets the file's cooldown
	w.stability.Touch(event.Name)
}

// triggerPush sends a non-blocking signal to the push channel.
func (w *Watcher) triggerPush() {
	select {
	case w.pushCh <- struct{}{}:
	default:
	}
}

// runFullSync runs a complete manifest-based three-way sync.
func (w *Watcher) runFullSync(reason string) {
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	w.cfg.Logger.Printf("Full sync (%s)...", reason)

	w.pulling = true
	defer func() { w.pulling = false }()

	engine := w.newEngine()
	result, err := engine.Sync()
	if err != nil {
		w.cfg.Logger.Printf("Sync error: %v", err)
		return
	}

	w.logResult(result)
}

// runPull fetches remote changes via cursor-based API.
func (w *Watcher) runPull() {
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	w.pulling = true
	defer func() { w.pulling = false }()

	engine := w.newEngine()
	result, err := engine.SyncPull()
	if err != nil {
		w.cfg.Logger.Printf("Pull error: %v", err)
		return
	}

	if result.Pulled > 0 || result.Deleted > 0 || result.Conflicts > 0 {
		w.cfg.Logger.Printf("⬇ %d pulled, %d deleted, %d conflicts",
			result.Pulled, result.Deleted, result.Conflicts)
	}
	for _, e := range result.Errors {
		w.cfg.Logger.Printf("⚠ pull: %s", e)
	}
}

// runPush scans stable local changes and uploads them.
func (w *Watcher) runPush() {
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	engine := w.newEngine()
	result, err := engine.SyncPush()
	if err != nil {
		w.cfg.Logger.Printf("Push error: %v", err)
		return
	}

	// Clear stable files from tracker after successful sync
	for _, path := range w.stability.StableFiles() {
		w.stability.Remove(path)
	}

	if result.Pushed > 0 || result.Deleted > 0 || result.Conflicts > 0 {
		w.cfg.Logger.Printf("⬆ %d pushed, %d deleted, %d conflicts",
			result.Pushed, result.Deleted, result.Conflicts)
	}
	for _, e := range result.Errors {
		w.cfg.Logger.Printf("⚠ push: %s", e)
	}
}

// newEngine creates a fresh engine instance for this cycle.
func (w *Watcher) newEngine() *Engine {
	engine := NewEngine(w.cfg.Client, w.cfg.SyncDir, w.cfg.Profile)
	engine.Verbose = w.cfg.Verbose
	engine.Stability = w.stability
	return engine
}

// logResult logs a full sync result.
func (w *Watcher) logResult(result *SyncResult) {
	if result.Pushed > 0 || result.Pulled > 0 || result.Deleted > 0 || result.Conflicts > 0 {
		w.cfg.Logger.Printf("⬆ %d pushed, ⬇ %d pulled, 🗑 %d deleted, ⚠ %d conflicts",
			result.Pushed, result.Pulled, result.Deleted, result.Conflicts)
	}
	for _, e := range result.Errors {
		w.cfg.Logger.Printf("⚠ %s", e)
	}

	// Report pending conflicts
	if result.Conflicts > 0 {
		w.cfg.Logger.Printf("Run 'izerop conflicts' to view and resolve")
	}
}

// addWatchRecursive adds a directory and all its subdirectories to fsnotify.
func (w *Watcher) addWatchRecursive(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && path != dir {
				return filepath.SkipDir
			}
			return w.fsw.Add(path)
		}
		return nil
	})
}
