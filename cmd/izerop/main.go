package main

import (
	"encoding/json"
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

// activeProfile is the profile used for this invocation.
// Defaults to the user's configured active profile (set via `izerop profile use <name>`).
var activeProfile string

func main() {
	// Save original args before any modification
	originalArgs = make([]string, len(os.Args))
	copy(originalArgs, os.Args)

	// Extract --server and --profile flags before command parsing
	args := os.Args[1:]
	var serverOverride string
	var filtered []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--server" && i+1 < len(args) {
			serverOverride = args[i+1]
			i++
		} else if len(args[i]) > 9 && args[i][:9] == "--server=" {
			serverOverride = args[i][9:]
		} else if args[i] == "--profile" && i+1 < len(args) {
			activeProfile = args[i+1]
			i++
		} else if len(args[i]) > 10 && args[i][:10] == "--profile=" {
			activeProfile = args[i][10:]
		} else {
			filtered = append(filtered, args[i])
		}
	}
	os.Args = append([]string{os.Args[0]}, filtered...)

	// If no --profile flag was given, use the configured default profile
	if activeProfile == "" {
		activeProfile = config.GetActiveProfile()
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg, err := config.LoadProfile(activeProfile)
	if err != nil && os.Args[1] != "login" && os.Args[1] != "version" && os.Args[1] != "help" && os.Args[1] != "profile" {
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
	case "reconcile":
		cmdReconcile(cfg)
	case "push":
		cmdPush(cfg)
	case "url":
		cmdURL(cfg)
	case "conflicts":
		cmdConflicts(cfg)
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
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "start":
				// izerop watch start [--all]
				for _, arg := range os.Args[3:] {
					if arg == "--all" {
						startAllWatchers()
						return
					}
				}
				// Single profile: treat as regular watch --daemon
				os.Args = append(os.Args[:2], append(os.Args[3:], "--daemon")...)
				cmdWatch(cfg)
				return
			case "stop":
				// izerop watch stop [--all]
				for _, arg := range os.Args[3:] {
					if arg == "--all" {
						stopAllWatchers()
						return
					}
				}
				cmdWatchStop()
				return
			case "status":
				cmdWatchStatus()
				return
			case "help", "--help", "-h":
				printCommandHelp("watch")
				return
			}
		}
		// Handle legacy flags
		for _, arg := range os.Args[2:] {
			if arg == "--stop" {
				cmdWatchStop()
				return
			}
			if arg == "--status" {
				cmdWatchStatus()
				return
			}
		}
		for _, arg := range os.Args[2:] {
			if arg == "--all" {
				startAllWatchers()
				return
			}
		}
		cmdWatch(cfg)
	case "logs":
		cmdLogs()
	case "update":
		cmdUpdate()
	case "profile":
		cmdProfile()
	case "client":
		cmdClient(cfg)
	case "help":
		if len(os.Args) > 2 {
			printCommandHelp(os.Args[2])
		} else {
			printUsage()
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func newClient(cfg *config.Config) *api.Client {
	client := api.NewClient(cfg.ServerURL, cfg.Token)
	client.ClientKey = cfg.EnsureClientKey(activeProfile)
	return client
}

func cmdStatus(cfg *config.Config) {
	profiles, _ := config.ListProfiles()
	if len(profiles) == 0 {
		profiles = []string{activeProfile}
	}

	for i, name := range profiles {
		if i > 0 {
			fmt.Println()
		}

		pcfg, err := config.LoadProfile(name)
		if err != nil {
			fmt.Printf("Profile: %s (error: %v)\n", name, err)
			continue
		}

		active := ""
		if name == activeProfile {
			active = " ★"
		}
		fmt.Printf("Profile: %s%s\n", name, active)
		fmt.Printf("Server:  %s\n", pcfg.ServerURL)
		if pcfg.SyncDir != "" {
			fmt.Printf("Sync:    %s\n", pcfg.SyncDir)
		}

		// Watcher status
		running, pid := getWatcherStatusForProfile(name)
		if running {
			fmt.Printf("Watcher: ✅ running (PID %d)\n", pid)
			if statInfo, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
				uptime := time.Since(statInfo.ModTime()).Truncate(time.Second)
				fmt.Printf("Uptime:  %s\n", uptime)
			}
		} else {
			fmt.Printf("Watcher: ⏹ not running\n")
		}

		// Remote stats
		if pcfg.Token != "" {
			client := api.NewClient(pcfg.ServerURL, pcfg.Token)
			status, err := client.GetSyncStatus()
			if err != nil {
				fmt.Printf("Remote:  error (%v)\n", err)
			} else {
				fmt.Printf("Files:   %d\n", status.FileCount)
				fmt.Printf("Dirs:    %d\n", status.DirectoryCount)
				fmt.Printf("Size:    %s\n", formatSize(status.TotalSize))
			}
		}

		// Local state
		if pcfg.SyncDir != "" {
			state, _ := sync.LoadState(name)
			fmt.Printf("Tracked: %d files, %d notes\n", len(state.Files), len(state.Notes))
		}
	}
}

