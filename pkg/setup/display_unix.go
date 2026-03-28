//go:build !windows

package setup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ensureDisplay restores session environment variables that MCP hosts
// like Claude Desktop may not propagate to child processes.
// Covers: XDG_RUNTIME_DIR, WAYLAND_DISPLAY, DISPLAY, DBUS_SESSION_BUS_ADDRESS.
func ensureDisplay() {
	// Resolve XDG_RUNTIME_DIR first — other vars depend on it
	xrd := os.Getenv("XDG_RUNTIME_DIR")
	if xrd == "" {
		xrd = fmt.Sprintf("/run/user/%d", os.Getuid())
		if info, err := os.Stat(xrd); err == nil && info.IsDir() {
			os.Setenv("XDG_RUNTIME_DIR", xrd)
			log.Printf("CDP: set XDG_RUNTIME_DIR=%s", xrd)
		} else {
			xrd = ""
		}
	}

	// Display vars
	needsDisplay := os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
	if needsDisplay {
		log.Println("CDP: no display env vars set, attempting detection...")

		// Wayland
		if xrd != "" {
			matches, _ := filepath.Glob(filepath.Join(xrd, "wayland-[0-9]"))
			if len(matches) > 0 {
				name := filepath.Base(matches[0])
				os.Setenv("WAYLAND_DISPLAY", name)
				log.Printf("CDP: detected WAYLAND_DISPLAY=%s", name)
			}
		}

		// X11
		entries, err := os.ReadDir("/tmp/.X11-unix")
		if err == nil {
			for _, e := range entries {
				if len(e.Name()) > 0 && e.Name()[0] == 'X' {
					display := fmt.Sprintf(":%s", e.Name()[1:])
					os.Setenv("DISPLAY", display)
					log.Printf("CDP: detected DISPLAY=%s", display)
					break
				}
			}
		}

		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			log.Println("CDP: WARNING — no display server detected, browser will not be visible")
		}
	}

	// DBus session bus — needed for GNOME Keyring (cookie decryption)
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && xrd != "" {
		busPath := filepath.Join(xrd, "bus")
		if _, err := os.Stat(busPath); err == nil {
			addr := "unix:path=" + busPath
			os.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
			log.Printf("CDP: set DBUS_SESSION_BUS_ADDRESS=%s", addr)
		}
	}
}
