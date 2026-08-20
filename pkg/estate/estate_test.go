package estate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

var base = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

const team = "T0TEST"

func open(t *testing.T) *estate.Store {
	t.Helper()
	s, err := estate.Open(team)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func user(id, name string) slack.User {
	u := slack.User{ID: id, Name: name, RealName: name + " Real"}
	u.Profile.DisplayName = name
	u.Profile.Title = "Title of " + name
	return u
}

func channel(id, name string) slack.Channel {
	var c slack.Channel
	c.ID = id
	c.Name = name
	c.IsMember = true
	c.Purpose.Value = "Purpose of " + name
	return c
}

func fileSize(t *testing.T, s *estate.Store) int64 {
	t.Helper()
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	return fi.Size()
}

func TestARoundTripSurvivesReopen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana"), user("U2", "kai")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("ObserveUsers: %v", err)
	}
	if _, err := s.ObserveChannels([]slack.Channel{channel("C1", "general")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("ObserveChannels: %v", err)
	}
	before := s.Users()
	beforeCh := s.Channels()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := open(t)
	after := s2.Users()
	afterCh := s2.Channels()

	if len(after) != len(before) || len(afterCh) != len(beforeCh) {
		t.Fatalf("reopened fold sizes differ: users %d != %d, channels %d != %d",
			len(after), len(before), len(afterCh), len(beforeCh))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			t.Fatalf("user %s lost on reopen", id)
		}
		if got.Props != want.Props || !got.FirstSeen.Equal(want.FirstSeen) || !got.LastChanged.Equal(want.LastChanged) {
			t.Fatalf("user %s differs on reopen: got %+v want %+v", id, got, want)
		}
	}
	if afterCh["C1"].Props != beforeCh["C1"].Props {
		t.Fatalf("channel differs on reopen: got %+v want %+v", afterCh["C1"].Props, beforeCh["C1"].Props)
	}
}

func TestAnUnchangedDirectoryAppendsNothing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	users := []slack.User{user("U1", "dana"), user("U2", "kai")}
	if _, err := s.ObserveUsers(users, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	size := fileSize(t, s)

	res, err := s.ObserveUsers(users, true, estate.SourceSweep, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if res.Appended != 0 {
		t.Fatalf("unchanged directory appended %d events", res.Appended)
	}
	if got := fileSize(t, s); got != size {
		t.Fatalf("file grew from %d to %d on an unchanged directory", size, got)
	}
}

func TestACompleteObservationTombstonesTheMissing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana"), user("U2", "kai")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}

	later := base.Add(24 * time.Hour)
	res, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, later)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if res.AbsenceAborted {
		t.Fatalf("single absence should not trip the guard")
	}

	rec, ok := s.User("U2")
	if !ok || rec.Gone == nil {
		t.Fatalf("U2 should be tombstoned, got %+v", rec)
	}
	if rec.Gone.Reason != estate.ReasonAbsent {
		t.Fatalf("reason = %q, want %q", rec.Gone.Reason, estate.ReasonAbsent)
	}
	if !rec.Gone.At.Equal(later) {
		t.Fatalf("tombstone at %v, want %v", rec.Gone.At, later)
	}
	if !rec.Gone.NotBefore.Equal(base) {
		t.Fatalf("notBefore %v, want the last confirmation %v", rec.Gone.NotBefore, base)
	}
	if rec.Props.Name != "kai" {
		t.Fatalf("tombstone lost the last-known record: %+v", rec.Props)
	}
}

func TestAPartialObservationNeverTombstones(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana"), user("U2", "kai")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, false, estate.SourceTraffic, base.Add(time.Hour)); err != nil {
		t.Fatalf("partial observe: %v", err)
	}
	if rec, _ := s.User("U2"); rec.Gone != nil {
		t.Fatalf("partial sight tombstoned U2: %+v", rec.Gone)
	}
}

func TestADeactivatedUserIsATombstoneWithItsLastRecord(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}

	gone := user("U1", "dana")
	gone.Deleted = true
	later := base.Add(24 * time.Hour)
	if _, err := s.ObserveUsers([]slack.User{gone}, true, estate.SourceSweep, later); err != nil {
		t.Fatalf("observe: %v", err)
	}

	rec, _ := s.User("U1")
	if rec.Gone == nil || rec.Gone.Reason != estate.ReasonDeactivated {
		t.Fatalf("want deactivated tombstone, got %+v", rec.Gone)
	}
	if !rec.Props.Deleted {
		t.Fatalf("last record should carry deleted=true: %+v", rec.Props)
	}
}

func TestARevivalOpensANewInterval(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	gone := user("U1", "dana")
	gone.Deleted = true
	t1 := base.Add(24 * time.Hour)
	if _, err := s.ObserveUsers([]slack.User{gone}, true, estate.SourceSweep, t1); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	t2 := base.Add(48 * time.Hour)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, t2); err != nil {
		t.Fatalf("revive: %v", err)
	}

	rec, _ := s.User("U1")
	if rec.Gone != nil {
		t.Fatalf("revived user still tombstoned: %+v", rec.Gone)
	}
	if !rec.FirstSeen.Equal(t2) {
		t.Fatalf("revival should open a new interval at %v, got %v", t2, rec.FirstSeen)
	}
	if len(rec.Prior) != 1 || !rec.Prior[0].From.Equal(base) || !rec.Prior[0].To.Equal(t1) {
		t.Fatalf("prior interval wrong: %+v", rec.Prior)
	}
}

