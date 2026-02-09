package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/patricksimpson/izerop-cli/internal/auth"
	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/config"
	"github.com/patricksimpson/izerop-cli/pkg/sync"
	"github.com/patricksimpson/izerop-cli/pkg/updater"
	"github.com/patricksimpson/izerop-cli/pkg/watcher"
)

// version is set at build time via -ldflags
var version = "dev"

func main() {
	// Extract --server flag before command parsing
	args := os.Args[1:]
	var serverOverride string
	var filtered []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--server" && i+1 < len(args) {
			serverOverride = args[i+1]
			i++ // skip value
		} else if len(args[i]) > 9 && args[i][:9] == "--server=" {
			serverOverride = args[i][9:]
		} else {
			filtered = append(filtered, args[i])
		}
	}
	os.Args = append([]string{os.Args[0]}, filtered...)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil && os.Args[1] != "login" && os.Args[1] != "version" && os.Args[1] != "help" {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		fmt.Fprintf(os.Stderr, "Run 'izerop login' to configure.\n")
		os.Exit(1)
	}

	// --server flag takes highest priority
	if serverOverride != "" && cfg != nil {
		cfg.ServerURL = serverOverride
	}

	switch os.Args[1] {
	case "version":
		v := strings.TrimPrefix(version, "v")
		fmt.Printf("izerop-cli v%s\n", v)
	case "login":
		if err := auth.Login(); err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
	case "status":
		cmdStatus(cfg)
	case "sync":
		cmdSync(cfg)
	case "push":
		cmdPush(cfg)
	case "pull":
		cmdPull(cfg)
	case "ls":
		cmdList(cfg)
	case "mkdir":
		cmdMkdir(cfg)
	case "rm":
		cmdRm(cfg)
	case "mv":
		cmdMv(cfg)
	case "watch":
		// Handle --stop before full watch
		for _, arg := range os.Args[2:] {
			if arg == "--stop" {
				cmdWatchStop()
				return
			}
		}
		cmdWatch(cfg)
	case "logs":
		cmdLogs()
	case "update":
		cmdUpdate()
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func newClient(cfg *config.Config) *api.Client {
	return api.NewClient(cfg.ServerURL, cfg.Token)
}

func cmdStatus(cfg *config.Config) {
	client := newClient(cfg)

	fmt.Printf("Server:  %s\n", cfg.ServerURL)

	status, err := client.GetSyncStatus()
	if err != nil {
		fmt.Printf("Status:  error (%v)\n", err)
		return
	}
	fmt.Printf("Files:   %d\n", status.FileCount)
	fmt.Printf("Dirs:    %d\n", status.DirectoryCount)
	fmt.Printf("Size:    %s\n", formatSize(status.TotalSize))
	if status.LastSync != "" {
		fmt.Printf("Cursor:  %s\n", status.Cursor)
	}
}

func cmdSync(cfg *config.Config) {
	// Usage: izerop sync [<directory>] [--push-only] [--pull-only] [--verbose]
	syncDir := cfg.SyncDir
	pushOnly := false
	pullOnly := false
	verbose := false

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--push-only":
			pushOnly = true
		case "--pull-only":
			pullOnly = true
		case "--verbose", "-v":
			verbose = true
		default:
			if !strings.HasPrefix(os.Args[i], "--") {
				syncDir = os.Args[i]
			}
		}
	}

	if syncDir == "" {
		syncDir = "."
	}

	// Resolve to absolute path
	absDir, err := filepath.Abs(syncDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory: %v\n", err)
		os.Exit(1)
	}
	syncDir = absDir

	// Verify directory exists
	info, err := os.Stat(syncDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Not a directory: %s\n", syncDir)
		os.Exit(1)
	}

	client := newClient(cfg)

	// Load sync state
	state, _ := sync.LoadState(syncDir)

	engine := sync.NewEngine(client, syncDir, state)
	engine.Verbose = verbose

	fmt.Printf("Syncing: %s ↔ %s\n", syncDir, cfg.ServerURL)

	// Pull remote changes
	if !pushOnly {
		fmt.Println("⬇ Pulling remote changes...")
		pullResult, newCursor, err := engine.PullSync(state.Cursor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pull error: %v\n", err)
		} else {
			state.Cursor = newCursor
			fmt.Printf("  Downloaded: %d, Deleted: %d, Conflicts: %d, Skipped: %d\n",
				pullResult.Downloaded, pullResult.Deleted, pullResult.Conflicts, pullResult.Skipped)
			for _, e := range pullResult.Errors {
				fmt.Fprintf(os.Stderr, "  ⚠ %s\n", e)
			}
		}
	}

	// Push local changes
	if !pullOnly {
		fmt.Println("⬆ Pushing local changes...")
		pushResult, err := engine.PushSync()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Push error: %v\n", err)
		} else {
			fmt.Printf("  Uploaded: %d, Conflicts: %d, Skipped: %d\n",
				pushResult.Uploaded, pushResult.Conflicts, pushResult.Skipped)
			for _, e := range pushResult.Errors {
				fmt.Fprintf(os.Stderr, "  ⚠ %s\n", e)
			}
		}
	}

	// Save state
	if err := sync.SaveState(syncDir, state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save sync state: %v\n", err)
	}

	fmt.Println("✅ Sync complete")
}

