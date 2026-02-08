# izerop-cli

A command-line file sync client for [izerop](https://izerop.com).

## Install

```bash
git clone https://github.com/patricksimpson/izerop-cli.git
cd izerop-cli
make build
```

Binary will be at `./bin/izerop`.

## Quick Start

```bash
# 1. Authenticate
izerop login

# 2. Check connection
izerop status

# 3. List your files
izerop ls

# 4. Upload a file
izerop push myfile.txt --dir <directory-id>

# 5. Download a file
izerop pull <file-id> --out myfile.txt
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

List remote directories and files. Shows names, sizes, timestamps, and IDs.

```bash
# List everything
izerop ls

# List files in a specific directory
izerop ls <directory-id>
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
# Download (filename from server, or falls back to file ID)
izerop pull <file-id>

# Download to a specific path
izerop pull <file-id> --out photo.jpg
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
  "token": "your-jwt-token"
}
```

### Environment Variables

Override any config value with environment variables:

| Variable | Description |
|---|---|
| `IZEROP_SERVER_URL` | Override server URL |
| `IZEROP_TOKEN` | Override API token |
| `IZEROP_SYNC_DIR` | Override sync directory |

### Flags

| Flag | Description |
|---|---|
| `--server <url>` | Override server URL for this command |

**Precedence:** `--server` flag > env vars > config file > default (`https://izerop.com`)

### Local Development

Point at a local Rails server:

```bash
# Via flag
izerop --server http://localhost:3000 ls

# Via env
export IZEROP_SERVER_URL=http://localhost:3000
export IZEROP_TOKEN=your-local-jwt
izerop ls
```

## Project Structure

```
cmd/izerop/       CLI entrypoint
pkg/api/          API client (reusable library)
pkg/sync/         Sync engine (reusable library)
pkg/config/       Configuration management
internal/auth/    Authentication flow
```

The `pkg/` packages are designed to be reusable — a GUI wrapper (e.g., [Wails](https://wails.io)) can import `pkg/api` and `pkg/sync` directly without depending on CLI code.

## Roadmap

- [ ] `mkdir` — create directories
- [ ] `rm` — delete files/directories
- [ ] `sync` — bidirectional file sync
- [ ] File watching (auto-sync on change)
- [ ] Wails GUI wrapper
- [ ] Cross-platform builds (Mac/Windows/Linux)

## License

MIT
