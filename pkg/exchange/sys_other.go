//go:build !unix && !windows

package exchange

import (
	"io/fs"
	"os"
)

const openReadFlags = os.O_RDONLY

// verifyRoot refuses on platforms where the directory checks are not
// implemented, so file operations fail closed.
func verifyRoot(p string, _ *os.Root, _ fs.FileInfo) error {
	return &Error{Path: p, Rule: "file operations are not supported on this platform"}
}

func linkCount(*os.File, fs.FileInfo) (uint64, error) { return 0, os.ErrInvalid }
