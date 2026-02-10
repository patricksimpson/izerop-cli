# izerop-cli

A command-line file sync client for [izerop](https://izerop.com). Syncs files between your local machine and your izerop server with real-time file watching, conflict detection, and background daemon support.

## Install

### From Source

```bash
git clone https://github.com/patricksimpson/izerop-cli.git
cd izerop-cli
make install
```

### From Release

Download the latest binary for your platform:

| Platform | CLI | Desktop |
|----------|-----|---------|
| Linux x64 | [izerop-linux-amd64](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-linux-amd64) | [izerop-desktop-linux-amd64](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-desktop-linux-amd64) |
| Linux ARM64 | [izerop-linux-arm64](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-linux-arm64) | — |
| macOS Intel | [izerop-darwin-amd64](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-darwin-amd64) | [izerop-desktop-macos-amd64.zip](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-desktop-macos-amd64.zip) |
| macOS Apple Silicon | [izerop-darwin-arm64](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-darwin-arm64) | [izerop-desktop-macos-arm64.zip](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-desktop-macos-arm64.zip) |
| Windows x64 | [izerop-windows-amd64.exe](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-windows-amd64.exe) | [izerop-desktop-windows-amd64.exe](https://github.com/patricksimpson/izerop-cli/releases/latest/download/izerop-desktop-windows-amd64.exe) |

Then for the CLI:

```bash
chmod +x izerop-linux-amd64
mv izerop-linux-amd64 ~/.local/bin/izerop
```

### Self-Update

```bash
izerop update
```

## Quick Start

```bash
# 1. Log in
izerop login

# 2. Check connection
izerop status

# 3. List your files
izerop ls

# 4. Start syncing a folder
izerop watch ~/izerop
```

## Commands

### `login`

Authenticate with an izerop server. Prompts for server URL and API token.

```bash
izerop login
```

Config is saved to `~/.config/izerop/profiles/<name>/config.json`.

### `status`

Show connection info, file/directory counts, and storage usage.

```bash
izerop status
```

### `ls`

List remote directories and files with names, sizes, timestamps, and IDs.

```bash
# List everything
izerop ls

# List files in a specific directory
izerop ls <directory-id>
```

### `sync`

Run a one-shot bidirectional sync between a local directory and the server.

```bash
# Sync current directory
izerop sync

# Sync a specific directory
izerop sync ~/izerop

# Pull only (no uploads)
izerop sync --pull-only

# Push only (no downloads)
izerop sync --push-only

# Verbose output
izerop sync -v
```

### `watch`

Watch a directory and sync continuously. Combines **fsnotify** for instant local change detection with periodic server polling for remote changes.

```bash
# Watch current directory (foreground)
izerop watch

# Watch a specific directory
izerop watch ~/izerop

# Custom poll interval (default: 30s)
izerop watch --interval 10

# Verbose — log every poll tick
izerop watch -v
```

#### Daemon Mode

Run the watcher in the background:

```bash
# Start as daemon
izerop watch ~/izerop --daemon

# Stop the daemon
izerop watch --stop

# Stop all profile watchers
izerop watch --stop --all

# Custom log file location
izerop watch ~/izerop --daemon --log /path/to/watch.log
```

Default log location: `~/.config/izerop/profiles/<name>/watch.log`

### `logs`

View the watch daemon's log output.

```bash
# Last 50 lines (default)
izerop logs

# Last 100 lines
izerop logs --tail 100

# Follow live (like tail -f)
izerop logs --follow
```

### `push`

Upload a file to the server.

```bash
# Upload to a directory
izerop push photo.jpg --dir <directory-id>

# Upload with a custom name
izerop push IMG_001.jpg --dir <directory-id> --name vacation.jpg
```

### `pull`

Download a file by ID.

```bash
# Download (auto-names from server)
izerop pull <file-id>

# Download to a specific path
izerop pull <file-id> --out photo.jpg
```

### `mkdir`

Create a remote directory.

```bash
# Create a top-level directory
izerop mkdir photos

# Create a subdirectory
izerop mkdir thumbnails --parent <directory-id>
```

### `rm`

Delete a file or directory (soft-delete on server).

```bash
# Delete a file
izerop rm <file-id>

# Delete a directory
izerop rm <directory-id> --dir
```

### `mv`

Move or rename a file.

```bash
# Rename a file
izerop mv <file-id> --name new-name.txt

# Move to a different directory
izerop mv <file-id> --dir <directory-id>

# Both at once
izerop mv <file-id> --name new-name.txt --dir <directory-id>
```

### `update`

Self-update to the latest GitHub release. Downloads the correct binary for your OS and architecture, then replaces the current executable.

```bash
izerop update
```

### `version`

```bash
izerop version
```

## Profiles

Profiles let you manage multiple izerop accounts or servers. Each profile has its own server URL, API token, sync directory, state file, and watcher process.

### Managing Profiles

```bash
# List all profiles (active marked with ★)
izerop profile list

# Create a new profile
izerop profile add ranger

# Authenticate a profile
izerop --profile ranger login

# Set the active (default) profile
izerop profile use ranger

# Delete a profile
izerop profile remove ranger
```

### Using Profiles

The **active profile** is used when no `--profile` flag is given:

```bash
# Set ranger as default
izerop profile use ranger

# These all use ranger now
izerop sync
izerop ls
izerop watch --daemon

# Explicitly use a different profile
izerop --profile default sync
```

### Running Multiple Watchers

Each profile runs its own independent watcher. You can run them simultaneously:

```bash
# Start watchers for two profiles
izerop --profile default watch --daemon
izerop --profile ranger watch --daemon

# Check status — shows both watchers
izerop status

# Stop one
izerop --profile ranger watch --stop

# Stop all
izerop watch --stop --all
```

Each watcher has its own PID file, log file, and sync state stored under `~/.config/izerop/profiles/<name>/`.

## Configuration

### Config File

Each profile's config is stored at `~/.config/izerop/profiles/<name>/config.json`:

```json
{
  "server_url": "https://izerop.com",
  "token": "your-jwt-token",
  "sync_dir": "~/izerop"
}
```

### Environment Variables

| Variable | Description |
|---|---|
| `IZEROP_SERVER_URL` | Override server URL |
| `IZEROP_TOKEN` | Override API token |
| `IZEROP_SYNC_DIR` | Override default sync directory |

### Server Override

```bash
# Via flag (works with any command)
izerop --server http://localhost:3000 ls

# Via env
export IZEROP_SERVER_URL=http://localhost:3000
izerop ls
```

**Precedence:** `--server` flag → env vars → config file → `https://izerop.com`

## Sync Behavior

### How It Works

- **Push:** Walks your local directory, compares against remote files by path and size, uploads new or changed files
- **Pull:** Uses a cursor-based changes API to fetch only what's new since the last sync
- **Watch:** Combines fsnotify (instant local detection) with periodic server polling (remote changes)

### Conflict Detection

When both local and remote versions of a file change between syncs:

- The **winning** version overwrites the file
- The **losing** version is saved as `filename.conflict.ext`
- Conflict files are skipped during push (won't re-upload)

Review `.conflict` files manually and delete them when resolved.

### State File

Sync state is stored at `~/.config/izerop/profiles/<name>/sync-state.json`. This tracks:

- Server cursor (for incremental pull)
- File records (size, mod time, remote timestamp for conflict detection)
- Note mappings (text files synced via contents API)

Don't delete this file unless you want a full re-sync.

> **Note:** Older versions stored state as `.izerop-sync.json` inside the sync directory. The CLI automatically migrates this to the config directory on first run.

### What Gets Synced

- All files in the sync directory (recursively)
- Directories are mirrored on the server under a `root` directory
- Hidden files/dirs (starting with `.`) are skipped
- Temp files (`.swp`, `~` suffix) are skipped

## Local Development

Point at a local Rails server:

```bash
export IZEROP_SERVER_URL=http://localhost:3000
export IZEROP_TOKEN=your-local-jwt
izerop status
```

Or use the `--server` flag:

```bash
izerop --server http://localhost:3000 ls
```

## Project Structure

```
cmd/izerop/        CLI entrypoint
pkg/api/           API client (reusable library)
pkg/sync/          Sync engine (reusable library)
pkg/config/        Configuration management
pkg/watcher/       fsnotify + polling watcher
pkg/updater/       Self-update from GitHub releases
internal/auth/     Authentication flow
```

The `pkg/` packages are designed as reusable libraries — a GUI wrapper (e.g., [Wails](https://wails.io)) can import `pkg/api`, `pkg/sync`, and `pkg/watcher` directly without depending on CLI code.

## Building

```bash
make build          # Build for current platform → bin/izerop
make install        # Build and install to ~/.local/bin
make release        # Cross-compile for all platforms → dist/
make test           # Run tests
make clean          # Remove build artifacts
```

### Release Platforms

| OS | Architecture |
|---|---|
| Linux | amd64, arm64 |
| macOS | amd64 (Intel), arm64 (Apple Silicon) |
| Windows | amd64 |

Releases are automated via GitHub Actions — push a `v*` tag to create a release with pre-built binaries.

## Desktop App

A native desktop app is available for all platforms. Download the appropriate file from [Releases](https://github.com/patricksimpson/izerop-cli/releases).

### macOS

1. Download `izerop-desktop-macos-arm64.zip` (Apple Silicon) or `izerop-desktop-macos-amd64.zip` (Intel)
2. Unzip and drag `izerop.app` to your Applications folder
3. **First launch:** macOS will block the app because it's unsigned. To bypass:
   - Right-click (or Control-click) `izerop.app` → **Open**
   - Click **Open** in the dialog that appears
   - Alternatively: **System Settings → Privacy & Security** → scroll down and click **Open Anyway**
   - You only need to do this once

### Windows

1. Download `izerop-desktop-windows-amd64.exe`
2. **First launch:** Windows SmartScreen may block the app:
   - Click **More info**
   - Click **Run anyway**
3. Optionally move the `.exe` to a permanent location like `C:\Program Files\izerop\`

### Linux

1. Download `izerop-desktop-linux-amd64`
2. Make it executable and run:
   ```bash
   chmod +x izerop-desktop-linux-amd64
   ./izerop-desktop-linux-amd64
   ```
3. **Dependencies:** The desktop app requires GTK3 and WebKit2GTK:
   - **Ubuntu/Debian:** `sudo apt install libgtk-3-0 libwebkit2gtk-4.0-37`
   - **Fedora:** `sudo dnf install gtk3 webkit2gtk4.0`
   - **Arch:** `sudo pacman -S gtk3 webkit2gtk` (provides webkit2gtk-4.0 compat)

### Building from Source

```bash
# Requires Go 1.25+ and Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Build
make desktop

# Or build + run in dev mode
make desktop-dev
```

## Roadmap

- [x] Authentication and config management
- [x] File upload/download (`push`, `pull`)
- [x] Directory listing and creation (`ls`, `mkdir`)
- [x] Delete and move/rename (`rm`, `mv`)
- [x] Bidirectional sync with cursor-based changes
- [x] Conflict detection
- [x] File watching with fsnotify
- [x] Background daemon mode with logging
- [x] Self-updater from GitHub releases
- [x] Cross-platform release builds
- [x] Selective sync (.izeropignore patterns)
- [x] Desktop GUI app (Wails)
- [x] Cross-platform desktop builds (macOS, Windows, Linux)
- [ ] System tray integration

## License

MIT
