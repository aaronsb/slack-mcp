// Package logsink owns where the server's log goes and what it may contain.
//
// Log sites name explicit fields and never print a token, a cookie, or a
// whole response. The redacting writer here is the safety net under that
// rule, not the rule itself: it masks anything token- or cookie-shaped that
// a future log line lets through.
package logsink

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// FileEnv overrides the stdio log path. It is read from the client
// environment only; .env may not set it. /dev/null discards the log.
const FileEnv = "SLACK_MCP_LOG_FILE"

// FileName is the log's name inside the state directory.
const FileName = "slack-mcp.log"

// Redacted replaces every masked value.
const Redacted = "<redacted>"

var (
	// Slack tokens: xoxc-, xoxd-, xoxp-, xoxb-, xoxe-, and the
	// xoxe.xoxp- refresh form. The value runs over URL-safe and
	// percent-encoded characters, as a xoxd cookie appears in a header.
	tokenPattern = regexp.MustCompile(`xox[a-z](?:\.xox[a-z])?-[A-Za-z0-9%._~+/=\-]+`)

	// Session cookies by name: d= (the xoxd cookie) and uc=. The leading
	// boundary keeps "id=" and "uid=" out of the match.
	cookiePattern = regexp.MustCompile(`\b(d|uc)=[^;\s"'&,]+`)

	// Bearer credentials in an Authorization header or its echo.
	bearerPattern = regexp.MustCompile(`(?i)\b(bearer\s+)[^\s"',;]+`)

	// URL userinfo, as in a proxy URL: scheme://user:pass@host.
	userinfoPattern = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s@]+@`)
)

// Redact masks token-shaped substrings, d=/uc= cookie values, bearer
// credentials, and URL userinfo in s.
//
// It errs toward masking: "d=5" and "bearer of the message" lose their
// values too. For a safety net that is the right trade.
func Redact(s []byte) []byte {
	s = userinfoPattern.ReplaceAll(s, []byte("${1}"+Redacted+"@"))
	s = tokenPattern.ReplaceAll(s, []byte(Redacted))
	s = cookiePattern.ReplaceAll(s, []byte("${1}="+Redacted))
	s = bearerPattern.ReplaceAll(s, []byte("${1}"+Redacted))
	return s
}

// maxPending bounds how much of an unterminated line is held. Past it the
// held bytes are redacted and written anyway, so a writer that never sends
// a newline cannot grow memory without limit; a value split at that cut
// would be redacted only in part.
const maxPending = 64 << 10

// RedactingWriter redacts output by line before it reaches the underlying
// writer.
//
// Line buffering assumes log.Logger-style writers, which emit each entry
// whole and end it with a newline: such an entry is redacted and written
// at once, and nothing is held back. A writer that sends a partial line
// has it held until its newline, the 64 KiB cap, or Flush. Call Flush
// before exiting so a held partial line is not lost.
//
// If the underlying write fails, the held lines stay pending and are
// retried on the next Write or Flush; nothing is dropped, though pending
// keeps growing while the sink keeps failing, and a sink that failed
// partway through a write may see part of a line twice.
type RedactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

// NewRedactingWriter wraps w so output is redacted before it reaches w.
func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

// Write holds p until it completes a line, then redacts and writes every
// complete line held.
func (r *RedactingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pending = append(r.pending, p...)
	cut := bytes.LastIndexByte(r.pending, '\n') + 1
	if cut == 0 && len(r.pending) > maxPending {
		cut = len(r.pending)
	}
	if err := r.emit(cut); err != nil {
		return 0, err
	}
	// Report the caller's length: the redacted form differs in size, and
	// io.Writer callers treat a short count as an error.
	return len(p), nil
}

// Flush redacts and writes whatever is held, including a partial line.
func (r *RedactingWriter) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.emit(len(r.pending))
}

// emit redacts and writes pending[:cut], and drops it from pending only
// once the write succeeds. The caller holds mu.
func (r *RedactingWriter) emit(cut int) error {
	if cut == 0 {
		return nil
	}
	if _, err := r.w.Write(Redact(append([]byte(nil), r.pending[:cut]...))); err != nil {
		return err
	}
	r.pending = append(r.pending[:0], r.pending[cut:]...)
	return nil
}

// Path returns where the stdio log goes: SLACK_MCP_LOG_FILE when set,
// otherwise slack-mcp.log in the XDG state directory.
func Path() string {
	if p := os.Getenv(FileEnv); p != "" {
		return p
	}
	return filepath.Join(paths.StateDir(), FileName)
}

// Open opens the log at path for appending, readable by the owner only.
//
//   - The directory is created 0700. The default state directory must not
//     be a symlink and is tightened to 0700; the parent of an override path
//     is the operator's and is left as it is.
//   - On Unix a symlink at the path itself is refused (O_NOFOLLOW), so a
//     planted link can neither redirect the log nor have its target
//     chmodded.
//   - A regular file is tightened to 0600, since OpenFile applies the mode
//     only on create. Anything else, such as /dev/null or a pipe, is used
//     as it is.
func Open(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	if filepath.Clean(dir) == filepath.Clean(paths.StateDir()) {
		fi, err := os.Lstat(dir)
		if err != nil {
			return nil, fmt.Errorf("check log directory: %w", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return nil, errors.New("log directory " + dir + " is not a plain directory; refusing it")
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("restrict log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|noFollow, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("check log: %w", err)
	}
	if fi.Mode().IsRegular() {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, fmt.Errorf("restrict log: %w", err)
		}
	}
	return f, nil
}
