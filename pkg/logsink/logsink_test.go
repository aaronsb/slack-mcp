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
