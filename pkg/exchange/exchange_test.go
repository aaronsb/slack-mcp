package exchange

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME, the XDG dirs, and the override at a fresh tree so no
// test touches the real home directory. It returns home.
func isolate(t *testing.T) string {
	t.Helper()
	// Resolved, so a temp dir under a symlinked /tmp (macOS /var) does not
	// read as a symlinked default path.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DESKTOP_DIR", "")
	t.Setenv("XDG_DOCUMENTS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	t.Setenv(EnvOverride, "")
	return home
}

func mustOpen(t *testing.T) *Dir {
	t.Helper()
	d, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func writeFile(t *testing.T, d *Dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.Path(), name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDirIsCreatedAndAbsolute(t *testing.T) {
	home := isolate(t)
	d := mustOpen(t)
	want := filepath.Join(home, ".local", "share", "slack-mcp", "exchange")
	if d.Path() != want {
		t.Fatalf("path = %s, want %s", d.Path(), want)
	}
	fi, err := os.Stat(want)
	if err != nil || !fi.IsDir() {
		t.Fatalf("not created: %v", err)
	}
}

func TestRelativeLocationsAreRefused(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_DATA_HOME", "relative/data")
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative XDG_DATA_HOME: %v", err)
	}
	t.Setenv(EnvOverride, "relative/exchange")
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative override: %v", err)
	}
}

func TestOverrideMustExist(t *testing.T) {
	home := isolate(t)
	missing := filepath.Join(home, "nope")
	t.Setenv(EnvOverride, missing)
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing override: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the server created the override")
	}
}

func TestSymlinkedExchangeDirIsRefused(t *testing.T) {
	home := isolate(t)
	target := filepath.Join(home, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	// Override that is a symlink.
	link := filepath.Join(home, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvOverride, link)
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked override: %v", err)
	}

	// Default path that is a symlink.
	t.Setenv(EnvOverride, "")
	def := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, def); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked default: %v", err)
	}
}

func TestOverrideRefusals(t *testing.T) {
	home := isolate(t)
	data := filepath.Join(home, ".local", "share", "slack-mcp")
	config := filepath.Join(home, ".config", "slack-mcp")
	mk := func(p string) string {
		t.Helper()
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk(data)
	mk(config)
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, tc := range []struct {
		name, dir, rule string
	}{
		{"home", home, "home directory"},
		{"home via ..", filepath.Join(home, "Desktop", ".."), "home directory"},
		{"ancestor of data", filepath.Join(home, ".local"), "ancestor of the data"},
		{"ancestor of data 2", filepath.Join(home, ".local", "share"), "ancestor of the data"},
		{"ancestor of config", mk(filepath.Join(home, ".config")), "ancestor of the config"},
		{"data dir", data, "data directory or inside it"},
		{"inside data", mk(filepath.Join(data, "reports")), "data directory or inside it"},
		{"inside default exchange", mk(filepath.Join(data, "exchange", "sub")), "data directory or inside it"},
		{"config dir", config, "config directory"},
		{"inside config", mk(filepath.Join(config, "x")), "config directory"},
		{"dot dir", mk(filepath.Join(home, ".ssh")), "dot-directory"},
		{"dot dir via alias", filepath.Join(alias, ".ssh"), "dot-directory"},
		{"nested dot dir", mk(filepath.Join(home, "work", ".secret", "x")), "dot-directory"},
		{"desktop", mk(filepath.Join(home, "Desktop")), "desktop, documents, or downloads"},
		{"documents", mk(filepath.Join(home, "Documents")), "desktop, documents, or downloads"},
		{"downloads", mk(filepath.Join(home, "Downloads")), "desktop, documents, or downloads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvOverride, tc.dir)
			_, err := Open()
			if err == nil || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("Open with override %s: %v, want rule %q", tc.dir, err, tc.rule)
			}
		})
	}

	t.Run("xdg user dir", func(t *testing.T) {
		dl := mk(filepath.Join(home, "dl"))
		t.Setenv("XDG_DOWNLOAD_DIR", dl)
		t.Setenv(EnvOverride, dl)
		if _, err := Open(); err == nil || !strings.Contains(err.Error(), "downloads") {
			t.Fatalf("XDG_DOWNLOAD_DIR override: %v", err)
		}
	})

	t.Run("default exchange path is allowed", func(t *testing.T) {
		t.Setenv(EnvOverride, mk(filepath.Join(data, "exchange")))
		if _, err := Open(); err != nil {
			t.Fatalf("default path as override: %v", err)
		}
	})

	t.Run("plain dir under home is allowed", func(t *testing.T) {
		t.Setenv(EnvOverride, mk(filepath.Join(home, "slack-exchange")))
		if _, err := Open(); err != nil {
			t.Fatalf("plain override: %v", err)
		}
	})
}

