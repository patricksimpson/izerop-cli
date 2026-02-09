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

Download the latest binary for your platform from [Releases](https://github.com/patricksimpson/izerop-cli/releases), then:

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

Config is saved to `~/.config/izerop/config.json`.

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

# Custom log file location
izerop watch ~/izerop --daemon --log /path/to/watch.log
```

Default log location: `~/.config/izerop/watch.log`

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

## Configuration

### Config File

Stored at `~/.config/izerop/config.json`:

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

Sync state is stored in `.izerop-sync.json` inside the sync directory. This tracks:

- Server cursor (for incremental pull)
- File records (size, mod time, remote timestamp for conflict detection)
- Note mappings (text files synced via contents API)

Don't delete this file unless you want a full re-sync.

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
- [ ] Selective sync (ignore patterns)
- [ ] Wails GUI wrapper

## License

MIT
