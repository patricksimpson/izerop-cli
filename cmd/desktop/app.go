package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	gosync "sync"
	"syscall"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/config"
	pkgsync "github.com/patricksimpson/izerop-cli/pkg/sync"
	"github.com/patricksimpson/izerop-cli/pkg/updater"
	"github.com/patricksimpson/izerop-cli/pkg/watcher"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct holds the application state
type App struct {
	ctx     context.Context
	client  *api.Client
	cfg     *config.Config
	profile string
	watcher *watcher.Watcher
	watchMu gosync.Mutex

	logMu   gosync.Mutex
	logs    []LogEntry
	maxLogs int
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
		maxLogs: 500,
	}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.profile = config.GetActiveProfile()
	cfg, err := config.LoadProfile(a.profile)
	if err == nil && cfg.Token != "" {
		a.cfg = cfg
		a.client = api.NewClient(cfg.ServerURL, cfg.Token)
		a.client.ClientKey = cfg.EnsureClientKey(a.profile)
	}

	// Load existing logs from CLI watcher log file
	a.loadExistingLogs()

	// Check if a CLI watcher is already running
	if running, pid := a.cliWatcherRunning(); running {
		a.addLog("info", fmt.Sprintf("CLI watcher detected (PID %d) for profile %q", pid, a.profile))
	}
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
	client.ClientKey = a.cfg.EnsureClientKey(a.profile)
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
	if err := config.SaveProfile(a.profile, cfg); err != nil {
		return LoginResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	a.cfg = cfg
	a.client = client
	a.addLog("success", fmt.Sprintf("Connected to %s (profile: %s)", serverURL, a.profile))

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
	appWatching := a.watcher != nil
	a.watchMu.Unlock()
	cliRunning, _ := a.cliWatcherRunning()
	cfg.IsWatching = appWatching || cliRunning

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
	if err := config.SaveProfile(a.profile, a.cfg); err != nil {
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

	pkgsync.MigrateState(a.profile, a.cfg.SyncDir)
	state, _ := pkgsync.LoadState(a.profile)
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
	pkgsync.SaveState(a.profile, state)

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

	settleMs := a.cfg.SettleTimeMs
	if settleMs <= 0 {
		settleMs = config.DefaultSettleTimeMs
	}

	w, err := watcher.New(watcher.Config{
		SyncDir:      a.cfg.SyncDir,
		ServerURL:    a.cfg.ServerURL,
		Client:       a.client,
		PollInterval: 30 * time.Second,
		SettleTime:   time.Duration(settleMs) * time.Millisecond,
		Logger:       a.newLogger(),
	})
	if err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Could not start watcher: %v", err)}
	}

	a.watchMu.Lock()
	a.watcher = w
	a.watchMu.Unlock()

	a.addLog("success", "Started watching: "+a.cfg.SyncDir)

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
	}()

	return ActionResult{Success: true}
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

func (a *App) GetVersion() string {
	return strings.TrimPrefix(version, "v")
}

func (a *App) ClearLogs() {
	a.logMu.Lock()
	a.logs = nil
	a.logMu.Unlock()
}

// ---- Ignore Rules ----

// ---- CLI Watcher Integration ----

func (a *App) cliPIDPath() string {
	p, err := config.ProfilePIDPath(a.profile)
	if err != nil {
		dir, _ := os.UserConfigDir()
		return filepath.Join(dir, "izerop", "watch.pid")
	}
	return p
}

func (a *App) cliLogPath() string {
	p, err := config.ProfileLogPath(a.profile)
	if err != nil {
		dir, _ := os.UserConfigDir()
		return filepath.Join(dir, "izerop", "watch.log")
	}
	return p
}

// cliWatcherRunning checks if the CLI watcher daemon is running.
func (a *App) cliWatcherRunning() (bool, int) {
	data, err := os.ReadFile(a.cliPIDPath())
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, 0
	}
	return true, pid
}

// GetWatcherInfo returns whether a CLI or app watcher is running.
type WatcherInfo struct {
	Running    bool   `json:"running"`
	Source     string `json:"source"` // "cli", "app", or ""
	PID        int    `json:"pid,omitempty"`
}

func (a *App) GetWatcherInfo() WatcherInfo {
	// Check app watcher first
	a.watchMu.Lock()
	appWatching := a.watcher != nil
	a.watchMu.Unlock()
	if appWatching {
		return WatcherInfo{Running: true, Source: "app", PID: os.Getpid()}
	}

	// Check CLI watcher
	if running, pid := a.cliWatcherRunning(); running {
		return WatcherInfo{Running: true, Source: "cli", PID: pid}
	}

	return WatcherInfo{Running: false}
}

// loadExistingLogs reads the last N lines from the CLI watcher log file.
func (a *App) loadExistingLogs() {
	logPath := a.cliLogPath()
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()

	// Read last 100 lines
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	// Keep last 100
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}

	a.logMu.Lock()
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		level := "info"
		if strings.Contains(line, "error") || strings.Contains(line, "ERROR") || strings.Contains(line, "failed") {
			level = "error"
		} else if strings.Contains(line, "⬆") || strings.Contains(line, "⬇") || strings.Contains(line, "uploaded") || strings.Contains(line, "downloaded") {
			level = "success"
		} else if strings.Contains(line, "conflict") {
			level = "warn"
		}
		a.logs = append(a.logs, LogEntry{
			Time:    "",
			Message: line,
			Level:   level,
		})
	}
	a.logMu.Unlock()
}

