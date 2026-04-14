package paths

import (
	"os"
	"path/filepath"
)

const AppName = "slack-mcp"

// ConfigDir returns the XDG config directory: $XDG_CONFIG_HOME/slack-mcp
func ConfigDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, AppName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", AppName)
	}
	return filepath.Join(home, ".config", AppName)
}

// ConfigPath returns the config file path
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// DownloadsDir returns the user's downloads directory.
//
// Order of preference:
//  1. $XDG_DOWNLOAD_DIR if set. Note: this env var is a convention, not part
//     of the XDG Base Directory spec — the real user-dirs.dirs file isn't
//     parsed here. We honor it so callers can override without touching
//     CLI args.
//  2. $HOME/Downloads.
//  3. os.TempDir() as a last resort for headless environments where $HOME
//     isn't set — avoids returning a relative path that resolves against
//     a surprising CWD.
func DownloadsDir() string {
	if dir := os.Getenv("XDG_DOWNLOAD_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, "Downloads")
}

// DataDir returns the XDG data directory: $XDG_DATA_HOME/slack-mcp
func DataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, AppName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "share", AppName)
	}
	return filepath.Join(home, ".local", "share", AppName)
}
