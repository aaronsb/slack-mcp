//go:build unix

package report

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file belongs to this process's uid.
func ownedByCurrentUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid())
}
