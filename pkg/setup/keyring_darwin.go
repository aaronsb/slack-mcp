//go:build darwin

package setup

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// getChromeSafeStorageKey retrieves Chrome's encryption password from the
// macOS Keychain using the security command.
func getChromeSafeStorageKey() (string, error) {
	for _, service := range []string{"Chrome Safe Storage", "Chromium Safe Storage"} {
		out, err := exec.Command(
			"security", "find-generic-password",
			"-s", service,
			"-w",
		).Output()
		if err == nil && len(out) > 0 {
			key := strings.TrimSpace(string(out))
			if key != "" {
				log.Printf("Cookie extract: got keychain key for %s", service)
				return key, nil
			}
		}
	}

	return "", fmt.Errorf("no Chrome safe storage key found in macOS Keychain")
}
