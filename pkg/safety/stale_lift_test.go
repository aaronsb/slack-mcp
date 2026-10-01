package safety

import (
	"errors"
	"os"
	"testing"
	"time"
)

func isolateXDG(t *testing.T) {
	t.Helper()
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
}

func liftOf(k Key) Request {
	return Request{Cases: []Case{CaseLift}, Tool: "say", Destination: channel(k.ID, k.Name), Lift: []Key{k}}
}

var keyA = Conversation("C1", "#a")

// The reviewer's sequence: #a quarantined, lift P1 issued, the page clears
// #a, a new block quarantines #a again, and the next refusal must not hand
// back P1, whose approval would lift nothing. It issues a new request and
// supersedes P1.
func TestReissuedLiftAfterAClearIsNew(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Soft) // soft: the lock does not engage at two
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	block(t, w, channel("C1", "#a")) // soft quarantines from the second
	p1, _, err := w.IssueLift(liftOf(keyA), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(keyA, ByWeb, "", now); err != nil {
		t.Fatal(err)
	}
	block(t, w, channel("C1", "#a"))

	p2, created, err := w.IssueLift(liftOf(keyA), now)
	if err != nil {
		t.Fatal(err)
	}
	if !created || p2.ID == p1.ID {
		t.Fatalf("the stale request %s was handed back for a quarantine it never saw", p1.ID)
	}
	if _, err := w.Approve(p1, now); !errors.Is(err, ErrNotPending) {
		t.Fatalf("the superseded request is still answerable: %v", err)
	}
	if _, err := w.Approve(p2, now); err != nil {
		t.Fatal(err)
	}
	if _, q := w.Quarantine.State().IsQuarantined("C1"); q {
		t.Fatal("approving the new request did not lift #a")
	}
}

// An unchanged quarantine reuses its pending lift, as before.
func TestReissuedLiftWithoutAClearIsReused(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	p1, _, _ := w.IssueLift(liftOf(keyA), now)
	p2, created, _ := w.IssueLift(liftOf(keyA), now)
	if created || p2.ID != p1.ID {
		t.Fatalf("an unchanged lift was issued again: %s then %s", p1.ID, p2.ID)
	}
}

// Approving a lift whose key was cleared after it was issued skips that key
// and says so, whichever process approves: the CLI opens its own fold.
func TestApprovingAStaleLiftSkipsAndSaysSo(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	strikes, _, _ := w.IssueLift(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey}}, now)
	conv, _, _ := w.IssueLift(liftOf(keyA), now)
	if _, err := w.Quarantine.Clear(StrikesKey, ByWeb, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(keyA, ByWeb, "", now); err != nil {
		t.Fatal(err)
	}
	block(t, w, channel("C1", "#a")) // a strike and #a again

	cli, err := OpenDir(w.Dir, testOrg, Strict) // another process's fold
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Request{strikes, conv} {
		got, err := cli.Approve(r, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Skipped) != 1 {
			t.Fatalf("approval of %s skipped %v", r.ID, got.Skipped)
		}
	}
	st := cli.Quarantine.State()
	if st.Strikes != 1 {
		t.Fatalf("a stale strikes lift wiped a later strike: strikes=%d", st.Strikes)
	}
	if _, ok := st.IsQuarantined("C1"); !ok {
		t.Fatal("a stale lift cleared a later quarantine")
	}
}

// A lift nothing cleared since it was issued still clears when approved,
// from another process too.
func TestApprovingAFreshLiftStillClears(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	r, _, _ := w.IssueLift(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey, keyA}}, now)
	cli, err := OpenDir(w.Dir, testOrg, Strict)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cli.Approve(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if st := cli.Quarantine.State(); st.Strikes != 0 || len(st.Conversations) != 0 || len(got.Skipped) != 0 {
		t.Fatalf("fresh lift left %+v, skipped %v", st, got.Skipped)
	}
}

