package exchange

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
