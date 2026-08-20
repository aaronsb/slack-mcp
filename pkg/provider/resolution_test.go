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
		"U1": mk("U1", "chanceyc", "Clayton Chancey", "Clayton", "Head of AI Strategy", false),
		"U2": mk("U2", "cpeters", "Clay Peterson", "Clay", "Design", false),
		"U3": mk("U3", "dana", "Dana Okafor", "Dana O.", "", false),
		"U4": mk("U4", "ghost", "Gone Person", "", "", true),
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
	r := ladderProvider().ResolvePerson("U3")
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
	departed := slack.User{ID: "U9", Name: "leaver", RealName: "Lee Ver"}
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
