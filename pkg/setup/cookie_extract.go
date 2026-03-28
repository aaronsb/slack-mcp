package setup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
	"golang.org/x/crypto/pbkdf2"
)

// ExtractSlackDCookie reads the "d" cookie directly from Chrome's encrypted
// Cookies SQLite database without launching Chrome.
//
// Steps:
//  1. Copy the Cookies file (Chrome holds a lock on the original)
//  2. Query for the "d" cookie on slack.com
//  3. Get the decryption key from the system keyring via secret-tool
//  4. Decrypt: PBKDF2-SHA1(keyring_password, "saltysalt", 1 iter) → AES-128-CBC
//  5. Strip 32-byte SHA256 domain hash (cookie DB version 24+)
//  6. Delete the copy
func ExtractSlackDCookie(userDataDir, profileDir string) (string, error) {
	cookiesPath := filepath.Join(userDataDir, profileDir, "Cookies")
	if _, err := os.Stat(cookiesPath); err != nil {
		return "", fmt.Errorf("cookies file not found: %s", cookiesPath)
	}

	// Copy to temp file to avoid SQLite lock conflicts
	tmpFile, err := os.CreateTemp("", "slack-mcp-cookies-*.db")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	data, err := os.ReadFile(cookiesPath)
	if err != nil {
		return "", fmt.Errorf("failed to read cookies file: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return "", fmt.Errorf("failed to write temp cookies: %w", err)
	}

	log.Printf("Cookie extract: reading %s", cookiesPath)

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return "", fmt.Errorf("failed to open cookies db: %w", err)
	}
	defer db.Close()

	// Check cookie database version (affects decrypted value format)
	var dbVersion int
	if err := db.QueryRow("SELECT value FROM meta WHERE key='version'").Scan(&dbVersion); err != nil {
		dbVersion = 0
	}

	var encryptedValue []byte
	err = db.QueryRow(
		`SELECT encrypted_value FROM cookies
		 WHERE name = 'd' AND host_key LIKE '%slack.com'
		 LIMIT 1`,
	).Scan(&encryptedValue)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no d cookie found for slack.com in %s/%s", userDataDir, profileDir)
	}
	if err != nil {
		return "", fmt.Errorf("failed to query cookies: %w", err)
	}

	if len(encryptedValue) < 4 {
		return "", fmt.Errorf("encrypted cookie value too short (%d bytes)", len(encryptedValue))
	}

	prefix := string(encryptedValue[:3])
	if prefix != "v10" && prefix != "v11" {
		return "", fmt.Errorf("unknown encryption prefix: %q", prefix)
	}

	log.Printf("Cookie extract: found encrypted d cookie (prefix=%s, dbVersion=%d, %d bytes)", prefix, dbVersion, len(encryptedValue))

	// Get the encryption password from the system keyring
	password, err := getChromeSafeStorageKey()
	if err != nil {
		return "", err
	}

	// Decrypt: PBKDF2-SHA1, salt="saltysalt", 1 iteration, 16-byte key → AES-128-CBC
	key := pbkdf2.Key([]byte(password), []byte("saltysalt"), 1, 16, sha1.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	ciphertext := encryptedValue[3:]
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext length %d not a multiple of block size", len(ciphertext))
	}

	iv := make([]byte, aes.BlockSize)
	mode := cipher.NewCBCDecrypter(block, iv)

	plain := make([]byte, len(ciphertext))
	mode.CryptBlocks(plain, ciphertext)

	// Remove PKCS7 padding
	if len(plain) > 0 {
		padLen := int(plain[len(plain)-1])
		if padLen > 0 && padLen <= aes.BlockSize && padLen <= len(plain) {
			plain = plain[:len(plain)-padLen]
		}
	}

	// Cookie DB version 24+: first 32 bytes are SHA256 hash of the domain
	if dbVersion >= 24 && len(plain) > 32 {
		plain = plain[32:]
	}

	result := string(plain)
	if result == "" {
		return "", fmt.Errorf("decrypted cookie is empty")
	}

	log.Printf("Cookie extract: successfully decrypted d cookie (%d chars)", len(result))
	return result, nil
}

// getChromeSafeStorageKey retrieves Chrome's encryption password from the
// system keyring using secret-tool (freedesktop.org Secret Service API).
// Works with both GNOME Keyring and KWallet.
func getChromeSafeStorageKey() (string, error) {
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