// getWatcherStatusForProfile checks if a profile's watcher is running.
func getWatcherStatusForProfile(profile string) (bool, int) {
	pidPath := profilePIDPath(profile)
	data, err := os.ReadFile(pidPath)
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
		os.Remove(pidPath)
		return false, 0
	}

	return true, pid
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

	// Migrate legacy state file if needed
	sync.MigrateState(activeProfile, syncDir)

	// Load sync state
	state, _ := sync.LoadState(activeProfile)

	engine := sync.NewEngine(client, syncDir, state)
	engine.Verbose = verbose

	// Register/update client with server
	client.RegisterClient(cfg.EnsureClientKey(activeProfile), cfg.ClientName, config.Platform(), version)

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
	if err := sync.SaveState(activeProfile, state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save sync state: %v\n", err)
	}

	fmt.Println("✅ Sync complete")
}

func cmdReconcile(cfg *config.Config) {
	// Usage: izerop reconcile [<directory>] [--dry-run] [--verbose]
	syncDir := cfg.SyncDir
	dryRun := false
	verbose := false

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--dry-run", "-n":
			dryRun = true
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

	client := newClient(cfg)
	sync.MigrateState(activeProfile, syncDir)
	state, _ := sync.LoadState(activeProfile)

	engine := sync.NewEngine(client, syncDir, state)
	engine.Verbose = verbose

	if dryRun {
		fmt.Printf("Reconcile (dry run): %s ↔ %s\n", syncDir, cfg.ServerURL)
	} else {
		fmt.Printf("Reconciling: %s ↔ %s\n", syncDir, cfg.ServerURL)
	}

	fmt.Println("📋 Fetching server manifest...")
	result, err := engine.Reconcile(dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Reconcile error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n  Downloaded: %d\n  Uploaded:   %d\n  Deleted:    %d\n  Conflicts:  %d\n  Skipped:    %d\n",
		result.Downloaded, result.Uploaded, result.Deleted, result.Conflicts, result.Skipped)
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "  ⚠ %s\n", e)
	}

	if !dryRun {
		if err := sync.SaveState(activeProfile, state); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save state: %v\n", err)
		}
	}

	if dryRun {
		fmt.Println("\n🔍 Dry run complete (no changes made)")
	} else {
		fmt.Println("\n✅ Reconcile complete")
	}
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

