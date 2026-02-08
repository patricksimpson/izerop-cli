# izerop-cli

A command-line file sync client for [izerop](https://izerop.com).

## Install

```bash
make build
# Binary at ./bin/izerop
```

## Usage

```bash
# Authenticate
izerop login

# Check status
izerop status

# List remote files
izerop ls

# Sync files
izerop sync
```

## Project Structure

```
cmd/izerop/       CLI entrypoint
pkg/api/          API client (reusable)
pkg/sync/         Sync engine (reusable)
pkg/config/       Configuration management
internal/auth/    Authentication flow
```

## Architecture

The `pkg/` packages are designed to be reusable — a GUI wrapper (e.g., Wails) can import `pkg/api` and `pkg/sync` directly without depending on CLI code.
