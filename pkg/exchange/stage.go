package exchange

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// stagingDir holds files being written before they take their name. Its
// name carries a format character (U+2060 WORD JOINER), which the bare-name
// rule refuses, so no file parameter can name it, create it, or occupy it
// with a file of its own; the files inside are a path away from any bare
// name. Filesystems on every supported platform accept the character.
const stagingDir = ".⁠staging"

// Staged is a file written out of reach of every file parameter, then
// placed under its name whole (ADR-012, put amendment): a say running
// at the same time never reads a partial file.
type Staged struct {
	// File is open for writing. The caller closes it before Commit or
	// Discard.
	File  *os.File
	d     *Dir
	tmp   string
	stuck bool
}

// Stage creates an empty staged file at mode 0600.
func (d *Dir) Stage() (*Staged, error) {
	if err := d.root.Mkdir(stagingDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create the exchange directory's staging area: %w", err)
	}
	// No file parameter can create the staging name, but a client's own
	// file tools can. A symlink there would be followed inside the root and
	// land the partial file under a reachable name, so anything other than
	// a plain directory is refused.
	if fi, err := d.root.Lstat(stagingDir); err != nil {
		return nil, fmt.Errorf("check the exchange directory's staging area: %w", err)
	} else if !fi.IsDir() || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, fmt.Errorf("the exchange directory's staging area %q is not a plain directory; remove it", stagingDir)
	}
	d.sweepStaging()
	var b [12]byte
	for i := 0; i < 3; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		tmp := filepath.Join(stagingDir, hex.EncodeToString(b[:]))
		f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return &Staged{File: f, d: d, tmp: tmp}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("stage a file in the exchange directory: %w", err)
		}
	}
	return nil, errors.New("stage a file in the exchange directory: no free staging name")
}

// Commit gives the staged file its name, a valid bare name, by hard-linking
// it there; a link never replaces an existing name, so a taken name is
// suffixed as Create does, failing past the bound with a *CollisionError.
// The staged name is then removed, leaving the file one link. The result's
// File is nil. On an error the caller calls Discard.
func (s *Staged) Commit(name string) (*CreateResult, error) {
	got, err := s.d.claim(name, func(cand string) error { return s.d.root.Link(s.tmp, cand) })
	if err != nil {
		return nil, err
	}
	if err := s.d.root.Remove(s.tmp); err != nil {
		// With two links the file is refused on read; take the name back.
		if rmErr := s.d.root.Remove(got); rmErr != nil {
			s.stuck = true
			return nil, fmt.Errorf("unstage %s: %v; %s keeps a second link and is refused on read until the staged copy is removed: %v", got, err, got, rmErr)
		}
		return nil, fmt.Errorf("unstage %s: %w", got, err)
	}
	return &CreateResult{Name: got, Requested: name}, nil
}

// Discard removes the staged file. One already removed is not an error.
// After a Commit that could take back neither name, Discard leaves the
// staged copy alone: removing it would turn the named file, which the
// error reported refused, into an attachable one.
func (s *Staged) Discard() error {
	if s.stuck {
		return nil
	}
	if err := s.d.root.Remove(s.tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// stagingOrphanAge is how old a staged file must be before Stage treats it
// as left by a write that never finished.
const stagingOrphanAge = 10 * time.Minute

// sweepStaging removes staged files older than stagingOrphanAge. A crash
// mid-write leaves one nothing else can reach; a crash between Commit's
// link and its unstage leaves one that holds a second link on a named
// file, which Reads refuses until the staged name is gone. The link comes
// after the write, so removing it leaves that file whole.
func (d *Dir) sweepStaging() {
	f, err := d.root.Open(stagingDir)
	if err != nil {
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stagingOrphanAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = d.root.Remove(filepath.Join(stagingDir, e.Name()))
	}
}
