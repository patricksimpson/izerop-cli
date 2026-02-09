package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/config"
	pkgsync "github.com/patricksimpson/izerop-cli/pkg/sync"
	"github.com/patricksimpson/izerop-cli/pkg/watcher"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct holds the application state
type App struct {
	ctx     context.Context
	client  *api.Client
	cfg     *config.Config
	watcher *watcher.Watcher
	watchMu gosync.Mutex

	logMu      gosync.Mutex
	logs       []LogEntry
	maxLogs    int
	traySyncCh chan struct{}
}

// LogEntry represents a single log line
type LogEntry struct {
	Time    string `json:"time"`
	Message string `json:"message"`
	Level   string `json:"level"` // info, success, warn, error
}

// NewApp creates a new App instance
func NewApp() *App {
	return &App{
		maxLogs:    500,
		traySyncCh: make(chan struct{}, 1),
	}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, err := config.Load()
	if err == nil && cfg.Token != "" {
		a.cfg = cfg
		a.client = api.NewClient(cfg.ServerURL, cfg.Token)
	}

	// Start system tray
	go a.startTray()
}

// beforeClose hides to tray instead of quitting
func (a *App) beforeClose(ctx context.Context) bool {
	runtime.WindowHide(ctx)
	return true // prevent actual close
}

// ---- Log capture ----

func (a *App) addLog(level, msg string) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Message: msg,
		Level:   level,
	}
	a.logMu.Lock()
	a.logs = append(a.logs, entry)
	if len(a.logs) > a.maxLogs {
		a.logs = a.logs[len(a.logs)-a.maxLogs:]
	}
	a.logMu.Unlock()

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sync-log", entry)
	}
}

// logWriter adapts addLog to an io.Writer for use with log.Logger
type logWriter struct {
	app   *App
	level string
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}
	// Detect level from message content
	level := lw.level
	if strings.Contains(msg, "ERROR") || strings.Contains(msg, "error") || strings.Contains(msg, "failed") {
		level = "error"
	} else if strings.Contains(msg, "↓") || strings.Contains(msg, "Downloaded") || strings.Contains(msg, "↑") || strings.Contains(msg, "Uploaded") {
		level = "success"
	} else if strings.Contains(msg, "conflict") || strings.Contains(msg, "Conflict") {
		level = "warn"
	}
	lw.app.addLog(level, msg)
	return len(p), nil
}

func (a *App) newLogger() *log.Logger {
	return log.New(&logWriter{app: a, level: "info"}, "", 0)
}

// ---- Types ----

type ConnectionStatus struct {
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	HasToken  bool   `json:"hasToken"`
}

type StatusInfo struct {
	FileCount      int    `json:"fileCount"`
	DirectoryCount int    `json:"directoryCount"`
	TotalSize      int64  `json:"totalSize"`
	StorageLimit   int64  `json:"storageLimit"`
	Cursor         string `json:"cursor"`
	Connected      bool   `json:"connected"`
	Server         string `json:"server"`
	Error          string `json:"error,omitempty"`
}

type LoginResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type ActionResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type SyncConfig struct {
	SyncDir     string `json:"syncDir"`
	IsWatching  bool   `json:"isWatching"`
	IgnoreRules string `json:"ignoreRules"`
}

// ---- Auth ----

func (a *App) GetConnectionStatus() ConnectionStatus {
	if a.cfg == nil {
		return ConnectionStatus{Connected: false}
	}
	return ConnectionStatus{
		Connected: a.client != nil,
		Server:    a.cfg.ServerURL,
		HasToken:  a.cfg.Token != "",
	}
}

func (a *App) Login(serverURL, token string) LoginResult {
	if serverURL == "" {
		serverURL = "https://izerop.com"
	}

	client := api.NewClient(serverURL, token)
	_, err := client.GetSyncStatus()
	if err != nil {
		return LoginResult{Success: false, Error: fmt.Sprintf("Connection failed: %v", err)}
	}

	cfg := &config.Config{
		ServerURL: serverURL,
		Token:     token,
	}
	if a.cfg != nil {
		cfg.SyncDir = a.cfg.SyncDir
	}
	if err := config.Save(cfg); err != nil {
		return LoginResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	a.cfg = cfg
	a.client = client
	a.addLog("success", "Connected to "+serverURL)

	return LoginResult{Success: true}
}

func (a *App) GetStatus() StatusInfo {
	if a.client == nil {
		return StatusInfo{Connected: false, Error: "Not logged in"}
	}

	status, err := a.client.GetSyncStatus()
	if err != nil {
		return StatusInfo{
			Connected: false,
			Server:    a.cfg.ServerURL,
			Error:     err.Error(),
		}
	}

	return StatusInfo{
		FileCount:      status.FileCount,
		DirectoryCount: status.DirectoryCount,
		TotalSize:      status.TotalSize,
		StorageLimit:   status.StorageLimit,
		Cursor:         status.Cursor,
		Connected:      true,
		Server:         a.cfg.ServerURL,
	}
}

func (a *App) Logout() {
	a.StopWatch()
	a.client = nil
	a.cfg = nil
}

// ---- Sync Config ----

func (a *App) GetSyncConfig() SyncConfig {
	cfg := SyncConfig{}
	if a.cfg != nil {
		cfg.SyncDir = a.cfg.SyncDir
	}

	a.watchMu.Lock()
	cfg.IsWatching = a.watcher != nil
	a.watchMu.Unlock()

	// Load ignore rules from file
	if cfg.SyncDir != "" {
		ignorePath := filepath.Join(cfg.SyncDir, ".izeropignore")
		if data, err := os.ReadFile(ignorePath); err == nil {
			cfg.IgnoreRules = string(data)
		}
	}

	return cfg
}

func (a *App) SetSyncDir() ActionResult {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose Sync Directory",
	})
	if err != nil {
		return ActionResult{Success: false, Error: err.Error()}
	}
	if dir == "" {
		return ActionResult{Success: false, Error: "No directory selected"}
	}
	return a.setSyncDir(dir)
}

