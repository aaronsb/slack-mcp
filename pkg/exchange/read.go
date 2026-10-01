package exchange

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode"
)

// MaxHintNames caps how many matching names a miss lists.
const MaxHintNames = 20

// File is a file opened for reading from the exchange directory and checked
// on its handle: a regular file with exactly one link, within the size limit.
type File struct {
	*os.File
	// Name is the bare name it was opened by.
	Name string
	// Size is the size from the Stat of the handle. The file can grow after
	// the check; ReadAll bounds the read by the limit regardless.
	Size int64

	limit int64
}

// ReadAll reads the file, bounded by the limit it was opened with. A file
// that grew past the limit after the check is refused.
func (f *File) ReadAll() ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f.File, f.limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > f.limit {
		return nil, fmt.Errorf("%s grew past the %d byte limit while being read", f.Name, f.limit)
	}
	return b, nil
}

// NotFoundError is a missing name. Error() is the full answer: the count
// line and the matching names (ADR-012, Reads).
type NotFoundError struct {
	Name string
	// Matches are the matching names, sorted, at most MaxHintNames.
	Matches []string
	// Total counts every match, listed or not.
	Total int
	// ListErr is set when the directory could not be listed.
	ListErr error
}

func (e *NotFoundError) Error() string {
	if e.ListErr != nil {
		return fmt.Sprintf("No file named %s is in the exchange directory. (Listing the directory failed: %v)", e.Name, e.ListErr)
	}
	if e.Total == 0 {
		return fmt.Sprintf("No file named %s is in the exchange directory, and no name matches it.", e.Name)
	}
	var b strings.Builder
	switch {
	case e.Total > len(e.Matches):
		fmt.Fprintf(&b, "No file named %s is in the exchange directory. %d names match; the first %d by name:", e.Name, e.Total, len(e.Matches))
	case e.Total == 1:
		fmt.Fprintf(&b, "No file named %s is in the exchange directory. 1 name matches:", e.Name)
	default:
		fmt.Fprintf(&b, "No file named %s is in the exchange directory. %d names match:", e.Name, e.Total)
	}
	for _, m := range e.Matches {
		b.WriteString("\n- " + m)
	}
	return b.String()
}

// Open opens a bare name for reading through the root (O_NONBLOCK on Unix),
// then checks the open handle: a regular file, exactly one link, at most
// limit bytes. A missing name returns a *NotFoundError listing the names
// that match it; a malformed one a *NameError. The caller closes the File.
func (d *Dir) Open(name string, limit int64) (*File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	f, err := d.root.OpenFile(name, openReadFlags, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, d.notFound(name)
		}
		return nil, fmt.Errorf("open %s in the exchange directory: %w", name, err)
	}
	fail := func(format string, a ...any) (*File, error) {
		f.Close()
		return nil, fmt.Errorf(format, a...)
	}
	fi, err := f.Stat()
	if err != nil {
		return fail("stat %s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return fail("%s is not a regular file; only regular files are read from the exchange directory", name)
	}
	links, err := linkCount(f, fi)
	if err != nil {
		return fail("%s: cannot read its link count: %w", name, err)
	}
	if links != 1 {
		return fail("%s has %d links; a file with another name (a hard link) is refused. Copy the file into the exchange directory instead of linking it", name, links)
	}
	if fi.Size() > limit {
		return fail("%s is %d bytes, over the %d byte limit", name, fi.Size(), limit)
	}
	return &File{File: f, Name: name, Size: fi.Size(), limit: limit}, nil
}

// ReadFile opens name with Open and reads it whole, bounded by limit.
func (d *Dir) ReadFile(name string, limit int64) ([]byte, error) {
	f, err := d.Open(name, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadAll()
}

// notFound builds the miss answer: entries that are regular files or
// symlinks (as ReadDir reports them, unfollowed) with valid bare names,
// matching by stem under simple case folding, sorted by fold key then by
// name, capped at MaxHintNames.
func (d *Dir) notFound(name string) *NotFoundError {
	e := &NotFoundError{Name: name}
	dir, err := d.root.Open(".")
	if err != nil {
		e.ListErr = err
		return e
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		e.ListErr = err
		return e
	}

	reqKey := FoldKey(name)
	reqStem, _ := SplitExt(name)
	reqStemKey := FoldKey(reqStem)

	var matches []string
	for _, ent := range entries {
		t := ent.Type()
		if !t.IsRegular() && t&fs.ModeSymlink == 0 {
			continue
		}
		n := ent.Name()
		if ValidateName(n) != nil {
			continue
		}
		stem, _ := SplitExt(n)
		if strings.Contains(FoldKey(n), reqStemKey) || strings.Contains(reqKey, FoldKey(stem)) {
			matches = append(matches, n)
		}
	}
	SortNames(matches)
	e.Total = len(matches)
	if len(matches) > MaxHintNames {
		matches = matches[:MaxHintNames]
	}
	e.Matches = matches
	return e
}

// FoldKey replaces each rune by the least rune of its unicode.SimpleFold
// orbit, so names strings.EqualFold calls equal get the same key.
func FoldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		b.WriteRune(least)
	}
	return b.String()
}

// SortNames orders names by fold key, byte order, breaking ties by the
// byte order of the names themselves.
func SortNames(names []string) {
	sort.Slice(names, func(i, j int) bool {
		ki, kj := FoldKey(names[i]), FoldKey(names[j])
		if ki != kj {
			return ki < kj
		}
		return names[i] < names[j]
	})
}
