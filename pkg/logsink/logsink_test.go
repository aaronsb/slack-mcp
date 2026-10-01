package logsink

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRedactMasksCredentials(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"xoxc token", "token xoxc-1234-5678-abcdef here", "token <redacted> here"},
		{"xoxd cookie value", "cookie xoxd-FAKE%2Fabc%3D%3D;", "cookie <redacted>;"},
		{"xoxp token", "xoxp-1-2-3", "<redacted>"},
		{"refresh token", "xoxe.xoxp-1-abc", "<redacted>"},
		{"set-cookie header", "Set-Cookie:[uc=SECRETVALUE; path=/]", "Set-Cookie:[uc=<redacted>; path=/]"},
		{"d cookie", "Cookie: d=abc123; d-s=17", "Cookie: d=<redacted>; d-s=17"},
		{"bearer", "Authorization: Bearer sk-live-secret", "Authorization: Bearer <redacted>"},
		{"bearer token shape", "Authorization: Bearer xoxc-1-2", "Authorization: Bearer <redacted>"},
		{"proxy userinfo", "proxyconnect http://alice:s3cret@proxy.example:3128: refused", "proxyconnect http://<redacted>@proxy.example:3128: refused"},
		{"userinfo user only", "dial socks5://tok@10.0.0.1:1080", "dial socks5://<redacted>@10.0.0.1:1080"},
		// Accepted false positives: the net errs toward masking, so these
		// ordinary phrases lose their values. Pinned so the trade is a
		// decision rather than a surprise.
		{"false positive d=5", "retry d=5 attempts", "retry d=<redacted> attempts"},
		{"false positive bearer prose", "the bearer of the message", "the bearer <redacted> the message"},
		{
			"auth.test struct dump",
			"Authenticated as: &{https://x.slack.com/ Team user T1 U1   map[Set-Cookie:[uc=xoxd-FAKE%2Fv%3D; path=/; secure d=xoxd-FAKE2; path=/]]}",
			"Authenticated as: &{https://x.slack.com/ Team user T1 U1   map[Set-Cookie:[uc=<redacted>; path=/; secure d=<redacted>; path=/]]}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Redact([]byte(c.in))); got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRedactLeavesOrdinaryLinesAlone(t *testing.T) {
	for _, in := range []string{
		"Authenticated: team=Praecipio user=bockeliea",
		"Loaded 12 channels from cache",
		"Failed to get DM info for id=D123: not_found",
		"Cookie extract: found encrypted d cookie (prefix=v11, dbVersion=24, 120 bytes)",
		"Ignoring env var tokens: they lack the xoxc/xoxd prefixes",
		"Post \"https://slack.com/api/auth.test\": dial tcp: timeout",
		"Open this URL manually: http://localhost:13080/path@x",
	} {
		if got := string(Redact([]byte(in))); got != in {
			t.Errorf("Redact(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestRedactingWriterThroughLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(NewRedactingWriter(&buf), "", 0)
	logger.Printf("resp header map[Set-Cookie:[d=xoxd-FAKEabc%%2F; Path=/]]")
	if strings.Contains(buf.String(), "xox") || strings.Contains(buf.String(), "FAKE") {
		t.Fatalf("log output carries the credential: %q", buf.String())
	}
}

func TestRedactingWriterReportsCallerLength(t *testing.T) {
	var buf bytes.Buffer
	w := NewRedactingWriter(&buf)
	in := []byte("x xoxc-1-2-3\n")
	n, err := w.Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
	if string(in) != "x xoxc-1-2-3\n" {
		t.Fatalf("Write modified the caller's buffer: %q", in)
	}
}

// A token written across two Write calls must still be redacted: the
// writer holds a partial line until its newline arrives.
func TestRedactingWriterBuffersSplitLines(t *testing.T) {
	var buf bytes.Buffer
	w := NewRedactingWriter(&buf)
	_, _ = w.Write([]byte("cookie xoxd-FAKE"))
	if buf.Len() != 0 {
		t.Fatalf("partial line written early: %q", buf.String())
	}
	_, _ = w.Write([]byte("tail; next\nheld"))
	if got := buf.String(); got != "cookie <redacted>; next\n" {
		t.Fatalf("got %q", got)
	}
	_, _ = w.Write([]byte(" line\n"))
	if got := buf.String(); got != "cookie <redacted>; next\nheld line\n" {
		t.Fatalf("got %q", got)
	}
}

// A writer that never sends a newline cannot grow the buffer without bound.
func TestRedactingWriterFlushesPastCap(t *testing.T) {
	var buf bytes.Buffer
	w := NewRedactingWriter(&buf)
	_, _ = w.Write(bytes.Repeat([]byte("a"), maxPending+1))
	if buf.Len() != maxPending+1 {
		t.Fatalf("wrote %d bytes, want %d", buf.Len(), maxPending+1)
	}
}

func TestPathDefaultsToStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv(FileEnv, "")
	want := filepath.Join(state, "slack-mcp", FileName)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestPathHonorsOverride(t *testing.T) {
	t.Setenv(FileEnv, "/somewhere/else.log")
	if got := Path(); got != "/somewhere/else.log" {
		t.Fatalf("Path() = %q, want the override", got)
	}
}

func skipModeChecksOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not apply on Windows")
	}
}

func TestOpenCreatesPrivateFileInStateDir(t *testing.T) {
	skipModeChecksOnWindows(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(FileEnv, "")

	f, err := Open(Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("log mode = %o, want 600", mode)
	}
	di, err := os.Stat(filepath.Dir(Path()))
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Errorf("state dir mode = %o, want 700", mode)
	}
}

func TestOpenTightensExistingFileAndDir(t *testing.T) {
	skipModeChecksOnWindows(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(FileEnv, "")

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("existing log mode = %o, want 600", mode)
	}
	di, _ := os.Stat(filepath.Dir(path))
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Errorf("existing state dir mode = %o, want 700", mode)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old line\nnew line\n" {
		t.Errorf("log was not appended to: %q", data)
	}
}