func (a *App) SetSyncDirManual(path string) ActionResult {
	// Expand ~ to home dir
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ActionResult{Success: false, Error: "Could not determine home directory"}
		}
		path = filepath.Join(home, path[2:])
	}

	// Create dir if it doesn't exist
	if err := os.MkdirAll(path, 0755); err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Could not create directory: %v", err)}
	}

	return a.setSyncDir(path)
}

func (a *App) setSyncDir(dir string) ActionResult {
	if a.cfg == nil {
		return ActionResult{Success: false, Error: "Not logged in"}
	}

	a.cfg.SyncDir = dir
	if err := config.Save(a.cfg); err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	a.addLog("info", "Sync directory set to: "+dir)
	return ActionResult{Success: true}
}

// ---- Sync ----

func (a *App) RunSync() ActionResult {
	if a.client == nil {
		return ActionResult{Success: false, Error: "Not connected"}
	}
	if a.cfg == nil || a.cfg.SyncDir == "" {
		return ActionResult{Success: false, Error: "No sync directory configured. Set one in Sync settings."}
	}

	a.addLog("info", "Starting sync...")

	state, _ := pkgsync.LoadState(a.cfg.SyncDir)
	engine := pkgsync.NewEngine(a.client, a.cfg.SyncDir, state)

	// Pull
	pullResult, newCursor, err := engine.PullSync(state.Cursor)
	if err != nil {
		a.addLog("error", fmt.Sprintf("Pull failed: %v", err))
		return ActionResult{Success: false, Error: err.Error()}
	}
	if pullResult.Downloaded > 0 || pullResult.Deleted > 0 {
		a.addLog("success", fmt.Sprintf("↓ Downloaded: %d, Deleted: %d", pullResult.Downloaded, pullResult.Deleted))
	}
	if pullResult.Conflicts > 0 {
		a.addLog("warn", fmt.Sprintf("Conflicts: %d", pullResult.Conflicts))
	}
	state.Cursor = newCursor

	// Push
	pushResult, err := engine.PushSync()
	if err != nil {
		a.addLog("error", fmt.Sprintf("Push failed: %v", err))
		return ActionResult{Success: false, Error: err.Error()}
	}
	if pushResult.Uploaded > 0 {
		a.addLog("success", fmt.Sprintf("↑ Uploaded: %d", pushResult.Uploaded))
	}

	// Save state
	pkgsync.SaveState(a.cfg.SyncDir, state)

	total := pullResult.Downloaded + pullResult.Uploaded + pushResult.Uploaded + pullResult.Deleted
	if total == 0 {
		a.addLog("info", "Everything up to date")
	} else {
		a.addLog("success", "Sync complete")
	}

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sync-complete", nil)
	}

	return ActionResult{Success: true}
}

// ---- Watch ----

func (a *App) StartWatch() ActionResult {
	if a.client == nil {
		return ActionResult{Success: false, Error: "Not connected"}
	}
	if a.cfg == nil || a.cfg.SyncDir == "" {
		return ActionResult{Success: false, Error: "No sync directory configured. Set one in Sync settings."}
	}

	a.watchMu.Lock()
	if a.watcher != nil {
		a.watchMu.Unlock()
		return ActionResult{Success: false, Error: "Already watching"}
	}
	a.watchMu.Unlock()

	w, err := watcher.New(watcher.Config{
		SyncDir:      a.cfg.SyncDir,
		ServerURL:    a.cfg.ServerURL,
		Client:       a.client,
		PollInterval: 30 * time.Second,
		Logger:       a.newLogger(),
	})
	if err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Could not start watcher: %v", err)}
	}

	a.watchMu.Lock()
	a.watcher = w
	a.watchMu.Unlock()

	a.addLog("success", "Started watching: "+a.cfg.SyncDir)
	a.notifyTray()

	// Run watcher in background
	go func() {
		if err := w.Run(); err != nil {
			a.addLog("error", fmt.Sprintf("Watcher stopped: %v", err))
		} else {
			a.addLog("info", "Watcher stopped")
		}

		a.watchMu.Lock()
		a.watcher = nil
		a.watchMu.Unlock()

		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "watch-stopped", nil)
		}
		a.notifyTray()
	}()

	return ActionResult{Success: true}
}

func (a *App) notifyTray() {
	select {
	case a.traySyncCh <- struct{}{}:
	default:
	}
}

func (a *App) StopWatch() ActionResult {
	a.watchMu.Lock()
	w := a.watcher
	a.watchMu.Unlock()

	if w == nil {
		return ActionResult{Success: true}
	}

	w.Stop()
	a.addLog("info", "Stopping watcher...")
	return ActionResult{Success: true}
}

// ---- Logs ----

func (a *App) GetSyncLogs() []LogEntry {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	result := make([]LogEntry, len(a.logs))
	copy(result, a.logs)
	return result
}

func (a *App) ClearLogs() {
	a.logMu.Lock()
	a.logs = nil
	a.logMu.Unlock()
}

// ---- Ignore Rules ----

func (a *App) SaveIgnoreRules(rules string) ActionResult {
	if a.cfg == nil || a.cfg.SyncDir == "" {
		return ActionResult{Success: false, Error: "No sync directory configured"}
	}

	ignorePath := filepath.Join(a.cfg.SyncDir, ".izeropignore")
	if err := os.WriteFile(ignorePath, []byte(rules), 0644); err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Could not save: %v", err)}
	}

	a.addLog("info", "Ignore rules saved")
	return ActionResult{Success: true}
}
