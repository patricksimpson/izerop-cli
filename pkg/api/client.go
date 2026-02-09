package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client communicates with the izerop API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient creates a new API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// do executes an authenticated HTTP request.
func (c *Client) do(method, path string, body io.Reader) (*http.Response, error) {
	url := fmt.Sprintf("%s%s", c.BaseURL, path)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.HTTPClient.Do(req)
}

// SyncStatus represents the response from /api/v1/sync/status.
type SyncStatus struct {
	FileCount      int    `json:"file_count"`
	DirectoryCount int    `json:"directory_count"`
	TotalSize      int64  `json:"total_size"`
	StorageLimit   int64  `json:"storage_limit"`
	Cursor         string `json:"cursor"`
	LastSync       string `json:"last_sync"`
}

// GetSyncStatus fetches the current sync status.
func (c *Client) GetSyncStatus() (*SyncStatus, error) {
	resp, err := c.do("GET", "/api/v1/sync/status", nil)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var status SyncStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return &status, nil
}

// FileEntry represents a file from /api/v1/files.
type FileEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	DirectoryID string `json:"directory_id"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Public      bool   `json:"public"`
	HasBinary   bool   `json:"has_binary"`
	HasText     bool   `json:"has_text"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ListFiles fetches the file listing.
func (c *Client) ListFiles(directoryID string) ([]FileEntry, error) {
	path := "/api/v1/files"
	if directoryID != "" {
		path = fmt.Sprintf("/api/v1/files?directory_id=%s", directoryID)
	}

	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var wrapper struct {
		Files []FileEntry `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return wrapper.Files, nil
}

// Directory represents a directory from /api/v1/directories.
type Directory struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	ParentID  *string `json:"parent_id"`
	Public    bool    `json:"public"`
	FileCount int     `json:"file_count"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// ListDirectories fetches the directory listing.
func (c *Client) ListDirectories() ([]Directory, error) {
	resp, err := c.do("GET", "/api/v1/directories", nil)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var wrapper struct {
		Directories []Directory `json:"directories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return wrapper.Directories, nil
}

// Change represents a single change from the sync/changes API.
type Change struct {
	Type        string `json:"type"`
	Action      string `json:"action"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	DirectoryID string `json:"directory_id"`
	ParentID    string `json:"parent_id"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	UpdatedAt   string `json:"updated_at"`
}

// ChangesResponse is the response from /api/v1/sync/changes.
type ChangesResponse struct {
	Changes []Change `json:"changes"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"has_more"`
}

// GetChanges fetches changes since the given cursor.
func (c *Client) GetChanges(cursor string) (*ChangesResponse, error) {
	path := "/api/v1/sync/changes"
	if cursor != "" {
		path = fmt.Sprintf("%s?since=%s", path, cursor)
	}

	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result ChangesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return &result, nil
}

// UploadFile uploads a local file to the server.
func (c *Client) UploadFile(localPath, directoryID, name string) (*FileEntry, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("could not open file: %w", err)
	}
	defer f.Close()

	if name == "" {
		name = filepath.Base(localPath)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return nil, fmt.Errorf("could not create form file: %w", err)
	}

	if _, err := io.Copy(part, f); err != nil {
		return nil, fmt.Errorf("could not copy file data: %w", err)
	}

	if directoryID != "" {
		writer.WriteField("directory_id", directoryID)
	}
	writer.WriteField("name", name)
	writer.Close()

	url := fmt.Sprintf("%s/api/v1/files", c.BaseURL)
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var wrapper struct {
		File FileEntry `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return &wrapper.File, nil
}

// DownloadFile downloads a file by ID and writes it to the given writer.
// Returns the suggested filename from Content-Disposition if available.
func (c *Client) DownloadFile(fileID string, dest io.Writer) (string, error) {
	// Strip auth headers when redirected to S3/external hosts
	client := &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Strip Authorization header when redirecting to a different host (e.g., S3)
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}

	url := fmt.Sprintf("%s/api/v1/files/%s/download", c.BaseURL, fileID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("download failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Try to get filename from Content-Disposition header
	filename := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := bytes.Index([]byte(cd), []byte("filename=")); i >= 0 {
			filename = string([]byte(cd)[i+9:])
			filename = strings.Trim(filename, `"' `)
		}
	}

	if _, err := io.Copy(dest, resp.Body); err != nil {
		return filename, fmt.Errorf("error writing file: %w", err)
	}

	return filename, nil
}

// CreateTextFile creates a text file (stored in DB, not S3).
func (c *Client) CreateTextFile(name, contents, directoryID, contentType string) (*FileEntry, error) {
	if contentType == "" {
		contentType = "text/plain"
	}
	payload := map[string]string{
		"name":         name,
		"contents":     contents,
		"directory_id": directoryID,
		"content_type": contentType,
	}
	data, _ := json.Marshal(payload)
	resp, err := c.do("POST", "/api/v1/files/text", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create text file failed (status %d): %s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		File FileEntry `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}
	return &wrapper.File, nil
}

// UpdateFile updates a file's contents or metadata.
func (c *Client) UpdateFile(fileID string, updates map[string]string) (*FileEntry, error) {
	data, _ := json.Marshal(updates)
	resp, err := c.do("PATCH", fmt.Sprintf("/api/v1/files/%s", fileID), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("update file failed (status %d): %s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		File FileEntry `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}
	return &wrapper.File, nil
}

// DeleteFile soft-deletes a file by ID.
func (c *Client) DeleteFile(fileID string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/v1/files/%s", fileID), nil)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeleteDirectory soft-deletes a directory by ID.
func (c *Client) DeleteDirectory(dirID string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/v1/directories/%s", dirID), nil)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// MoveFile moves/renames a file (updates name and/or directory).
func (c *Client) MoveFile(fileID string, newName string, newDirID string) (*FileEntry, error) {
	updates := make(map[string]string)
	if newName != "" {
		updates["name"] = newName
	}
	if newDirID != "" {
		updates["directory_id"] = newDirID
	}
	return c.UpdateFile(fileID, updates)
}

// CreateDirectory creates a new directory on the server.
func (c *Client) CreateDirectory(name, parentID string) (*Directory, error) {
	payload := map[string]string{"name": name}
	if parentID != "" {
		payload["user_directory_id"] = parentID
	}

	data, _ := json.Marshal(payload)
	resp, err := c.do("POST", "/api/v1/directories", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create directory failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var wrapper struct {
		Directory Directory `json:"directory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}

	return &wrapper.Directory, nil
}