func TestOpenOverrideCreatesPrivateFile(t *testing.T) {
	skipModeChecksOnWindows(t)
	path := filepath.Join(t.TempDir(), "nested", "custom.log")
	t.Setenv(FileEnv, path)

	f, err := Open(Path())
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("override log mode = %o, want 600", mode)
	}
}

// A symlink planted at the log path must not redirect the log or get its
// target chmodded.
func TestOpenRefusesSymlinkAtLogPath(t *testing.T) {
	skipModeChecksOnWindows(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(FileEnv, "")

	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if f, err := Open(path); err == nil {
		f.Close()
		t.Fatal("Open followed a symlink at the log path")
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o644 {
		t.Errorf("symlink target mode changed to %o", mode)
	}
	if data, _ := os.ReadFile(target); string(data) != "untouched\n" {
		t.Errorf("symlink target written to: %q", data)
	}
}

// A symlinked default state directory is refused, and its target's mode is
// left alone.
func TestOpenRefusesSymlinkedStateDir(t *testing.T) {
	skipModeChecksOnWindows(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv(FileEnv, "")

	elsewhere := t.TempDir()
	if err := os.Chmod(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(state, "slack-mcp")); err != nil {
		t.Fatal(err)
	}

	if f, err := Open(Path()); err == nil {
		f.Close()
		t.Fatal("Open accepted a symlinked state directory")
	}
	fi, err := os.Stat(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o755 {
		t.Errorf("symlinked dir target mode changed to %o", mode)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, FileName)); err == nil {
		t.Error("log was created through the symlinked state directory")
	}
}

// SLACK_MCP_LOG_FILE=/dev/null discards the log cleanly: a non-regular file
// is used as it is, with no chmod attempted.
func TestOpenAcceptsDevNull(t *testing.T) {
	skipModeChecksOnWindows(t)
	before, err := os.Stat(os.DevNull)
	if err != nil {
		t.Skip("no /dev/null")
	}
	t.Setenv(FileEnv, os.DevNull)

	f, err := Open(Path())
	if err != nil {
		t.Fatalf("Open(/dev/null): %v", err)
	}
	if _, err := f.WriteString("discarded\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	after, err := os.Stat(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != before.Mode() {
		t.Errorf("/dev/null mode changed from %v to %v", before.Mode(), after.Mode())
	}
}
