package sync2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patricksimpson/izerop-cli/pkg/api"
)

// mockServer provides a fake izerop API for testing.
type mockServer struct {
	files map[string]*mockFile // id → file
	dirs  map[string]*mockDir  // id → dir
	ts    *httptest.Server
	t     *testing.T
}

type mockFile struct {
	ID          string
	Name        string
	Path        string
	DirID       string
	Contents    string
	ContentHash string
	Size        int64
	UpdatedAt   string
}

type mockDir struct {
	ID       string
	Name     string
	Path     string
	ParentID string
}

func newMockServer(t *testing.T) *mockServer {
	m := &mockServer{
		files: make(map[string]*mockFile),
		dirs:  make(map[string]*mockDir),
		t:     t,
	}

	// Create root directory
	m.dirs["root-id"] = &mockDir{
		ID:   "root-id",
		Name: "root",
		Path: "/root",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sync/manifest", m.handleManifest)
	mux.HandleFunc("/api/v1/sync/changes", m.handleChanges)
	mux.HandleFunc("/api/v1/directories", m.handleDirectories)
	mux.HandleFunc("/api/v1/files/text", m.handleCreateText)
	mux.HandleFunc("/api/v1/files", m.handleFiles)
	mux.HandleFunc("/", m.handleCatchAll)

	m.ts = httptest.NewServer(mux)
	return m
}

func (m *mockServer) close() {
	m.ts.Close()
}

func (m *mockServer) client() *api.Client {
	return api.NewClient(m.ts.URL, "test-token")
}

func (m *mockServer) addFile(name, dirPath, contents string) *mockFile {
	hash := sha256Hash(contents)
	id := fmt.Sprintf("file-%s-%d", name, len(m.files))
	path := dirPath + "/" + name
	f := &mockFile{
		ID:          id,
		Name:        name,
		Path:        path,
		DirID:       "root-id",
		Contents:    contents,
		ContentHash: hash,
		Size:        int64(len(contents)),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	m.files[id] = f
	return f
}

func (m *mockServer) handleManifest(w http.ResponseWriter, r *http.Request) {
	var files []map[string]interface{}
	for _, f := range m.files {
		files = append(files, map[string]interface{}{
			"id":           f.ID,
			"name":         f.Name,
			"path":         f.Path,
			"directory_id": f.DirID,
			"size":         f.Size,
			"content_type": "text/plain",
			"content_hash": f.ContentHash,
			"has_text":     true,
			"has_binary":   false,
			"updated_at":   f.UpdatedAt,
		})
	}

	var dirs []map[string]interface{}
	for _, d := range m.dirs {
		dirs = append(dirs, map[string]interface{}{
			"id":        d.ID,
			"name":      d.Name,
			"path":      d.Path,
			"parent_id": d.ParentID,
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"files":        files,
		"directories":  dirs,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (m *mockServer) handleChanges(w http.ResponseWriter, r *http.Request) {
	// Return empty changes (no new changes)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"changes":  []interface{}{},
		"cursor":   "cursor-1",
		"has_more": false,
	})
}

func (m *mockServer) handleDirectories(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		name := body["name"]
		parentID := body["user_directory_id"]
		parentPath := "/root"
		if parentID != "" {
			if p, ok := m.dirs[parentID]; ok {
				parentPath = p.Path
			}
		}
		id := fmt.Sprintf("dir-%s-%d", name, len(m.dirs))
		d := &mockDir{
			ID:       id,
			Name:     name,
			Path:     parentPath + "/" + name,
			ParentID: parentID,
		}
		m.dirs[id] = d
		json.NewEncoder(w).Encode(map[string]interface{}{
			"directory": map[string]interface{}{
				"id": d.ID, "name": d.Name, "path": d.Path, "parent_id": d.ParentID,
			},
		})
		return
	}

	var dirs []map[string]interface{}
	for _, d := range m.dirs {
		dirs = append(dirs, map[string]interface{}{
			"id":        d.ID,
			"name":      d.Name,
			"path":      d.Path,
			"parent_id": d.ParentID,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"directories": dirs})
}

func (m *mockServer) handleCreateText(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	name := body["name"]
	contents := body["contents"]
	hash := sha256Hash(contents)
	id := fmt.Sprintf("file-%s-%d", name, len(m.files))
	f := &mockFile{
		ID:          id,
		Name:        name,
		Path:        "/root/" + name,
		DirID:       body["directory_id"],
		Contents:    contents,
		ContentHash: hash,
		Size:        int64(len(contents)),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	m.files[id] = f
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"file": map[string]interface{}{
			"id": f.ID, "name": f.Name, "content_hash": f.ContentHash,
			"size": f.Size, "updated_at": f.UpdatedAt,
		},
	})
}

func (m *mockServer) handleFiles(w http.ResponseWriter, r *http.Request) {
	// Handle /api/v1/files/:id/download and /api/v1/files/:id
	path := r.URL.Path
	if strings.Contains(path, "/download") {
		id := extractFileID(path)
		if f, ok := m.files[id]; ok {
			w.Write([]byte(f.Contents))
			return
		}
		w.WriteHeader(404)
		return
	}

	// PATCH /api/v1/files/:id
	if r.Method == "PATCH" {
		id := extractFileID(path)
		f, ok := m.files[id]
		if !ok {
			w.WriteHeader(404)
			return
		}

		// Check If-Match
		ifMatch := r.Header.Get("If-Match")
		if ifMatch != "" {
			expected := strings.Trim(ifMatch, `"`)
			if f.ContentHash != expected {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{"current_hash": f.ContentHash})
				return
			}
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if contents, ok := body["contents"]; ok {
			f.Contents = contents
			f.ContentHash = sha256Hash(contents)
			f.Size = int64(len(contents))
			f.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"file": map[string]interface{}{
				"id": f.ID, "name": f.Name, "content_hash": f.ContentHash,
				"size": f.Size, "updated_at": f.UpdatedAt,
			},
		})
		return
	}

	// DELETE /api/v1/files/:id
	if r.Method == "DELETE" {
		id := extractFileID(path)
		delete(m.files, id)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
		return
	}

	// GET /api/v1/files (list)
	var files []map[string]interface{}
	for _, f := range m.files {
		files = append(files, map[string]interface{}{
			"id": f.ID, "name": f.Name, "size": f.Size, "content_hash": f.ContentHash,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"files": files})
}

func (m *mockServer) handleCatchAll(w http.ResponseWriter, r *http.Request) {
	// Handle file-specific routes that didn't match above
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v1/files/") {
		m.handleFiles(w, r)
		return
	}
	m.t.Logf("unhandled request: %s %s", r.Method, path)
	w.WriteHeader(404)
}

func extractFileID(path string) string {
	// /api/v1/files/FILE-ID or /api/v1/files/FILE-ID/download
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/files/"), "/")
	return parts[0]
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// --- Helper functions ---

func setupTestDir(t *testing.T) string {
	dir := t.TempDir()
	return dir
}

func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func newTestEngine(client *api.Client, syncDir, profile string) *Engine {
	e := NewEngine(client, syncDir, profile)
	e.Verbose = true
	return e
}

// --- Tests ---

func TestSync_NewLocalFile_PushesToServer(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	writeFile(t, dir, "hello.txt", "hello world")

	engine := newTestEngine(srv.client(), dir, "test-push")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pushed != 1 {
		t.Errorf("expected 1 push, got %d", result.Pushed)
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}

	// Verify state was saved
	tree, _ := LoadState("test-push")
	if _, ok := tree.Files["hello.txt"]; !ok {
		t.Error("hello.txt not tracked in synced tree")
	}
}

func TestSync_NewRemoteFile_PullsToLocal(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)

	// Add a file on the server
	srv.addFile("remote.txt", "/root", "remote content")

	engine := newTestEngine(srv.client(), dir, "test-pull")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pulled != 1 {
		t.Errorf("expected 1 pull, got %d", result.Pulled)
	}

	// Verify file was downloaded
	if !fileExists(dir, "remote.txt") {
		t.Error("remote.txt not downloaded")
	}
	contents := readFile(t, dir, "remote.txt")
	if contents != "remote content" {
		t.Errorf("expected 'remote content', got %q", contents)
	}
}

func TestSync_IdenticalFiles_NoAction(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	content := "same content"
	writeFile(t, dir, "same.txt", content)
	srv.addFile("same.txt", "/root", content)

	engine := newTestEngine(srv.client(), dir, "test-identical")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pushed != 0 {
		t.Errorf("expected 0 pushes, got %d", result.Pushed)
	}
	if result.Pulled != 0 {
		t.Errorf("expected 0 pulls, got %d", result.Pulled)
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}
	// Should be skipped since content matches
	if result.Skipped != 1 {
		t.Errorf("expected 1 skip, got %d", result.Skipped)
	}
}

func TestSync_LocalUpdated_RemoteUnchanged_Pushes(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	original := "original"
	writeFile(t, dir, "doc.txt", original)
	rf := srv.addFile("doc.txt", "/root", original)

	// Establish synced state
	hash := sha256Hash(original)
	tree := NewSyncedTree()
	tree.Files["doc.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(original)),
	}
	SaveState("test-local-update", tree)

	// Now modify the local file
	writeFile(t, dir, "doc.txt", "updated locally")

	engine := newTestEngine(srv.client(), dir, "test-local-update")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pushed != 1 {
		t.Errorf("expected 1 push, got %d", result.Pushed)
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}
}

func TestSync_RemoteUpdated_LocalUnchanged_Pulls(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	original := "original"
	writeFile(t, dir, "doc.txt", original)
	rf := srv.addFile("doc.txt", "/root", original)

	// Establish synced state
	hash := sha256Hash(original)
	tree := NewSyncedTree()
	tree.Files["doc.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(original)),
	}
	SaveState("test-remote-update", tree)

	// Now modify the remote file
	rf.Contents = "updated remotely"
	rf.ContentHash = sha256Hash("updated remotely")
	rf.Size = int64(len("updated remotely"))

	engine := newTestEngine(srv.client(), dir, "test-remote-update")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pulled != 1 {
		t.Errorf("expected 1 pull, got %d", result.Pulled)
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}

	// Verify local file was updated
	contents := readFile(t, dir, "doc.txt")
	if contents != "updated remotely" {
		t.Errorf("expected 'updated remotely', got %q", contents)
	}
}

func TestSync_BothChanged_DifferentContent_Conflict(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	original := "original"
	writeFile(t, dir, "doc.txt", original)
	rf := srv.addFile("doc.txt", "/root", original)

	// Establish synced state
	hash := sha256Hash(original)
	tree := NewSyncedTree()
	tree.Files["doc.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(original)),
	}
	SaveState("test-conflict", tree)

	// Modify both sides differently
	writeFile(t, dir, "doc.txt", "local version")
	rf.Contents = "remote version"
	rf.ContentHash = sha256Hash("remote version")

	engine := newTestEngine(srv.client(), dir, "test-conflict")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
	if result.Pushed != 0 {
		t.Errorf("expected 0 pushes, got %d", result.Pushed)
	}
	if result.Pulled != 0 {
		t.Errorf("expected 0 pulls, got %d", result.Pulled)
	}

	// Verify conflict is in the queue
	conflicts, _ := LoadConflicts("test-conflict")
	unresolved := conflicts.Unresolved()
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 unresolved conflict, got %d", len(unresolved))
	}
	if unresolved[0].Path != "doc.txt" {
		t.Errorf("conflict path: expected doc.txt, got %s", unresolved[0].Path)
	}
}

func TestSync_BothChanged_SameContent_NoConflict(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	original := "original"
	converged := "both sides wrote this"
	writeFile(t, dir, "doc.txt", original)
	rf := srv.addFile("doc.txt", "/root", original)

	// Establish synced state
	hash := sha256Hash(original)
	tree := NewSyncedTree()
	tree.Files["doc.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(original)),
	}
	SaveState("test-converge", tree)

	// Both sides update to the same content
	writeFile(t, dir, "doc.txt", converged)
	rf.Contents = converged
	rf.ContentHash = sha256Hash(converged)

	engine := newTestEngine(srv.client(), dir, "test-converge")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts (content converged), got %d", result.Conflicts)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skip (converged), got %d", result.Skipped)
	}
}

