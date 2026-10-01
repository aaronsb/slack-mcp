package exchange

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCollisionErrorNamesTheCallersParameter(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "a.txt", "x")
	for i := 1; i < MaxCollisionAttempts; i++ {
		writeFile(t, d, Suffixed("a.txt", i), "x")
	}
	_, err := d.Create("a.txt")
	var ce *CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("Create over a full range = %v, want a *CollisionError", err)
	}
	if !strings.Contains(ce.Error(), `pass filename="a (100).txt"`) {
		t.Fatalf("default wording lost download's parameter: %s", ce.Error())
	}
	if msg := ce.Message("name"); !strings.Contains(msg, `pass name="a (100).txt"`) || strings.Contains(msg, "filename") {
		t.Fatalf("Message(name) = %s", msg)
	}
}

// The staged copy lives where no bare name reaches: the staging directory's
// name fails the bare-name rule, so no caller can create, open, or occupy it.
func TestStagingIsUnreachableByName(t *testing.T) {
	if ValidateName(stagingDir) == nil {
		t.Fatalf("staging directory %q passes the bare-name rule", stagingDir)
	}
}

func TestStageCommitPlacesTheFileUnderItsNameOnly(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	st, err := d.Stage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.File.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	st.File.Close()
	if _, err := os.Stat(filepath.Join(d.Path(), "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("the name exists before commit: %v", err)
	}
	res, err := st.Commit("out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "out.txt" || res.Renamed() {
		t.Fatalf("Commit = %+v", res)
	}
	b, err := d.ReadFile("out.txt", 100) // also proves one link: the staged name is gone
	if err != nil || string(b) != "body" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if left, _ := os.ReadDir(filepath.Join(d.Path(), stagingDir)); len(left) != 0 {
		t.Fatalf("staging not emptied: %v", left)
	}
}

func TestStageCommitSuffixesATakenNameAndNeverReplaces(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	writeFile(t, d, "out.txt", "original")
	st, err := d.Stage()
	if err != nil {
		t.Fatal(err)
	}
	st.File.Write([]byte("new"))
	st.File.Close()
	res, err := st.Commit("out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "out (1).txt" || res.Notice() != "out.txt existed; saved as out (1).txt" {
		t.Fatalf("Commit = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(d.Path(), "out.txt")); string(b) != "original" {
		t.Fatalf("out.txt replaced: %q", b)
	}
}

func TestStageDiscardLeavesNothing(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	st, err := d.Stage()
	if err != nil {
		t.Fatal(err)
	}
	st.File.Close()
	if err := st.Discard(); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(filepath.Join(d.Path(), stagingDir)); len(left) != 0 {
		t.Fatalf("staging not emptied: %v", left)
	}
}

// A staging entry planted by a client's own file tools, as a symlink back
// into the exchange directory, is refused: following it would land the
// partial file under a name say can read.
func TestStageRefusesAStagingEntryThatIsNotAPlainDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	isolate(t)
	d := mustOpen(t)
	if err := os.Symlink(".", filepath.Join(d.Path(), stagingDir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if s, err := d.Stage(); err == nil {
		s.File.Close()
		t.Fatal("Stage followed a staging symlink back into the exchange directory")
	}
	entries, _ := os.ReadDir(d.Path())
	for _, e := range entries {
		if e.Name() != stagingDir {
			t.Errorf("Stage left %q in the exchange directory", e.Name())
		}
	}
}

// A crash between Commit's link and its unstage leaves a staged copy that
// holds a second link on the named file. The next Stage sweeps copies past
// the orphan age, so the named file reads again; a fresh copy stays.
func TestStageSweepsOrphanedStagedCopies(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	s, err := d.Stage()
	if err != nil {
		t.Fatal(err)
	}
	s.File.WriteString("whole")
	s.File.Close()
	if err := d.root.Link(s.tmp, "report.txt"); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	old := time.Now().Add(-2 * stagingOrphanAge)
	if err := os.Chtimes(filepath.Join(d.Path(), s.tmp), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadFile("report.txt", 1<<20); err == nil {
		t.Fatal("a file with two links read before the sweep")
	}

	fresh, err := d.Stage()
	if err != nil {
		t.Fatal(err)
	}
	fresh.File.Close()
	defer fresh.Discard()

	if got, err := d.ReadFile("report.txt", 1<<20); err != nil || string(got) != "whole" {
		t.Fatalf("after the sweep ReadFile = %q, %v; want the whole file", got, err)
	}
	if _, err := os.Lstat(filepath.Join(d.Path(), fresh.tmp)); err != nil {
		t.Errorf("the sweep removed a fresh staged copy: %v", err)
	}
}
