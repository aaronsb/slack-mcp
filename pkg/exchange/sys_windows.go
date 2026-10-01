//go:build windows

package exchange

import (
	"io/fs"
	"os"
	"syscall"
)

// openReadFlags: Windows has no O_NONBLOCK; the handle is still checked for
// a regular file after the open.
const openReadFlags = os.O_RDONLY

// verifyRoot has no ownership, mode, or identity check on Windows (ADR-012):
// the default location sits inside the user profile, and os.SameFile there
// reopens paths lazily.
func verifyRoot(string, *os.Root, fs.FileInfo) error { return nil }

// linkCount reads NumberOfLinks from the open handle.
func linkCount(f *os.File, _ fs.FileInfo) (uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
