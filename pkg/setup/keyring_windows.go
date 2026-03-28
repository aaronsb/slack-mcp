//go:build windows

package setup

import (
	"fmt"
)

// getChromeSafeStorageKey on Windows.
// Chrome on Windows uses DPAPI (Data Protection API) which encrypts with
// the user's login credentials — no separate password to retrieve.
// The encrypted_key is stored in Local State as base64(DPAPI(key)).
//
// TODO: Implement DPAPI decryption via CryptUnprotectData syscall.
// For now, return an error directing the user to manual setup.
func getChromeSafeStorageKey() (string, error) {
	return "", fmt.Errorf("automatic cookie extraction is not yet supported on Windows — use manual token entry or the auth-setup browser flow")
}
