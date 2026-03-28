//go:build linux

package setup

import (
	"log"
	"os/exec"
	"strings"
)

// getChromeSafeStorageKey retrieves Chrome's encryption password from the
// system keyring. Chrome on Linux tries multiple backends in order:
//
//  1. libsecret (Secret Service API) — GNOME Keyring or KWallet portal
//  2. KWallet native — direct DBus via kwallet-query
//  3. Hardcoded fallback "peanuts" — headless / no keyring environments
//
// We mirror this priority chain.
func getChromeSafeStorageKey() (string, error) {
	ensureDBus()

	// 1. Try libsecret / Secret Service API (GNOME Keyring, KWallet portal)
	if key := trySecretTool(); key != "" {
		return key, nil
	}

	// 2. Try KWallet native (KDE without Secret Service bridge)
	if key := tryKWallet(); key != "" {
		return key, nil
	}

	// 3. Chrome's hardcoded fallback when no keyring is available
	// (see chromium source: components/os_crypt/sync/os_crypt_linux.cc)
	log.Printf("Cookie extract: no keyring key found, using Chrome default")
	return "peanuts", nil
}

func trySecretTool() string {
	path, err := exec.LookPath("secret-tool")
	if err != nil {
		return ""
	}
	for _, app := range []string{"chrome", "chromium"} {
		out, err := exec.Command(path, "lookup", "application", app).Output()
		if err == nil && len(out) > 0 {
			key := strings.TrimSpace(string(out))
			if key != "" {
				log.Printf("Cookie extract: got key via secret-tool for %s", app)
				return key
			}
		}
	}
	return ""
}

func tryKWallet() string {
	path, err := exec.LookPath("kwallet-query")
	if err != nil {
		return ""
	}
	for _, folder := range []string{"Chrome Keys", "Chromium Keys"} {
		label := strings.Replace(folder, "Keys", "Safe Storage", 1)
		out, err := exec.Command(path, "-r", label, "-f", folder, "kdewallet").Output()
		if err == nil && len(out) > 0 {
			key := strings.TrimSpace(string(out))
			if key != "" {
				log.Printf("Cookie extract: got key via kwallet-query from %q", folder)
				return key
			}
		}
	}
	return ""
}