func cmdConflicts(cfg *config.Config) {
	// Usage: izerop conflicts [--clean] [--keep-local|--keep-remote]
	syncDir := cfg.SyncDir
	if syncDir == "" {
		syncDir = "."
	}
	absDir, err := filepath.Abs(syncDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory: %v\n", err)
		os.Exit(1)
	}

	clean := false
	keepLocal := false
	keepRemote := false

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--clean":
			clean = true
		case "--keep-local":
			keepLocal = true
		case "--keep-remote":
			keepRemote = true
		default:
			if !strings.HasPrefix(os.Args[i], "--") {
				absDir, _ = filepath.Abs(os.Args[i])
			}
		}
	}

	// Find all conflict files
	var conflicts []string
	filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") && info.IsDir() {
			return filepath.SkipDir
		}
		if strings.Contains(info.Name(), ".conflict") {
			rel, _ := filepath.Rel(absDir, path)
			conflicts = append(conflicts, rel)
		}
		return nil
	})

	if len(conflicts) == 0 {
		fmt.Println("No conflict files found. ✅")
		return
	}

	fmt.Printf("Found %d conflict file(s):\n\n", len(conflicts))
	for _, c := range conflicts {
		// Figure out the original file name
		original := strings.Replace(c, ".conflict", "", 1)
		fmt.Printf("  ⚠ %s\n    original: %s\n", c, original)
	}

	if !clean {
		fmt.Println("\nTo resolve:")
		fmt.Println("  izerop conflicts --clean              # delete all conflict files (keep originals)")
		fmt.Println("  izerop conflicts --clean --keep-local  # keep local version, delete conflict copies")
		fmt.Println("  izerop conflicts --clean --keep-remote # keep conflict (remote) version, replace originals")
		return
	}

	removed := 0
	for _, c := range conflicts {
		conflictPath := filepath.Join(absDir, c)

		if keepRemote {
			// The conflict file is the remote version — replace original with it
			original := strings.Replace(c, ".conflict", "", 1)
			originalPath := filepath.Join(absDir, original)
			if err := os.Rename(conflictPath, originalPath); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ Could not replace %s: %v\n", original, err)
				continue
			}
			fmt.Printf("  ✅ Replaced with remote: %s\n", original)
			removed++
		} else if keepLocal || (!keepLocal && !keepRemote) {
			// Default: keep original, delete conflict file
			if err := os.Remove(conflictPath); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ Could not remove %s: %v\n", c, err)
				continue
			}
			fmt.Printf("  🗑 Removed: %s\n", c)
			removed++
		}
	}

	fmt.Printf("\n✅ Resolved %d conflict(s)\n", removed)
}