func cmdPush(cfg *config.Config) {
	// Usage: izerop push <file> [--dir <directory_id>] [--name <name>]
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop push <file> [--dir <directory_id>] [--name <name>]\n")
		os.Exit(1)
	}

	filePath := os.Args[2]
	var dirID, name string

	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--dir":
			if i+1 < len(os.Args) {
				dirID = os.Args[i+1]
				i++
			}
		case "--name":
			if i+1 < len(os.Args) {
				name = os.Args[i+1]
				i++
			}
		}
	}

	// Verify file exists
	info, err := os.Stat(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "File not found: %s\n", filePath)
		os.Exit(1)
	}
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "Cannot push a directory (yet). Use a file path.\n")
		os.Exit(1)
	}

	client := newClient(cfg)

	fmt.Printf("Uploading %s (%s)...\n", filePath, formatSize(info.Size()))
	file, err := client.UploadFile(filePath, dirID, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Upload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Uploaded: %s (%s)\n", file.Name, file.ID[:8])
}

func cmdPull(cfg *config.Config) {
	// Usage: izerop pull <file_id> [--out <path>]
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop pull <file_id> [--out <path>]\n")
		os.Exit(1)
	}

	fileID := os.Args[2]
	var outPath string

	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--out" && i+1 < len(os.Args) {
			outPath = os.Args[i+1]
			i++
		}
	}

	client := newClient(cfg)

	// If no output path, we need to figure out the filename
	// First download to a buffer to get the filename from headers
	if outPath == "" {
		// Download to temp, then rename
		tmpFile, err := os.CreateTemp("", "izerop-dl-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not create temp file: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Downloading %s...\n", fileID)
		filename, err := client.DownloadFile(fileID, tmpFile)
		tmpFile.Close()
		if err != nil {
			os.Remove(tmpFile.Name())
			fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
			os.Exit(1)
		}

		if filename == "" {
			filename = fileID
		}
		outPath = filename

		if err := os.Rename(tmpFile.Name(), outPath); err != nil {
			// Cross-device rename, copy instead
			src, _ := os.Open(tmpFile.Name())
			dst, _ := os.Create(outPath)
			io.Copy(dst, src)
			src.Close()
			dst.Close()
			os.Remove(tmpFile.Name())
		}
	} else {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not create file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()

		fmt.Printf("Downloading %s...\n", fileID)
		_, err = client.DownloadFile(fileID, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
			os.Exit(1)
		}
	}

	info, _ := os.Stat(outPath)
	fmt.Printf("✅ Downloaded: %s (%s)\n", outPath, formatSize(info.Size()))
}

func cmdList(cfg *config.Config) {
	client := newClient(cfg)

	// Optional directory ID as second arg
	dirID := ""
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "--") {
		dirID = os.Args[2]
	}

	// List directories
	dirs, err := client.ListDirectories()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing directories: %v\n", err)
		os.Exit(1)
	}

	if dirID == "" {
		// Show all directories and all files
		for _, d := range dirs {
			fmt.Printf("📁 %-30s  %d files  %s\n", d.Path+"/", d.FileCount, d.ID)

			// List files in this directory
			files, err := client.ListFiles(d.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ Error listing files: %v\n", err)
				continue
			}
			for _, f := range files {
				size := formatSize(f.Size)
				fmt.Printf("  📄 %-28s  %8s  %s  %s\n", f.Name, size, f.UpdatedAt, f.ID)
			}
		}

		// Also show files without a directory filter (root-level)
	} else {
		// List files in specific directory
		files, err := client.ListFiles(dirID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing files: %v\n", err)
			os.Exit(1)
		}
		if len(files) == 0 {
			fmt.Println("No files found.")
			return
		}
		for _, f := range files {
			size := formatSize(f.Size)
			fmt.Printf("  📄 %-28s  %8s  %s  %s\n", f.Name, size, f.UpdatedAt, f.ID)
		}
	}
}