func TestCreateWritesExclusiveAndSuffixes(t *testing.T) {
	isolate(t)
	d := mustOpen(t)

	for i, want := range []string{"report.pdf", "report (1).pdf", "report (2).pdf"} {
		res, err := d.Create("report.pdf")
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		res.File.Close()
		if res.Name != want {
			t.Fatalf("create %d named %q, want %q", i, res.Name, want)
		}
		if i > 0 && res.Notice() != "report.pdf existed; saved as "+want {
			t.Fatalf("notice = %q", res.Notice())
		}
		if i == 0 && res.Renamed() {
			t.Fatalf("first create reported a rename")
		}
	}

	if _, err := d.Create("../escape"); err == nil {
		t.Fatalf("Create accepted a path")
	}
}

func TestCreateCollisionTableNames(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	for _, tc := range []struct{ name, want string }{
		{".env", ".env (1)"},
		{"archive.tar.gz", "archive.tar (1).gz"},
		{"README", "README (1)"},
	} {
		writeFile(t, d, tc.name, "x")
		res, err := d.Create(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		res.File.Close()
		if res.Name != tc.want {
			t.Errorf("Create(%q) over an existing one = %q, want %q", tc.name, res.Name, tc.want)
		}
	}
}

func TestCreateGivesUpAfterHundredAttempts(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "a.txt", "x")
	for i := 1; i < MaxCollisionAttempts; i++ {
		writeFile(t, d, Suffixed("a.txt", i), "x")
	}
	_, err := d.Create("a.txt")
	if err == nil || !strings.Contains(err.Error(), `filename="a (100).txt"`) {
		t.Fatalf("expected a bounded failure suggesting a free name, got %v", err)
	}
}

func TestReadFileAndChecks(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "notes.txt", "hello")

	b, err := d.ReadFile("notes.txt", 100)
	if err != nil || string(b) != "hello" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := d.ReadFile("notes.txt", 3); err == nil || !strings.Contains(err.Error(), "notes.txt is 5 bytes, over the 3 byte limit") {
		t.Fatalf("size cap: %v", err)
	}
	if err := os.Mkdir(filepath.Join(d.Path(), "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open("sub", 100); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory read: %v", err)
	}
	if _, err := d.Open("../notes.txt", 100); err == nil {
		t.Fatalf("Open accepted a path")
	}
}