func cmdURL(cfg *config.Config) {
	// Usage: izerop url <file>
	// Resolves a local file path to its remote URL via the sync state or by searching remote files.
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: izerop url <file>\n")
		os.Exit(1)
	}

	filePath := os.Args[2]

	// Resolve to absolute path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid path: %v\n", err)
		os.Exit(1)
	}

	client := newClient(cfg)

	// Try to find via sync state first (faster, no API calls for ID lookup)
	syncDir := cfg.SyncDir
	if syncDir != "" {
		absSyncDir, _ := filepath.Abs(syncDir)
		if strings.HasPrefix(absPath, absSyncDir+"/") {
			relPath, _ := filepath.Rel(absSyncDir, absPath)
			state, _ := sync.LoadState(activeProfile)

			// Check Files state
			if rec, ok := state.Files[relPath]; ok && rec.RemoteID != "" {
				file, err := client.GetFile(rec.RemoteID)
				if err == nil && file.URL != "" {
					fmt.Println(file.URL)
					return
				}
				// If URL not available, fall through to show the download endpoint
				if err == nil {
					fmt.Printf("%s/api/v1/files/%s/download\n", cfg.ServerURL, rec.RemoteID)
					return
				}
			}

			// Check Notes state
			if noteID, ok := state.Notes[relPath]; ok {
				file, err := client.GetFile(noteID)
				if err == nil && file.URL != "" {
					fmt.Println(file.URL)
					return
				}
				if err == nil {
					fmt.Printf("%s/api/v1/files/%s/download\n", cfg.ServerURL, noteID)
					return
				}
			}
		}
	}

	// Fallback: search remote files by name
	fileName := filepath.Base(absPath)
	dirs, err := client.ListDirectories()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, dir := range dirs {
		files, err := client.ListFiles(dir.ID)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Name == fileName {
				if f.URL != "" {
					fmt.Println(f.URL)
				} else {
					fmt.Printf("%s/api/v1/files/%s/download\n", cfg.ServerURL, f.ID)
				}
				return
			}
		}
	}

	fmt.Fprintf(os.Stderr, "File not found on server: %s\n", fileName)
	os.Exit(1)
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
		case "--daemon", "-d", "--background":
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

	// Check if a watcher is already running for this profile
	if running, pid := getWatcherStatusForProfile(activeProfile); running {
		fmt.Fprintf(os.Stderr, "⚠ Watcher already running for profile %q (PID %d)\n", activeProfile, pid)
		fmt.Fprintf(os.Stderr, "   Stop it first: izerop --profile %s watch --stop\n", activeProfile)
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

	// Write PID file and daemon args
	pidPath := pidFilePath()
	os.MkdirAll(filepath.Dir(pidPath), 0755)
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(pidPath)

	// Save watch args for restart after update
	watchArgs := os.Args[1:] // everything after the binary name
	argsData, _ := json.Marshal(watchArgs)
	os.WriteFile(watchArgsPath(), argsData, 0644)
	defer os.Remove(watchArgsPath())

	client := newClient(cfg)

	settleTime := time.Duration(cfg.SettleTimeMs) * time.Millisecond

	w, err := watcher.New(watcher.Config{
		Profile:      activeProfile,
		SyncDir:      syncDir,
		ServerURL:    cfg.ServerURL,
		Client:       client,
		PollInterval: interval,
		SettleTime:   settleTime,
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

// originalArgs stores the full os.Args before --server extraction.
var originalArgs []string

func daemonize(logPath string) error {
	// Re-exec ourselves with --log and without --daemon
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find executable path: %w", err)
	}

	// Use original args (before --server was stripped) to preserve all flags
	srcArgs := originalArgs
	if len(srcArgs) == 0 {
		srcArgs = os.Args
	}

	args := []string{execPath}
	hasProfile := false
	for _, arg := range srcArgs[1:] {
		if arg == "--daemon" || arg == "-d" || arg == "--background" {
			continue
		}
		if arg == "--profile" {
			hasProfile = true
		}
		args = append(args, arg)
	}
	// Always inject --profile so the daemon uses the correct profile
	// even if the active profile changes later
	if !hasProfile {
		args = append(args, "--profile", activeProfile)
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
	return profileLogPath(activeProfile)
}

func pidFilePath() string {
	return profilePIDPath(activeProfile)
}

func watchArgsPath() string {
	dir, _ := config.ProfileDir(activeProfile)
	return filepath.Join(dir, "watch.args.json")
}

func profileLogPath(profile string) string {
	p, err := config.ProfileLogPath(profile)
	if err != nil {
		dir, _ := os.UserConfigDir()
		return filepath.Join(dir, "izerop", "watch.log")
	}
	return p
}

func profilePIDPath(profile string) string {
	p, err := config.ProfilePIDPath(profile)
	if err != nil {
		dir, _ := os.UserConfigDir()
		return filepath.Join(dir, "izerop", "watch.pid")
	}
	return p
}

func cmdWatchStop() {
	// If --all flag, stop all profile watchers
	for _, arg := range os.Args[2:] {
		if arg == "--all" {
			stopAllWatchers()
			return
		}
	}

	pidPath := pidFilePath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No running watcher found for profile %q\n", activeProfile)
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
	fmt.Printf("⏹ Stopped watcher for %q (PID %d)\n", activeProfile, pid)
}

func stopAllWatchers() {
	profiles, _ := config.ListProfiles()
	stopped := 0
	for _, name := range profiles {
		running, pid := getWatcherStatusForProfile(name)
		if running {
			proc, _ := os.FindProcess(pid)
			if err := proc.Signal(syscall.SIGTERM); err == nil {
				pidPath := profilePIDPath(name)
				os.Remove(pidPath)
				fmt.Printf("⏹ Stopped %q (PID %d)\n", name, pid)
				stopped++
			}
		}
	}
	if stopped == 0 {
		fmt.Println("No running watchers found.")
	}
}

func startAllWatchers() {
	profiles, _ := config.ListProfiles()
	started := 0
	skipped := 0

	for _, name := range profiles {
		pcfg, err := config.LoadProfile(name)
		if err != nil || pcfg.SyncDir == "" {
			fmt.Printf("  ⏭ %s (no sync dir configured)\n", name)
			skipped++
			continue
		}

		if running, pid := getWatcherStatusForProfile(name); running {
			fmt.Printf("  ✅ %s already running (PID %d)\n", name, pid)
			skipped++
			continue
		}

		// Launch daemon for this profile
		execPath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: could not find executable: %v\n", name, err)
			continue
		}

		cmd := exec.Command(execPath, "--profile", name, "watch", "--daemon")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: failed to start: %v\n", name, err)
			continue
		}
		started++
	}

	if started == 0 && skipped == 0 {
		fmt.Println("No profiles configured. Run 'izerop profile add <name>' first.")
	} else {
		fmt.Printf("\n🎯 Started %d, skipped %d\n", started, skipped)
	}
}

func cmdWatchStatus() {
	profiles, _ := config.ListProfiles()
	if len(profiles) == 0 {
		fmt.Println("No profiles configured.")
		return
	}

	fmt.Println("Watcher Status:")
	for _, name := range profiles {
		running, pid := getWatcherStatusForProfile(name)
		pcfg, _ := config.LoadProfile(name)
		syncDir := ""
		if pcfg != nil {
			syncDir = pcfg.SyncDir
		}

		if running {
			uptime := ""
			if statInfo, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
				uptime = fmt.Sprintf(", uptime %s", time.Since(statInfo.ModTime()).Truncate(time.Second))
			}
			fmt.Printf("  ✅ %-15s  PID %d%s  %s\n", name, pid, uptime, syncDir)
		} else {
			status := "⏹ not running"
			if syncDir == "" {
				status = "⏭ no sync dir"
			}
			fmt.Printf("  %s %-15s  %s\n", status, name, syncDir)
		}
	}
}

