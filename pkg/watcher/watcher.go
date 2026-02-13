package watcher

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/sync"
)

// Config holds watcher configuration.
type Config struct {
	Profile      string // profile name for state storage
	SyncDir      string
	ServerURL    string
	Client       *api.Client
	PollInterval time.Duration // how often to poll server for remote changes
	SettleTime   time.Duration // debounce delay before pushing local changes (default 12s)
	Verbose      bool
	Logger       *log.Logger
}

// Watcher monitors a directory and syncs changes.
type Watcher struct {
	cfg      Config
	state    *sync.State
	fsw      *fsnotify.Watcher
	pushCh   chan struct{} // signal to trigger a push
	stopCh   chan struct{}
	pulling  bool // true while pull is in progress — suppresses fsnotify events
}

// New creates a new Watcher.
func New(cfg Config) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify init failed: %w", err)
	}

	sync.MigrateState(cfg.Profile, cfg.SyncDir)
	state, _ := sync.LoadState(cfg.Profile)

	return &Watcher{
		cfg:    cfg,
		state:  state,
		fsw:    fsw,
		pushCh: make(chan struct{}, 1), // buffered so we don't block
		stopCh: make(chan struct{}),
	}, nil
}

// Run starts the watcher. Blocks until stopped.
func (w *Watcher) Run() error {
	// Default settle time if not set
	if w.cfg.SettleTime == 0 {
		w.cfg.SettleTime = 12 * time.Second
	}

	w.cfg.Logger.Printf("Watching: %s ↔ %s", w.cfg.SyncDir, w.cfg.ServerURL)
	w.cfg.Logger.Printf("Poll interval: %s, settle time: %s, fsnotify: enabled", w.cfg.PollInterval, w.cfg.SettleTime)

	// Add the sync dir and all subdirs to fsnotify
	if err := w.addWatchRecursive(w.cfg.SyncDir); err != nil {
		return fmt.Errorf("could not watch directory: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Run initial sync
	w.runSync("startup")

	// Server poll ticker
	pollTicker := time.NewTicker(w.cfg.PollInterval)
	defer pollTicker.Stop()

	// Debounce timer for local changes — wait 2s after last change before pushing
	var debounce *time.Timer

	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if w.pulling || w.shouldIgnore(event.Name) {
				continue
			}
			if w.cfg.Verbose {
				w.cfg.Logger.Printf("fs event: %s %s", event.Op, event.Name)
			}

			// If a new directory was created, watch it too
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					w.addWatchRecursive(event.Name)
				}
			}

			// Debounce: reset timer on each event, push after settle time of quiet
			// This gives the user time to finish renaming files/folders before sync fires
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(w.cfg.SettleTime, func() {
				select {
				case w.pushCh <- struct{}{}:
				default:
				}
			})

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.cfg.Logger.Printf("fsnotify error: %v", err)

		case <-w.pushCh:
			w.runPush()

		case <-pollTicker.C:
			w.runPull()

		case <-sigCh:
			w.cfg.Logger.Println("Shutting down...")
			w.saveState()
			w.fsw.Close()
			w.cfg.Logger.Println("State saved. Goodbye!")
			return nil

		case <-w.stopCh:
			w.saveState()
			w.fsw.Close()
			return nil
		}
	}
}

// Stop signals the watcher to stop.
func (w *Watcher) Stop() {
	close(w.stopCh)
}

func (w *Watcher) runSync(reason string) {
	w.cfg.Logger.Printf("Sync (%s)...", reason)
	w.pulling = true
	engine := sync.NewEngine(w.cfg.Client, w.cfg.SyncDir, w.state)
	engine.Verbose = w.cfg.Verbose

	// Pull
	pullResult, newCursor, err := engine.PullSync(w.state.Cursor)
	if err != nil {
		w.cfg.Logger.Printf("Pull error: %v", err)
	} else {
		w.state.Cursor = newCursor
		if pullResult.Downloaded > 0 || pullResult.Deleted > 0 || pullResult.Conflicts > 0 {
			w.cfg.Logger.Printf("⬇ %d downloaded, %d deleted, %d conflicts",
				pullResult.Downloaded, pullResult.Deleted, pullResult.Conflicts)
		}
		for _, e := range pullResult.Errors {
			w.cfg.Logger.Printf("⚠ pull: %s", e)
		}
	}

	// Done pulling — allow fsnotify events again before push
	w.pulling = false

	// Push
	pushResult, err := engine.PushSync()
	if err != nil {
		w.cfg.Logger.Printf("Push error: %v", err)
	} else {
		if pushResult.Uploaded > 0 || pushResult.Deleted > 0 || pushResult.Conflicts > 0 {
			w.cfg.Logger.Printf("⬆ %d uploaded, %d deleted, %d conflicts",
				pushResult.Uploaded, pushResult.Deleted, pushResult.Conflicts)
		}
		for _, e := range pushResult.Errors {
			w.cfg.Logger.Printf("⚠ push: %s", e)
		}
	}

	w.saveState()
}

func (w *Watcher) runPull() {
	w.pulling = true
	defer func() { w.pulling = false }()

	engine := sync.NewEngine(w.cfg.Client, w.cfg.SyncDir, w.state)
	engine.Verbose = w.cfg.Verbose

	pullResult, newCursor, err := engine.PullSync(w.state.Cursor)
	if err != nil {
		w.cfg.Logger.Printf("Pull error: %v", err)
		return
	}
	w.state.Cursor = newCursor
	if pullResult.Downloaded > 0 || pullResult.Deleted > 0 || pullResult.Conflicts > 0 {
		w.cfg.Logger.Printf("⬇ %d downloaded, %d deleted, %d conflicts",
			pullResult.Downloaded, pullResult.Deleted, pullResult.Conflicts)
	}
	for _, e := range pullResult.Errors {
		w.cfg.Logger.Printf("⚠ pull: %s", e)
	}
	w.saveState()
}

func (w *Watcher) runPush() {
	engine := sync.NewEngine(w.cfg.Client, w.cfg.SyncDir, w.state)
	engine.Verbose = w.cfg.Verbose

	pushResult, err := engine.PushSync()
	if err != nil {
		w.cfg.Logger.Printf("Push error: %v", err)
		return
	}
	if pushResult.Uploaded > 0 || pushResult.Deleted > 0 || pushResult.Conflicts > 0 {
		w.cfg.Logger.Printf("⬆ %d uploaded, %d deleted, %d conflicts",
			pushResult.Uploaded, pushResult.Deleted, pushResult.Conflicts)
	}
	for _, e := range pushResult.Errors {
		w.cfg.Logger.Printf("⚠ push: %s", e)
	}
	w.saveState()
}

func (w *Watcher) saveState() {
	if err := sync.SaveState(w.cfg.Profile, w.state); err != nil {
		w.cfg.Logger.Printf("Warning: could not save state: %v", err)
	}
}

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

func (w *Watcher) shouldIgnore(path string) bool {
	name := filepath.Base(path)
	// Ignore hidden files, sync state, conflict files, temp files
	if strings.HasPrefix(name, ".") {
		return true
	}
	if name == ".izerop-sync.json" {
		return true
	}
	if strings.Contains(name, ".conflict") {
		return true
	}
	if strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".swp") || strings.HasSuffix(name, ".izerop-tmp") {
		return true
	}
	return false
}
