// Package report renders relationship views as self-contained graph pages
// (ADR-008, amendment 2026-09-30: graph reports).
//
// A report is a static HTML file: the vendored graph library inlined, the
// graph data in a JSON block, and a short hand-written script that draws it.
// The page fetches nothing — its Content-Security-Policy forbids every
// network source — and every data string reaches the screen through the
// canvas renderer or textContent, never through markup. Files are written
// under the server's data directory, 0700/0600, one per view and subject,
// overwritten on each render. Nothing here serves, opens, or launches.
package report

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// The vendored graph library: Cytoscape.js, MIT, from the npm registry
// tarball cytoscape-3.34.3.tgz (integrity sha512-yfYGhRcGAntq6YBD583j4n0Eg3jIxvWmZtz/5uz9UYkeIStSlMxuUja+ec5j3iBD8nv1rwaOAYMW09tBdkSeaQ==),
// file dist/cytoscape.min.js, unmodified. Its license is assets/LICENSE and
// is also carried in the bundle's header comment. To upgrade: replace both
// files from the new release tarball and update the version and pin below;
// TestVendoredLibraryMatchesItsPin fails until the pin matches.
const (
	CytoscapeVersion = "3.34.3"
	CytoscapeSHA256  = "5f3b5b529546d5af1fc5628590af033b74511a5b6f789f5f4682845863228b91"
)

//go:embed assets/cytoscape.min.js
var cytoscapeJS []byte

// CytoscapeJS returns the vendored library bytes.
func CytoscapeJS() []byte { return cytoscapeJS }

// Node kinds and edge kinds the page's legend knows.
const (
	KindSeed    = "seed"
	KindPerson  = "person"
	KindChannel = "channel"
	KindDM      = "dm"
	KindFamily  = "family"

	EdgeActive    = "active"    // person active in a conversation
	EdgeCoActive  = "co-active" // seed and counterpart co-active
	EdgeCreated   = "created"   // seed created a channel
	EdgeFounder   = "founder"   // seed founded channels in a family
	EdgeConverges = "converges" // person in a convergence window
)

// Node is one vertex. ID is page-local and synthetic — never a Slack ID.
type Node struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Kind   string   `json:"kind"`
	Detail []string `json:"detail,omitempty"`
}

// Edge joins two nodes by their page-local IDs.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Weight int    `json:"weight"`
	Label  string `json:"label,omitempty"`
}

// Graph is the render seam's model: what a view says, as nodes and edges.
type Graph struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges"`
	Notes    []string `json:"notes,omitempty"`
}

// dataJSON marshals the graph for the page's data block. encoding/json's
// default HTML escaping writes <, >, and & as \u003c, \u003e, \u0026, and
// always escapes U+2028/U+2029, so no data string can close the script
// element or open a comment; the replacement below holds that line even
// if the encoder's defaults ever change.
func dataJSON(g Graph) ([]byte, error) {
	if g.Nodes == nil {
		g.Nodes = []Node{}
	}
	if g.Edges == nil {
		g.Edges = []Edge{}
	}
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	s := strings.NewReplacer(
		"<", `\u003c`, ">", `\u003e`, "&", `\u0026`,
		"\u2028", `\u2028`, "\u2029", `\u2029`,
	).Replace(string(b))
	return []byte(s), nil
}

// Page renders the graph as one self-contained HTML document.
func Page(g Graph) ([]byte, error) {
	data, err := dataJSON(g)
	if err != nil {
		return nil, fmt.Errorf("encode graph: %w", err)
	}
	var b strings.Builder
	b.Grow(len(cytoscapeJS) + len(data) + len(pageHead) + len(pageBody) + len(pageScript) + 256)
	b.WriteString(strings.Replace(pageHead, "{{SCRIPT_HASHES}}", scriptHashes(), 1))
	b.WriteString(pageBody)
	b.WriteString("<script>")
	b.Write(cytoscapeJS)
	b.WriteString("</script>\n")
	b.WriteString(`<script type="application/json" id="data">`)
	b.Write(data)
	b.WriteString("</script>\n")
	b.WriteString("<script>")
	b.WriteString(pageScript)
	b.WriteString("</script>\n</body>\n</html>\n")
	return []byte(b.String()), nil
}

// ScriptHash is the CSP hash source for an inline script's exact bytes.
func ScriptHash(script []byte) string {
	sum := sha256.Sum256(script)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// scriptHashes lists the hash sources of the page's two executable scripts.
// Both are constants, so the CSP pins them exactly and admits nothing else.
func scriptHashes() string {
	return ScriptHash(cytoscapeJS) + " " + ScriptHash([]byte(pageScript))
}

// Dir is where reports live: <data dir>/reports. It is never the exchange
// directory — reports hold relationship data and stay out of reach of the
// file parameters.
func Dir() string {
	return filepath.Join(paths.DataDir(), "reports")
}

// staleTempAge is how old an orphaned temp file must be before Write sweeps
// it: a crash between create and rename leaves relationship data behind,
// and a live write finishes in well under this.
const staleTempAge = 10 * time.Minute

const tempPattern = ".report-*.tmp"

// Write stores a page as <view>-<subject>-<digest>.html in Dir, atomically
// (temp file and rename) so a reader never sees half a page, and privately:
// the directory is 0700 and the file 0600. A later render of the same view
// and subject replaces the file. Returns the absolute path.
func Write(view, subject string, page []byte) (string, error) {
	dir, err := reportsDir()
	if err != nil {
		return "", err
	}
	sweepStaleTemps(dir, time.Now())

	final := filepath.Join(dir, FileName(view, subject))
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return "", fmt.Errorf("create temp report: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("restrict temp report: %w", err)
	}
	if _, err := tmp.Write(page); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("write report: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("sync report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return "", fmt.Errorf("place report: %w", err)
	}
	return final, nil
}

// reportsDir creates Dir if needed and returns its absolute path, refusing
// anything but a real directory owned by the current user: a symlink
// planted at reports/ would otherwise redirect relationship data, and a
// chmod through it would loosen or tighten someone else's directory.
func reportsDir() (string, error) {
	dir, err := filepath.Abs(Dir())
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", fmt.Errorf("refusing %s: not a real directory (a symlink or file is in its place)", dir)
	}
	if !ownedByCurrentUser(fi) {
		return "", fmt.Errorf("refusing %s: owned by another user", dir)
	}
	// MkdirAll leaves an existing directory's mode alone; the reports
	// directory is ours, so tighten it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict %s: %w", dir, err)
	}
	return dir, nil
}

// sweepStaleTemps removes orphaned temp files older than staleTempAge.
// Best effort: a failure here never blocks the write.
func sweepStaleTemps(dir string, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(dir, tempPattern))
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(m)
	}
}

// FileName is a report's name: the view and a readable slug of the subject
// handle, plus a short digest of the raw handle so handles that slug alike
// (john.smith, john_smith) never share a file.
func FileName(view, subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return slug(view, "view") + "-" + slug(subject, "subject") + "-" + hex.EncodeToString(sum[:])[:8] + ".html"
}

// slug reduces a name to [a-z0-9-] so it can never carry a separator, a
// dot-dot, or a hidden-file prefix out of the reports directory.
func slug(s, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	if out == "" {
		return fallback
	}
	return out
}

// FileURL renders an absolute path as a file:// URL.
func FileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths: C:/x -> /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
