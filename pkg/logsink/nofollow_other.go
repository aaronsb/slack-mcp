//go:build !unix

package logsink

// noFollow has no portable equivalent off Unix.
const noFollow = 0
