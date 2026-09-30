package provider

// The ladder is pure map-and-fold logic, so it tests directly against a
// constructed provider — no fake Slack host needed.

import (
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

func ladderProvider() *ApiProvider {
	mk := func(id, handle, real, display, title string, deleted bool) slack.User {
		u := slack.User{ID: id, Name: handle, RealName: real, Deleted: deleted}
		u.Profile.DisplayName = display
		u.Profile.Title = title
		return u
	}
	return &ApiProvider{users: map[string]slack.User{
		"U01AAAAA1": mk("U01AAAAA1", "chanceyc", "Clayton Chancey", "Clayton", "Head of AI Strategy", false),
		"U01AAAAA2": mk("U01AAAAA2", "cpeters", "Clay Peterson", "Clay", "Design", false),
		"U01AAAAA3": mk("U01AAAAA3", "dana", "Dana Okafor", "Dana O.", "", false),
		"U01AAAAA4": mk("U01AAAAA4", "ghost", "Gone Person", "", "", true),
	}}
}

func TestAnExactHandleResolvesOutright(t *testing.T) {
	r := ladderProvider().ResolvePerson("@chanceyc")
	if !r.Resolved || r.Handle != "chanceyc" || r.Via != "exact-handle" {
		t.Fatalf("got %+v", r)
	}
}

func TestAUniqueRealNameResolves(t *testing.T) {
	r := ladderProvider().ResolvePerson("Clayton Chancey")
	if !r.Resolved || r.Handle != "chanceyc" || r.Via != "unique-name" {
		t.Fatalf("got %+v", r)
	}
}

func TestAUniqueFragmentResolvesAndSaysHow(t *testing.T) {
	r := ladderProvider().ResolvePerson("okafor")
	if !r.Resolved || r.Handle != "dana" || r.Via != "unique-match" {
		t.Fatalf("got %+v", r)
	}
}

func TestAUserIDResolvesDirectly(t *testing.T) {
	r := ladderProvider().ResolvePerson("U01AAAAA3")
	if !r.Resolved || r.Handle != "dana" || r.Via != "user-id" {
		t.Fatalf("got %+v", r)
	}
}

func TestAnExactDisplayNameBeatsASharedFragment(t *testing.T) {
	// "clay" is a fragment of both Clays, but it is exactly one person's
	// display name, and the exact rung outranks the fragment rung.
	r := ladderProvider().ResolvePerson("clay")
	if !r.Resolved || r.Handle != "cpeters" || r.Via != "unique-name" {
		t.Fatalf("got %+v", r)
	}
}

func TestAnAmbiguousFragmentReturnsRankedCandidatesWithEvidence(t *testing.T) {
	r := ladderProvider().ResolvePerson("cl")
	if r.Resolved {
		t.Fatalf("ambiguous input resolved to %+v", r)
	}
	if r.Reason != "ambiguous" || len(r.Candidates) != 2 {
		t.Fatalf("got %+v", r)
	}
	// Both are prefix matches; rank falls back to handle order.
	if r.Candidates[0].Handle != "chanceyc" || r.Candidates[0].Title != "Head of AI Strategy" {
		t.Fatalf("candidates carry no evidence: %+v", r.Candidates)
	}
}

func TestADeletedUserNeverResolvesOnTheLadder(t *testing.T) {
	r := ladderProvider().ResolvePerson("ghost")
	if r.Resolved {
		t.Fatalf("deleted user resolved: %+v", r)
	}
}

func TestAMissDistinguishesTombstonedFromUnswept(t *testing.T) {
	ap := ladderProvider()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, err := estate.Open("T0LADDER")
	if err != nil {
		t.Fatalf("open estate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ap.estate = st

	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	departed := slack.User{ID: "U01AAAAA9", Name: "leaver", RealName: "Lee Ver"}
	if _, err := st.ObserveUsers([]slack.User{departed}, true, estate.SourceSweep, at); err != nil {
		t.Fatalf("observe: %v", err)
	}
	gone := departed
	gone.Deleted = true
	if _, err := st.ObserveUsers([]slack.User{gone}, true, estate.SourceSweep, at.Add(24*time.Hour)); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	r := ap.ResolvePerson("leaver")
	if r.Reason != "tombstoned" || len(r.Candidates) != 1 {
		t.Fatalf("got %+v", r)
	}
	c := r.Candidates[0]
	if !c.Deleted || c.GoneReason != "deactivated" || len(c.GoneBetween) != 2 {
		t.Fatalf("tombstoned candidate carries no dates: %+v", c)
	}

	// No sweep event recorded yet: an unmatched name is unswept, not
	// never_seen.
	if r := ap.ResolvePerson("zorptangle"); r.Reason != "unswept" {
		t.Fatalf("reason = %q, want unswept", r.Reason)
	}

	if err := st.RecordSweep(estate.SweepReport{
		Users:    estate.ClassReport{Complete: true, Count: 1},
		Channels: estate.ClassReport{Complete: true, Count: 0},
	}, at.Add(48*time.Hour)); err != nil {
		t.Fatalf("record sweep: %v", err)
	}
	if r := ap.ResolvePerson("zorptangle"); r.Reason != "never_seen" {
		t.Fatalf("reason = %q, want never_seen", r.Reason)
	}
}

// An all-caps name must not be misrouted as a user ID and skip the ladder.
func TestAnAllCapsNameIsNotMistakenForAnID(t *testing.T) {
	ap := ladderProvider()
	mk := ap.users["U01AAAAA3"]
	mk.RealName = "Ursula Vance"
	ap.users["U01AAAAA3"] = mk

	r := ap.ResolvePerson("URSULA")
	if !r.Resolved || r.Handle != "dana" {
		t.Fatalf("all-caps name skipped the ladder: %+v", r)
	}
}

// The write policy acts only on an exact handle (plus '@me' and a literal
// user ID); the read policy acts on any resolved rung (ADR-005).
func TestPoliciesSplitReadsFromWrites(t *testing.T) {
	ap := ladderProvider()
	cases := []struct {
		input       string
		read, write bool
	}{
		{"@chanceyc", true, true},        // exact handle
		{"U01AAAAA1", true, true},        // user ID
		{"Clayton Chancey", true, false}, // unique real name
		{"okafor", true, false},          // unique fragment
		{"lay", false, false},            // ambiguous
		{"Gone Person", false, false},    // deactivated
	}
	for _, c := range cases {
		r := ap.ResolvePerson(c.input)
		if got := ReadPolicy.Accepts(r); got != c.read {
			t.Errorf("ReadPolicy.Accepts(%q via %q) = %v, want %v", c.input, r.Via, got, c.read)
		}
		if got := WritePolicy.Accepts(r); got != c.write {
			t.Errorf("WritePolicy.Accepts(%q via %q) = %v, want %v", c.input, r.Via, got, c.write)
		}
	}
}

// Channel IDs carry the same bounds as user IDs: an all-caps word is a name.
func TestLooksLikeChannelIDRejectsAllCapsWords(t *testing.T) {
	for s, want := range map[string]bool{
		"C024BE91L": true, "D0123ABCDE": true, "G01ABCDEF2": true,
		"DAVE": false, "GARY": false, "CHARLOTTE": false, "C1": false, "c024be91l": false,
	} {
		if got := LooksLikeChannelID(s); got != want {
			t.Errorf("LooksLikeChannelID(%q) = %v, want %v", s, got, want)
		}
	}
}

// The channel-name index holds channel names only: a DM whose user shares a
// channel's name never shadows the channel.
func TestChannelNamesHoldNoDMKeys(t *testing.T) {
	ap := ladderProvider()
	ap.channels = map[string]slack.Channel{}
	ap.channelNames = map[string]string{}
	ap.dmMap = map[string]string{}
	var ch, dm slack.Channel
	ch.ID, ch.Name = "C1", "dana"
	dm.ID, dm.IsIM, dm.User = "D77", true, "U01AAAAA3"
	for _, c := range []slack.Channel{ch, dm} {
		ap.channels[c.ID] = c
		ap.indexChannel(c)
		ap.indexChannelDM(c)
	}
	if ch, ok := ap.LookupChannel("dana"); !ok || ch.ID != "C1" {
		t.Fatalf("LookupChannel(dana) = %q, %v; want the channel", ch.ID, ok)
	}
	if ap.dmMap["U01AAAAA3"] != "D77" {
		t.Errorf("the DM is no longer reachable by user through dmMap")
	}
	if _, ok := ap.LookupChannelName("D77"); ok {
		t.Errorf("a name-only lookup matched a DM by its ID")
	}
}

// A Policy nobody chose, or one missing from the table, accepts nothing.
func TestPolicyFailsClosed(t *testing.T) {
	r := ladderProvider().ResolvePerson("@chanceyc")
	if !r.Resolved || r.Via != "exact-handle" {
		t.Fatalf("fixture: %+v", r)
	}
	var unset Policy
	if unset.Accepts(r) {
		t.Errorf("the zero Policy accepted an exact handle")
	}
	if Policy(99).Accepts(r) {
		t.Errorf("an unknown Policy accepted an exact handle")
	}
}

// Reads reach a deactivated person by exact handle or user ID; writes never.
func TestReadsReachTheDeactivatedByExactHandle(t *testing.T) {
	ap := ladderProvider()
	for _, in := range []string{"@ghost", "ghost", "U01AAAAA4"} {
		r := ap.ResolvePersonFor(in, ReadPolicy)
		if !ReadPolicy.Accepts(r) || r.UserID != "U01AAAAA4" {
			t.Errorf("read of %q: %+v", in, r)
		}
		if w := ap.ResolvePersonFor(in, WritePolicy); WritePolicy.Accepts(w) {
			t.Errorf("write accepted deactivated %q: %+v", in, w)
		}
	}
	if r := ap.ResolvePersonFor("Gone Person", ReadPolicy); r.Resolved {
		t.Errorf("a deactivated real name resolved on a read: %+v", r)
	}
}
