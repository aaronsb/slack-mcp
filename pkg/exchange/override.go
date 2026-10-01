package exchange

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// checkOverride is ADR-012's foot-gun guard on SLACK_MCP_EXCHANGE_DIR. It
// compares by file identity (os.SameFile on Stat results of paths resolved
// with filepath.EvalSymlinks), not by string, and runs on every use.
func checkOverride(p string) error {
	refuse := func(rule string) error { return &Error{Path: p, Rule: EnvOverride + " " + rule} }

	ov, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refuse("names a directory that does not exist; the server does not create a directory the operator named")
		}
		return refuse("cannot be checked: " + err.Error())
	}
	ovChain := chain(resolve(p))

	home := absOrEmpty(homeDir())
	data := absOrEmpty(paths.DataDir())
	config := absOrEmpty(paths.ConfigDir())

	// The default exchange path named explicitly is the directory the server
	// would use anyway. ADR-012 carves it out of the data-directory rule; the
	// carve-out covers the dot-directory rule too, since the default sits in
	// ~/.local/share.
	if data != "" && sameFile(DefaultPath(), ov) {
		return nil
	}
	if sameFile(home, ov) {
		return refuse("is your home directory")
	}
	for _, x := range []struct{ dir, label string }{{data, "data"}, {config, "config"}} {
		if x.dir == "" {
			continue
		}
		for _, a := range chain(resolve(x.dir))[1:] {
			if sameFile(a, ov) {
				return refuse("is an ancestor of the " + x.label + " directory (" + x.dir + ")")
			}
		}
	}
	if data != "" && within(ovChain, data) {
		return refuse("is the data directory or inside it (" + data + "); only the default exchange path is allowed there")
	}
	if config != "" && within(ovChain, config) {
		return refuse("is the config directory or inside it (" + config + ")")
	}
	if home != "" {
		if hi, err := os.Stat(home); err == nil {
			for _, a := range ovChain {
				ai, err := os.Stat(a)
				if err != nil || !os.SameFile(ai, hi) {
					continue
				}
				rel, err := filepath.Rel(a, ovChain[0])
				if err != nil {
					break
				}
				for _, c := range strings.Split(rel, string(filepath.Separator)) {
					if strings.HasPrefix(c, ".") && c != "." {
						return refuse("is inside a dot-directory under your home directory (" + c + ")")
					}
				}
				break
			}
		}
	}
	for _, u := range userDirs(home) {
		if sameFile(u, ov) {
			return refuse("is your desktop, documents, or downloads directory (" + u + ")")
		}
	}
	return nil
}

// homeDir is $HOME (or the platform equivalent), or "" when unknown.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func absOrEmpty(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

// userDirs are the desktop, documents, and downloads directories: the XDG
// user-dir variables when set, else the conventional names under home.
func userDirs(home string) []string {
	var out []string
	for _, d := range []struct{ env, name string }{
		{"XDG_DESKTOP_DIR", "Desktop"},
		{"XDG_DOCUMENTS_DIR", "Documents"},
		{"XDG_DOWNLOAD_DIR", "Downloads"},
	} {
		if v := absOrEmpty(os.Getenv(d.env)); v != "" {
			out = append(out, v)
		} else if home != "" {
			out = append(out, filepath.Join(home, d.name))
		}
	}
	return out
}

// sameFile reports whether path a and the file fi are the same file. A path
// that cannot be stat'd is not the same file.
func sameFile(a string, fi fs.FileInfo) bool {
	if a == "" {
		return false
	}
	ai, err := os.Stat(a)
	return err == nil && os.SameFile(ai, fi)
}

// within reports whether any path in c (a path and its ancestors) is the
// same file as dir.
func within(c []string, dir string) bool {
	di, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for _, a := range c {
		if ai, err := os.Stat(a); err == nil && os.SameFile(ai, di) {
			return true
		}
	}
	return false
}

// resolve evaluates symlinks in p. When p does not exist, the longest
// existing prefix is resolved and the rest appended, so a path's ancestors
// can be compared before it is created.
func resolve(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// chain is p followed by each of its parents up to the filesystem root.
func chain(p string) []string {
	out := []string{p}
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return out
		}
		out = append(out, parent)
		p = parent
	}
}
