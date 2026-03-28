//go:build linux

package setup

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// getChromeSafeStorageKey retrieves Chrome's encryption password from the
// system keyring using secret-tool (freedesktop.org Secret Service API).
// Works with both GNOME Keyring and KWallet.
func getChromeSafeStorageKey() (string, error) {
	ensureDBus()
	for _, app := range []string{"chrome", "chromium"} {
		out, err := exec.Command("secret-tool", "lookup", "application", app).Output()
		if err == nil && len(out) > 0 {
			key := strings.TrimSpace(string(out))
			if key != "" {
				log.Printf("Cookie extract: got keyring key for %s", app)
				return key, nil
			}
		}
	}

	return "", fmt.Errorf("no Chrome safe storage key found in keyring (tried secret-tool for chrome and chromium)")
}
