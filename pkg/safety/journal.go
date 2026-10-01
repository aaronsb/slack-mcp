package safety

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// LockTimeout bounds how long a writer waits for another process's write.
// A write is one small append, so a wait this long means a wedged holder.
var LockTimeout = 10 * time.Second

// journal is one append-only JSON-lines file and its reader cursor.
//
// Reading (ADR-013, The quarantine file): the cursor keeps the size,
// modification time, and identity of its last read, and a SHA-256 of every
// byte it has consumed, and reads on from the end of the last complete line.
// A file that shrank, was replaced, was modified in place without growing,
// or whose consumed bytes no longer hash the same (an edit that also grew
// it) is read again from offset 0 and the state rebuilt. The files are
// small, so each read takes the whole file and the hash costs one pass.
//
// An unterminated final line is a write in progress and is left for the
// next read; a complete line that does not parse is skipped and remembered
// by line number; a missing file is empty; a file that exists but cannot be
// read, or is not a regular file (a symlink, a FIFO), sets err. A
// non-regular file is refused before it is opened, so a FIFO cannot block
// the reader.
//
// Writing: under an advisory lock on a sibling .lock file, a newline first
// when the last byte is not one, then the entry, in one write.
type journal struct {
	path     string
	lockPath string

	fi     os.FileInfo
	size   int64 // bytes covered by the last read, torn tail included
	mtime  time.Time
	offset int64             // end of the last complete line consumed
	prefix [sha256.Size]byte // SHA-256 of [0, offset)
	lines  int               // complete lines consumed
	// prefixes[n] is the hex SHA-256 of the bytes through line n, n=0 the
	// empty file: what lets another process check a Place.
	prefixes []string

	malformed []int // 1-based line numbers of lines that did not parse
	err       error // the file exists and cannot be read
}

func newJournal(path string) *journal {
	j := &journal{path: path, lockPath: path + ".lock"}
	j.clearCursor()
	return j
}

func (j *journal) clearCursor() {
	j.fi = nil
	j.size, j.offset, j.lines = 0, 0, 0
	j.mtime = time.Time{}
	j.prefix = sha256.Sum256(nil)
	j.prefixes = []string{hex.EncodeToString(j.prefix[:])}
	j.malformed = nil
	j.err = nil
}

// checkRegular refuses a path that exists and is not a regular file,
// without following a symlink. A missing path passes.
func checkRegular(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	return nil
}

// refresh brings the caller's fold up to date. reset empties the fold before
// a rebuild; apply folds one complete, non-blank line and returns an error
// when it does not parse.
func (j *journal) refresh(reset func(), apply func([]byte) error) {
	if err := checkRegular(j.path); err != nil {
		j.unreadable(reset, err)
		return
	}
	f, err := os.OpenFile(j.path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if j.fi != nil || j.err != nil || j.lines > 0 {
				j.clearCursor()
				reset()
			}
			return
		}
		j.unreadable(reset, err)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		j.unreadable(reset, err)
		return
	}
	if !fi.Mode().IsRegular() {
		j.unreadable(reset, fmt.Errorf("%s: not a regular file", filepath.Base(j.path)))
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		j.unreadable(reset, err)
		return
	}

	size := int64(len(data))
	rebuild := j.fi == nil ||
		!os.SameFile(j.fi, fi) ||
		size < j.offset ||
		size < j.size ||
		(size == j.size && !fi.ModTime().Equal(j.mtime)) ||
		sha256.Sum256(data[:j.offset]) != j.prefix
	if rebuild {
		j.clearCursor()
		reset()
	}

	h := sha256.New()
	h.Write(data[:j.offset])
	consumed := j.offset
	for {
		nl := bytes.IndexByte(data[consumed:], '\n')
		if nl < 0 {
			break // torn or in-progress final line: left for the next read
		}
		end := consumed + int64(nl) + 1
		line := bytes.TrimSpace(data[consumed : end-1])
		h.Write(data[consumed:end])
		consumed = end
		j.lines++
		j.prefixes = append(j.prefixes, hex.EncodeToString(h.Sum(nil)))
		if len(line) == 0 {
			continue
		}
		if err := apply(line); err != nil {
			j.malformed = append(j.malformed, j.lines)
		}
	}
	j.offset = consumed
	copy(j.prefix[:], h.Sum(nil))
	j.size = size
	j.fi = fi
	j.mtime = fi.ModTime()
	j.err = nil
}

// place is where the last read ended.
func (j *journal) place() Place {
	return Place{Line: j.lines, Prefix: j.prefixes[j.lines]}
}

func (j *journal) unreadable(reset func(), err error) {
	j.clearCursor()
	reset()
	j.err = err
}

// withLock runs fn holding the journal's advisory lock, shared by every
// process using the data directory.
func (j *journal) withLock(fn func() error) error {
	if err := checkRegular(j.lockPath); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	lk := flock.New(j.lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), LockTimeout)
	defer cancel()
	ok, err := lk.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(j.lockPath), err)
	}
	if !ok {
		return fmt.Errorf("lock %s: timed out", filepath.Base(j.lockPath))
	}
	defer func() { _ = lk.Unlock() }()
	return fn()
}

// appendLine writes one entry. The caller holds the lock. A crash mid-entry
// leaves an unterminated line, which the next writer terminates first, so it
// never merges into the next entry.
func (j *journal) appendLine(entry []byte) error {
	if bytes.IndexByte(entry, '\n') >= 0 {
		return fmt.Errorf("journal entry contains a newline")
	}
	if err := checkRegular(j.path); err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE|os.O_APPEND|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", filepath.Base(j.path))
	}
	buf := make([]byte, 0, len(entry)+2)
	if fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			buf = append(buf, '\n')
		}
	}
	buf = append(buf, entry...)
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		return err
	}
	return f.Sync()
}

// rewritePrefix names the temporary files rewrite leaves behind if it dies
// before its rename; OpenDir sweeps old ones.
const rewritePrefix = ".rewrite-"

// rewrite replaces the file with lines, through a temporary file and a
// rename, so a reader sees the old file or the new one and, by identity,
// rebuilds. The caller holds the lock.
func (j *journal) rewrite(lines [][]byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(j.path), rewritePrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows refuses a rename over a file another process holds open; a
	// reader holds it only for one read, so a short retry suffices.
	for i := 0; ; i++ {
		err = os.Rename(tmpName, j.path)
		if err == nil || i >= 20 {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// sweepRewrites removes temporary files a crashed rewrite left in dir. Only
// files older than a minute go, so a rewrite in progress in another process
// keeps its file.
func sweepRewrites(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), rewritePrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > time.Minute {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// malformedLines returns a copy of the skipped line numbers.
func (j *journal) malformedLines() []int {
	return append([]int(nil), j.malformed...)
}