func TestSync_LocalDeleted_RemoteUnchanged_DeletesRemote(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	content := "to be deleted"
	rf := srv.addFile("bye.txt", "/root", content)

	// Establish synced state (file existed on both sides)
	hash := sha256Hash(content)
	tree := NewSyncedTree()
	tree.Files["bye.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(content)),
	}
	SaveState("test-local-delete", tree)

	// Local file is NOT on disk (deleted)

	engine := newTestEngine(srv.client(), dir, "test-local-delete")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Deleted != 1 {
		t.Errorf("expected 1 delete, got %d", result.Deleted)
	}

	// Verify removed from server
	if _, exists := srv.files[rf.ID]; exists {
		t.Error("file should have been deleted from server")
	}
}

func TestSync_RemoteDeleted_LocalUnchanged_DeletesLocal(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	content := "will be removed from server"
	writeFile(t, dir, "gone.txt", content)

	// Establish synced state
	hash := sha256Hash(content)
	tree := NewSyncedTree()
	tree.Files["gone.txt"] = SyncedFile{
		RemoteID:   "deleted-remote-id",
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len(content)),
	}
	SaveState("test-remote-delete", tree)

	// File is NOT on the server (deleted remotely)

	engine := newTestEngine(srv.client(), dir, "test-remote-delete")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Deleted != 1 {
		t.Errorf("expected 1 delete, got %d", result.Deleted)
	}

	// Verify local file removed
	if fileExists(dir, "gone.txt") {
		t.Error("gone.txt should have been deleted locally")
	}
}

