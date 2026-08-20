package features_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

func estateViewOut(t *testing.T, ap *provider.ApiProvider, params map[string]any) string {
	t.Helper()
	params["_provider"] = ap
	res, err := features.EstateViews.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("estate: %v", err)
	}
	return features.FormatResult("estate", res)
}

func tsDaysAgo(days int) string {
	return fmt.Sprintf("%d.000000", time.Now().AddDate(0, 0, -days).Unix())
}

// seedActivity records encounters through the provider path, exactly as a
// render-path tap would.
func seedActivity(ap *provider.ApiProvider, conv string, user string, daysAgo ...int) {
	for _, d := range daysAgo {
		ap.ObserveEncounters(conv, []provider.EncounterSample{{User: user, TS: tsDaysAgo(d)}})
	}
}

func TestPersonViewRendersFootprintAndCounterparts(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	// Sarah active in #eng three days; Aaron (self) co-active the same days.
	for _, d := range []int{1, 2, 3} {
		seedActivity(ap, "C1", "U2", d)
		seedActivity(ap, "C1", "U1", d)
	}
	seedActivity(ap, "C2", "U2", 2)

	out := estateViewOut(t, ap, map[string]any{"view": "person", "person": "schen"})

	if !strings.Contains(out, "person view — Sarah Chen") {
		t.Fatalf("person not resolved:\n%s", out)
	}
	if !strings.Contains(out, "#eng — 3 active days") {
		t.Fatalf("footprint missing or miscounted:\n%s", out)
	}
	if !strings.Contains(out, "Aaron Bockelie — 3 co-active days") {
		t.Fatalf("counterpart join missing:\n%s", out)
	}
	if !strings.Contains(out, "fold executor, 0 Slack calls") {
		t.Fatalf("coverage does not name the executor:\n%s", out)
	}
	if !strings.Contains(out, "as observed by this agent's reading") {
		t.Fatalf("as-observed caveat missing:\n%s", out)
	}
}

func TestPersonViewSelfCarriesHourParallelism(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	// The operator active in two conversations inside the same hour.
	now := fmt.Sprintf("%d.000000", time.Now().Add(-2*time.Hour).Unix())
	ap.ObserveEncounters("C1", []provider.EncounterSample{{User: "U1", TS: now}})
	ap.ObserveEncounters("C2", []provider.EncounterSample{{User: "U1", TS: now}})

	out := estateViewOut(t, ap, map[string]any{"view": "person", "person": "bockeliea"})

	if !strings.Contains(out, "Hour-cadence parallelism") {
		t.Fatalf("self parallelism missing:\n%s", out)
	}
	if !strings.Contains(out, "peak: 2 concurrent") {
		t.Fatalf("peak wrong:\n%s", out)
	}
}

func TestConvergenceFindsTheSharedWindow(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	for _, d := range []int{1, 2, 3} {
		seedActivity(ap, "C1", "U1", d)
		seedActivity(ap, "C1", "U2", d)
	}
	seedActivity(ap, "C2", "U1", 1) // solo activity is not convergence

	out := estateViewOut(t, ap, map[string]any{"view": "convergence", "people": "bockeliea,schen"})

	if !strings.Contains(out, "#eng — co-active 3 day(s)") {
		t.Fatalf("shared window missing:\n%s", out)
	}
	if strings.Contains(out, "#platform — co-active") {
		t.Fatalf("solo activity counted as convergence:\n%s", out)
	}
	if !strings.Contains(out, "Baselines") {
		t.Fatalf("baselines missing:\n%s", out)
	}
}

func TestInitiativesGroupsByCreatorAndReportsAbsence(t *testing.T) {
	srv := slacktest.New(t)
	eng := channelWithCreator("C1", "eng", "U2")
	srv.SeedChannels(eng)
	ap := bootedProvider(t, srv)

	// Activity by Aaron only; the creator (Sarah) is not observed.
	seedActivity(ap, "C1", "U1", 1, 2)

	out := estateViewOut(t, ap, map[string]any{"view": "initiatives"})

	if !strings.Contains(out, "Sarah Chen — 1 channel(s) moving") {
		t.Fatalf("creator grouping missing:\n%s", out)
	}
	if !strings.Contains(out, "creator not observed") {
		t.Fatalf("creator absence not reported:\n%s", out)
	}
	if !strings.Contains(out, "#eng — 2 active days") {
		t.Fatalf("channel movement miscounted:\n%s", out)
	}
}

