package safety

import (
	"testing"
	"time"
)

// A lift request left pending after the page (or the CLI's clear) already
// cleared its key must not, when approved later, wipe what was recorded
// after that clear: a later block's strike, or a later quarantine.
func TestApprovingAStaleLiftKeepsLaterBlocks(t *testing.T) {
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	w := openTest(t, Strict)
	t0 := time.Now()
	block(t, w, channel("C1", "#a"))
	block(t, w, channel("C2", "#b"))
	strikes, _, _ := w.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey}}, t0)
	conv, _, _ := w.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Destination: channel("C1", "#a"), Lift: []Key{Conversation("C1", "#a")}}, t0)

	t1 := t0.Add(time.Second)
	if _, err := w.Quarantine.Clear(StrikesKey, ByWeb, "", t1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(Conversation("C1", "#a"), ByWeb, "", t1); err != nil {
		t.Fatal(err)
	}
	// A later block quarantines #a again and counts a new strike.
	if _, err := w.Quarantine.RecordBlock(Block{Time: t1.Add(time.Second), Destination: channel("C1", "#a"),
		Call: CallRecord{Tool: "say", Destination: "#a"}, Match: MatchRecord{Class: "jwt", Location: "text"}}); err != nil {
		t.Fatal(err)
	}

	t3 := t1.Add(time.Minute)
	if _, err := w.Approve(strikes, t3); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Approve(conv, t3); err != nil {
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if st.Strikes != 1 {
		t.Fatalf("a stale strikes lift wiped a later strike: strikes=%d", st.Strikes)
	}
	if _, ok := st.IsQuarantined("C1"); !ok {
		t.Fatal("a stale lift cleared a later quarantine")
	}
}

// A lift nothing cleared since it was issued still clears when approved.
func TestApprovingAFreshLiftStillClears(t *testing.T) {
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	r, _, _ := w.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey, Conversation("C1", "#a")}}, now)
	if _, err := w.Approve(r, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if st := w.Quarantine.State(); st.Strikes != 0 || len(st.Conversations) != 0 {
		t.Fatalf("fresh lift left %+v", st)
	}
}