func TestSync_LocalModified_RemoteDeleted_Conflict(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	writeFile(t, dir, "edited.txt", "i edited this")

	// Establish synced state with original content
	hash := sha256Hash("original")
	tree := NewSyncedTree()
	tree.Files["edited.txt"] = SyncedFile{
		RemoteID:   "deleted-remote-id",
		LocalHash:  hash,
		RemoteHash: hash,
		Size:       int64(len("original")),
	}
	SaveState("test-edit-vs-delete", tree)

	// Remote deleted the file (not in server), local modified it

	engine := newTestEngine(srv.client(), dir, "test-edit-vs-delete")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict (modified locally, deleted remotely), got %d", result.Conflicts)
	}

	// Local file should still exist
	if !fileExists(dir, "edited.txt") {
		t.Error("edited.txt should NOT be deleted (conflict)")
	}
}

func TestSync_StabilityTracker_SkipsUnstableFiles(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	writeFile(t, dir, "editing.txt", "work in progress")

	// Create a stability tracker with a long cooldown
	stability := NewStabilityTracker(1 * time.Hour)
	stability.Touch(filepath.Join(dir, "editing.txt")) // just touched — not stable

	engine := newTestEngine(srv.client(), dir, "test-stability")
	engine.Stability = stability

	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pushed != 0 {
		t.Errorf("expected 0 pushes (file not stable), got %d", result.Pushed)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skip (not stable), got %d", result.Skipped)
	}
}

