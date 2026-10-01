package safety

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
)

func openTest(t *testing.T, p Posture) *Workspace {
	t.Helper()
	w, err := OpenDir(t.TempDir(), "T1", p)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func channel(id, name string) Destination {
	return Destination{Kind: DestChannel, ConversationID: id, Name: name}
}

func dm(conv, user, handle string) Destination {
	return Destination{Kind: DestDM, ConversationID: conv, Name: handle, Members: []Key{Person(user, handle)}}
}

func block(t *testing.T, w *Workspace, d Destination) BlockOutcome {
	t.Helper()
	out, err := w.Quarantine.RecordBlock(Block{
		Destination: d,
		Call:        CallRecord{Tool: "say", Destination: d.Name, TextSHA256: HashText("hello")},
		Match:       MatchRecord{Class: "slack-token", Location: "text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func blockLine(strike int, convID string) string {
	q := true
	raw, _ := json.Marshal(quarantineLine{
		Time: time.Unix(0, 0).UTC(), Kind: kindBlock, Posture: Strict, Quarantined: &q, Strike: strike,
		Destination: &Destination{Kind: DestChannel, ConversationID: convID},
		Keys:        []Key{Conversation(convID, "#"+convID)},
	})
	return string(raw)
}

func qpath(w *Workspace) string { return filepath.Join(w.Dir, QuarantineFile) }

func TestJournalMissingFileIsEmpty(t *testing.T) {
	w := openTest(t, Strict)
	st := w.Quarantine.State()
	if st.Err != nil || st.Strikes != 0 || st.LockEngaged() || st.Any() {
		t.Fatalf("missing file: %+v", st)
	}
	if ts := w.Trust.State(); ts.Err != nil {
		t.Fatalf("trust: %v", ts.Err)
	}
	if _, err := os.Stat(qpath(w)); !os.IsNotExist(err) {
		t.Fatalf("reading created the file: %v", err)
	}
}

func TestJournalFileRemovedResetsState(t *testing.T) {
	w := openTest(t, Soft)
	block(t, w, channel("C1", "#a"))
	if w.Quarantine.State().Strikes != 1 {
		t.Fatal("want 1 strike")
	}
	if err := os.Remove(qpath(w)); err != nil {
		t.Fatal(err)
	}
	if st := w.Quarantine.State(); st.Strikes != 0 || st.Err != nil {
		t.Fatalf("after removal: %+v", st)
	}
}

func TestJournalTornFinalLineIgnoredThenRepaired(t *testing.T) {
	w := openTest(t, Strict)
	body := blockLine(1, "C1") + "\n" + `{"time":"2026-01-01T00:00:00Z","kind":"blo`
	if err := os.WriteFile(qpath(w), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if st.Strikes != 1 || len(st.Malformed) != 0 {
		t.Fatalf("torn tail: strikes=%d malformed=%v", st.Strikes, st.Malformed)
	}

	// The next writer terminates the torn line first, so it becomes a
	// complete line that does not parse: skipped and flagged, and the new
	// entry is intact.
	block(t, w, channel("C2", "#b"))
	raw, _ := os.ReadFile(qpath(w))
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), raw)
	}
	st = w.Quarantine.State()
	if st.Strikes != 2 {
		t.Fatalf("strikes=%d, want 2", st.Strikes)
	}
	if len(st.Malformed) != 1 || st.Malformed[0] != 2 {
		t.Fatalf("malformed=%v, want [2]", st.Malformed)
	}

	// A fresh reader agrees.
	w2, _ := OpenDir(w.Dir, "T1", Strict)
	if st2 := w2.Quarantine.State(); st2.Strikes != 2 || len(st2.Malformed) != 1 {
		t.Fatalf("fresh reader: %+v", st2)
	}
}

func TestJournalTornLineCompletedByWriterParses(t *testing.T) {
	w := openTest(t, Strict)
	full := blockLine(1, "C1")
	half := len(full) / 2
	if err := os.WriteFile(qpath(w), []byte(full[:half]), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := w.Quarantine.State(); st.Strikes != 0 {
		t.Fatalf("in-progress line counted: %d", st.Strikes)
	}
	f, _ := os.OpenFile(qpath(w), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(full[half:] + "\n")
	f.Close()
	if st := w.Quarantine.State(); st.Strikes != 1 || len(st.Malformed) != 0 {
		t.Fatalf("completed line: %+v", st)
	}
}

func TestJournalMalformedLineSkippedAndFlagged(t *testing.T) {
	w := openTest(t, Soft)
	body := blockLine(1, "C1") + "\nnot json\n" + `{"kind":"mystery"}` + "\n" + blockLine(2, "C2") + "\n"
	os.WriteFile(qpath(w), []byte(body), 0o600)
	st := w.Quarantine.State()
	if st.Strikes != 2 {
		t.Fatalf("strikes=%d", st.Strikes)
	}
	if fmt.Sprint(st.Malformed) != "[2 3]" {
		t.Fatalf("malformed=%v", st.Malformed)
	}
	if !st.Any() {
		t.Fatal("malformed lines belong in the banner")
	}
}

func TestJournalIncrementalRead(t *testing.T) {
	w := openTest(t, Soft)
	os.WriteFile(qpath(w), []byte(blockLine(1, "C1")+"\n"), 0o600)
	if w.Quarantine.State().Strikes != 1 {
		t.Fatal()
	}
	f, _ := os.OpenFile(qpath(w), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(blockLine(2, "C2") + "\n")
	f.Close()
	st := w.Quarantine.State()
	if st.Strikes != 2 || len(st.Conversations) != 2 {
		t.Fatalf("incremental: %+v", st)
	}
}

func TestJournalTruncationRebuilds(t *testing.T) {
	w := openTest(t, Soft)
	block(t, w, channel("C1", "#a"))
	block(t, w, channel("C2", "#b"))
	if w.Quarantine.State().Strikes != 2 {
		t.Fatal()
	}
	// Truncate in place to one line: same file, smaller.
	if err := os.WriteFile(qpath(w), []byte(blockLine(1, "C9")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if st.Strikes != 1 {
		t.Fatalf("strikes=%d after truncation", st.Strikes)
	}
	if _, ok := st.IsQuarantined("C9"); !ok {
		t.Fatal("rebuilt state lacks C9")
	}
	if _, ok := st.IsQuarantined("C2"); ok {
		t.Fatal("stale C2 survived the rebuild")
	}
}

func TestJournalReplacementRebuilds(t *testing.T) {
	w := openTest(t, Soft)
	os.WriteFile(qpath(w), []byte(blockLine(1, "CA")+"\n"), 0o600)
	if _, ok := w.Quarantine.State().IsQuarantined("CA"); !ok {
		t.Fatal()
	}
	// Same size, same mtime, different file.
	fi, _ := os.Stat(qpath(w))
	tmp := filepath.Join(w.Dir, "new")
	os.WriteFile(tmp, []byte(blockLine(1, "CB")+"\n"), 0o600)
	os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp, qpath(w)); err != nil {
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if _, ok := st.IsQuarantined("CB"); !ok {
		t.Fatal("replacement not read")
	}
	if _, ok := st.IsQuarantined("CA"); ok {
		t.Fatal("replaced file's state survived")
	}
}

func TestJournalInPlaceSameSizeEditRebuilds(t *testing.T) {
	w := openTest(t, Soft)
	os.WriteFile(qpath(w), []byte(blockLine(1, "CA")+"\n"), 0o600)
	w.Quarantine.State()
	fi, _ := os.Stat(qpath(w))
	// Edit in place, keeping the size; the mtime moves.
	f, _ := os.OpenFile(qpath(w), os.O_WRONLY, 0o600)
	f.WriteAt([]byte(blockLine(1, "CB")+"\n"), 0)
	f.Close()
	later := fi.ModTime().Add(2 * time.Second)
	os.Chtimes(qpath(w), later, later)
	st := w.Quarantine.State()
	if _, ok := st.IsQuarantined("CB"); !ok {
		t.Fatal("in-place edit not read")
	}
	if _, ok := st.IsQuarantined("CA"); ok {
		t.Fatal("edited-away state survived")
	}
}

func TestJournalInPlaceEditThatGrowsRebuilds(t *testing.T) {
	w := openTest(t, Soft)
	os.WriteFile(qpath(w), []byte(blockLine(1, "CA")+"\n"), 0o600)
	w.Quarantine.State()
	// Rewrite the first line and append a second: grown, but the bytes
	// before the old offset changed, so the tail check forces a rebuild.
	os.WriteFile(qpath(w), []byte(blockLine(1, "CB")+"\n"+blockLine(2, "CC")+"\n"), 0o600)
	st := w.Quarantine.State()
	if _, ok := st.IsQuarantined("CA"); ok {
		t.Fatal("edited-away state survived")
	}
	if st.Strikes != 2 {
		t.Fatalf("strikes=%d", st.Strikes)
	}
}

func TestJournalUnreadableFailsClosedForQuarantine(t *testing.T) {
	w := openTest(t, Soft)
	if err := os.Mkdir(qpath(w), 0o700); err != nil { // exists, cannot be read as a file
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if st.Err == nil || !st.LockEngaged() {
		t.Fatalf("unreadable: err=%v locked=%v", st.Err, st.LockEngaged())
	}
	if _, err := w.Quarantine.RecordBlock(Block{Destination: channel("C1", "#a")}); err == nil {
		t.Fatal("RecordBlock on an unreadable file must fail")
	}
	// Fixed: the lock lifts at the next read.
	os.Remove(qpath(w))
	if st := w.Quarantine.State(); st.Err != nil || st.LockEngaged() {
		t.Fatalf("after fix: %+v", st)
	}
}

func TestJournalUnreadablePermissions(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this reader")
	}
	w := openTest(t, Strict)
	block(t, w, channel("C1", "#a"))
	tw := w.Trust
	if err := tw.Add(TrustAdd{Key: Conversation("C1", "#a"), Cases: []Case{CaseCrossConversation}, Source: SourceCLI}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(qpath(w), 0)
	os.Chmod(filepath.Join(w.Dir, TrustFile), 0)
	defer os.Chmod(qpath(w), 0o600)
	defer os.Chmod(filepath.Join(w.Dir, TrustFile), 0o600)

	st := w.Quarantine.State()
	if st.Err == nil || !st.LockEngaged() {
		t.Fatalf("quarantine unreadable must lock: %+v", st)
	}
	ts := w.Trust.State()
	if ts.Err == nil {
		t.Fatal("trust unreadable must report")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: channel("C1", "#a"), Case: CaseCrossConversation}, time.Now()); ok {
		t.Fatal("unreadable trust must trust nothing")
	}
}

func TestJournalUnreadableTrustTrustsNothing(t *testing.T) {
	w := openTest(t, Strict)
	os.Mkdir(filepath.Join(w.Dir, TrustFile), 0o700)
	ts := w.Trust.State()
	if ts.Err == nil {
		t.Fatal("want an error")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: channel("C1", "#a"), Case: CaseCrossConversation}, time.Now()); ok {
		t.Fatal("trusted through an unreadable file")
	}
	// No lock follows from an unreadable trust file.
	if w.Quarantine.State().LockEngaged() {
		t.Fatal("trust error engaged the lock")
	}
}

func TestJournalFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	w := openTest(t, Strict)
	block(t, w, channel("C1", "#a"))
	fi, err := os.Stat(qpath(w))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestJournalTwoHandlesShareState(t *testing.T) {
	dir := t.TempDir()
	a, _ := OpenDir(dir, "T1", Soft)
	b, _ := OpenDir(dir, "T1", Soft)
	block(t, a, channel("C1", "#a"))
	out := block(t, b, channel("C2", "#b"))
	if out.Strike != 2 {
		t.Fatalf("second handle saw strike %d, want 2", out.Strike)
	}
	if a.Quarantine.State().Strikes != 2 {
		t.Fatal("first handle did not see the second's write")
	}
}

// TestHelperAppender is run as a child process by
// TestJournalConcurrentProcesses.
func TestHelperAppender(t *testing.T) {
	dir := os.Getenv("SAFETY_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	n, _ := strconv.Atoi(os.Getenv("SAFETY_HELPER_N"))
	w, err := OpenDir(dir, "T1", Strict)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := w.Quarantine.RecordBlock(Block{Destination: channel(fmt.Sprintf("C%s-%d", os.Getenv("SAFETY_HELPER_TAG"), i), "#x")}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJournalConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	dir := t.TempDir()
	const n = 40
	var cmds []*exec.Cmd
	for _, tag := range []string{"a", "b"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperAppender$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SAFETY_HELPER_DIR="+dir, "SAFETY_HELPER_N="+strconv.Itoa(n), "SAFETY_HELPER_TAG="+tag)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if t.Failed() {
				t.Log(out.String())
			}
		})
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}

	w, _ := OpenDir(dir, "T1", Strict)
	st := w.Quarantine.State()
	if len(st.Malformed) != 0 {
		t.Fatalf("interleaved writes: malformed lines %v", st.Malformed)
	}
	if st.Strikes != 2*n {
		t.Fatalf("strikes=%d, want %d", st.Strikes, 2*n)
	}
	// The lock covers read-decide-append, so every strike number is
	// assigned exactly once.
	raw, _ := os.ReadFile(qpath(w))
	var strikes []int
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var ql quarantineLine
		if err := json.Unmarshal(l, &ql); err != nil {
			t.Fatal(err)
		}
		strikes = append(strikes, ql.Strike)
	}
	sort.Ints(strikes)
	for i, s := range strikes {
		if s != i+1 {
			t.Fatalf("strike numbers not 1..%d: %v", 2*n, strikes)
		}
	}
}

func TestDirMatchesEstateLayout(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, team := range []string{"T0123ABC", "E01:weird/team", ""} {
		if team == "" {
			continue
		}
		st, err := estate.Open(team)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Dir(st.Path())
		st.Close()
		if got := Dir(team); got != want {
			t.Fatalf("Dir(%q) = %s, estate uses %s", team, got, want)
		}
	}
}
