//go:build unix

package logsink

import "syscall"

// noFollow makes the open fail when the log path's last component is a
// symlink, so a planted link cannot redirect the log or have its target
// chmodded.
const noFollow = syscall.O_NOFOLLOW
