package features

import (
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// A miss whose candidates are all deactivated says so and offers no retry
// target — following one would fail again. A tombstone with no handle is
// named, never rendered as a bare '@'.
func TestUnresolvedPersonNeverOffersTheDeactivated(t *testing.T) {
	res := provider.PersonResolution{
		Input:  "@dana",
		Reason: "tombstoned",
		Candidates: []provider.PersonCandidate{
			{ID: "U100DANA01", Handle: "dana", DisplayName: "Dana Okafor", Deleted: true},
			{ID: "U100DANA02", DisplayName: "Dana Whitfield", Deleted: true},
		},
	}
	e := unresolvedPerson("@dana", res)
	if !strings.Contains(e.Message, "deactivated") || !strings.Contains(e.Message, "Dana Whitfield") {
		t.Errorf("want the deactivated candidates named, got:\n%s", e.Message)
	}
	if strings.Contains(e.Guidance, "to='@") {
		t.Errorf("a deactivated candidate was offered as a retry: %s", e.Guidance)
	}
	for _, line := range strings.Split(e.Message, "\n") {
		if strings.TrimSpace(line) == "@" || strings.Contains(line, "@ ") || strings.Contains(line, "(@)") {
			t.Errorf("empty handle rendered as '@': %q", line)
		}
	}
	if strings.Contains(e.Message+e.Guidance, "U100") {
		t.Errorf("an ID leaked:\n%s\n%s", e.Message, e.Guidance)
	}
}

// With a living candidate among the deactivated, the retry names the living.
func TestUnresolvedPersonRetriesTheLiving(t *testing.T) {
	res := provider.PersonResolution{
		Reason: "ambiguous",
		Candidates: []provider.PersonCandidate{
			{Handle: "adana", DisplayName: "A Dana", Deleted: true},
			{Handle: "bdana", DisplayName: "B Dana"},
		},
	}
	e := unresolvedPerson("dana", res)
	if !strings.Contains(e.Guidance, "to='@bdana'") {
		t.Errorf("want the living candidate as the retry, got: %s", e.Guidance)
	}
}
