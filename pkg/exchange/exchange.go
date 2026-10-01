// Package exchange owns the exchange directory (ADR-012): the one directory
// every file parameter on the tool surface reads from or writes to.
//
// File parameters are bare names, never paths. Every access goes through an
// os.Root opened on the directory, and the directory itself is verified on
// every operation with nothing cached. Callers open a Dir per operation,
// use it, and close it.
package exchange

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// EnvOverride names the setting that moves the exchange directory. It is
// read only from the process environment the MCP client sets; ADR-014's
// .env allowlist refuses it from a .env file.
const EnvOverride = "SLACK_MCP_EXCHANGE_DIR"

// MaxCollisionAttempts bounds how many names a write tries: the name itself
// and up to 99 suffixes.
const MaxCollisionAttempts = 100

// Location is where the exchange directory is, before any check.
type Location struct {
	// Path is the configured path, as given (override) or derived (default).
	Path string
	// Override reports whether Path came from SLACK_MCP_EXCHANGE_DIR.
	Override bool
}

// DefaultPath is <DataDir>/exchange.
func DefaultPath() string {
	return filepath.Join(paths.DataDir(), "exchange")
}

// Locate reports where the exchange directory is configured to be. It does
// not touch the filesystem.
func Locate() Location {
	if v := os.Getenv(EnvOverride); v != "" {
		return Location{Path: v, Override: true}
	}
	return Location{Path: DefaultPath()}
}

// Dir is a verified, open exchange directory. Close it when the operation
// is done; do not keep it across operations.
type Dir struct {
	root *os.Root
	path string
}

// Path is the exchange directory's absolute path, for display only. Nothing
// is opened by it.
func (d *Dir) Path() string { return d.path }

// Close releases the root.
func (d *Dir) Close() error { return d.root.Close() }

// DisplayPath joins name to the directory path for display only.
func (d *Dir) DisplayPath(name string) string { return filepath.Join(d.path, name) }

// Error is a refused exchange directory. It names the rule.
type Error struct {
	Path string
	Rule string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "exchange directory refused: " + e.Rule
	}
	return fmt.Sprintf("exchange directory %s refused: %s", e.Path, e.Rule)
}

// afterLstat, when set, runs between the Lstat of the directory and the
// OpenRoot, so tests can swap the directory in that window. Nil in
// production.
var afterLstat func(path string)

// Open locates and verifies the exchange directory and opens it as a root.
// The default directory is created at 0700 on first use; an override must
// already exist.
func Open() (*Dir, error) {
	return open(Locate())
}

func open(loc Location) (*Dir, error) {
	p := loc.Path
	refuse := func(rule string) error { return &Error{Path: p, Rule: rule} }

	if !filepath.IsAbs(p) {
		if loc.Override {
			return nil, refuse(EnvOverride + " must be an absolute path")
		}
		return nil, refuse("the data directory does not resolve to an absolute path; set XDG_DATA_HOME to an absolute path or " + EnvOverride)
	}
	p = filepath.Clean(p)

	if !loc.Override {
		if err := createDefault(p); err != nil {
			return nil, refuse(err.Error())
		}
	} else if err := checkOverride(p); err != nil {
		return nil, err
	}

	lst, err := os.Lstat(p)
	if err != nil {
		return nil, refuse(err.Error())
	}
	switch {
	case lst.Mode()&fs.ModeSymlink != 0:
		return nil, refuse("it is a symlink; the exchange directory must be a plain directory")
	case lst.Mode()&fs.ModeIrregular != 0:
		return nil, refuse("it is a junction, reparse point, or other irregular file; the exchange directory must be a plain directory")
	case !lst.IsDir():
		return nil, refuse("it is not a directory")
	}

	if afterLstat != nil {
		afterLstat(p)
	}
	root, err := os.OpenRoot(p)
	if err != nil {
		return nil, refuse(err.Error())
	}
	if err := verifyRoot(p, root, lst); err != nil {
		root.Close()
		return nil, err
	}
	return &Dir{root: root, path: p}, nil
}

// createDefault makes the default directory, and the data directory above
// it, at 0700 when missing. An existing directory is left as it is, to be
// verified, not repaired.
func createDefault(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// CreateResult reports what a write created.
type CreateResult struct {
	// File is open for writing. The caller closes it, and removes Name
	// through Dir.Remove if the write fails.
	File *os.File
	// Name is the name actually created.
	Name string
	// Requested is the name asked for; it differs from Name after a rename.
	Requested string
}

// Renamed reports whether the requested name was taken and a suffix used.
func (r *CreateResult) Renamed() bool { return r.Name != r.Requested }

// Notice states the rename, or is empty.
func (r *CreateResult) Notice() string {
	if !r.Renamed() {
		return ""
	}
	return fmt.Sprintf("%s existed; saved as %s", r.Requested, r.Name)
}

// Create makes a new file named name (a valid bare name) through the root,
// with O_CREATE|O_EXCL at mode 0600. A taken name is suffixed "name (n).ext",
// up to MaxCollisionAttempts names in all.
func (d *Dir) Create(name string) (*CreateResult, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	tried, skipped := 0, 0
	for i := 0; i < MaxCollisionAttempts; i++ {
		cand := name
		if i > 0 {
			cand = Suffixed(name, i)
			if ValidateName(cand) != nil {
				skipped++
				continue
			}
		}
		tried++
		f, err := d.root.OpenFile(cand, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return &CreateResult{File: f, Name: cand, Requested: name}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create %s in the exchange directory: %w", cand, err)
		}
	}
	msg := fmt.Sprintf("%s is taken in the exchange directory, and so are the %d suffixed names tried", name, tried-1)
	if skipped > 0 {
		msg += fmt.Sprintf(" (%d suffixed names were skipped as invalid, too long for the %d-byte limit)", skipped, MaxNameBytes)
	}
	if free := d.freeName(name); free != "" {
		msg += fmt.Sprintf("; pass filename=%q", free)
	} else {
		msg += "; pass a different filename="
	}
	return nil, errors.New(msg)
}

// freeName finds a suffixed name past the collision bound that is free now,
// to suggest to the caller.
func (d *Dir) freeName(name string) string {
	for i := MaxCollisionAttempts; i < MaxCollisionAttempts+1000; i++ {
		cand := Suffixed(name, i)
		if ValidateName(cand) != nil {
			continue
		}
		if _, err := d.root.Lstat(cand); errors.Is(err, fs.ErrNotExist) {
			return cand
		}
	}
	return ""
}

// Remove deletes name through the root.
func (d *Dir) Remove(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return d.root.Remove(name)
}