func cmdClient(cfg *config.Config) {
	if cfg == nil {
		fmt.Fprintf(os.Stderr, "Not logged in. Run 'izerop login' first.\n")
		os.Exit(1)
	}

	client := newClient(cfg)
	clientKey := cfg.EnsureClientKey(activeProfile)

	if len(os.Args) < 3 {
		// Show current client info
		info, err := client.RegisterClient(clientKey, cfg.ClientName, config.Platform(), version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Client Key:  %s\n", info.ClientKey)
		fmt.Printf("Name:        %s\n", info.Name)
		fmt.Printf("Platform:    %s\n", info.Platform)
		fmt.Printf("Version:     %s\n", info.Version)
		fmt.Printf("Last Seen:   %s\n", info.LastSeenAt)
		return
	}

	switch os.Args[2] {
	case "name":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: izerop client name <name>\n")
			os.Exit(1)
		}
		name := strings.Join(os.Args[3:], " ")
		cfg.ClientName = name
		config.SaveProfile(activeProfile, cfg)

		info, err := client.RegisterClient(clientKey, name, config.Platform(), version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error updating server: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Client named %q\n", info.Name)
	case "register":
		info, err := client.RegisterClient(clientKey, cfg.ClientName, config.Platform(), version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Client registered: %s (%s)\n", info.Name, info.ClientKey)
	default:
		fmt.Fprintf(os.Stderr, "Unknown client command: %s\n", os.Args[2])
		fmt.Fprintf(os.Stderr, "Usage: izerop client [name <name>|register]\n")
		os.Exit(1)
	}
}

func cmdProfile() {
	if len(os.Args) < 3 {
		// Default: list profiles
		cmdProfileList()
		return
	}

	switch os.Args[2] {
	case "list", "ls":
		cmdProfileList()
	case "add", "create":
		cmdProfileAdd()
	case "remove", "rm", "delete":
		cmdProfileRemove()
	case "use", "switch":
		cmdProfileUse()
	default:
		fmt.Fprintf(os.Stderr, "Unknown profile command: %s\n", os.Args[2])
		fmt.Fprintf(os.Stderr, "Usage: izerop profile [list|add|remove|use]\n")
		os.Exit(1)
	}
}

func cmdProfileList() {
	profiles, err := config.ListProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Println("No profiles configured. Run 'izerop profile add <name>' to create one.")
		return
	}
	current := config.GetActiveProfile()
	for _, name := range profiles {
		marker := "  "
		if name == current {
			marker = "★ "
		}
		pcfg, _ := config.LoadProfile(name)
		server := ""
		if pcfg != nil {
			server = pcfg.ServerURL
		}
		running, _ := getWatcherStatusForProfile(name)
		status := "⏹"
		if running {
			status = "✅"
		}
		fmt.Printf("%s%s %s  %s\n", marker, name, status, server)
	}
}

func cmdProfileAdd() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: izerop profile add <name> [--server <url>] [--token <token>] [--sync-dir <path>]\n")
		os.Exit(1)
	}
	name := os.Args[3]

	cfg := &config.Config{
		ServerURL: "https://izerop.com",
	}

	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--server":
			if i+1 < len(os.Args) {
				cfg.ServerURL = os.Args[i+1]
				i++
			}
		case "--token":
			if i+1 < len(os.Args) {
				cfg.Token = os.Args[i+1]
				i++
			}
		case "--sync-dir":
			if i+1 < len(os.Args) {
				cfg.SyncDir = os.Args[i+1]
				i++
			}
		}
	}

	if err := config.SaveProfile(name, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating profile: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Profile %q created\n", name)
	if cfg.Token == "" {
		fmt.Printf("   Set token: izerop --profile %s login\n", name)
	}
}

