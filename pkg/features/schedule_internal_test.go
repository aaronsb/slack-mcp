package features

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("zone %s: %v", name, err)
	}
	return loc
}

// The accepted forms resolve to the same instant; the explicit ones need
// no zone at all.
func TestParseAtAcceptedForms(t *testing.T) {
	now := time.Unix(1799900000, 0)
	denver := mustZone(t, "America/Denver")
	cases := map[string]int64{
		"1800000000":                1800000000,
		"2027-01-15T09:00:00-05:00": 1800021600,
		"2027-01-15T14:00:00Z":      1800021600,
		// 07:00 MST is 14:00 UTC.
		"2027-01-15T07:00":    1800021600,
		"2027-01-15 07:00:00": 1800021600,
	}
	for in, want := range cases {
		got, err := parseAt(in, now, denver, denver)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got.Unix() != want {
			t.Fatalf("%s resolved to %d, want %d", in, got.Unix(), want)
		}
	}
	if got, err := parseAt(atString(float64(1800000000)), now, nil, time.UTC); err != nil || got.Unix() != 1800000000 {
		t.Fatalf("JSON number: %v %v", got, err)
	}
}

func TestParseAtRefusesOutOfBoundsAndNonsense(t *testing.T) {
	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	for _, in := range []string{
		"2027-01-15T11:00:00Z",                             // past
		"2027-01-15T12:01:00Z",                             // inside the 2-minute floor
		now.Add(121 * 24 * time.Hour).Format(time.RFC3339), // past 120 days
		"soon", "", "tomorrow at 9", "2027-13-01T09:00",
	} {
		if _, err := parseAt(in, now, time.UTC, time.UTC); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
	if _, err := parseAt("2027-01-15T12:03:00Z", now, time.UTC, time.UTC); err != nil {
		t.Fatalf("3 minutes out refused: %v", err)
	}
	_, err := parseAt("soon", now, time.UTC, time.UTC)
	if !strings.Contains(err.Error(), "RFC 3339") || !strings.Contains(err.Error(), "Unix seconds") {
		t.Fatalf("refusal does not name the accepted forms: %v", err)
	}
}

// A time with no offset is refused when the profile and system zones
// disagree at that instant, and taken when they agree; an explicit offset
// is taken either way. Denver against a fixed UTC-6 agrees in summer (MDT)
// and not in winter (MST): the DST edge.
func TestParseAtComparesZonesAtTheInstant(t *testing.T) {
	// The zone check runs before the bounds, so the winter refusal is about
	// zones even though January is behind this clock.
	now := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	denver := mustZone(t, "America/Denver")
	fixed := time.FixedZone("UTC-6", -6*3600)

	if got, err := parseAt("2027-07-15T09:00", now, denver, fixed); err != nil || got.UTC().Hour() != 15 {
		t.Fatalf("summer, zones agree: %v %v", got, err)
	}
	_, err := parseAt("2027-01-15T09:00", now, denver, fixed)
	if err == nil {
		t.Fatalf("winter, zones disagree: accepted")
	}
	for _, want := range []string{"America/Denver", "UTC-07:00", "UTC-6", "UTC-06:00", "Give an offset"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q: %v", want, err)
		}
	}
	if _, err := parseAt("2027-07-15T09:00:00-07:00", now, denver, fixed); err != nil {
		t.Fatalf("explicit offset refused: %v", err)
	}
	if _, err := parseAt("2027-01-15T09:00", now, nil, fixed); err == nil || !strings.Contains(err.Error(), "profile zone could not be read") {
		t.Fatalf("unknown profile zone: %v", err)
	}
}

func TestRenderNamesZoneOffsetUTCAndDistance(t *testing.T) {
	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	at := time.Date(2027, 1, 15, 16, 0, 0, 0, time.UTC)
	z := zones{profile: mustZone(t, "America/Denver"), system: time.UTC}
	got := z.render(at, now)
	for _, want := range []string{"Fri 2027-01-15 09:00 MST (UTC-07:00)", "16:00 UTC on this machine", "16:00 UTC", "in 4h 0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render lacks %q: %s", want, got)
		}
	}
	same := zones{profile: mustZone(t, "America/Denver"), system: mustZone(t, "America/Denver")}.render(at, now)
	if strings.Contains(same, "on this machine") {
		t.Fatalf("matching zones named twice: %s", same)
	}
}

// The send time is part of the approval binding; an immediate send's hash
// does not change because the field exists.
func TestContentHashBindsTheSendTime(t *testing.T) {
	base := outbound{Text: "the roadmap"}
	now := base
	at := base
	at.At = time.Unix(1800000000, 0)
	later := base
	later.At = time.Unix(1800003600, 0)
	if contentHash(&now) == contentHash(&at) || contentHash(&at) == contentHash(&later) {
		t.Fatalf("the send time does not change the hash")
	}
	if contentHash(&now) != contentHash(&outbound{Text: "the roadmap"}) {
		t.Fatalf("an immediate send's hash is not stable")
	}
}

func TestOriginOfReportsEditsWithoutBlocking(t *testing.T) {
	now := time.Unix(1799990000, 0)
	z := zones{profile: time.UTC, system: time.UTC}
	blocks := json.RawMessage(`[{"type":"rich_text","elements":[]}]`)
	// Key order in Slack's JSON is not an edit.
	reordered := json.RawMessage(`[{"elements":[],"type":"rich_text"}]`)
	recs := map[string]scheduledRecord{"Dr1": {LastUpdatedTS: "1.000001", DateScheduled: 1800000000, BlocksSHA: blocksSHA(blocks)}}

	d := provider.Draft{ID: "Dr1", LastUpdatedTS: "1.000001", DateScheduled: 1800000000, Blocks: reordered}
	if got := originOf(recs, d, z, now); got != "scheduled here" {
		t.Fatalf("unchanged: %q", got)
	}
	d.LastUpdatedTS, d.DateScheduled = "2.000001", 1800003600
	if got := originOf(recs, d, z, now); !strings.Contains(got, "edited in Slack since: time moved from") || strings.Contains(got, "content changed") {
		t.Fatalf("rescheduled: %q", got)
	}
	d.DateScheduled, d.Blocks = 1800000000, json.RawMessage(`[{"type":"rich_text","elements":[{"type":"rich_text_section"}]}]`)
	if got := originOf(recs, d, z, now); !strings.Contains(got, "content changed") || strings.Contains(got, "time moved") {
		t.Fatalf("content edited: %q", got)
	}
	if got := originOf(recs, provider.Draft{ID: "Dr9"}, z, now); got != "scheduled from Slack" {
		t.Fatalf("unknown draft: %q", got)
	}
}
