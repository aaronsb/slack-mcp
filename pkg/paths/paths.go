package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const AppName = "slack-mcp"

// ConfigDir returns the XDG config directory: $XDG_CONFIG_HOME/slack-mcp
func ConfigDir() string {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

// ConfigPath returns the config file path
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// DataDir returns the XDG data directory: $XDG_DATA_HOME/slack-mcp
func DataDir() string {
	return xdgDir("XDG_DATA_HOME", ".local", "share")
}

// StateDir returns the XDG state directory: $XDG_STATE_HOME/slack-mcp,
// default ~/.local/state/slack-mcp. The stdio log lives here.
func StateDir() string {
	return xdgDir("XDG_STATE_HOME", ".local", "state")
}

// xdgDir resolves $env/slack-mcp, falling back to ~/<fallback>/slack-mcp.
// A test binary never falls back: a test that reaches here without setting
// env would read or write the operator's real directory, so it panics
// naming the variable to set (t.Setenv(env, t.TempDir())).
func xdgDir(env string, fallback ...string) string {
	if base := os.Getenv(env); base != "" {
		return filepath.Join(base, AppName)
	}
	if testing.Testing() {
		panic(fmt.Sprintf("paths: %s is unset in a test; set it with t.Setenv(%q, t.TempDir()) so the test cannot touch the real directory", env, env))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(append(fallback, AppName)...)
	}
	return filepath.Join(append(append([]string{home}, fallback...), AppName)...)
}
