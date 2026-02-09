package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	repoOwner = "patricksimpson"
	repoName  = "izerop-cli"
	releaseURL = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
)

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
	HTMLURL string  `json:"html_url"`
}

// Asset represents a release asset.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckForUpdate checks GitHub for the latest release and returns it if newer.
func CheckForUpdate(currentVersion string) (*Release, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(releaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("no releases found")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == currentVersion {
		return nil, nil // already up to date
	}

	return &release, nil
}

// assetName returns the expected asset name for the current platform.
func assetName() string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Normalize arch names to match build output
	switch goarch {
	case "amd64":
		goarch = "amd64"
	case "arm64":
		goarch = "arm64"
	}

	return fmt.Sprintf("izerop-%s-%s", goos, goarch)
}

// FindAsset finds the matching asset for this platform.
func FindAsset(release *Release) *Asset {
	name := assetName()
	for _, a := range release.Assets {
		if a.Name == name {
			return &a
		}
	}
	return nil
}

// DownloadAndReplace downloads the new binary and replaces the current executable.
func DownloadAndReplace(asset *Asset) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine executable path: %w", err)
	}

	// Write to temp file next to current binary
	tmpPath := execPath + ".new"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("could not create temp file: %w", err)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download write failed: %w", err)
	}
	tmpFile.Close()

	// Make executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic swap: rename old, rename new, remove old
	oldPath := execPath + ".old"
	os.Remove(oldPath) // clean up any previous .old

	if err := os.Rename(execPath, oldPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("could not move old binary: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		// Try to restore
		os.Rename(oldPath, execPath)
		return fmt.Errorf("could not move new binary: %w", err)
	}

	os.Remove(oldPath)
	return nil
}
