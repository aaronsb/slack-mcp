//go:build unix

package exchange

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// openReadFlags adds O_NONBLOCK so a FIFO swapped in after any earlier check
// cannot block the open; the handle is checked for a regular file after.
const openReadFlags = os.O_RDONLY | syscall.O_NONBLOCK

// verifyRoot checks, on the root just opened, that it is the directory the
// Lstat saw (eager device and inode comparison), owned by the current user,
// with no group or other permission bits.
func verifyRoot(p string, root *os.Root, lst fs.FileInfo) error {
	refuse := func(rule string) error { return &Error{Path: p, Rule: rule} }

	st, err := root.Stat(".")
	if err != nil {
		return refuse(err.Error())
	}
	if !os.SameFile(lst, st) {
		return refuse("it changed between the check and the open")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return refuse("its owner cannot be read")
	}
	if uid := os.Getuid(); int(sys.Uid) != uid {
		return refuse(fmt.Sprintf("it is owned by uid %d, not the server's uid %d", sys.Uid, uid))
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return refuse(fmt.Sprintf("its mode is %04o; it must have no group or other permission bits (chmod 700 %s)", perm, p))
	}
	return nil
}

// linkCount reports the number of names the open file has, from the Stat of
// the handle.
func linkCount(_ *os.File, fi fs.FileInfo) (uint64, error) {
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("link count unavailable")
	}
	return uint64(sys.Nlink), nil
}

// linkCountByName reports the link count of a name in the root, for the
// missing-name hint, from its Stat.
func linkCountByName(_ *os.Root, _ string, fi fs.FileInfo) (uint64, error) {
	return linkCount(nil, fi)
}