func TestSync_IfMatch_409_Conflict(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	original := "original"
	writeFile(t, dir, "race.txt", "my update")
	rf := srv.addFile("race.txt", "/root", original)

	// Establish synced state with a STALE remote hash
	// (simulates another client updating the server after our last sync)
	staleHash := sha256Hash("stale version")
	tree := NewSyncedTree()
	tree.Files["race.txt"] = SyncedFile{
		RemoteID:   rf.ID,
		LocalHash:  sha256Hash(original),
		RemoteHash: staleHash, // stale! server has sha256("original")
		Size:       int64(len(original)),
	}
	SaveState("test-ifmatch", tree)

	// The server's content_hash is sha256("original"), but our synced state
	// thinks it's sha256("stale version"). The If-Match will send the stale hash,
	// and the mock server should return 409.

	engine := newTestEngine(srv.client(), dir, "test-ifmatch")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The three-way comparison sees:
	// - local changed (sha256("my update") != sha256("original"))
	// - remote changed (sha256("original") != sha256("stale version"))
	// → conflict detected at the comparison stage, before If-Match even fires
	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
}

func TestSync_MultipleFiles_MixedActions(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)

	// File 1: exists only locally → push
	writeFile(t, dir, "local-only.txt", "new local file")

	// File 2: exists only remotely → pull
	srv.addFile("remote-only.txt", "/root", "new remote file")

	// File 3: same on both sides → skip
	sameContent := "identical content"
	writeFile(t, dir, "same.txt", sameContent)
	rf3 := srv.addFile("same.txt", "/root", sameContent)
	_ = rf3

	engine := newTestEngine(srv.client(), dir, "test-multi")
	result, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Pushed != 1 {
		t.Errorf("expected 1 push, got %d", result.Pushed)
	}
	if result.Pulled != 1 {
		t.Errorf("expected 1 pull, got %d", result.Pulled)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skip, got %d", result.Skipped)
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}

	// Verify all files exist locally
	if !fileExists(dir, "local-only.txt") {
		t.Error("local-only.txt should still exist")
	}
	if !fileExists(dir, "remote-only.txt") {
		t.Error("remote-only.txt should have been downloaded")
	}
	if !fileExists(dir, "same.txt") {
		t.Error("same.txt should still exist")
	}
}