func TestHardLinkIsRefusedOnRead(t *testing.T) {
	home := isolate(t)
	d := mustOpen(t)
	outside := filepath.Join(home, "secret")
	if err := os.WriteFile(outside, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(d.Path(), "linked.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := d.Open("linked.txt", 100)
	if err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("hard link read: %v", err)
	}
	// With the other name gone, the file reads normally.
	if err := os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadFile("linked.txt", 100); err != nil {
		t.Fatalf("after unlinking the other name: %v", err)
	}
}

func TestEscapingSymlinkIsRefusedOnRead(t *testing.T) {
	home := isolate(t)
	d := mustOpen(t)
	outside := filepath.Join(home, "secret")
	if err := os.WriteFile(outside, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.Path(), "out.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := d.ReadFile("out.txt", 100); err == nil {
		t.Fatalf("read followed a symlink out of the root")
	}
}

func TestMissingNameHintOrderAndFilter(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	for _, n := range []string{
		"zeta budget.txt", "budget.txt", "Budget.txt", "budget-2024.xlsx",
		"bud.txt", "unrelated.pdf", "budget:x", "budget ",
	} {
		writeFile(t, d, n, "x")
	}
	if err := os.Mkdir(filepath.Join(d.Path(), "budget dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("budget.txt", filepath.Join(d.Path(), "budget-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := d.Open("budget.xlsx", 100)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
	want := []string{"bud.txt", "budget-2024.xlsx", "budget-link", "Budget.txt", "budget.txt", "zeta budget.txt"}
	if fmt.Sprint(nf.Matches) != fmt.Sprint(want) || nf.Total != len(want) {
		t.Fatalf("matches = %q (total %d), want %q", nf.Matches, nf.Total, want)
	}
	if !strings.HasPrefix(err.Error(), "No file named budget.xlsx is in the exchange directory. 6 names match:\n- bud.txt\n") {
		t.Fatalf("answer:\n%s", err)
	}

	_, err = d.Open("qqq.zip", 100)
	if err == nil || err.Error() != "No file named qqq.zip is in the exchange directory, and no name matches it." {
		t.Fatalf("no-match answer: %v", err)
	}
}

func TestMissingNameHintIsCapped(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	for i := 0; i < 25; i++ {
		writeFile(t, d, fmt.Sprintf("f%02d.txt", i), "x")
	}
	_, err := d.Open("f.txt", 100)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
	if nf.Total != 25 || len(nf.Matches) != MaxHintNames || nf.Matches[0] != "f00.txt" || nf.Matches[19] != "f19.txt" {
		t.Fatalf("total %d, matches %q", nf.Total, nf.Matches)
	}
	if !strings.HasPrefix(err.Error(), "No file named f.txt is in the exchange directory. 25 names match; the first 20 by name:") {
		t.Fatalf("answer:\n%s", err)
	}
}

func TestFoldKeyMatchesEqualFold(t *testing.T) {
	for _, pair := range [][2]string{{"Report", "report"}, {"ſ", "S"}, {"K", "k"}, {"ÄBC", "äbc"}} {
		if !strings.EqualFold(pair[0], pair[1]) || FoldKey(pair[0]) != FoldKey(pair[1]) {
			t.Errorf("FoldKey(%q) = %q, FoldKey(%q) = %q", pair[0], FoldKey(pair[0]), pair[1], FoldKey(pair[1]))
		}
	}
}

func TestDefaultPathExemptionRequiresAPlainDefault(t *testing.T) {
	home := isolate(t)
	def := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ target, rule string }{
		{filepath.Join(home, ".ssh"), "dot-directory"},
		{filepath.Join(home, "Downloads"), "downloads"},
	} {
		t.Run(filepath.Base(tc.target), func(t *testing.T) {
			if err := os.MkdirAll(tc.target, 0o700); err != nil {
				t.Fatal(err)
			}
			os.Remove(def)
			if err := os.Symlink(tc.target, def); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Setenv(EnvOverride, tc.target)
			if _, err := Open(); err == nil || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("default linked to %s, override %s: %v", tc.target, tc.target, err)
			}
		})
	}
}

func TestDefaultPathExemptionRefusesASymlinkedAncestor(t *testing.T) {
	home := isolate(t)
	hidden := filepath.Join(home, ".hidden")
	if err := os.MkdirAll(filepath.Join(hidden, "exchange"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Dir(DefaultPath())
	if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hidden, data); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvOverride, filepath.Join(hidden, "exchange"))
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "passes through a symlink, so it can't be named as an override; unset SLACK_MCP_EXCHANGE_DIR") {
		t.Fatalf("override reached through a symlinked data directory: %v", err)
	}
}

func TestOverrideUnderSymlinkedAncestorIntoDotDirIsRefused(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".secret", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".secret"), filepath.Join(home, "work")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvOverride, filepath.Join(home, "work", "x"))
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "dot-directory") {
		t.Fatalf("~/work/x via ~/work -> ~/.secret: %v", err)
	}
}

func TestOverrideRefusedWhenGuardDirsAreNotAbsolute(t *testing.T) {
	home := isolate(t)
	ok := filepath.Join(home, "slack-exchange")
	if err := os.Mkdir(ok, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, ok)
	for _, env := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "relative/dir")
			if _, err := Open(); err == nil || !strings.Contains(err.Error(), "cannot be checked") {
				t.Fatalf("%s relative: %v", env, err)
			}
		})
	}
}

// infinite never ends; it counts what was taken from it.
type infinite struct{ n int64 }

