// Package logsink owns where the server's log goes and what it may contain.
//
// Log sites name explicit fields and never print a token, a cookie, or a
// whole response. The redacting writer here is the safety net under that
// rule, not the rule itself: it masks anything token- or cookie-shaped that
// a future log line lets through.
package logsink

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// FileEnv overrides the stdio log path. It is read from the client
// environment only; .env may not set it.
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
)

// Redact masks token-shaped substrings, d=/uc= cookie values, and bearer
// credentials in s.
func Redact(s []byte) []byte {
	s = tokenPattern.ReplaceAll(s, []byte(Redacted))
	s = cookiePattern.ReplaceAll(s, []byte("${1}="+Redacted))
	s = bearerPattern.ReplaceAll(s, []byte("${1}"+Redacted))
	return s
}

type redactingWriter struct{ w io.Writer }

// NewRedactingWriter wraps w so every write is redacted first. The log
// package issues one Write per entry, so a value is never split across
// writes.
func NewRedactingWriter(w io.Writer) io.Writer {
	return redactingWriter{w: w}
}

func (r redactingWriter) Write(p []byte) (int, error) {
	if _, err := r.w.Write(Redact(append([]byte(nil), p...))); err != nil {
		return 0, err
	}
	// Report the caller's length: the redacted form differs in size, and
	// io.Writer callers treat a short count as an error.
	return len(p), nil
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
// Its directory is created 0700. An existing file is tightened to 0600,
// since OpenFile applies the mode only on create. The default state
// directory is tightened to 0700 too; the parent of an override path is
// the operator's, and is left as it is.
func Open(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	if filepath.Clean(dir) == filepath.Clean(paths.StateDir()) {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("restrict log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("restrict log: %w", err)
	}
	return f, nil
}