func TestSync_SecondSync_NoChanges_AllSkipped(t *testing.T) {
	srv := newMockServer(t)
	defer srv.close()

	dir := setupTestDir(t)
	writeFile(t, dir, "stable.txt", "stable content")
	srv.addFile("stable.txt", "/root", "stable content")

	engine := newTestEngine(srv.client(), dir, "test-idempotent")

	// First sync — should detect identical content and skip
	result1, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync 1: %v", err)
	}

	// Second sync — everything should be skipped
	result2, err := engine.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}

	if result2.Pushed != 0 || result2.Pulled != 0 || result2.Deleted != 0 || result2.Conflicts != 0 {
		t.Errorf("second sync should be all skips, got pushed=%d pulled=%d deleted=%d conflicts=%d",
			result2.Pushed, result2.Pulled, result2.Deleted, result2.Conflicts)
	}
	_ = result1
}

func TestStabilityTracker_BasicBehavior(t *testing.T) {
	st := NewStabilityTracker(100 * time.Millisecond)

	// Untouched file is stable
	if !st.IsStable("/some/file") {
		t.Error("untouched file should be stable")
	}

	// Touch it — now unstable
	st.Touch("/some/file")
	if st.IsStable("/some/file") {
		t.Error("just-touched file should be unstable")
	}

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)
	if !st.IsStable("/some/file") {
		t.Error("file should be stable after cooldown")
	}

	// StableFiles should include it
	stable := st.StableFiles()
	if len(stable) != 1 {
		t.Errorf("expected 1 stable file, got %d", len(stable))
	}

	// Remove it
	st.Remove("/some/file")
	stable = st.StableFiles()
	if len(stable) != 0 {
		t.Errorf("expected 0 stable files after remove, got %d", len(stable))
	}
}

func TestStabilityTracker_ReTouch_ResetsCooldown(t *testing.T) {
	st := NewStabilityTracker(100 * time.Millisecond)

	st.Touch("/editing")
	time.Sleep(60 * time.Millisecond)

	// Re-touch before cooldown expires
	st.Touch("/editing")
	time.Sleep(60 * time.Millisecond)

	// Should still be unstable (cooldown reset)
	if st.IsStable("/editing") {
		t.Error("re-touched file should still be unstable")
	}

	// Wait for full cooldown from last touch
	time.Sleep(60 * time.Millisecond)
	if !st.IsStable("/editing") {
		t.Error("file should be stable after full cooldown")
	}
}

func TestConflictQueue_AddResolve(t *testing.T) {
	q := &ConflictQueue{}

	q.Add(ConflictEntry{Path: "a.txt", LocalHash: "aaa", RemoteHash: "bbb"})
	q.Add(ConflictEntry{Path: "b.txt", LocalHash: "ccc", RemoteHash: "ddd"})

	if len(q.Unresolved()) != 2 {
		t.Errorf("expected 2 unresolved, got %d", len(q.Unresolved()))
	}

	q.Resolve("a.txt")
	if len(q.Unresolved()) != 1 {
		t.Errorf("expected 1 unresolved after resolve, got %d", len(q.Unresolved()))
	}

	// Re-adding same path replaces
	q.Add(ConflictEntry{Path: "b.txt", LocalHash: "eee", RemoteHash: "fff"})
	unresolved := q.Unresolved()
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 unresolved, got %d", len(unresolved))
	}
	if unresolved[0].LocalHash != "eee" {
		t.Error("re-added conflict should have updated hash")
	}
}

// cleanup helper — remove test state files
func init() {
	// Clean up any leftover test state files when tests start
	profiles := []string{
		"test-push", "test-pull", "test-identical", "test-local-update",
		"test-remote-update", "test-conflict", "test-converge",
		"test-local-delete", "test-remote-delete", "test-edit-vs-delete",
		"test-stability", "test-ifmatch", "test-multi", "test-idempotent",
	}
	for _, p := range profiles {
		path, err := statePath(p)
		if err == nil {
			os.Remove(path)
		}
		cpath, err := conflictPath(p)
		if err == nil {
			os.Remove(cpath)
		}
	}
}

// readBody is a helper for reading response bodies in tests.
func readBody(r io.Reader) string {
	data, _ := io.ReadAll(r)
	return string(data)
}
