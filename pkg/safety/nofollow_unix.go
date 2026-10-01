//go:build unix

package safety

import "syscall"

// oNoFollow makes an open fail on a symlink rather than follow it.
const oNoFollow = syscall.O_NOFOLLOW
