//go:build !windows

package setup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ensureDBus sets DBUS_SESSION_BUS_ADDRESS if missing.
// MCP hosts like Claude Desktop may not propagate session env vars.
// secret-tool needs DBus to reach the system keyring (KWallet/GNOME Keyring).
func ensureDBus() {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return
	}

	// Resolve XDG_RUNTIME_DIR — standard location for the session bus socket
	xrd := os.Getenv("XDG_RUNTIME_DIR")
	if xrd == "" {
		xrd = fmt.Sprintf("/run/user/%d", os.Getuid())
		if info, err := os.Stat(xrd); err == nil && info.IsDir() {
			os.Setenv("XDG_RUNTIME_DIR", xrd)
			log.Printf("Session: set XDG_RUNTIME_DIR=%s", xrd)
		} else {
			return
		}
	}

	busPath := filepath.Join(xrd, "bus")
	if _, err := os.Stat(busPath); err == nil {
		addr := "unix:path=" + busPath
		os.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
		log.Printf("Session: set DBUS_SESSION_BUS_ADDRESS=%s", addr)
	}
}
