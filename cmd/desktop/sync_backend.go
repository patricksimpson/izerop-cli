package main

import (
	gocontext "context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/config"
	"github.com/patricksimpson/izerop-cli/pkg/sync"
	"github.com/patricksimpson/izerop-cli/pkg/watcher"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SyncConfig represents the sync settings shown in the UI
type SyncConfig struct {
	SyncDir      string `json:"syncDir"`
	PollInterval int    `json:"pollInterval"` // seconds
	IgnoreRules  string `json:"ignoreRules"`
	IsWatching   bool   `json:"isWatching"`
}

// SyncLogEntry represents a single log line in the activity feed
type SyncLogEntry struct {
	Time    string `json:"time"`
	Message string `json:"message"`
	Level   string `json:"level"` // info, warn, error, success
}

// SyncResult represents the result of a sync operation
type SyncActionResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

var (
	activeWatcher *watcher.Watcher
	watcherMu     gosync.Mutex
	syncLogs      []SyncLogEntry
	logMu         gosync.Mutex
)

// GetSyncConfig returns the current sync configuration
func (a *App) GetSyncConfig() SyncConfig {
	cfg := SyncConfig{
		PollInterval: 30,
	}

	if a.cfg != nil {
		cfg.SyncDir = a.cfg.SyncDir
	}

	// Read ignore file if sync dir is set
	if cfg.SyncDir != "" {
		ignorePath := filepath.Join(cfg.SyncDir, ".izeropignore")
		if data, err := os.ReadFile(ignorePath); err == nil {
			cfg.IgnoreRules = string(data)
		}
	}

	watcherMu.Lock()
	cfg.IsWatching = activeWatcher != nil
	watcherMu.Unlock()

	return cfg
}

// SetSyncDir sets the sync directory (opens a folder picker)
func (a *App) SetSyncDir() SyncActionResult {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Sync Directory",
	})
	if err != nil {
		return SyncActionResult{Success: false, Error: err.Error()}
	}
	if dir == "" {
		return SyncActionResult{Success: false, Error: "No directory selected"}
	}

	if a.cfg == nil {
		return SyncActionResult{Success: false, Error: "Not logged in"}
	}

	a.cfg.SyncDir = dir
	if err := config.Save(a.cfg); err != nil {
		return SyncActionResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	addLog("info", "Sync directory set to: %s", dir)
	return SyncActionResult{Success: true}
}

// SetSyncDirManual sets the sync directory from a typed path
func (a *App) SetSyncDirManual(dir string) SyncActionResult {
	if dir == "" {
		return SyncActionResult{Success: false, Error: "Empty path"}
	}

	// Expand ~ to home dir
	if strings.HasPrefix(dir, "~") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[1:])
	}

	// Verify it exists
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return SyncActionResult{Success: false, Error: "Not a valid directory"}
	}

	if a.cfg == nil {
		return SyncActionResult{Success: false, Error: "Not logged in"}
	}

	a.cfg.SyncDir = dir
	if err := config.Save(a.cfg); err != nil {
		return SyncActionResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	addLog("info", "Sync directory set to: %s", dir)
	return SyncActionResult{Success: true}
}

// SaveIgnoreRules writes the .izeropignore file
func (a *App) SaveIgnoreRules(rules string) SyncActionResult {
	if a.cfg == nil || a.cfg.SyncDir == "" {
		return SyncActionResult{Success: false, Error: "Set a sync directory first"}
	}

	path := filepath.Join(a.cfg.SyncDir, ".izeropignore")
	if err := os.WriteFile(path, []byte(rules), 0644); err != nil {
		return SyncActionResult{Success: false, Error: err.Error()}
	}

	addLog("info", "Ignore rules updated")
	return SyncActionResult{Success: true}
}