// RefreshLogs reloads logs from the CLI watcher log file (for manual refresh).
func (a *App) RefreshLogs() ActionResult {
	a.logMu.Lock()
	a.logs = nil
	a.logMu.Unlock()
	a.loadExistingLogs()
	return ActionResult{Success: true}
}

// ---- Self Update ----

type UpdateInfo struct {
	Available  bool   `json:"available"`
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	ReleaseURL string `json:"releaseUrl,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (a *App) CheckForUpdate() UpdateInfo {
	current := strings.TrimPrefix(version, "v")
	release, err := updater.CheckForUpdate(current)
	if err != nil {
		return UpdateInfo{Current: current, Error: err.Error()}
	}
	if release == nil {
		return UpdateInfo{Available: false, Current: current, Latest: current}
	}
	return UpdateInfo{
		Available:  true,
		Current:    current,
		Latest:     strings.TrimPrefix(release.TagName, "v"),
		ReleaseURL: release.HTMLURL,
	}
}

func (a *App) DoUpdate() ActionResult {
	current := strings.TrimPrefix(version, "v")
	a.addLog("info", "Checking for updates...")

	release, err := updater.CheckForUpdate(current)
	if err != nil {
		a.addLog("error", fmt.Sprintf("Update check failed: %v", err))
		return ActionResult{Success: false, Error: err.Error()}
	}
	if release == nil {
		a.addLog("info", "Already up to date")
		return ActionResult{Success: true}
	}

	asset := updater.FindAsset(release)
	if asset == nil {
		a.addLog("error", "No compatible binary found for this platform")
		return ActionResult{Success: false, Error: "No compatible binary for this platform"}
	}

	a.addLog("info", fmt.Sprintf("Downloading %s (%s)...", release.TagName, asset.Name))

	if err := updater.DownloadAndReplace(asset); err != nil {
		a.addLog("error", fmt.Sprintf("Update failed: %v", err))
		return ActionResult{Success: false, Error: err.Error()}
	}

	a.addLog("success", fmt.Sprintf("Updated to %s! Restart the app to use the new version.", release.TagName))
	return ActionResult{Success: true}
}

func (a *App) RestartApp() {
	execPath, err := os.Executable()
	if err != nil {
		a.addLog("error", fmt.Sprintf("Could not determine executable: %v", err))
		return
	}

	a.StopWatch()
	a.addLog("info", "Restarting...")

	// Launch new instance
	proc, err := os.StartProcess(execPath, os.Args, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		a.addLog("error", fmt.Sprintf("Restart failed: %v", err))
		return
	}
	proc.Release()

	// Exit current instance
	os.Exit(0)
}

// ---- Profile Management ----

type ProfileInfo struct {
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Server    string `json:"server"`
	SyncDir   string `json:"syncDir"`
	HasToken  bool   `json:"hasToken"`
	Watching  bool   `json:"watching"`
}

func (a *App) GetProfiles() []ProfileInfo {
	profiles, _ := config.ListProfiles()
	if len(profiles) == 0 {
		profiles = []string{config.DefaultProfile}
	}
	var result []ProfileInfo
	for _, name := range profiles {
		info := ProfileInfo{Name: name, Active: name == a.profile}
		if pcfg, err := config.LoadProfile(name); err == nil {
			info.Server = pcfg.ServerURL
			info.SyncDir = pcfg.SyncDir
			info.HasToken = pcfg.Token != ""
		}
		// Check if watcher running for this profile
		pidPath, _ := config.ProfilePIDPath(name)
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if proc, err := os.FindProcess(pid); err == nil {
					if proc.Signal(syscall.Signal(0)) == nil {
						info.Watching = true
					}
				}
			}
		}
		result = append(result, info)
	}
	return result
}

func (a *App) SwitchProfile(name string) ActionResult {
	pcfg, err := config.LoadProfile(name)
	if err != nil {
		return ActionResult{Success: false, Error: fmt.Sprintf("Profile %q not found", name)}
	}

	// Stop current watcher if app-managed
	a.StopWatch()

	a.profile = name
	a.cfg = pcfg
	if pcfg.Token != "" {
		a.client = api.NewClient(pcfg.ServerURL, pcfg.Token)
		a.client.ClientKey = pcfg.EnsureClientKey(name)
	} else {
		a.client = nil
	}

	config.SetActiveProfile(name)

	// Reload logs for this profile
	a.logMu.Lock()
	a.logs = nil
	a.logMu.Unlock()
	a.loadExistingLogs()

	a.addLog("info", fmt.Sprintf("Switched to profile: %s", name))
	return ActionResult{Success: true}
}

func (a *App) AddProfile(name, serverURL, token, syncDir string) ActionResult {
	if name == "" {
		return ActionResult{Success: false, Error: "Profile name is required"}
	}
	if serverURL == "" {
		serverURL = "https://izerop.com"
	}

	cfg := &config.Config{
		ServerURL: serverURL,
		Token:     token,
		SyncDir:   syncDir,
	}

	if err := config.SaveProfile(name, cfg); err != nil {
		return ActionResult{Success: false, Error: err.Error()}
	}

	a.addLog("success", fmt.Sprintf("Profile %q created", name))
	return ActionResult{Success: true}
}

func (a *App) RemoveProfile(name string) ActionResult {
	if err := config.DeleteProfile(name); err != nil {
		return ActionResult{Success: false, Error: err.Error()}
	}

	if a.profile == name {
		a.SwitchProfile(config.DefaultProfile)
	}

	a.addLog("info", fmt.Sprintf("Profile %q removed", name))
	return ActionResult{Success: true}
}

func (a *App) GetActiveProfileName() string {
	return a.profile
}

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
