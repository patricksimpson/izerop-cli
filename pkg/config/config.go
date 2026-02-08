package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the CLI configuration.
type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	SyncDir   string `json:"sync_dir,omitempty"`
}

// DefaultConfigDir returns the config directory path (~/.config/izerop).
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "izerop"), nil
}

// ConfigPath returns the full path to the config file.
func ConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config from disk, then applies env var overrides.
// Precedence: env vars > config file.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		// Config file missing is OK if env vars provide what we need
		cfg = Config{}
	} else if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Env var overrides
	if v := os.Getenv("IZEROP_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("IZEROP_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("IZEROP_SYNC_DIR"); v != "" {
		cfg.SyncDir = v
	}

	if cfg.ServerURL == "" {
		cfg.ServerURL = "https://izerop.com"
	}

	return &cfg, nil
}

// Save writes the config to disk.
func Save(cfg *Config) error {
	dir, err := DefaultConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("could not create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}

	return nil
}
