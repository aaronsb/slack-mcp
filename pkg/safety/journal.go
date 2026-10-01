package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/gofrs/flock"
)

// LockTimeout bounds how long a writer waits for another process's write.
// A write is one small append, so a wait this long means a wedged holder.
var LockTimeout = 10 * time.Second

// tailCheck is how many bytes before the read offset are compared on an
// incremental read. Size, mtime, and identity catch a shrink, a replacement,
// and an in-place edit that kept the size; this catches an in-place edit
// that also grew the file within one mtime tick.
const tailCheck = 64

// journal is one append-only JSON-lines file and its reader cursor.
//
// Reading (ADR-013, The quarantine file): the cursor keeps the size,
// modification time, and identity of its last read and reads on from the
// end of the last complete line. A file that shrank, was replaced, or was
// modified in place without growing is read again from offset 0 and the
// state rebuilt. An unterminated final line is a write in progress and is
// left for the next read; a complete line that does not parse is skipped and
// remembered by line number; a missing file is empty; a file that exists but
// cannot be read sets err.
//
// Writing: under an advisory lock on a sibling .lock file, a newline first
// when the last byte is not one, then the entry, in one write.
type journal struct {
	path     string
	lockPath string

	fi     os.FileInfo
	size   int64 // bytes covered by the last read, torn tail included
	mtime  time.Time
	offset int64  // end of the last complete line consumed
	tail   []byte // up to tailCheck bytes ending at offset
	lines  int    // complete lines consumed

	malformed []int // 1-based line numbers of lines that did not parse
	err       error // the file exists and cannot be read
}

func newJournal(path string) *journal {
	return &journal{path: path, lockPath: path + ".lock"}
}

func (j *journal) clearCursor() {
	j.fi = nil
	j.size, j.offset, j.lines = 0, 0, 0
	j.mtime = time.Time{}
	j.tail = nil
	j.malformed = nil
	j.err = nil
}

// refresh brings the caller's fold up to date. reset empties the fold before
// a rebuild; apply folds one complete, non-blank line and returns an error
// when it does not parse.
func (j *journal) refresh(reset func(), apply func([]byte) error) {
	f, err := os.Open(j.path)
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
		j.unreadable(reset, fmt.Errorf("%s: not a regular file", j.path))
		return
	}

	rebuild := j.fi == nil ||
		!os.SameFile(j.fi, fi) ||
		fi.Size() < j.size ||
		(fi.Size() == j.size && !fi.ModTime().Equal(j.mtime))
	if !rebuild && fi.Size() == j.size {
		return // nothing new
	}
	if !rebuild && len(j.tail) > 0 {
		got := make([]byte, len(j.tail))
		if _, err := f.ReadAt(got, j.offset-int64(len(j.tail))); err != nil || !bytes.Equal(got, j.tail) {
			rebuild = true
		}
	}
	if rebuild {
		j.clearCursor()
		reset()
	}

	start := j.offset
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		j.unreadable(reset, err)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		j.unreadable(reset, err)
		return
	}

	consumed := 0
	for {
		nl := bytes.IndexByte(data[consumed:], '\n')
		if nl < 0 {
			break // torn or in-progress final line: left for the next read
		}
		line := bytes.TrimSpace(data[consumed : consumed+nl])
		consumed += nl + 1
		j.lines++
		if len(line) == 0 {
			continue
		}
		if err := apply(line); err != nil {
			j.malformed = append(j.malformed, j.lines)
		}
	}

	j.offset = start + int64(consumed)
	j.size = start + int64(len(data))
	j.fi = fi
	j.mtime = fi.ModTime()
	j.err = nil
	// The tail is the bytes just before offset, from this read when it
	// reached back far enough, else from the file.
	n := int64(tailCheck)
	if n > j.offset {
		n = j.offset
	}
	j.tail = make([]byte, n)
	if int64(consumed) >= n {
		copy(j.tail, data[consumed-int(n):consumed])
	} else if _, err := f.ReadAt(j.tail, j.offset-n); err != nil {
		j.tail = nil // no tail check next time; the size and mtime still apply
	}
}

func (j *journal) unreadable(reset func(), err error) {
	j.clearCursor()
	reset()
	j.err = err
}

// withLock runs fn holding the journal's advisory lock, shared by every
// process using the data directory.
func (j *journal) withLock(fn func() error) error {
	lk := flock.New(j.lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), LockTimeout)
	defer cancel()
	ok, err := lk.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock %s: %w", j.lockPath, err)
	}
	if !ok {
		return fmt.Errorf("lock %s: timed out", j.lockPath)
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
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
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

// rewrite replaces the file with lines, through a temporary file and a
// rename, so a reader sees the old file or the new one and, by identity,
// rebuilds. The caller holds the lock.
func (j *journal) rewrite(lines [][]byte) error {
	tmp, err := os.CreateTemp(dirOf(j.path), ".rewrite-*")
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

// malformedLines returns a copy of the skipped line numbers.
func (j *journal) malformedLines() []int {
	return append([]int(nil), j.malformed...)
}