func cmdMkdir(cfg *config.Config) {
	// Usage: izerop mkdir <name> [--parent <directory_id>]
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop mkdir <name> [--parent <directory_id>]\n")
		os.Exit(1)
	}

	name := os.Args[2]
	var parentID string

	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--parent" && i+1 < len(os.Args) {
			parentID = os.Args[i+1]
			i++
		}
	}

	client := newClient(cfg)

	dir, err := client.CreateDirectory(name, parentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Created: %s/ (%s)\n", dir.Name, dir.ID)
}

func cmdRm(cfg *config.Config) {
	// Usage: izerop rm <id> [--dir]
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop rm <file_id|directory_id> [--dir]\n")
		os.Exit(1)
	}

	id := os.Args[2]
	isDir := false

	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--dir" {
			isDir = true
		}
	}

	client := newClient(cfg)

	if isDir {
		if err := client.DeleteDirectory(id); err != nil {
			fmt.Fprintf(os.Stderr, "Delete failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Directory deleted: %s\n", id)
	} else {
		if err := client.DeleteFile(id); err != nil {
			fmt.Fprintf(os.Stderr, "Delete failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ File deleted: %s\n", id)
	}
}

func cmdMv(cfg *config.Config) {
	// Usage: izerop mv <file_id> [--name <new_name>] [--dir <directory_id>]
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop mv <file_id> [--name <new_name>] [--dir <directory_id>]\n")
		os.Exit(1)
	}

	fileID := os.Args[2]
	var newName, newDirID string

	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--name":
			if i+1 < len(os.Args) {
				newName = os.Args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(os.Args) {
				newDirID = os.Args[i+1]
				i++
			}
		}
	}

	if newName == "" && newDirID == "" {
		fmt.Fprintf(os.Stderr, "Specify --name and/or --dir\n")
		os.Exit(1)
	}

	client := newClient(cfg)

	file, err := client.MoveFile(fileID, newName, newDirID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Move failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Moved: %s → %s\n", fileID[:8], file.Name)
}

func cmdWatch(cfg *config.Config) {
	// Usage: izerop watch [<directory>] [--interval <seconds>] [--daemon] [--log <path>] [--verbose]
	syncDir := cfg.SyncDir
	interval := 30 * time.Second
	verbose := false
	daemon := false
	logPath := ""

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--interval":
			if i+1 < len(os.Args) {
				secs, err := strconv.Atoi(os.Args[i+1])
				if err != nil || secs < 1 {
					fmt.Fprintf(os.Stderr, "Invalid interval: %s\n", os.Args[i+1])
					os.Exit(1)
				}
				interval = time.Duration(secs) * time.Second
				i++
			}
		case "--daemon", "-d":
			daemon = true
		case "--log":
			if i+1 < len(os.Args) {
				logPath = os.Args[i+1]
				i++
			}
		case "--verbose", "-v":
			verbose = true
		default:
			if !strings.HasPrefix(os.Args[i], "--") {
				syncDir = os.Args[i]
			}
		}
	}

	if syncDir == "" {
		syncDir = "."
	}

	absDir, err := filepath.Abs(syncDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory: %v\n", err)
		os.Exit(1)
	}
	syncDir = absDir

	info, err := os.Stat(syncDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Not a directory: %s\n", syncDir)
		os.Exit(1)
	}

	// Daemon mode: fork and exit parent
	if daemon {
		if logPath == "" {
			logPath = defaultLogPath()
		}
		if err := daemonize(logPath); err != nil {
			fmt.Fprintf(os.Stderr, "Daemon failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Set up logger
	logger := log.New(os.Stdout, "", log.LstdFlags)
	if logPath != "" {
		logFile, err := openLogFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		logger = log.New(logFile, "", log.LstdFlags)
	}

	// Write PID file
	pidPath := pidFilePath()
	os.MkdirAll(filepath.Dir(pidPath), 0755)
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(pidPath)

	client := newClient(cfg)

	w, err := watcher.New(watcher.Config{
		SyncDir:      syncDir,
		ServerURL:    cfg.ServerURL,
		Client:       client,
		PollInterval: interval,
		Verbose:      verbose,
		Logger:       logger,
	})
	if err != nil {
		logger.Fatalf("Failed to start watcher: %v", err)
	}

	if logPath == "" {
		fmt.Printf("👁 Watching: %s ↔ %s\n", syncDir, cfg.ServerURL)
		fmt.Printf("   fsnotify: enabled, poll: every %s\n", interval)
		fmt.Println("   Press Ctrl+C to stop.")
	}

	if err := w.Run(); err != nil {
		logger.Fatalf("Watcher error: %v", err)
	}
}

func daemonize(logPath string) error {
	// Re-exec ourselves with --log and without --daemon
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find executable path: %w", err)
	}

	args := []string{execPath}
	for _, arg := range os.Args[1:] {
		if arg == "--daemon" || arg == "-d" {
			continue
		}
		args = append(args, arg)
	}
	args = append(args, "--log", logPath)

	// Open log file for the child
	os.MkdirAll(filepath.Dir(logPath), 0755)
	logFile, err := openLogFile(logPath)
	if err != nil {
		return err
	}

	attr := &os.ProcAttr{
		Dir:   ".",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, logFile, logFile},
	}

	proc, err := os.StartProcess(execPath, args, attr)
	if err != nil {
		logFile.Close()
		return fmt.Errorf("could not start daemon: %w", err)
	}

	fmt.Printf("👁 Daemon started (PID %d)\n", proc.Pid)
	fmt.Printf("   Log: %s\n", logPath)
	fmt.Printf("   Stop: izerop watch --stop\n")

	proc.Release()
	logFile.Close()
	return nil
}

