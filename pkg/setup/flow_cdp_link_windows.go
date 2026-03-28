//go:build windows

package setup

import (
	"fmt"
	"os"
	"os/exec"
)

// linkProfileDir creates a directory junction on Windows. Junctions don't
// require Developer Mode or admin privileges, unlike symlinks.
// Falls back to symlink if junction creation fails.
func linkProfileDir(target, link string) error {
	// Try symlink first (works if Developer Mode is on)
	if err := os.Symlink(target, link); err == nil {
		return nil
	}

	// Fall back to directory junction via mklink /J
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create directory junction: %w (output: %s)", err, string(out))
	}
	return nil
}
