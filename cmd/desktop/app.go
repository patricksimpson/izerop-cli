package main

import (
	"context"
	"fmt"

	"github.com/patricksimpson/izerop-cli/pkg/api"
	"github.com/patricksimpson/izerop-cli/pkg/config"
)

// App struct holds the application state
type App struct {
	ctx    context.Context
	client *api.Client
	cfg    *config.Config
}

// NewApp creates a new App instance
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Try to load existing config
	cfg, err := config.Load()
	if err == nil && cfg.Token != "" {
		a.cfg = cfg
		a.client = api.NewClient(cfg.ServerURL, cfg.Token)
	}
}

// ConnectionStatus represents the current connection state
type ConnectionStatus struct {
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	HasToken  bool   `json:"hasToken"`
}

// StatusInfo represents sync status from the server
type StatusInfo struct {
	FileCount      int    `json:"fileCount"`
	DirectoryCount int    `json:"directoryCount"`
	TotalSize      int64  `json:"totalSize"`
	Cursor         string `json:"cursor"`
	Connected      bool   `json:"connected"`
	Server         string `json:"server"`
	Error          string `json:"error,omitempty"`
}

// LoginResult represents the result of a login attempt
type LoginResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// GetConnectionStatus returns whether we have a valid config
func (a *App) GetConnectionStatus() ConnectionStatus {
	if a.cfg == nil {
		return ConnectionStatus{Connected: false}
	}
	return ConnectionStatus{
		Connected: a.client != nil,
		Server:    a.cfg.ServerURL,
		HasToken:  a.cfg.Token != "",
	}
}

// Login saves credentials and tests the connection
func (a *App) Login(serverURL, token string) LoginResult {
	if serverURL == "" {
		serverURL = "https://izerop.com"
	}

	client := api.NewClient(serverURL, token)

	// Test connection
	_, err := client.GetSyncStatus()
	if err != nil {
		return LoginResult{Success: false, Error: fmt.Sprintf("Connection failed: %v", err)}
	}

	// Save config
	cfg := &config.Config{
		ServerURL: serverURL,
		Token:     token,
	}
	if err := config.Save(cfg); err != nil {
		return LoginResult{Success: false, Error: fmt.Sprintf("Could not save config: %v", err)}
	}

	a.cfg = cfg
	a.client = client

	return LoginResult{Success: true}
}

// GetStatus fetches the current sync status from the server
func (a *App) GetStatus() StatusInfo {
	if a.client == nil {
		return StatusInfo{Connected: false, Error: "Not logged in"}
	}

	status, err := a.client.GetSyncStatus()
	if err != nil {
		return StatusInfo{
			Connected: false,
			Server:    a.cfg.ServerURL,
			Error:     err.Error(),
		}
	}

	return StatusInfo{
		FileCount:      status.FileCount,
		DirectoryCount: status.DirectoryCount,
		TotalSize:      status.TotalSize,
		Cursor:         status.Cursor,
		Connected:      true,
		Server:         a.cfg.ServerURL,
	}
}

// Logout clears the current session
func (a *App) Logout() {
	a.client = nil
	a.cfg = nil
}