func cmdProfileRemove() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: izerop profile remove <name>\n")
		os.Exit(1)
	}
	name := os.Args[3]

	// Stop watcher if running
	if running, _ := getWatcherStatusForProfile(name); running {
		fmt.Fprintf(os.Stderr, "Stop the watcher first: izerop --profile %s watch --stop\n", name)
		os.Exit(1)
	}

	if err := config.DeleteProfile(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🗑 Profile %q removed\n", name)

	// If we removed the active profile, switch to default
	if config.GetActiveProfile() == name {
		config.SetActiveProfile(config.DefaultProfile)
	}
}

func cmdProfileUse() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: izerop profile use <name>\n")
		os.Exit(1)
	}
	name := os.Args[3]

	// Verify profile exists
	if _, err := config.LoadProfile(name); err != nil {
		fmt.Fprintf(os.Stderr, "Profile %q not found\n", name)
		os.Exit(1)
	}

	if err := config.SetActiveProfile(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("★ Active profile: %s\n", name)
}

func cmdLogs() {
	// Usage: izerop logs [--tail <n>] [--follow] [--profile <name>]
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
		args := []string{"-n", strconv.Itoa(tail), "-f", logPath}
		proc := exec.Command("tail", args...)
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
		args := []string{"-n", strconv.Itoa(tail), logPath}
		proc := exec.Command("tail", args...)
		proc.Stdout = os.Stdout
		proc.Stderr = os.Stderr
		proc.Run()
	}
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

	fmt.Printf("✅ Updated to %s!\n", release.TagName)

	// Restart daemon if running
	pidPath := pidFilePath()
	if data, err := os.ReadFile(pidPath); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					// Daemon is running — stop it
					fmt.Printf("Restarting watcher daemon (PID %d)...\n", pid)
					proc.Signal(syscall.SIGTERM)
					// Wait briefly for it to stop
					time.Sleep(1 * time.Second)
					os.Remove(pidPath)

					// Re-launch with saved watch args
					execPath, _ := os.Executable()
					watchArgs := []string{"watch", "--daemon"}
					if argsData, err := os.ReadFile(watchArgsPath()); err == nil {
						var savedArgs []string
						if json.Unmarshal(argsData, &savedArgs) == nil && len(savedArgs) > 0 {
							// Ensure --daemon is present
							hasDaemon := false
							for _, a := range savedArgs {
								if a == "--daemon" || a == "-d" || a == "--background" {
									hasDaemon = true
								}
							}
							if !hasDaemon {
								savedArgs = append(savedArgs, "--daemon")
							}
							watchArgs = savedArgs
						}
					}
					newProc := exec.Command(execPath, watchArgs...)
					newProc.Stdout = os.Stdout
					newProc.Stderr = os.Stderr
					if err := newProc.Run(); err != nil {
						fmt.Fprintf(os.Stderr, "⚠ Could not restart daemon: %v\n", err)
						fmt.Fprintf(os.Stderr, "  Start manually: izerop watch <dir> --daemon\n")
					}
				}
			}
		}
	}
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

