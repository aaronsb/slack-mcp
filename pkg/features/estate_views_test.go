package features_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

func estateView(t *testing.T, ap *provider.ApiProvider, params map[string]any) string {
	t.Helper()
	params["_provider"] = ap
	res, err := features.EstateViews.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("estate: %v", err)
	}
	return features.FormatResult("estate", res)
}

func famChannel(id, name, creator string, created time.Time) slack.Channel {
	var ch slack.Channel
	ch.ID, ch.Name = id, name
	ch.Creator = creator
	ch.Created = slack.JSONTime(created.Unix())
	return ch
}

func TestEstateFamiliesGroupsByStemWithLifecycleAndCreator(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedUsers(slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"})
	srv.SeedChannels(
		famChannel("C1", "acme-sales", "U1", estateBase.AddDate(-1, 0, 0)),
		famChannel("C2", "acme-implementation", "U1", estateBase),
		famChannel("C3", "loner", "U1", estateBase),
	)
	ap := bootedProvider(t, srv)

	out := estateView(t, ap, map[string]any{"view": "families"})

	if !strings.Contains(out, "### acme — 2 channels") {
		t.Fatalf("acme family not grouped:\n%s", out)
	}
	if !strings.Contains(out, "#acme-sales") || !strings.Contains(out, "#acme-implementation") {
		t.Fatalf("family channels missing:\n%s", out)
	}
	if !strings.Contains(out, "by Aaron Bockelie") {
		t.Fatalf("creator not resolved to a name:\n%s", out)
	}
	if strings.Contains(out, "### loner") {
		t.Fatalf("single unphased channel listed as a family:\n%s", out)
	}
	if !strings.Contains(out, "fold executor, 0 Slack calls") {
		t.Fatalf("coverage does not name the executor:\n%s", out)
	}
}

func TestEstateFamiliesDefaultCriteriaRequireAPhaseTag(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(
		famChannel("C1", "random-chatter", "U1", estateBase),
		famChannel("C2", "random-stuff", "U1", estateBase),
	)
	ap := bootedProvider(t, srv)

	out := estateView(t, ap, map[string]any{"view": "families"})
	if strings.Contains(out, "### random") {
		t.Fatalf("unphased stem passed the default criteria:\n%s", out)
	}

	searched := estateView(t, ap, map[string]any{"view": "families", "search": "random"})
	if !strings.Contains(searched, "### random — 2 channels") {
		t.Fatalf("search did not relax the criteria:\n%s", searched)
	}
}

func TestEstateFamiliesPersonAnchorBindsAcrossStems(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedUsers(slack.User{ID: "U7", Name: "bromleyb", RealName: "Bethany Example"})
	srv.SeedChannels(
		famChannel("C1", "help-all-recruiting", "U7", estateBase),
		famChannel("C2", "recruiting-ai-growth", "U7", estateBase.AddDate(0, 0, 3)),
		famChannel("C3", "unrelated-sales", "U9", estateBase),
	)
	ap := bootedProvider(t, srv)

	out := estateView(t, ap, map[string]any{"view": "families", "person": "@bromleyb"})

	if !strings.Contains(out, "#help-all-recruiting") || !strings.Contains(out, "#recruiting-ai-growth") {
		t.Fatalf("person anchor missed a created channel:\n%s", out)
	}
	if strings.Contains(out, "unrelated-sales") {
		t.Fatalf("channel by someone else leaked into the person anchor:\n%s", out)
	}
	if !strings.Contains(out, "created by Bethany Example") {
		t.Fatalf("title does not name the person:\n%s", out)
	}
}

func TestEstateFamiliesPersonMissReturnsCandidates(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U7", Name: "bromleyb", RealName: "Bethany Example", Profile: slack.UserProfile{Title: "Director"}},
		slack.User{ID: "U8", Name: "bethk", RealName: "Bethany Keller"},
	)
	ap := bootedProvider(t, srv)

	out := estateView(t, ap, map[string]any{"view": "families", "person": "bethany"})

	if !strings.Contains(out, "Could not resolve") {
		t.Fatalf("miss not reported:\n%s", out)
	}
	if !strings.Contains(out, "@bromleyb") {
		t.Fatalf("no candidate offered on the miss:\n%s", out)
	}
}

func TestEstateFamiliesIncludeGoneChannelsFromTheEstate(t *testing.T) {
	srv := slacktest.New(t)

	seedEstate(t, func(st *estate.Store) {
		live := famChannel("C1", "acme-sales", "U1", estateBase.AddDate(-1, 0, 0))
		gone := famChannel("C2", "acme-implementation", "U1", estateBase.AddDate(-1, 0, 5))
		if _, err := st.ObserveChannels([]slack.Channel{live, gone}, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
		if _, err := st.ObserveChannels([]slack.Channel{live}, true, estate.SourceSweep, estateBase.Add(24*time.Hour)); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	})

	srv.SeedChannels(famChannel("C1", "acme-sales", "U1", estateBase.AddDate(-1, 0, 0)))
	ap := bootedProvider(t, srv)

	out := estateView(t, ap, map[string]any{"view": "families", "search": "acme"})

	if !strings.Contains(out, "#acme-implementation") {
		t.Fatalf("gone channel forgotten:\n%s", out)
	}
	if !strings.Contains(out, "[gone ") {
		t.Fatalf("gone channel not dated:\n%s", out)
	}
	if !strings.Contains(out, "gone channels only the estate still remembers") {
		t.Fatalf("estate contribution not stated:\n%s", out)
	}
}

func TestEstateRejectsAnUnknownViewByNamingTheKnown(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	params := map[string]any{"view": "bogus", "_provider": ap}
	res, err := features.EstateViews.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("estate: %v", err)
	}
	if res.Success {
		t.Fatalf("unknown view accepted")
	}
	if !strings.Contains(res.Message, "families") {
		t.Fatalf("error does not name the available views: %s", res.Message)
	}
}

func TestListUsersSurvivesAMissingQueryParam(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	res := listUsers(t, ap, map[string]any{})
	if res.Success {
		t.Fatalf("missing query reported success")
	}
}