func TestCompiledExecutorBackfillsTheLedger(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("search.messages", func(r *http.Request) any {
		return map[string]any{
			"ok": true,
			"messages": map[string]any{
				"total": 2,
				"matches": []any{
					searchMatch("C1", tsDaysAgo(2)),
					searchMatch("C1", tsDaysAgo(3)),
				},
			},
		}
	})
	ap := bootedProvider(t, srv)

	// Sarah has no observed activity; the person view compiles her.
	out := estateViewOut(t, ap, map[string]any{"view": "person", "person": "schen"})

	if !strings.Contains(out, "compiled executor — 1 search calls") {
		t.Fatalf("compiled executor not reported:\n%s", out)
	}
	if !strings.Contains(out, "#eng — 2 active days") {
		t.Fatalf("compiled observations did not reach the fold:\n%s", out)
	}

	// The observations persisted: a second ask answers from the fold.
	out = estateViewOut(t, ap, map[string]any{"view": "person", "person": "schen"})
	if !strings.Contains(out, "fold executor, 0 Slack calls") {
		t.Fatalf("second ask compiled again:\n%s", out)
	}
}

func TestAboutComposesAndOffersTheDeeperHop(t *testing.T) {
	srv := slacktest.New(t)
	eng := channelWithCreator("C3", "acme-sales", "U2")
	srv.SeedChannels(channelWithCreator("C1", "eng", "U9"), eng)
	ap := bootedProvider(t, srv)

	for _, d := range []int{1, 2} {
		seedActivity(ap, "C1", "U2", d)
		seedActivity(ap, "C1", "U1", d)
	}

	out := estateViewOut(t, ap, map[string]any{"view": "about", "person": "schen"})

	if !strings.Contains(out, "about Sarah Chen") {
		t.Fatalf("seed not resolved:\n%s", out)
	}
	if !strings.Contains(out, "acme") {
		t.Fatalf("founder plane missing her family:\n%s", out)
	}
	if !strings.Contains(out, "Reading plan") {
		t.Fatalf("reading plan missing:\n%s", out)
	}
	if !strings.Contains(out, "deeper=true") {
		t.Fatalf("deeper hint missing:\n%s", out)
	}
}

func channelWithCreator(id, name, creator string) slack.Channel {
	var ch slack.Channel
	ch.ID, ch.Name = id, name
	ch.Creator = creator
	ch.IsMember = true
	ch.Created = slack.JSONTime(time.Now().AddDate(0, -1, 0).Unix())
	return ch
}

func searchMatch(channelID, ts string) map[string]any {
	return map[string]any{
		"type":    "message",
		"user":    "U2",
		"text":    "redacted",
		"ts":      ts,
		"channel": map[string]any{"id": channelID, "name": "eng"},
	}
}

func TestCompiledExecutorRefusesAReadOnlyLedger(t *testing.T) {
	srv := slacktest.New(t)
	writer := bootedProvider(t, srv) // wins the attention writer election
	_ = writer
	reader := bootedProvider(t, srv) // loses it

	srv.ResetCalls()
	out := estateViewOut(t, reader, map[string]any{"view": "person", "person": "schen"})

	if n := srv.Calls("search.messages"); n != 0 {
		t.Fatalf("read-only instance compiled anyway: %d search calls", n)
	}
	if !strings.Contains(out, "fold executor, 0 Slack calls") {
		t.Fatalf("coverage does not admit the fold-only answer:\n%s", out)
	}
}

func TestDMDaysCountOnceWhenBothSidesObserved(t *testing.T) {
	srv := slacktest.New(t)
	var dm slack.Channel
	dm.ID = "D1"
	dm.IsIM = true
	dm.User = "U2"
	srv.SeedChannels(dm)
	ap := bootedProvider(t, srv)

	// Day 1: both sides of the DM observed. Day 2: only the operator's side.
	seedActivity(ap, "D1", "U1", 1, 2)
	seedActivity(ap, "D1", "U2", 1)

	out := estateViewOut(t, ap, map[string]any{"view": "person", "person": "bockeliea"})

	if !strings.Contains(out, "Sarah Chen — 1 co-active days + 1 DM days") {
		t.Fatalf("DM day counting wrong (want 1 co-active + 1 DM, both-observed day counted once):\n%s", out)
	}
}
