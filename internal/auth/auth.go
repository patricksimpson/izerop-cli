package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/patricksimpson/izerop-cli/pkg/config"
)

// Login prompts for server URL and API token, then saves the config.
func Login() error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Server URL [https://izerop.com]: ")
	serverURL, _ := reader.ReadString('\n')
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = "https://izerop.com"
	}

	fmt.Print("API Token: ")
	token, _ := reader.ReadString('\n')
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is required")
	}

	// Load existing config to preserve settings like SyncDir
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.ServerURL = serverURL
	cfg.Token = token

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}

	fmt.Printf("Login successful! Config saved to profile %q.\n", config.GetActiveProfile())
	return nil
}
