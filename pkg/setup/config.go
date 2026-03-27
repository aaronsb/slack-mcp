package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const appName = "slack-mcp"

// WorkspaceConfig holds tokens for a single workspace
type WorkspaceConfig struct {
	XoxcToken string `json:"xoxc_token"`
	XoxdToken string `json:"xoxd_token"`
	TeamName  string `json:"team_name,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// FlowState persists the setup flow's current position across server restarts
type FlowState struct {
	State       string    `json:"state"`
	Tier        int       `json:"tier,omitempty"`
	BrowserPath string    `json:"browser_path,omitempty"`
	BrowserName string    `json:"browser_name,omitempty"`
	ProfileDir  string    `json:"profile_dir,omitempty"`
	UserDataDir string    `json:"user_data_dir,omitempty"`
	TempDir     string    `json:"temp_dir,omitempty"`
	Port        int       `json:"port,omitempty"`
	StartedAt   time.Time `json:"started_at"`
}

const flowTTL = 1 * time.Hour

// FlowExpired returns true if the flow state is older than the TTL
func (fs *FlowState) FlowExpired() bool {
	if fs == nil {
		return true
	}
	return time.Since(fs.StartedAt) > flowTTL
}

// Config holds all workspace configurations
type Config struct {
	Workspaces       map[string]WorkspaceConfig `json:"workspaces"`
	DefaultWorkspace string                     `json:"default_workspace,omitempty"`
	SetupFlow        *FlowState                 `json:"setup_flow,omitempty"`
}

// ClearFlow removes setup flow state and saves the config
func (c *Config) ClearFlow() error {
	c.SetupFlow = nil
	return SaveConfig(c)
}

// ConfigDir returns the XDG config directory: $XDG_CONFIG_HOME/slack-mcp
func ConfigDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, appName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", appName)
	}
	return filepath.Join(home, ".config", appName)
}

// DataDir returns the XDG data directory: $XDG_DATA_HOME/slack-mcp
func DataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, appName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "share", appName)
	}
	return filepath.Join(home, ".local", "share", appName)
}

// ConfigPath returns the config file path
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// LoadConfig reads the config file, returning an empty config if it doesn't exist
func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				Workspaces: make(map[string]WorkspaceConfig),
			}, nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.Workspaces == nil {
		cfg.Workspaces = make(map[string]WorkspaceConfig)
	}

	return &cfg, nil
}

// SaveConfig writes the config file
func SaveConfig(cfg *Config) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write with restrictive permissions — this file contains tokens
	if err := os.WriteFile(ConfigPath(), data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}
