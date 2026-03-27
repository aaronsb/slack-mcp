package setup

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed flow_firefox_ext/manifest.json
var firefoxManifest []byte

//go:embed flow_firefox_ext/background.js
var firefoxBackgroundJS []byte

//go:embed flow_firefox_ext/content.js
var firefoxContentJS []byte

// WriteFirefoxExtension writes the temporary WebExtension files to a temp
// directory, substituting the callback port. Returns the temp dir path.
func WriteFirefoxExtension(port int) (string, error) {
	dir, err := os.MkdirTemp("", "slack-mcp-ext-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	portStr := fmt.Sprintf("%d", port)

	// Write manifest (no substitution needed)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), firefoxManifest, 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("failed to write manifest.json: %w", err)
	}

	// Write background.js with port substitution
	bgJS := strings.ReplaceAll(string(firefoxBackgroundJS), "{{CALLBACK_PORT}}", portStr)
	if err := os.WriteFile(filepath.Join(dir, "background.js"), []byte(bgJS), 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("failed to write background.js: %w", err)
	}

	// Write content.js (no substitution needed)
	if err := os.WriteFile(filepath.Join(dir, "content.js"), firefoxContentJS, 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("failed to write content.js: %w", err)
	}

	return dir, nil
}

// CleanupFirefoxExtension removes the temporary extension directory
func CleanupFirefoxExtension(dir string) {
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// Ensure embed import is used (the go:embed directives reference it implicitly)
var _ embed.FS