func printCommandHelp(cmd string) {
	help := map[string]string{
		"login": `izerop login

  Authenticate with an izerop server. Prompts for server URL and API token.
  Config is saved to ~/.config/izerop/config.json.

  Examples:
    izerop login
    izerop --server http://localhost:3000 login`,

		"status": `izerop status

  Show server connection, file/directory counts, storage usage, and sync cursor.

  Examples:
    izerop status
    izerop --server http://localhost:3000 status`,

		"sync": `izerop sync [<directory>] [options]

  Run a one-shot bidirectional sync between a local directory and the server.
  Downloads remote changes first, then uploads local changes.

  Options:
    --pull-only    Only download remote changes
    --push-only    Only upload local changes
    -v, --verbose  Show detailed output

  Ignore patterns:
    Create a .izeropignore file in the sync directory to skip files/dirs.
    Works like .gitignore — supports globs, directory patterns, and negation.

    Example .izeropignore:
      build/          # skip entire directory
      *.log           # skip by extension
      secret.env      # skip specific file
      !important.log  # un-ignore a file

  Examples:
    izerop sync                    # sync current directory
    izerop sync ~/izerop           # sync a specific directory
    izerop sync --pull-only        # download only
    izerop sync ~/izerop -v        # verbose output`,

		"watch": `izerop watch <subcommand|directory> [options]

  Watch a directory and sync continuously. Uses fsnotify for instant local
  change detection and periodic server polling for remote changes.

  Each profile runs its own independent watcher with separate PID and log files.
  You can run multiple profile watchers simultaneously.

  Subcommands:
    start [--all]    Start watcher daemon (all profiles with --all)
    stop [--all]     Stop watcher daemon (all profiles with --all)
    status           Show watcher status for all profiles
    help             Show this help

  Options (for direct watch):
    --interval N   Server poll interval in seconds (default: 30)
    -d, --daemon   Run in background (writes PID file)
    --log <path>   Log file path (default: ~/.config/izerop/profiles/<name>/watch.log)
    -v, --verbose  Log every poll tick, not just changes

  Examples:
    izerop watch                          # watch current dir (foreground)
    izerop watch ~/izerop --daemon        # run in background
    izerop watch --interval 10            # poll every 10s

    izerop watch start                    # start daemon for current profile
    izerop watch start --all              # start daemons for all profiles
    izerop watch stop                     # stop current profile watcher
    izerop watch stop --all               # stop all watchers
    izerop watch status                   # show all watcher statuses

  Multi-profile:
    izerop --profile default watch start       # start default watcher
    izerop --profile ranger watch start        # start ranger watcher
    izerop --profile ranger watch stop         # stop ranger only`,

		"client": `izerop client [subcommand]

  View or name this sync client. Each device gets a unique key on first use.
  The client name is shown in the file explorer so you know which device
  uploaded each file.

  Subcommands:
    (none)          Show current client info
    name <name>     Set a friendly name for this device
    register        Register/update this client with the server

  Examples:
    izerop client                          # show client info
    izerop client name "Patrick's Laptop"  # name this device
    izerop client name "Work Desktop"      # rename it`,

		"profile": `izerop profile <subcommand>

  Manage multiple profiles. Each profile has its own server, token, sync
  directory, and watcher. Set a default profile so you don't need --profile
  on every command.

  Subcommands:
    list              List all profiles (active profile marked with ★)
    add <name>        Create a new profile
    remove <name>     Delete a profile
    use <name>        Set the active (default) profile

  The active profile is used when no --profile flag is given.

  Config: ~/.config/izerop/profiles/<name>/config.json
  State:  ~/.config/izerop/profiles/<name>/sync-state.json

  Examples:
    izerop profile list                    # show all profiles
    izerop profile add ranger              # create "ranger" profile
    izerop --profile ranger login          # authenticate ranger
    izerop profile use ranger              # make ranger the default
    izerop sync                            # syncs using ranger (active)
    izerop --profile default sync          # explicitly use default
    izerop profile remove ranger           # delete ranger profile`,

		"logs": `izerop logs [options]

  View the watch daemon's log output.

  Options:
    -n, --tail N     Number of lines to show (default: 50)
    -f, --follow     Follow log output (like tail -f)
    --path <file>    Use a custom log file path

  Examples:
    izerop logs                   # last 50 lines
    izerop logs --tail 100        # last 100 lines
    izerop logs --follow          # tail -f style`,

		"reconcile": `izerop reconcile [<directory>] [options]

  Full reconciliation using the server manifest as source of truth.
  Compares every remote file against local files and resolves differences.

  - Remote files missing locally → download
  - Local files missing on remote (and previously tracked) → delete locally
  - Local files not on remote (untracked) → upload
  - Hash mismatch → server wins (local saved as .conflict if modified)

  Use --dry-run to preview changes without modifying anything.

  Options:
    -n, --dry-run  Preview what would change without doing it
    -v, --verbose  Show detailed output

  Examples:
    izerop reconcile                   # full reconcile of sync dir
    izerop reconcile --dry-run         # preview only
    izerop reconcile ~/izerop -v       # verbose, specific dir`,

		"push": `izerop push <file> [options]

  Upload a file to the server.

  Options:
    --dir <id>     Target directory ID
    --name <name>  Override the filename on the server

  Examples:
    izerop push photo.jpg --dir abc123
    izerop push IMG_001.jpg --dir abc123 --name vacation.jpg`,

		"conflicts": `izerop conflicts [options]

  List and resolve conflict files in the sync directory.

  When both local and remote versions of a file change simultaneously,
  the sync engine saves the other version as a .conflict file. This command
  helps you find and clean them up.

  Options:
    --clean          Remove conflict files (default: keep originals)
    --keep-local     Keep your local version, delete conflict copies (default)
    --keep-remote    Replace originals with the remote (conflict) version

  Examples:
    izerop conflicts                          # list all conflicts
    izerop conflicts --clean                  # delete all .conflict files
    izerop conflicts --clean --keep-remote    # use remote versions instead`,

		"url": `izerop url <file>

  Get the direct asset URL for a synced file. Looks up the file in your sync
  state first (fast), then falls back to searching by filename on the server.

  Output is just the URL — pipe-friendly for scripts.

  Examples:
    izerop url photo.jpg                      # from current directory
    izerop url ~/izerop/docs/readme.md        # absolute path
    izerop push photo.jpg && izerop url photo.jpg   # push then get URL`,

		"pull": `izerop pull <file-id> [options]

  Download a file by ID.

  Options:
    --out <path>   Save to a specific local path (default: auto-named)

  Examples:
    izerop pull abc123                   # auto-named from server
    izerop pull abc123 --out photo.jpg   # save to specific path`,

		"ls": `izerop ls [<directory-id>]

  List remote directories and files with names, sizes, timestamps, and IDs.

  Examples:
    izerop ls              # list all directories and files
    izerop ls abc123       # list files in a specific directory`,

		"mkdir": `izerop mkdir <name> [options]

  Create a remote directory.

  Options:
    --parent <id>  Parent directory ID (for subdirectories)

  Examples:
    izerop mkdir photos                        # top-level directory
    izerop mkdir thumbnails --parent abc123     # subdirectory`,

		"rm": `izerop rm <id> [options]

  Delete a file or directory (soft-delete on server).

  Options:
    --dir   Treat the ID as a directory (default: file)

  Examples:
    izerop rm abc123           # delete a file
    izerop rm abc123 --dir     # delete a directory`,

		"mv": `izerop mv <file-id> [options]

  Move or rename a file.

  Options:
    --name <name>  New filename
    --dir <id>     Move to a different directory

  Examples:
    izerop mv abc123 --name new-name.txt
    izerop mv abc123 --dir def456
    izerop mv abc123 --name new-name.txt --dir def456`,

		"update": `izerop update

  Self-update to the latest GitHub release. Downloads the correct binary
  for your OS and architecture, then replaces the current executable.

  Examples:
    izerop update`,

		"version": `izerop version

  Print the current version.`,
	}

	if h, ok := help[cmd]; ok {
		fmt.Println(h)
	} else {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
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
  reconcile Full reconcile using server manifest (recovery/verification)
  watch     Watch and sync (fsnotify + polling, --daemon for background)
  logs      View watch daemon logs (--follow, --tail N)
  push      Upload files to server
  url       Get the direct asset URL for a file
  conflicts List and resolve conflict files
  pull      Download files from server
  ls        List remote files and directories
  rm        Delete a file or directory
  mv        Move/rename a file
  client    Name this device for sync tracking
  profile   Manage profiles (list, add, remove, use)
  update    Self-update to latest release
  version   Print version
  help      Show this help

Profile Commands:
  profile list                  List all profiles
  profile add <name> [opts]     Create a profile (--server, --token, --sync-dir)
  profile remove <name>         Delete a profile
  profile use <name>            Set active profile

Options:
  --server URL      Override server URL
  --profile NAME    Use a specific profile (default: active profile)

Environment:
  IZEROP_SERVER_URL   Override server URL
  IZEROP_TOKEN        Override API token
  IZEROP_SYNC_DIR     Override sync directory

Precedence: --server flag > env vars > config file

`, v)
}