func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

func defaultLogPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "izerop", "watch.log")
}

func pidFilePath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "izerop", "watch.pid")
}

func cmdWatchStop() {
	pidPath := pidFilePath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No running watcher found (no PID file)\n")
		os.Exit(1)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid PID file\n")
		os.Remove(pidPath)
		os.Exit(1)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Process %d not found\n", pid)
		os.Remove(pidPath)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "Could not stop process %d: %v\n", pid, err)
		os.Remove(pidPath)
		os.Exit(1)
	}

	os.Remove(pidPath)
	fmt.Printf("⏹ Stopped watcher (PID %d)\n", pid)
}

func cmdLogs() {
	// Usage: izerop logs [--tail <n>] [--follow]
	logPath := defaultLogPath()
	tail := 50
	follow := false

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--tail", "-n":
			if i+1 < len(os.Args) {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil {
					tail = n
				}
				i++
			}
		case "--follow", "-f":
			follow = true
		case "--path":
			if i+1 < len(os.Args) {
				logPath = os.Args[i+1]
				i++
			}
		}
	}

	if _, err := os.Stat(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "No log file found at %s\n", logPath)
		os.Exit(1)
	}

	if follow {
		// Use tail -f
		cmd := fmt.Sprintf("tail -n %d -f %s", tail, logPath)
		proc := execCommand(cmd)
		proc.Stdout = os.Stdout
		proc.Stderr = os.Stderr

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			if proc.Process != nil {
				proc.Process.Kill()
			}
		}()

		proc.Run()
	} else {
		cmd := fmt.Sprintf("tail -n %d %s", tail, logPath)
		proc := execCommand(cmd)
		proc.Stdout = os.Stdout
		proc.Stderr = os.Stderr
		proc.Run()
	}
}

func execCommand(cmd string) *exec.Cmd {
	return exec.Command("sh", "-c", cmd)
}

func cmdUpdate() {
	v := strings.TrimPrefix(version, "v")
	fmt.Printf("Current version: v%s\n", v)
	fmt.Println("Checking for updates...")

	release, err := updater.CheckForUpdate(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update check failed: %v\n", err)
		os.Exit(1)
	}

	if release == nil {
		fmt.Println("✅ Already up to date!")
		return
	}

	fmt.Printf("New version available: %s\n", release.TagName)

	asset := updater.FindAsset(release)
	if asset == nil {
		fmt.Fprintf(os.Stderr, "No binary available for your platform. Download manually:\n  %s\n", release.HTMLURL)
		os.Exit(1)
	}

	fmt.Printf("Downloading %s (%s)...\n", asset.Name, formatSize(asset.Size))

	if err := updater.DownloadAndReplace(asset); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Updated to %s! Restart to use the new version.\n", release.TagName)
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func printUsage() {
	v := strings.TrimPrefix(version, "v")
	fmt.Printf(`izerop-cli v%s — file sync client for izerop

Usage:
  izerop <command> [options]

Commands:
  login     Authenticate with izerop server
  status    Show connection and sync status
  sync      Sync local directory with server
  watch     Watch and sync (fsnotify + polling, --daemon for background)
  logs      View watch daemon logs (--follow, --tail N)
  push      Upload files to server
  pull      Download files from server
  ls        List remote files and directories
  rm        Delete a file or directory
  mv        Move/rename a file
  update    Self-update to latest release
  version   Print version
  help      Show this help

Options:
  --server URL    Override server URL (default: config or https://izerop.com)

Environment:
  IZEROP_SERVER_URL   Override server URL
  IZEROP_TOKEN        Override API token
  IZEROP_SYNC_DIR     Override sync directory

Precedence: --server flag > env vars > config file

`, v)
}
