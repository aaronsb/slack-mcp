//go:build !windows

package setup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ensureDisplay detects and sets DISPLAY/WAYLAND_DISPLAY if missing.
// MCP hosts like Claude Desktop may not propagate display env vars
// to child processes, but the display server is still running.
func ensureDisplay() {
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}

	log.Println("CDP: no display env vars set, attempting detection...")

	// Try Wayland first — check XDG_RUNTIME_DIR for wayland sockets
	if xrd := os.Getenv("XDG_RUNTIME_DIR"); xrd != "" {
		matches, _ := filepath.Glob(filepath.Join(xrd, "wayland-[0-9]"))
		if len(matches) > 0 {
			name := filepath.Base(matches[0])
			os.Setenv("WAYLAND_DISPLAY", name)
			log.Printf("CDP: detected %s from XDG_RUNTIME_DIR", name)
		}
	}

	// Try X11 — check /tmp/.X11-unix for sockets
	entries, err := os.ReadDir("/tmp/.X11-unix")
	if err == nil {
		for _, e := range entries {
			if len(e.Name()) > 0 && e.Name()[0] == 'X' {
				display := fmt.Sprintf(":%s", e.Name()[1:])
				os.Setenv("DISPLAY", display)
				log.Printf("CDP: detected DISPLAY=%s from /tmp/.X11-unix", display)
				break
			}
		}
	}

	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		log.Println("CDP: WARNING — no display server detected, browser will not be visible")
	}
}
