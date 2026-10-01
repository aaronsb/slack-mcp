package features_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/features"
)

// put (ADR-012, amendment 2026-10-01): a write-only way into the exchange
// directory for clients whose file tools cannot reach it. No Slack call,
// so these tests run with no provider at all.

func putEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(exchange.EnvOverride, "")
}

func runPut(t *testing.T, params map[string]any) (*features.FeatureResult, string) {
	t.Helper()
	res, err := features.Put.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return res, features.FormatResult("put", res)
}

func exchangeEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(exchangeDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read exchange dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPutWrites(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x01binary\xff")
	cases := []struct {
		name   string
		params map[string]any
		file   string
		want   []byte
	}{
		{"text", map[string]any{"name": "notes.md", "content": "# hi\nthere\n"}, "notes.md", []byte("# hi\nthere\n")},
		{"base64 padded", map[string]any{"name": "chart.png", "base64": base64.StdEncoding.EncodeToString(png)}, "chart.png", png},
		{"base64 unpadded", map[string]any{"name": "chart.png", "base64": base64.RawStdEncoding.EncodeToString(png)}, "chart.png", png},
		{"base64 wrapped", map[string]any{"name": "a.bin", "base64": "aGVsbG8g\nd29ybGQ=\n"}, "a.bin", []byte("hello world")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putEnv(t)
			res, out := runPut(t, tc.params)
			if !res.Success {
				t.Fatalf("put refused:\n%s", out)
			}
			got, err := os.ReadFile(filepath.Join(exchangeDir(), tc.file))
			if err != nil {
				t.Fatalf("file not written: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("wrote %q, want %q", got, tc.want)
			}
			fi, _ := os.Stat(filepath.Join(exchangeDir(), tc.file))
			if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
			}
			mustContain(t, out, "as "+tc.file, "files=['"+tc.file+"']", filepath.Join(exchangeDir(), tc.file))
		})
	}
}

func TestPutSuffixesATakenNameAndNeverOverwrites(t *testing.T) {
	putEnv(t)
	if res, out := runPut(t, map[string]any{"name": "report.txt", "content": "first"}); !res.Success {
		t.Fatalf("first put refused:\n%s", out)
	}
	res, out := runPut(t, map[string]any{"name": "report.txt", "content": "second"})
	if !res.Success {
		t.Fatalf("second put refused:\n%s", out)
	}
	mustContain(t, out, "report.txt existed; saved as report (1).txt", "files=['report (1).txt']")
	first, _ := os.ReadFile(filepath.Join(exchangeDir(), "report.txt"))
	second, _ := os.ReadFile(filepath.Join(exchangeDir(), "report (1).txt"))
	if string(first) != "first" || string(second) != "second" {
		t.Fatalf("contents: report.txt=%q report (1).txt=%q", first, second)
	}
}

// The bare-name rule on put's file parameter (ADR-012 Risks): a malformed
// name is refused before anything else, and nothing is created.
func TestPutRefusesMalformedNamesWritingNothing(t *testing.T) {
	putEnv(t)
	for _, bad := range []string{
		"../x", "/abs", "a:b", "CON", "CON.txt", "NUL.txt", "name.", "name ",
		"a\x01b", strings.Repeat("a", 256), `..\x`, "x/y", "..", ".",
	} {
		res, out := runPut(t, map[string]any{"name": bad, "content": "payload"})
		if res.Success || !strings.Contains(out, "is not a bare file name") || !strings.Contains(out, "Nothing was written") {
			t.Errorf("name %q not refused:\n%s", bad, out)
		}
	}
	res, out := runPut(t, map[string]any{"name": "", "content": "payload"})
	if res.Success || !strings.Contains(out, "name is required") {
		t.Errorf("empty name not refused:\n%s", out)
	}
	if got := exchangeEntries(t); len(got) != 0 {
		t.Fatalf("something was written: %v", got)
	}
}

func TestPutRefusals(t *testing.T) {
	over := strings.Repeat("a", 5<<20+1)
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"both", map[string]any{"name": "a.txt", "content": "x", "base64": "eA=="}, "exactly one of"},
		{"neither", map[string]any{"name": "a.txt"}, "exactly one of"},
		{"invalid base64", map[string]any{"name": "a.bin", "base64": "not*base64!"}, "not valid standard base64"},
		{"truncated base64", map[string]any{"name": "a.bin", "base64": "aGVsbG8=x"}, "not valid standard base64"},
		{"url-safe base64", map[string]any{"name": "a.bin", "base64": "-_8="}, "standard alphabet"},
		{"empty content", map[string]any{"name": "a.txt", "content": ""}, "content is empty"},
		{"empty base64", map[string]any{"name": "a.bin", "base64": ""}, "content is empty"},
		{"content over cap", map[string]any{"name": "a.txt", "content": over}, "over put's 5242880 byte (5 MiB) limit"},
		{"base64 over cap", map[string]any{"name": "a.bin", "base64": base64.StdEncoding.EncodeToString([]byte(over))}, "over put's 5242880 byte (5 MiB) limit"},
		{"content not a string", map[string]any{"name": "a.txt", "content": 3.0}, "content must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putEnv(t)
			res, out := runPut(t, tc.params)
			if res.Success {
				t.Fatalf("put accepted:\n%s", out)
			}
			mustContain(t, out, tc.want, "Nothing was written")
			if got := exchangeEntries(t); len(got) != 0 {
				t.Fatalf("something was written: %v", got)
			}
		})
	}
}

func TestPutAtTheCapIsWritten(t *testing.T) {
	putEnv(t)
	res, out := runPut(t, map[string]any{"name": "max.bin", "content": strings.Repeat("a", 5<<20)})
	if !res.Success {
		t.Fatalf("a file at the cap was refused:\n%s", out)
	}
}