func TestAnArchiveIsAChangeNotATombstone(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveChannels([]slack.Channel{channel("C1", "general")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	archived := channel("C1", "general")
	archived.IsArchived = true
	if _, err := s.ObserveChannels([]slack.Channel{archived}, true, estate.SourceSweep, base.Add(time.Hour)); err != nil {
		t.Fatalf("archive: %v", err)
	}

	rec, _ := s.Channel("C1")
	if rec.Gone != nil {
		t.Fatalf("archive produced a tombstone: %+v", rec.Gone)
	}
	if !rec.Props.IsArchived {
		t.Fatalf("archive not recorded: %+v", rec.Props)
	}
}

func TestTheMassTombstoneGuardRefusesADegradedListing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	var users []slack.User
	for _, id := range []string{"U1", "U2", "U3", "U4", "U5", "U6", "U7", "U8", "U9", "U10"} {
		users = append(users, user(id, strings.ToLower(id)))
	}
	if _, err := s.ObserveUsers(users, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := s.ObserveUsers(users[:2], true, estate.SourceSweep, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("degraded observe: %v", err)
	}
	if !res.AbsenceAborted {
		t.Fatalf("guard did not trip on 8 of 10 proposed absences")
	}
	if res.ProposedAbsent != 8 {
		t.Fatalf("proposed = %d, want 8", res.ProposedAbsent)
	}
	for _, id := range []string{"U3", "U10"} {
		if rec, _ := s.User(id); rec.Gone != nil {
			t.Fatalf("guard tripped but %s was tombstoned anyway", id)
		}
	}
}

func TestASingleAbsenceStillRecordsInASmallWorkspace(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana"), user("U2", "kai"), user("U3", "mo")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 1 of 3 is over twenty percent, but a single absence must always
	// record or small workspaces could never tombstone a real departure.
	res, err := s.ObserveUsers([]slack.User{user("U1", "dana"), user("U2", "kai")}, true, estate.SourceSweep, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if res.AbsenceAborted {
		t.Fatalf("guard tripped on a single absence")
	}
	if rec, _ := s.User("U3"); rec.Gone == nil {
		t.Fatalf("single departure not tombstoned")
	}
}

func TestATornTrailingLineIsSkippedAndTruncated(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	size := fileSize(t, s)
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open for tearing: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"at":"2026-08-18T`); err != nil {
		t.Fatalf("tear: %v", err)
	}
	f.Close()

	s2 := open(t)
	if _, ok := s2.User("U1"); !ok {
		t.Fatalf("fold lost U1 to a torn tail")
	}
	if got := fileSize(t, s2); got != size {
		t.Fatalf("torn tail not truncated: size %d, want %d", got, size)
	}
	if _, err := s2.ObserveUsers([]slack.User{user("U2", "kai")}, false, estate.SourceTraffic, base.Add(time.Hour)); err != nil {
		t.Fatalf("append after truncation: %v", err)
	}
}

func TestInteriorGarbageIsAnError(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	path := s.Path()
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open for damage: %v", err)
	}
	f.WriteString("not json at all\n")
	f.WriteString(`{"v":1,"at":"2026-08-18T12:00:00Z","src":"sweep","kind":"sweep","users":{"complete":true}}` + "\n")
	f.Close()

	if _, err := estate.Open(team); err == nil {
		t.Fatalf("interior garbage did not error")
	}
}

func TestUnknownEventKindsAreIgnoredButKept(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	path := s.Path()
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open for future event: %v", err)
	}
	f.WriteString(`{"v":9,"at":"2026-08-18T12:00:00Z","src":"traffic","kind":"encounter","entity":"user","id":"U1"}` + "\n")
	f.Close()
	damaged, _ := os.Stat(path)

	s2 := open(t)
	rec, ok := s2.User("U1")
	if !ok || rec.Gone != nil {
		t.Fatalf("unknown kind disturbed the fold: %+v", rec)
	}
	if got := fileSize(t, s2); got != damaged.Size() {
		t.Fatalf("unknown kind was not preserved: size %d, want %d", got, damaged.Size())
	}
}

func TestFoldAsOfReconstructsThePast(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	renamed := user("U1", "dana")
	renamed.Profile.DisplayName = "Dana Okafor-Reyes"
	t2 := base.Add(24 * time.Hour)
	if _, err := s.ObserveUsers([]slack.User{renamed}, true, estate.SourceSweep, t2); err != nil {
		t.Fatalf("rename: %v", err)
	}

	then, err := s.FoldAsOf(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("FoldAsOf: %v", err)
	}
	if got := then.Users["U1"].Props.DisplayName; got != "dana" {
		t.Fatalf("as-of name = %q, want the old %q", got, "dana")
	}

	now, err := s.FoldAsOf(t2)
	if err != nil {
		t.Fatalf("FoldAsOf: %v", err)
	}
	if got := now.Users["U1"].Props.DisplayName; got != "Dana Okafor-Reyes" {
		t.Fatalf("as-of name = %q, want the new one", got)
	}
}

