package setup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

var xoxcPattern = regexp.MustCompile(`xoxc-[a-zA-Z0-9_-]{80,}`)

// ExtractSlackXoxcToken reads the xoxc token from Chrome's localStorage
// LevelDB files without launching Chrome. The token is stored unencrypted.
func ExtractSlackXoxcToken(userDataDir, profileDir string) (string, error) {
	ldbDir := filepath.Join(userDataDir, profileDir, "Local Storage", "leveldb")
	if _, err := os.Stat(ldbDir); err != nil {
		return "", fmt.Errorf("localStorage dir not found: %s", ldbDir)
	}

	log.Printf("Token extract: scanning %s for xoxc token", ldbDir)

	entries, err := os.ReadDir(ldbDir)
	if err != nil {
		return "", fmt.Errorf("failed to read localStorage dir: %w", err)
	}

	// Search .ldb files first (compacted), then .log (write-ahead)
	// Later files are more recent, so search in reverse
	var candidates []string
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if ext == ".ldb" || ext == ".log" {
			candidates = append(candidates, filepath.Join(ldbDir, e.Name()))
		}
	}

	// Search newest files first for the most recent token
	var latestToken string
	for i := len(candidates) - 1; i >= 0; i-- {
		data, err := os.ReadFile(candidates[i])
		if err != nil {
			continue
		}
		match := xoxcPattern.Find(data)
		if match != nil {
			latestToken = string(match)
		}
	}

	if latestToken == "" {
		return "", fmt.Errorf("no xoxc token found in localStorage — this profile may not have an active Slack session")
	}

	log.Printf("Token extract: found xoxc token (%d chars)", len(latestToken))
	return latestToken, nil
}

// ExtractTokensDirectly extracts both Slack tokens from Chrome's profile
// without launching a browser. Reads xoxc from localStorage LevelDB and
// d cookie from the encrypted Cookies SQLite database.
func ExtractTokensDirectly(userDataDir, profileDir string) (xoxc, xoxd string, err error) {
	xoxc, err = ExtractSlackXoxcToken(userDataDir, profileDir)
	if err != nil {
		return "", "", fmt.Errorf("xoxc extraction failed: %w", err)
	}

	xoxd, err = ExtractSlackDCookie(userDataDir, profileDir)
	if err != nil {
		return "", "", fmt.Errorf("xoxd extraction failed: %w", err)
	}

	return xoxc, xoxd, nil
}
