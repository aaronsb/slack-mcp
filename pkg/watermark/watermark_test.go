package watermark_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/watermark"
)

func open(t *testing.T) *watermark.Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

var now = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

// Nothing has been shown, so everything is unseen.
func TestUnknownConversationIsChanged(t *testing.T) {
	s := open(t)

	if !s.Changed("C1", "1782246118.543969") {
		t.Error("unknown conversation should count as changed")
	}
	if s.Known("C1") {
		t.Error("Known reported true before any Ack")
	}
}

func TestChangedTracksLatestPastTheWatermark(t *testing.T) {
	s := open(t)
	s.Ack("C1", "1782246118.543969", now)

	if s.Changed("C1", "1782246118.543969") {
		t.Error("same latest as the watermark should not be a change")
	}
	if s.Changed("C1", "1782246000.000000") {
		t.Error("older latest should not be a change")
	}
	if !s.Changed("C1", "1782246119.000001") {
		t.Error("newer latest should be a change")
	}
}

// The whole point of the store: a human reading in their own client moves
// last_read, and must not move this.
func TestReadStateIsIrrelevant(t *testing.T) {
	s := open(t)
	s.Ack("C1", "1782246000.000000", now)

	// Whatever the official client did, the only input here is `latest`.
	if !s.Changed("C1", "1782246118.543969") {
		t.Error("a conversation that moved should be changed regardless of read state")
	}
}

// A read marker can legitimately sit ahead of the newest message; the store
// must never be fed from it. Acking a stale position must not rewind.
func TestAckNeverMovesBackwards(t *testing.T) {
	s := open(t)
	s.Ack("C1", "1782246118.543969", now)
	s.Ack("C1", "1782240000.000000", now.Add(time.Minute))

	if s.Changed("C1", "1782246118.543969") {
		t.Error("watermark moved backwards on a stale Ack")
	}
	c, _ := s.Conversation("C1")
	if c.LastShownTS != "1782246118.543969" {
		t.Errorf("LastShownTS = %q, want the newer timestamp", c.LastShownTS)
	}
}

func TestEmptyLatestIsNotAChange(t *testing.T) {
	s := open(t)
	if s.Changed("C1", "") {
		t.Error("a conversation with no latest cannot have changed")
	}
}

func TestThreadsTrackedSeparately(t *testing.T) {
	s := open(t)

	if !s.ThreadChanged("C1", "1782246118.543969", "1786752114.508819") {
		t.Error("untracked thread should count as changed")
	}

	s.AckThread("C1", "1782246118.543969", "1786752114.508819", now)

	if s.ThreadChanged("C1", "1782246118.543969", "1786752114.508819") {
		t.Error("thread at the acknowledged reply should not be changed")
	}
	if !s.ThreadChanged("C1", "1782246118.543969", "1786752200.000000") {
		t.Error("a newer reply should be a change")
	}
	// A thread's movement is invisible in its channel's watermark.
	if s.Known("C1") {
		t.Error("acking a thread should not create a conversation watermark")
	}
}

func TestTrackedThreadsBoundedByRecency(t *testing.T) {
	s := open(t)
	s.AckThread("C1", "100.000000", "101.000000", now.Add(-30*24*time.Hour))
	s.AckThread("C1", "200.000000", "201.000000", now.Add(-1*time.Hour))

	recent := s.TrackedThreads(7*24*time.Hour, now)
	if len(recent) != 1 || recent[0].ThreadTS != "200.000000" {
		t.Fatalf("want only the recent thread, got %+v", recent)
	}

	if all := s.TrackedThreads(0, now); len(all) != 2 {
		t.Errorf("maxAge 0 should return every tracked thread, got %d", len(all))
	}
}

func TestPruneDropsStaleEntries(t *testing.T) {
	s := open(t)
	s.Ack("C1", "100.000000", now.Add(-90*24*time.Hour))
	s.Ack("C2", "200.000000", now)
	s.AckThread("C1", "300.000000", "301.000000", now.Add(-90*24*time.Hour))

	if got := s.Prune(30*24*time.Hour, now); got != 2 {
		t.Errorf("pruned %d entries, want 2", got)
	}
	if s.Known("C1") {
		t.Error("stale conversation survived prune")
	}
	if !s.Known("C2") {
		t.Error("fresh conversation was pruned")
	}
}

func TestSaveAndReopenRoundTrips(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Ack("C1", "1782246118.543969", now)
	s.AckThread("C1", "1782246118.543969", "1786752114.508819", now)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Changed("C1", "1782246118.543969") {
		t.Error("conversation watermark did not survive a reopen")
	}
	if reopened.ThreadChanged("C1", "1782246118.543969", "1786752114.508819") {
		t.Error("thread watermark did not survive a reopen")
	}
}

// Losing this state makes an agent re-show everything, so the file must not be
// readable by other users and must be replaced atomically.
func TestSavedFileIsPrivate(t *testing.T) {
	s := open(t)
	s.Ack("C1", "1782246118.543969", now)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("watermark file mode %o, want 600", perm)
	}

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Errorf("stray file left in the watermark directory: %s", e.Name())
		}
	}
}

// Scopes and workspaces must not share a position.
func TestScopesAreIsolated(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	a, err := watermark.Open("Praecipio", "monitor")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a.Ack("C1", "1782246118.543969", now)
	if err := a.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b, err := watermark.Open("Praecipio", "briefing")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if b.Known("C1") {
		t.Error("scopes share a watermark")
	}

	other, err := watermark.Open("OtherCorp", "monitor")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if other.Known("C1") {
		t.Error("workspaces share a watermark")
	}
}

// Slack timestamps are compared numerically, so a wider seconds field cannot
// invert the ordering the way a string comparison would.
func TestTimestampOrderingIsNumeric(t *testing.T) {
	s := open(t)
	s.Ack("C1", "999999999.999999", now)

	if !s.Changed("C1", "1000000000.000000") {
		t.Error("a 10-digit timestamp should be newer than a 9-digit one")
	}
}

func TestOpenRequiresWorkspace(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := watermark.Open("", watermark.DefaultScope); err == nil {
		t.Error("Open accepted an empty workspace")
	}
}

// A corrupt file must surface as an error rather than silently resetting an
// agent's position to zero.
func TestCorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	path := filepath.Join(dir, "slack-mcp", "watermarks", "Praecipio")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "default.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := watermark.Open("Praecipio", watermark.DefaultScope); err == nil {
		t.Error("Open silently accepted a corrupt watermark file")
	}
}