func TestRecordSweepAdvancesTheFullSweepWatermark(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if !s.LastFullSweep().IsZero() {
		t.Fatalf("unswept store claims a full sweep")
	}

	if err := s.RecordSweep(estate.SweepReport{
		Users: estate.ClassReport{Complete: true, Count: 2},
	}, base); err != nil {
		t.Fatalf("RecordSweep users: %v", err)
	}
	if !s.LastFullSweep().IsZero() {
		t.Fatalf("one complete class claims a full sweep")
	}

	t2 := base.Add(time.Hour)
	if err := s.RecordSweep(estate.SweepReport{
		Users:    estate.ClassReport{Complete: true, Count: 2},
		Channels: estate.ClassReport{Complete: true, Count: 1, ArchivedIncluded: true},
	}, t2); err != nil {
		t.Fatalf("RecordSweep both: %v", err)
	}
	if got := s.LastFullSweep(); !got.Equal(t2) {
		t.Fatalf("LastFullSweep = %v, want %v", got, t2)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2 := open(t)
	if got := s2.LastFullSweep(); !got.Equal(t2) {
		t.Fatalf("watermark lost on reopen: %v, want %v", got, t2)
	}
}

func TestAChangedLineCarriesItsHonestyBound(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	renamed := user("U1", "dana")
	renamed.Profile.Title = "Staff Eng"
	t2 := base.Add(24 * time.Hour)
	if _, err := s.ObserveUsers([]slack.User{renamed}, true, estate.SourceSweep, t2); err != nil {
		t.Fatalf("rename: %v", err)
	}

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e struct {
			Kind      string    `json:"kind"`
			Changed   []string  `json:"changed"`
			NotBefore time.Time `json:"notBefore"`
			At        time.Time `json:"at"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unparseable line: %v", err)
		}
		if e.Kind != "changed" {
			continue
		}
		found = true
		if len(e.Changed) != 1 || e.Changed[0] != "title" {
			t.Fatalf("changed = %v, want [title]", e.Changed)
		}
		if !e.NotBefore.Equal(base) || !e.At.Equal(t2) {
			t.Fatalf("interval (%v, %v], want (%v, %v]", e.NotBefore, e.At, base, t2)
		}
	}
	if !found {
		t.Fatalf("no changed line in the ledger")
	}
}

func TestFilesArePrivateAndNoStrayFilesRemain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	if _, err := s.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("observe: %v", err)
	}

	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %v, want 0600", fi.Mode().Perm())
	}

	dir := filepath.Dir(s.Path())
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		// estate.lock is the writer-election inode; its existence carries
		// no state.
		if e.Name() != "estate.jsonl" && e.Name() != "estate.lock" {
			t.Fatalf("stray file in ledger dir: %s", e.Name())
		}
	}
}

func TestTheSecondOpenerIsReadOnly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	writer := open(t)
	if writer.ReadOnly() {
		t.Fatalf("first opener did not win the writer lock")
	}
	if _, err := writer.ObserveUsers([]slack.User{user("U1", "dana")}, true, estate.SourceSweep, base); err != nil {
		t.Fatalf("writer observe: %v", err)
	}

	reader, err := estate.Open(team)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer reader.Close()
	if !reader.ReadOnly() {
		t.Fatalf("second opener won a lock the first still holds")
	}

	// Reads serve the fold as replayed at open; writes refuse.
	if _, ok := reader.User("U1"); !ok {
		t.Fatalf("read-only store lost the fold")
	}
	if _, err := reader.ObserveUsers([]slack.User{user("U2", "kai")}, false, estate.SourceTraffic, base); err != estate.ErrReadOnly {
		t.Fatalf("read-only observe returned %v, want ErrReadOnly", err)
	}
	if err := reader.RecordSweep(estate.SweepReport{}, base); err != estate.ErrReadOnly {
		t.Fatalf("read-only sweep record returned %v, want ErrReadOnly", err)
	}
}

func TestTheLockIsReleasedOnClose(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	first, err := estate.Open(team)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := open(t)
	if second.ReadOnly() {
		t.Fatalf("lock not released by close")
	}
}

func TestSimilarTeamIDsDoNotCollide(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	a, err := estate.Open("acme/eng")
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	defer a.Close()
	b, err := estate.Open("acme-eng")
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	defer b.Close()

	if a.Path() == b.Path() {
		t.Fatalf("colliding ledger paths: %s", a.Path())
	}
}

func TestConcurrentObserveAndReadIsSafe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := open(t)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			u := user("U1", "dana")
			if i%2 == 1 {
				u.Profile.Title = "flipped"
			}
			if _, err := s.ObserveUsers([]slack.User{u}, false, estate.SourceTraffic, base.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Errorf("observe: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.Users()
			_, _ = s.User("U1")
			_ = s.Stats()
			_ = s.LastFullSweep()
		}
	}()
	wg.Wait()
}
