//go:build !windows

package setup

import "os"

// linkProfileDir creates a symlink from target to link.
func linkProfileDir(target, link string) error {
	return os.Symlink(target, link)
}
