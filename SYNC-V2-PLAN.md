# Sync V2 — Migration Plan

## Goal
Replace the current sync engine with a simpler, more reliable one based on the **three-tree model** (à la Dropbox Nucleus). Eliminate false conflicts, kill `.conflict` file sprawl, and only sync files that are at rest.

## Architecture: Three Trees

```
┌──────────┐     ┌──────────┐     ┌──────────┐
│  Local    │     │  Synced   │     │  Remote   │
│  (disk)   │ ←→  │  (state)  │  ←→ │  (server) │
└──────────┘     └──────────┘     └──────────┘
```

- **Local tree**: What's actually on disk (computed via scan or fsnotify)
- **Remote tree**: What the server has (fetched via manifest/changes API)
- **Synced tree**: The last agreed-upon state — persisted in `sync-state-v2.json`

### Decision Matrix

| Local vs Synced | Remote vs Synced | Action |
|-----------------|------------------|--------|
| Same            | Same             | Skip (in sync) |
| Changed         | Same             | **Push** local → server |
| Same            | Changed          | **Pull** server → local |
| Changed         | Changed          | **Conflict** (queue it) |
| New (not in synced) | Not on remote | **Push** new file |
| Not on disk     | Same as synced   | **Delete** on server (local deletion) |
| Same as synced  | Not on remote    | **Delete** locally (remote deletion) |
| Not on disk     | Changed          | **Pull** (re-download) |
| Changed         | Not on remote    | **Conflict** (edited locally, deleted remotely) |

## Chunks

### Chunk 1: New State Model + Stability Tracker
**Files:** `pkg/sync2/state.go`, `pkg/sync2/stability.go`

New state model — clean break from v1:
```go
type SyncedTree struct {
    Files  map[string]SyncedFile `json:"files"`
    Cursor string                `json:"cursor"`
    Version int                  `json:"version"` // state format version
}

type SyncedFile struct {
    RemoteID   string `json:"id"`
    LocalHash  string `json:"local_hash"`   // hash at last sync
    RemoteHash string `json:"remote_hash"`  // server hash at last sync
    Size       int64  `json:"size"`
    IsNote     bool   `json:"is_note,omitempty"`
}
```

Stability tracker — fsnotify-aware, per-file cooldown:
```go
type StabilityTracker struct {
    pending map[string]time.Time  // path → last event time
    mu      sync.Mutex
    cooldown time.Duration        // e.g. 3 seconds
}

func (s *StabilityTracker) Touch(path string)           // reset cooldown
func (s *StabilityTracker) IsStable(path string) bool   // cooldown expired?
func (s *StabilityTracker) StableFiles() []string        // all stable paths
func (s *StabilityTracker) Remove(path string)           // done syncing
```

**Deliverable:** State can load/save, stability tracker works standalone. Unit tests.

---

### Chunk 2: Three-Way Sync Engine
**Files:** `pkg/sync2/engine.go`, `pkg/sync2/conflict.go`

The core engine with one `Sync()` method that does:
1. **Build local tree** — walk disk, hash files, check stability
2. **Build remote tree** — fetch manifest (full) or changes (incremental)
3. **Compare both against synced tree** — produce an action plan
4. **Execute actions** — push, pull, delete, queue conflicts
5. **Update synced tree** — only after successful action

Conflict queue (not `.conflict` files):
```go
type ConflictEntry struct {
    Path       string    `json:"path"`
    LocalHash  string    `json:"local_hash"`
    RemoteHash string    `json:"remote_hash"`
    RemoteID   string    `json:"remote_id"`
    DetectedAt time.Time `json:"detected_at"`
}
```

Conflicts stored in `conflicts.json` alongside state. Surfaced via `izerop conflicts` (reuse existing command, new backend).

**Deliverable:** `engine.Sync()` works for one-shot sync. Passes tests against mock API.

---

### Chunk 3: Event-Driven Watcher Integration
**Files:** `pkg/sync2/watcher.go`

Replace the current watcher with one that:
- Feeds fsnotify events into the stability tracker
- Runs sync cycles only for stable files (not full tree walks)
- Holds a sync mutex to prevent overlapping cycles
- Suppresses fsnotify events caused by pull downloads (the `pulling` flag, but cleaner)
- Tracks pending files so we know *which* files to push (not all of them)

```
fsnotify → stability tracker → sync queue → engine.SyncFiles(paths) → done
              ↑ poll tick → engine.Pull() → download → suppress fsnotify
```

**Deliverable:** `izerop watch` works with new engine. Old watcher still available via flag.

---

### Chunk 4: CLI Integration + State Migration
**Files:** `cmd/izerop/main.go`, `pkg/sync2/migrate.go`

- Wire `izerop sync` to use sync2 engine (default)
- `izerop sync --legacy` falls back to v1 engine
- `izerop reconcile` uses sync2's full-manifest mode
- `izerop conflicts` reads from `conflicts.json` queue
  - `izerop conflicts --resolve <path> --keep local|remote`
  - `izerop conflicts --resolve-all --keep local|remote`
- Auto-migrate v1 state → v2 state on first run:
  - Read `sync-state.json`, map `FileRecord` → `SyncedFile`
  - Write `sync-state-v2.json`
  - Keep v1 state file as backup

**Deliverable:** Full CLI works with v2 engine. Migration tested. v1 removable.

---

### Chunk 5: Cleanup + Delete v1
**Files:** Remove `pkg/sync/sync.go`, `pkg/sync/state.go`, clean up imports

- Remove `--legacy` flag
- Remove `pkg/sync/` (old engine)
- Remove `.conflict` file handling from ignore rules
- Update help text
- Tag release

**Deliverable:** Clean codebase, only sync2 remains.

## Order of Work
```
Chunk 1 (state + stability)  →  independent, no existing code changes
Chunk 2 (engine)             →  depends on chunk 1
Chunk 3 (watcher)            →  depends on chunk 2
Chunk 4 (CLI + migration)    →  depends on chunks 2-3
Chunk 5 (cleanup)            →  depends on chunk 4
```

## API Requirements
The existing API already provides everything we need:
- `GET /api/v1/sync/manifest` — full remote tree with `content_hash`
- `GET /api/v1/sync/changes?since=<cursor>` — incremental changes
- `POST /api/v1/files` — upload
- `PATCH /api/v1/files/:id` — update
- `DELETE /api/v1/files/:id` — delete
- `GET /api/v1/files/:id/download` — download

**Nice-to-have (future):** Server-side `If-Match` / ETag on updates for optimistic concurrency. Would let us eliminate the last edge case where two clients push simultaneously.

## Non-Goals (for now)
- Block-level dedup / chunked transfer (overkill for personal use)
- Multi-device conflict resolution UI (CLI is fine)
- Real-time push via WebSocket (polling is fine at 30s intervals)
