package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Config holds the CLI configuration for a single profile.
type Config struct {
	ServerURL    string `json:"server_url"`
	Token        string `json:"token"`
	SyncDir      string `json:"sync_dir,omitempty"`
	SettleTimeMs int    `json:"settle_time_ms,omitempty"` // debounce delay before syncing new/changed files (default 12000)
}

// DefaultSettleTimeMs is the default debounce delay in milliseconds.
// This gives users time to finish renaming files/folders before sync fires.
const DefaultSettleTimeMs = 12000

const DefaultProfile = "default"

// DefaultConfigDir returns the config directory path (~/.config/izerop).
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "izerop"), nil
}

// ProfileDir returns the directory for a specific profile.
func ProfileDir(name string) (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "profiles", name), nil
}

// ProfileConfigPath returns the config file path for a profile.
func ProfileConfigPath(name string) (string, error) {
	dir, err := ProfileDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ProfilePIDPath returns the PID file path for a profile's watcher.
func ProfilePIDPath(name string) (string, error) {
	dir, err := ProfileDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "watch.pid"), nil
}

// ProfileStatePath returns the sync state file path for a profile.
func ProfileStatePath(name string) (string, error) {
	dir, err := ProfileDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sync-state.json"), nil
}

// ProfileLogPath returns the log file path for a profile's watcher.
func ProfileLogPath(name string) (string, error) {
	dir, err := ProfileDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "watch.log"), nil
}

// ConfigPath returns the full path to the legacy config file.
func ConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ActiveProfilePath returns the path to the file storing the active profile name.
func ActiveProfilePath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "active_profile"), nil
}

// GetActiveProfile returns the name of the currently active profile.
func GetActiveProfile() string {
	path, err := ActiveProfilePath()
	if err != nil {
		return DefaultProfile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultProfile
	}
	name := string(data)
	if name == "" {
		return DefaultProfile
	}
	return name
}

// SetActiveProfile writes the active profile name.
func SetActiveProfile(name string) error {
	path, err := ActiveProfilePath()
	if err != nil {
		return err
	}
	dir, _ := DefaultConfigDir()
	os.MkdirAll(dir, 0700)
	return os.WriteFile(path, []byte(name), 0600)
}

// ListProfiles returns all profile names.
func ListProfiles() ([]string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return nil, err
	}
	profilesDir := filepath.Join(dir, "profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			// Only include if it has a config.json
			cfgPath := filepath.Join(profilesDir, e.Name(), "config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// Load reads the active profile's config, with legacy migration.
func Load() (*Config, error) {
	return LoadProfile(GetActiveProfile())
}

// LoadProfile reads a specific profile's config.
func LoadProfile(name string) (*Config, error) {
	// Try profile path first
	profilePath, err := ProfileConfigPath(name)
	if err != nil {
		return nil, err
	}

	var cfg Config

	data, err := os.ReadFile(profilePath)
	if err != nil {
		// Try legacy path for default profile
		if name == DefaultProfile {
			legacyPath, _ := ConfigPath()
			data, err = os.ReadFile(legacyPath)
			if err != nil {
				cfg = Config{}
			} else {
				if err := json.Unmarshal(data, &cfg); err != nil {
					return nil, fmt.Errorf("invalid config: %w", err)
				}
				// Migrate legacy config to profile
				if saveErr := SaveProfile(name, &cfg); saveErr == nil {
					os.Remove(legacyPath)
				}
			}
		} else {
			return nil, fmt.Errorf("profile %q not found", name)
		}
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
	}

	// Env var overrides (only for active profile)
	if name == GetActiveProfile() {
		if v := os.Getenv("IZEROP_SERVER_URL"); v != "" {
			cfg.ServerURL = v
		}
		if v := os.Getenv("IZEROP_TOKEN"); v != "" {
			cfg.Token = v
		}
		if v := os.Getenv("IZEROP_SYNC_DIR"); v != "" {
			cfg.SyncDir = v
		}
	}

	// Default settle time if not set
	if cfg.SettleTimeMs <= 0 {
		cfg.SettleTimeMs = DefaultSettleTimeMs
	}

	if cfg.ServerURL == "" {
		cfg.ServerURL = "https://izerop.com"
	}

	return &cfg, nil
}

// Save writes the active profile's config to disk.
func Save(cfg *Config) error {
	return SaveProfile(GetActiveProfile(), cfg)
}

// SaveProfile writes a specific profile's config to disk.
func SaveProfile(name string, cfg *Config) error {
	dir, err := ProfileDir(name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("could not create profile dir: %w", err)
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

// DeleteProfile removes a profile directory.
func DeleteProfile(name string) error {
	if name == DefaultProfile {
		return fmt.Errorf("cannot delete the default profile")
	}
	dir, err := ProfileDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
