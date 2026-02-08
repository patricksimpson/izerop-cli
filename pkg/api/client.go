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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Public      bool   `json:"public"`
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
