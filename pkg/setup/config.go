package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WorkspaceConfig holds tokens for a single workspace
type WorkspaceConfig struct {
	XoxcToken string `json:"xoxc_token"`
	XoxdToken string `json:"xoxd_token"`
	TeamName  string `json:"team_name,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// Config holds all workspace configurations
type Config struct {
	Workspaces       map[string]WorkspaceConfig `json:"workspaces"`
	DefaultWorkspace string                     `json:"default_workspace,omitempty"`
}

// ConfigDir returns the config directory path
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/slack-mcp"
	}
	return filepath.Join(home, ".config", "slack-mcp")
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