// When the file no longer holds the bytes the request was issued against
// (edited, replaced), places do not compare: the approval lifts nothing
// rather than guess, and says so.
func TestApprovingALiftAfterTheFileWasRewrittenSkips(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	r, _, _ := w.IssueLift(liftOf(keyA), now)
	raw, err := os.ReadFile(qpath(w))
	if err != nil {
		t.Fatal(err)
	}
	// Same entries, rewritten with a leading blank line: same fold, new bytes.
	if err := os.WriteFile(qpath(w), append([]byte("\n"), raw...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := w.Approve(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) != 1 {
		t.Fatalf("an unorderable lift was applied: skipped %v", got.Skipped)
	}
	if _, ok := w.Quarantine.State().IsQuarantined("C1"); !ok {
		t.Fatal("an unorderable lift cleared the quarantine")
	}
}

// The page clears #a and a new block closes it again between Approve's
// look at the file and its write: the approval must not wipe that block.
func TestApproveChecksAndClearsUnderOneLock(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Soft)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	block(t, w, channel("C1", "#a"))
	r, _, _ := w.IssueLift(liftOf(keyA), now)
	page, err := OpenDir(w.Dir, testOrg, Soft) // the server's page, another fold
	if err != nil {
		t.Fatal(err)
	}
	approveGapHook = func() {
		approveGapHook = nil
		if _, err := page.Quarantine.Clear(keyA, ByWeb, "", now); err != nil {
			t.Fatal(err)
		}
		block(t, page, channel("C1", "#a"))
	}
	t.Cleanup(func() { approveGapHook = nil })
	got, err := w.Approve(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Quarantine.State().IsQuarantined("C1"); !ok {
		t.Fatal("the approval wiped a block that landed after the page's clear")
	}
	if len(got.Skipped) != 1 {
		t.Fatalf("skipped %v, want #a", got.Skipped)
	}
}

// A place with a negative line (a corrupted pending entry) is not
// comparable; it must not panic the gate or the CLI.
func TestNegativePlaceLineIsUnordered(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	block(t, w, channel("C1", "#a"))
	st := w.Quarantine.State()
	if _, ordered := st.ClearedSince(keyA, Place{Line: -1, Prefix: st.Place().Prefix}); ordered {
		t.Fatal("a negative line compared")
	}
}

// A lift issued with no place (written by an older binary, or while the
// file was unreadable) cannot be ordered: approving it lifts nothing and
// names every key.
func TestApprovingALiftWithoutAPlaceSkipsEveryKey(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	r, _, err := w.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey, keyA}}, now)
	if err != nil || r.At != nil {
		t.Fatalf("request %+v err %v", r, err)
	}
	got, err := w.Approve(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) != 2 {
		t.Fatalf("skipped %v, want both keys", got.Skipped)
	}
	if st := w.Quarantine.State(); st.Strikes != 1 || len(st.Conversations) != 1 {
		t.Fatalf("a placeless lift cleared something: %+v", st)
	}
}

// ClearKeysAt applies nothing when the file moved past the place the page
// was read at, and clears every target when it did not.
func TestClearKeysAtRefusesAMovedFile(t *testing.T) {
	isolateXDG(t)
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	at := w.Quarantine.State().Place()
	block(t, w, channel("C2", "#b"))
	moved, _, err := w.Quarantine.ClearKeysAt([]Key{keyA}, ByWeb, at, now)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("cleared against a place the file has moved past")
	}
	if _, ok := w.Quarantine.State().IsQuarantined("C1"); !ok {
		t.Fatal("a moved file was cleared anyway")
	}
	at = w.Quarantine.State().Place()
	moved, cleared, err := w.Quarantine.ClearKeysAt([]Key{keyA, Conversation("C2", "#b")}, ByWeb, at, now)
	if err != nil || moved || len(cleared) != 2 || !cleared[0] || !cleared[1] {
		t.Fatalf("moved=%v cleared=%v err=%v", moved, cleared, err)
	}
	if st := w.Quarantine.State(); len(st.Conversations) != 0 {
		t.Fatalf("left %+v", st.Conversations)
	}
}
