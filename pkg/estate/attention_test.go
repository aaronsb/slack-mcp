package estate_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
)

var attnNow = time.Date(2026, 8, 20, 15, 30, 0, 0, time.UTC)

func openAttn(t *testing.T, scope string, now time.Time) *estate.AttentionStore {
	t.Helper()
	s, err := estate.OpenAttention(team, scope, now)
	if err != nil {
		t.Fatalf("OpenAttention: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func day(d string) estate.Encounter {
	return estate.Encounter{User: "U1", Conv: "C1", Day: d}
}

func TestAttentionRoundTripsAcrossReopen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	h := 9
	encs := []estate.Encounter{
		{User: "U1", Conv: "C1", Day: "2026-08-20"},
		{User: "SELF", Conv: "C1", Day: "2026-08-20", Hour: &h},
	}
	n, err := s.Observe(encs, attnNow)
	if err != nil || n != 2 {
		t.Fatalf("Observe = %d, %v; want 2, nil", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := openAttn(t, "self", attnNow)
	st := r.Stats()
	if st.Events != 2 || st.Users != 2 || st.Convs != 1 {
		t.Fatalf("stats after reopen: %+v", st)
	}
	self := r.Encounters("SELF")
	if len(self["C1"]) != 1 || self["C1"][0].Hour == nil || *self["C1"][0].Hour != 9 {
		t.Fatalf("self hour lost across reopen: %+v", self)
	}
}

func TestAttentionDedupsPerBucket(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)

	if n, _ := s.Observe([]estate.Encounter{day("2026-08-20"), day("2026-08-20")}, attnNow); n != 1 {
		t.Fatalf("in-batch duplicate appended %d, want 1", n)
	}
	if n, _ := s.Observe([]estate.Encounter{day("2026-08-20")}, attnNow); n != 0 {
		t.Fatalf("cross-batch duplicate appended %d, want 0", n)
	}
	if n, _ := s.Observe([]estate.Encounter{day("2026-08-21")}, attnNow); n != 1 {
		t.Fatalf("new day appended %d, want 1", n)
	}
}

func TestAttentionSelfHoursAreDistinctBuckets(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	h9, h10 := 9, 10
	encs := []estate.Encounter{
		{User: "SELF", Conv: "C1", Day: "2026-08-20", Hour: &h9},
		{User: "SELF", Conv: "C1", Day: "2026-08-20", Hour: &h10},
		{User: "SELF", Conv: "C1", Day: "2026-08-20", Hour: &h9},
	}
	if n, _ := s.Observe(encs, attnNow); n != 2 {
		t.Fatalf("two distinct hours appended %d, want 2", n)
	}
}

func TestAttentionDropsEncountersOutsideTheWindow(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	old := attnNow.Add(-91 * 24 * time.Hour).Format("2006-01-02")
	if n, _ := s.Observe([]estate.Encounter{day(old)}, attnNow); n != 0 {
		t.Fatalf("expired encounter appended %d, want 0", n)
	}
}

func TestAttentionCompactsExpiredEventsAtBoot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	if _, err := s.Observe([]estate.Encounter{day("2026-08-20"), day("2026-08-19")}, attnNow); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	path := s.Path()
	before, _ := os.Stat(path)
	s.Close()

	// Ninety-five days later both events are stale; the boot pass drops
	// them and rewrites the file smaller.
	later := attnNow.Add(95 * 24 * time.Hour)
	r := openAttn(t, "self", later)
	if st := r.Stats(); st.Events != 0 {
		t.Fatalf("expired events survived compaction: %+v", st)
	}
	after, _ := os.Stat(path)
	if after.Size() >= before.Size() {
		t.Fatalf("compaction did not shrink the file: %d -> %d", before.Size(), after.Size())
	}
}

func TestAttentionBackstopKeepsTheNewest(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	// 50,010 distinct encounters inside the window: 600 users x ~84 days.
	var encs []estate.Encounter
	for u := 0; len(encs) < 50_010; u++ {
		for d := 0; d < 84 && len(encs) < 50_010; d++ {
			encs = append(encs, estate.Encounter{
				User: fmt.Sprintf("U%04d", u),
				Conv: "C1",
				Day:  attnNow.AddDate(0, 0, -d).Format("2006-01-02"),
			})
		}
	}
	if n, err := s.Observe(encs, attnNow); err != nil || n != 50_010 {
		t.Fatalf("Observe = %d, %v", n, err)
	}
	s.Close()

	r := openAttn(t, "self", attnNow)
	if st := r.Stats(); st.Events != 50_000 {
		t.Fatalf("backstop kept %d events, want 50000", st.Events)
	}
}

func TestAttentionSecondOpenerIsReadOnly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	w := openAttn(t, "self", attnNow)
	if _, err := w.Observe([]estate.Encounter{day("2026-08-20")}, attnNow); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	r, err := estate.OpenAttention(team, "self", attnNow)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer r.Close()
	if !r.ReadOnly() {
		t.Fatalf("second opener won the writer election")
	}
	if st := r.Stats(); st.Events != 1 {
		t.Fatalf("read-only fold missing events: %+v", st)
	}
	if _, err := r.Observe([]estate.Encounter{day("2026-08-21")}, attnNow); err != estate.ErrReadOnly {
		t.Fatalf("read-only Observe = %v, want ErrReadOnly", err)
	}
}

func TestAttentionTornTailIsDroppedByTheWriter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := openAttn(t, "self", attnNow)
	if _, err := s.Observe([]estate.Encounter{day("2026-08-20")}, attnNow); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	path := s.Path()
	s.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open for tear: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"kind":"enc`); err != nil {
		t.Fatalf("tear: %v", err)
	}
	f.Close()

	r := openAttn(t, "self", attnNow)
	if st := r.Stats(); st.Events != 1 {
		t.Fatalf("torn tail corrupted the fold: %+v", st)
	}
	if n, err := r.Observe([]estate.Encounter{day("2026-08-21")}, attnNow); err != nil || n != 1 {
		t.Fatalf("append after tear: %d, %v", n, err)
	}
}

func TestAttentionScopesDoNotMix(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := openAttn(t, "operator-a", attnNow)
	if _, err := a.Observe([]estate.Encounter{day("2026-08-20")}, attnNow); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	b := openAttn(t, "operator-b", attnNow)
	if st := b.Stats(); st.Events != 0 {
		t.Fatalf("scope b sees scope a's events: %+v", st)
	}
	if b.ReadOnly() {
		t.Fatalf("different scope lost an unrelated writer election")
	}
}
