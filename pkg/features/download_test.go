package features_test

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// download (ADR-012): files land only in the exchange directory.

func downloadServer(t *testing.T, name string) (*slacktest.Server, *provider.ApiProvider) {
	t.Helper()
	t.Setenv(exchange.EnvOverride, "")
	srv := slacktest.New(t)
	srv.Handle("files.info", func(*http.Request) any {
		return map[string]any{"ok": true, "file": map[string]any{
			"id": "F1", "name": name, "size": 5, "mimetype": "application/pdf",
			"url_private_download": "https://files.slack.com/files-pri/T1-F1/download/x",
		}}
	})
	ap := bootedProvider(t, srv)
	srv.ResetCalls()
	t.Cleanup(features.SetFetchFileForTest(func(_ string, w io.Writer) (int64, error) {
		n, err := io.WriteString(w, "hello")
		return int64(n), err
	}))
	return srv, ap
}

func exchangeDir() string { return filepath.Join(paths.DataDir(), "exchange") }

func TestDownloadRefusesMalformedFilenameWithZeroSlackCalls(t *testing.T) {
	srv, ap := downloadServer(t, "report.pdf")
	for _, bad := range []string{
		"../x", "/abs", "a:b", "CON", "NUL.txt", "name.", "name ",
		"a\x01b", strings.Repeat("a", 256), `..\x`, "x/y",
	} {
		out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1", "filename": bad})
		if !strings.Contains(out, "is not a bare file name") {
			t.Errorf("filename %q not refused:\n%s", bad, out)
		}
	}
	if n := srv.Calls("files.info"); n != 0 {
		t.Fatalf("malformed names made %d files.info calls", n)
	}
	entries, _ := os.ReadDir(exchangeDir())
	if len(entries) != 0 {
		t.Fatalf("something was written: %v", entries)
	}
}

func TestDownloadRefusesDestDirNamingTheChange(t *testing.T) {
	srv, ap := downloadServer(t, "report.pdf")
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1", "destDir": "/tmp"})
	if !strings.Contains(out, "destDir was removed") || !strings.Contains(out, exchangeDir()) {
		t.Fatalf("destDir refusal does not name the change and the directory:\n%s", out)
	}
	if srv.Calls("files.info") != 0 {
		t.Fatalf("destDir refusal made a Slack call")
	}
}

func TestDownloadWritesPrivateFileIntoExchangeDir(t *testing.T) {
	_, ap := downloadServer(t, "report.pdf")
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1"})
	want := filepath.Join(exchangeDir(), "report.pdf")
	if !strings.Contains(out, want) || !strings.Contains(out, "as report.pdf") {
		t.Fatalf("output does not report the path:\n%s", out)
	}
	b, err := os.ReadFile(want)
	if err != nil || string(b) != "hello" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(want)
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("file mode %o is not private", fi.Mode().Perm())
		}
		di, _ := os.Stat(exchangeDir())
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("exchange dir mode %o, want 700", di.Mode().Perm())
		}
	}

	// A second download of the same name is suffixed, and says so.
	out = runTool(t, features.Download, ap, map[string]any{"fileId": "F1"})
	if !strings.Contains(out, "report.pdf existed; saved as report (1).pdf") {
		t.Fatalf("rename not stated:\n%s", out)
	}
}

func TestDownloadExplicitFilenameCollisionIsSuffixed(t *testing.T) {
	_, ap := downloadServer(t, "report.pdf")
	runTool(t, features.Download, ap, map[string]any{"fileId": "F1", "filename": "budget.xlsx"})
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1", "filename": "budget.xlsx"})
	if !strings.Contains(out, "budget.xlsx existed; saved as budget (1).xlsx") {
		t.Fatalf("explicit collision not suffixed with notice:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(exchangeDir(), "budget (1).xlsx")); err != nil {
		t.Fatalf("suffixed file missing: %v", err)
	}
}

func TestDownloadSanitizesSlackName(t *testing.T) {
	_, ap := downloadServer(t, "../../etc/CON.txt")
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1"})
	if _, err := os.Stat(filepath.Join(exchangeDir(), ".._.._etc_CON.txt")); err != nil {
		t.Fatalf("sanitized file missing (%v):\n%s", err, out)
	}
}

func TestDownloadFailureLeavesNoFile(t *testing.T) {
	_, ap := downloadServer(t, "report.pdf")
	t.Cleanup(features.SetFetchFileForTest(func(string, io.Writer) (int64, error) {
		return 0, errors.New("boom")
	}))
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1"})
	if !strings.Contains(out, "Download failed") {
		t.Fatalf("failure not reported:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(exchangeDir(), "report.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file left behind: %v", err)
	}
}

func TestDownloadRefusedExchangeDirMakesNoSlackCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode check on Windows")
	}
	srv, ap := downloadServer(t, "report.pdf")
	if err := os.MkdirAll(exchangeDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exchangeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	out := runTool(t, features.Download, ap, map[string]any{"fileId": "F1"})
	if !strings.Contains(out, "chmod 700") {
		t.Fatalf("refusal does not name the rule:\n%s", out)
	}
	if n := srv.Calls("files.info"); n != 0 {
		t.Fatalf("refused exchange dir still made %d files.info calls", n)
	}
	if entries, _ := os.ReadDir(exchangeDir()); len(entries) != 0 {
		t.Fatalf("something was written: %v", entries)
	}
}
