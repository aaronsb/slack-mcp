package playbook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/playbook"
)

var t0 = time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) *playbook.Store {
	t.Helper()
	s, err := playbook.Open("Test Workspace")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func sweep() []playbook.Command {
	return []playbook.Command{
		{Tool: "inbox", Params: map[string]interface{}{"view": "new"}},
		{Tool: "estate", Params: map[string]interface{}{"view": "initiatives", "days": float64(7)}},
	}
}

func TestPlaybooksSurviveReopen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if err := s.Save("morning", sweep(), t0); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened := open(t)
	cmds, ok := reopened.Get("morning")
	if !ok {
		t.Fatal("playbook missing after reopen")
	}
	if len(cmds) != 2 || cmds[0].Tool != "inbox" || cmds[1].Tool != "estate" {
		t.Fatalf("commands did not round-trip: %+v", cmds)
	}
	if v := cmds[1].Params["days"]; v != float64(7) {
		t.Fatalf("params did not round-trip: days = %v (%T)", v, v)
	}
}

func TestSaveReplacesTheWholeSequenceAndKeepsRunStats(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if err := s.Save("morning", sweep(), t0); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.MarkRun("morning", t0.Add(time.Hour)); err != nil {
		t.Fatalf("MarkRun: %v", err)
	}
	replacement := []playbook.Command{{Tool: "messages", Params: map[string]interface{}{"target": "#general"}}}
	if err := s.Save("morning", replacement, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("re-Save: %v", err)
	}

	cmds, _ := open(t).Get("morning")
	if len(cmds) != 1 || cmds[0].Tool != "messages" {
		t.Fatalf("re-save did not replace the sequence: %+v", cmds)
	}
	list := s.List()
	if len(list) != 1 || list[0].Runs != 1 || list[0].LastRun == nil {
		t.Fatalf("run stats lost on re-save: %+v", list)
	}
}

func TestDeleteRemovesAndPersists(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if err := s.Save("morning", sweep(), t0); err != nil {
		t.Fatalf("Save: %v", err)
	}
	found, err := s.Delete("morning")
	if err != nil || !found {
		t.Fatalf("Delete: found=%v err=%v", found, err)
	}
	if _, ok := open(t).Get("morning"); ok {
		t.Fatal("deleted playbook survived reopen")
	}
	found, err = s.Delete("morning")
	if err != nil || found {
		t.Fatalf("second Delete: found=%v err=%v", found, err)
	}
}

func TestListSortsByName(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	for _, name := range []string{"weekly", "morning", "audit"} {
		if err := s.Save(name, sweep(), t0); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	list := s.List()
	if len(list) != 3 || list[0].Name != "audit" || list[1].Name != "morning" || list[2].Name != "weekly" {
		t.Fatalf("listing not sorted by name: %+v", list)
	}
	if list[0].Commands != 2 {
		t.Fatalf("command count wrong: %+v", list[0])
	}
}

func TestStoreFileIsPrivateAndLeavesNoTempFiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if err := s.Save("morning", sweep(), t0); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}

	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("stray temp file: %s", e.Name())
		}
	}
}

func TestMarkRunOnMissingNameErrors(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := open(t).MarkRun("ghost", t0); err == nil {
		t.Fatal("MarkRun on a missing playbook did not error")
	}
}