// RunSync runs a one-shot sync
func (a *App) RunSync() SyncActionResult {
	if a.client == nil {
		return SyncActionResult{Success: false, Error: "Not logged in"}
	}
	if a.cfg.SyncDir == "" {
		return SyncActionResult{Success: false, Error: "Set a sync directory first"}
	}

	addLog("info", "Starting sync...")

	state, _ := sync.LoadState(a.cfg.SyncDir)
	engine := sync.NewEngine(a.client, a.cfg.SyncDir, state)
	engine.Verbose = true

	// Pull
	pullResult, newCursor, err := engine.PullSync(state.Cursor)
	if err != nil {
		addLog("error", "Pull failed: %v", err)
		return SyncActionResult{Success: false, Error: err.Error()}
	}
	state.Cursor = newCursor

	if pullResult.Downloaded > 0 || pullResult.Deleted > 0 {
		addLog("success", "⬇ %d downloaded, %d deleted", pullResult.Downloaded, pullResult.Deleted)
	}
	if pullResult.Conflicts > 0 {
		addLog("warn", "⚠ %d conflicts during pull", pullResult.Conflicts)
	}
	for _, e := range pullResult.Errors {
		addLog("error", "Pull: %s", e)
	}

	// Push
	pushResult, err := engine.PushSync()
	if err != nil {
		addLog("error", "Push failed: %v", err)
		return SyncActionResult{Success: false, Error: err.Error()}
	}

	if pushResult.Uploaded > 0 {
		addLog("success", "⬆ %d uploaded", pushResult.Uploaded)
	}
	if pushResult.Conflicts > 0 {
		addLog("warn", "⚠ %d conflicts during push", pushResult.Conflicts)
	}
	for _, e := range pushResult.Errors {
		addLog("error", "Push: %s", e)
	}

	sync.SaveState(a.cfg.SyncDir, state)

	if pullResult.Downloaded == 0 && pullResult.Deleted == 0 && pushResult.Uploaded == 0 {
		addLog("info", "✅ Everything up to date")
	} else {
		addLog("success", "✅ Sync complete")
	}

	// Emit event to frontend
	runtime.EventsEmit(a.ctx, "sync-complete", nil)

	return SyncActionResult{Success: true}
}

// StartWatch starts the background file watcher
func (a *App) StartWatch() SyncActionResult {
	if a.client == nil {
		return SyncActionResult{Success: false, Error: "Not logged in"}
	}
	if a.cfg.SyncDir == "" {
		return SyncActionResult{Success: false, Error: "Set a sync directory first"}
	}

	watcherMu.Lock()
	if activeWatcher != nil {
		watcherMu.Unlock()
		return SyncActionResult{Success: false, Error: "Already watching"}
	}
	watcherMu.Unlock()

	// Create a logger that feeds into our log buffer
	uiLogger := log.New(&logWriter{ctx: a.ctx}, "", 0)

	w, err := watcher.New(watcher.Config{
		SyncDir:      a.cfg.SyncDir,
		ServerURL:    a.cfg.ServerURL,
		Client:       a.client,
		PollInterval: 30 * time.Second,
		Verbose:      false,
		Logger:       uiLogger,
	})
	if err != nil {
		return SyncActionResult{Success: false, Error: err.Error()}
	}

	watcherMu.Lock()
	activeWatcher = w
	watcherMu.Unlock()

	addLog("success", "👁 Watching: %s", a.cfg.SyncDir)

	go func() {
		if err := w.Run(); err != nil {
			addLog("error", "Watcher stopped: %v", err)
		} else {
			addLog("info", "Watcher stopped")
		}
		watcherMu.Lock()
		activeWatcher = nil
		watcherMu.Unlock()
		runtime.EventsEmit(a.ctx, "watch-stopped", nil)
	}()

	return SyncActionResult{Success: true}
}

// StopWatch stops the background file watcher
func (a *App) StopWatch() SyncActionResult {
	watcherMu.Lock()
	w := activeWatcher
	watcherMu.Unlock()

	if w == nil {
		return SyncActionResult{Success: false, Error: "Not watching"}
	}

	w.Stop()
	addLog("info", "⏹ Stopping watcher...")
	return SyncActionResult{Success: true}
}

// GetSyncLogs returns recent sync log entries
func (a *App) GetSyncLogs() []SyncLogEntry {
	logMu.Lock()
	defer logMu.Unlock()

	// Return last 100 entries
	if len(syncLogs) > 100 {
		return syncLogs[len(syncLogs)-100:]
	}
	return syncLogs
}

// ClearLogs clears the sync log
func (a *App) ClearLogs() {
	logMu.Lock()
	syncLogs = nil
	logMu.Unlock()
}

func addLog(level, format string, args ...interface{}) {
	entry := SyncLogEntry{
		Time:    time.Now().Format("15:04:05"),
		Message: fmt.Sprintf(format, args...),
		Level:   level,
	}

	logMu.Lock()
	syncLogs = append(syncLogs, entry)
	// Keep max 500 entries
	if len(syncLogs) > 500 {
		syncLogs = syncLogs[len(syncLogs)-500:]
	}
	logMu.Unlock()
}

// logWriter implements io.Writer and forwards watcher log output to our UI log
type logWriter struct {
	ctx gocontext.Context
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}

	level := "info"
	if strings.Contains(msg, "error") || strings.Contains(msg, "Error") {
		level = "error"
	} else if strings.Contains(msg, "⚠") {
		level = "warn"
	} else if strings.Contains(msg, "⬇") || strings.Contains(msg, "⬆") || strings.Contains(msg, "✅") {
		level = "success"
	}

	addLog(level, "%s", msg)

	// Emit to frontend
	runtime.EventsEmit(w.ctx, "sync-log", SyncLogEntry{
		Time:    time.Now().Format("15:04:05"),
		Message: msg,
		Level:   level,
	})

	return len(p), nil
}
