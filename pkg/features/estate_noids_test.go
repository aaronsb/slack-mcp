package features_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// idShaped matches the shape of Slack internal IDs (U0123ABCD, C0123ABCD).
var idShaped = regexp.MustCompile(`\b[UCDGWT][A-Z0-9]{8,}\b`)

func noIDsFixture(t *testing.T) func(params map[string]any) string {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U0123ABCD", Name: "schen", RealName: "Sarah Chen"},
		slack.User{ID: "U0456EFGH", Name: "mlopez", RealName: "Maria Lopez"},
	)
	var eng slack.Channel
	eng.ID, eng.Name, eng.IsChannel, eng.IsMember = "C0123ABCD", "eng", true, true
	srv.SeedChannels(eng)
	ap := bootedProvider(t, srv)

	for _, d := range []int{1, 2, 3} {
		seedActivity(ap, "C0123ABCD", "U0123ABCD", d)
		seedActivity(ap, "C0123ABCD", "U0456EFGH", d)
		seedActivity(ap, "C0999ZZZZ", "U0123ABCD", d) // conversation no map names
		seedActivity(ap, "C0999ZZZZ", "U0777EXTL", d) // Slack Connect external
	}
	return func(params map[string]any) string {
		return estateViewOut(t, ap, params)
	}
}

func TestAboutAndPersonNeverPrintSlackIDs(t *testing.T) {
	run := noIDsFixture(t)
	for _, params := range []map[string]any{
		{"view": "person", "person": "schen"},
		{"view": "about", "person": "schen"},
		{"view": "about", "person": "schen", "deeper": true},
	} {
		out := run(params)
		if m := idShaped.FindString(out); m != "" {
			t.Errorf("%v prints Slack ID %q:\n%s", params, m, out)
		}
		if !strings.Contains(out, "unnamed conversation") {
			t.Errorf("%v: unlabelled conversation not rendered by kind:\n%s", params, out)
		}
	}
}

func TestAboutDeeperOnUnsweptEstateDoesNotFailOnItsOwnIDs(t *testing.T) {
	run := noIDsFixture(t)
	out := run(map[string]any{"view": "about", "person": "schen", "deeper": true})
	if strings.Contains(out, "Could not resolve") {
		t.Fatalf("deeper hop failed on an ID the server produced:\n%s", out)
	}
	if !strings.Contains(out, "Second hop") {
		t.Fatalf("second hop did not run:\n%s", out)
	}
	if !strings.Contains(out, "external user") {
		t.Fatalf("external counterpart not labelled by kind:\n%s", out)
	}
}

func TestAboutReadingPlanOmitsStepsWithoutAName(t *testing.T) {
	run := noIDsFixture(t)
	out := run(map[string]any{"view": "about", "person": "schen"})
	if !strings.Contains(out, "messages target='#eng'") {
		t.Fatalf("named surface missing from plan:\n%s", out)
	}
	if strings.Contains(out, "target='unnamed") || strings.Contains(out, "target='DM") {
		t.Fatalf("plan names an anonymised conversation:\n%s", out)
	}
	if !strings.Contains(out, "person='@mlopez'") {
		t.Fatalf("top counterpart not named by handle:\n%s", out)
	}
}
