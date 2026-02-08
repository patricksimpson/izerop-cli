package main

import (
	"fmt"
	"io"
	"os"

	"github.com/patricksimpson/izerop-cli/internal/auth"
	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/config"
)

const version = "0.1.0"

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
		fmt.Printf("izerop-cli v%s\n", version)
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

func cmdSync(_ *config.Config) {
	fmt.Println("Sync not yet implemented")
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
	if len(os.Args) > 2 {
		dirID = os.Args[2]
	}

	// List directories first
	if dirID == "" {
		dirs, err := client.ListDirectories()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing directories: %v\n", err)
			os.Exit(1)
		}
		for _, d := range dirs {
			fmt.Printf("  📁 %-30s  %s\n", d.Name+"/", d.ID)
		}
	}

	// List files
	files, err := client.ListFiles(dirID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing files: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 && dirID != "" {
		fmt.Println("No files found.")
		return
	}

	for _, f := range files {
		size := formatSize(f.Size)
		fmt.Printf("  📄 %-30s  %8s  %s  %s\n", f.Name, size, f.UpdatedAt, f.ID)
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

func printUsage() {
	fmt.Printf(`izerop-cli v%s — file sync client for izerop

Usage:
  izerop <command> [options]

Commands:
  login     Authenticate with izerop server
  status    Show connection and sync status
  sync      Sync local directory with server
  push      Upload files to server
  pull      Download files from server
  ls        List remote files and directories
  version   Print version
  help      Show this help

Options:
  --server URL    Override server URL (default: config or https://izerop.com)

Environment:
  IZEROP_SERVER_URL   Override server URL
  IZEROP_TOKEN        Override API token
  IZEROP_SYNC_DIR     Override sync directory

Precedence: --server flag > env vars > config file

`, version)
}
