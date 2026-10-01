package report_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/report"
)

const hostile = "</script><script>alert(1)</script>"

func hostileGraph() report.Graph {
	return report.Graph{
		Title:    "About " + hostile,
		Subtitle: "line sep para & <b>",
		Nodes: []report.Node{
			{ID: "n1", Label: hostile, Kind: report.KindSeed},
			{ID: "n2", Label: "#" + hostile, Kind: report.KindChannel, Detail: []string{"<img src=x onerror=alert(1)>"}},
		},
		Edges: []report.Edge{{Source: "n1", Target: "n2", Kind: report.EdgeActive, Weight: 3, Label: "3 days"}},
		Notes: []string{"<!-- note -->"},
	}
}

func TestPageNeverCarriesDataUnescaped(t *testing.T) {
	page, err := report.Page(hostileGraph())
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	s := string(page)
	for _, raw := range []string{hostile, "<img src=x", "<!-- note", "<b>", " ", " "} {
		if strings.Contains(s, raw) {
			t.Fatalf("page carries %q unescaped", raw)
		}
	}
	// Exactly the structural script tags: the vendored library, the JSON
	// data block, and the page's own code — nothing the data opened.
	if n := strings.Count(s, "<script"); n != 3 {
		t.Fatalf("page has %d <script openers, want 3", n)
	}
	if n := strings.Count(s, "</script"); n != 3 {
		t.Fatalf("page has %d </script closers, want 3", n)
	}
}

func TestPageDataRoundTripsThroughTheJSONBlock(t *testing.T) {
	g := hostileGraph()
	page, err := report.Page(g)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	s := string(page)
	open := `<script type="application/json" id="data">`
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatal("no JSON data block")
	}
	rest := s[i+len(open):]
	block := rest[:strings.Index(rest, "</script>")]

	var back report.Graph
	if err := json.Unmarshal([]byte(block), &back); err != nil {
		t.Fatalf("data block is not JSON: %v", err)
	}
	if back.Title != g.Title || back.Nodes[0].Label != hostile || back.Subtitle != g.Subtitle {
		t.Fatalf("data did not round-trip: %+v", back)
	}
}

func TestPageIsSelfContainedUnderAStrictCSP(t *testing.T) {
	page, err := report.Page(hostileGraph())
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	s := string(page)
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:">`
	if !strings.Contains(s, csp) {
		t.Fatal("CSP meta missing or altered")
	}
	if strings.Index(s, csp) > strings.Index(s, "<script") {
		t.Fatal("CSP must precede every script")
	}
	if regexp.MustCompile(`(?i)<(script|link|img|iframe)[^>]+(src|href)=`).MatchString(s) {
		t.Fatal("page references an external resource")
	}
	own := strings.Replace(s, string(report.CytoscapeJS()), "", 1)
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(own, sink) {
			t.Fatalf("page code uses the markup sink %s", sink)
		}
	}
	if !bytes.Contains(page, report.CytoscapeJS()) {
		t.Fatal("vendored library not inlined")
	}
}

func TestVendoredLibraryMatchesItsPin(t *testing.T) {
	sum := sha256.Sum256(report.CytoscapeJS())
	if got := hex.EncodeToString(sum[:]); got != report.CytoscapeSHA256 {
		t.Fatalf("cytoscape.min.js sha256 %s, pinned %s", got, report.CytoscapeSHA256)
	}
	if !bytes.Contains(report.CytoscapeJS(), []byte("Cytoscape Consortium")) {
		t.Fatal("license header missing from the vendored bundle")
	}
}

func TestWriteIsPrivateAtomicAndOverwrites(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path, err := report.Write("person", "schen", []byte("one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(path) != report.Dir() {
		t.Fatalf("written to %s, want under %s", path, report.Dir())
	}
	if filepath.Base(path) != "person-schen.html" {
		t.Fatalf("file name %q", filepath.Base(path))
	}
	again, err := report.Write("person", "schen", []byte("two"))
	if err != nil || again != path {
		t.Fatalf("rewrite went to %s (%v), want %s", again, err, path)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "two" {
		t.Fatalf("not overwritten: %q", got)
	}
	entries, _ := os.ReadDir(report.Dir())
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	if runtime.GOOS == "windows" {
		return
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(report.Dir())
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", di.Mode().Perm())
	}
}

func TestWriteTightensAPreexistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(report.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := report.Write("about", "x", []byte("p")); err != nil {
		t.Fatal(err)
	}
	di, _ := os.Stat(report.Dir())
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", di.Mode().Perm())
	}
}

func TestWriteNamesCannotEscapeTheDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, subject := range []string{"../../etc/passwd", "..", "a/b\\c", "", "Ünïcode Name!"} {
		path, err := report.Write("about", subject, []byte("p"))
		if err != nil {
			t.Fatalf("Write(%q): %v", subject, err)
		}
		if filepath.Dir(path) != report.Dir() {
			t.Fatalf("subject %q escaped to %s", subject, path)
		}
		if !regexp.MustCompile(`^about-[a-z0-9-]+\.html$`).MatchString(filepath.Base(path)) {
			t.Fatalf("subject %q produced name %q", subject, filepath.Base(path))
		}
	}
}

func TestFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path")
	}
	if got := report.FileURL("/home/a b/r.html"); got != "file:///home/a%20b/r.html" {
		t.Fatalf("FileURL = %s", got)
	}
}
