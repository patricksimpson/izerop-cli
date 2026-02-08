package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Checksum    string `json:"checksum"`
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
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	ParentID  *int   `json:"parent_id"`
	UpdatedAt string `json:"updated_at"`
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
