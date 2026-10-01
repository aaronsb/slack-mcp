//go:build !unix

package safety

// oNoFollow is zero where the platform has no O_NOFOLLOW; checkRegular's
// Lstat still refuses a symlink before the open.
const oNoFollow = 0