func (r *infinite) Read(p []byte) (int, error) {
	r.n += int64(len(p))
	return len(p), nil
}

func TestReadIsBoundedInMemory(t *testing.T) {
	r := &infinite{}
	_, err := readBounded(r, 1024, 0)
	if err == nil || !strings.Contains(err.Error(), "1024 byte limit") {
		t.Fatalf("unbounded source: %v", err)
	}
	// io.ReadAll may ask for more than it receives, but the LimitReader
	// passes on at most limit+1 bytes.
	if r.n > 1025 {
		t.Fatalf("read %d bytes from the source for a 1024-byte limit", r.n)
	}
}

func TestFileGrowingAfterTheCheckIsRefused(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "grow.txt", "abc")
	f, err := d.Open("grow.txt", 5)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(filepath.Join(d.Path(), "grow.txt"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAll(); err == nil || !strings.Contains(err.Error(), "grew past the 5 byte limit") {
		t.Fatalf("grown file: %v", err)
	}
}

func TestReadStatedIsBoundedByTheCheckedSize(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "grow.txt", "abc")
	f, err := d.Open("grow.txt", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(filepath.Join(d.Path(), "grow.txt"), []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadStated(); err == nil || !strings.Contains(err.Error(), "grew past the 3 byte limit") {
		t.Fatalf("file grown past its stated size: %v", err)
	}
}

func TestReadIsSizedFromTheHint(t *testing.T) {
	src := strings.Repeat("x", 100000)
	b, err := readBounded(strings.NewReader(src), math.MaxInt64, int64(len(src)))
	if err != nil || len(b) != len(src) {
		t.Fatalf("read %d bytes, %v", len(b), err)
	}
	if cap(b) != len(src)+1 {
		t.Fatalf("buffer grew: cap %d for a %d byte hint", cap(b), len(src))
	}
	// A wrong hint still reads everything, within the limit.
	if b, err := readBounded(strings.NewReader(src), math.MaxInt64, 10); err != nil || len(b) != len(src) {
		t.Fatalf("short hint: %d bytes, %v", len(b), err)
	}
}

func TestDanglingSymlinkIsReportedAndNotOffered(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "gonex.txt", "x")
	if err := os.Symlink("missing.txt", filepath.Join(d.Path(), "gone.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := d.Open("gone.txt", 100); err == nil || !strings.Contains(err.Error(), "symlink whose target does not exist") {
		t.Fatalf("dangling symlink: %v", err)
	}
	var nf *NotFoundError
	if _, err := d.Open("gone.pdf", 100); !errors.As(err, &nf) || fmt.Sprint(nf.Matches) != "[gonex.txt]" {
		t.Fatalf("hint offered the dangling link: %v", err)
	}
}

func TestReadLimitBounds(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "a.txt", "abc")
	if _, err := d.Open("a.txt", -1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative limit: %v", err)
	}
	if _, err := d.ReadFile("a.txt", -1); err == nil {
		t.Fatalf("ReadFile accepted a negative limit")
	}
	b, err := d.ReadFile("a.txt", math.MaxInt64)
	if err != nil || string(b) != "abc" {
		t.Fatalf("MaxInt64 limit: %q, %v", b, err)
	}
	if b, err := readBounded(strings.NewReader("xyz"), math.MaxInt64, 0); err != nil || string(b) != "xyz" {
		t.Fatalf("readBounded MaxInt64: %q, %v", b, err)
	}
	if _, err := readBounded(strings.NewReader("xyz"), -1, 0); err == nil {
		t.Fatalf("readBounded accepted a negative limit")
	}
}

func TestMissingNameHintLeavesOutWhatOpenRefuses(t *testing.T) {
	home := isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "plan.txt", "x")
	if err := os.Mkdir(filepath.Join(d.Path(), "plan-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plan-dir", filepath.Join(d.Path(), "plan-dirlink")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	other := filepath.Join(home, "other")
	if err := os.WriteFile(other, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, filepath.Join(d.Path(), "plan-hard.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	var nf *NotFoundError
	if _, err := d.Open("plan.pdf", 100); !errors.As(err, &nf) || fmt.Sprint(nf.Matches) != "[plan.txt]" || nf.Total != 1 {
		t.Fatalf("hint offered names Open refuses: %v", err)
	}
}
