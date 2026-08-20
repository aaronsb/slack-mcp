package features_test

import (
	"context"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

var estateBase = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

// seedEstate writes estate history for team T1 (slacktest's auth.test
// fixture) before the provider boots.
func seedEstate(t *testing.T, observe func(*estate.Store)) {
	t.Helper()
	st, err := estate.Open("T1")
	if err != nil {
		t.Fatalf("open estate: %v", err)
	}
	observe(st)
	if err := st.Close(); err != nil {
		t.Fatalf("close estate: %v", err)
	}
}

// sweptEstate seeds the default fixture directory, deactivates U2 a day
// later, and stamps a full sweep so absence is assertable.
func sweptEstate(t *testing.T) {
	t.Helper()
	seedEstate(t, func(st *estate.Store) {
		users := []slack.User{
			{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
			{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
		}
		if _, err := st.ObserveUsers(users, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe users: %v", err)
		}
		gone := users[1]
		gone.Deleted = true
		later := estateBase.Add(24 * time.Hour)
		if _, err := st.ObserveUsers([]slack.User{users[0], gone}, true, estate.SourceSweep, later); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
		if err := st.RecordSweep(estate.SweepReport{
			Users:    estate.ClassReport{Complete: true, Count: 2},
			Channels: estate.ClassReport{Complete: true, Count: 2, ArchivedIncluded: true},
		}, later); err != nil {
			t.Fatalf("record sweep: %v", err)
		}
	})
}

func bootedProvider(t *testing.T, srv *slacktest.Server) *provider.ApiProvider {
	t.Helper()
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)
	return ap
}

func listUsers(t *testing.T, ap *provider.ApiProvider, params map[string]any) *features.FeatureResult {
	t.Helper()
	params["_provider"] = ap
	res, err := features.ListUsers.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("list-users: %v", err)
	}
	return res
}

func listChannels(t *testing.T, ap *provider.ApiProvider, params map[string]any) *features.FeatureResult {
	t.Helper()
	params["_provider"] = ap
	res, err := features.ListChannels.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("list-channels: %v", err)
	}
	return res
}

func dataOf(t *testing.T, res *features.FeatureResult) map[string]interface{} {
	t.Helper()
	data, ok := res.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("result data is %T, want a map", res.Data)
	}
	return data
}

func coverageOf(t *testing.T, res *features.FeatureResult) map[string]interface{} {
	t.Helper()
	cov, ok := dataOf(t, res)["coverage"].(map[string]interface{})
	if !ok {
		t.Fatalf("no coverage block in %+v", res.Data)
	}
	est, ok := cov["estate"].(map[string]interface{})
	if !ok {
		t.Fatalf("no estate coverage in %+v", cov)
	}
	return est
}

func TestListUsersReportsATombstoneAsADatedFact(t *testing.T) {
	srv := slacktest.New(t)
	sweptEstate(t)
	ap := bootedProvider(t, srv)

	res := listUsers(t, ap, map[string]any{"query": "sarah"})

	users := dataOf(t, res)["users"].([]map[string]interface{})
	if len(users) != 0 {
		t.Fatalf("deactivated user listed as active: %+v", users)
	}
	tombstoned, ok := dataOf(t, res)["tombstoned"].([]map[string]interface{})
	if !ok || len(tombstoned) != 1 {
		t.Fatalf("departed user not reported as a dated fact: %+v", res.Data)
	}
	entry := tombstoned[0]
	if entry["id"] != "U2" || entry["reason"] != "deactivated" {
		t.Fatalf("wrong tombstone entry: %+v", entry)
	}
	interval, ok := entry["deactivatedBetween"].([]string)
	if !ok || len(interval) != 2 {
		t.Fatalf("tombstone carries no interval: %+v", entry)
	}
	if est := coverageOf(t, res); est["swept"] != true {
		t.Fatalf("coverage does not report the sweep: %+v", est)
	}
}

func TestListUsersIncludeDeletedListsTheDeparted(t *testing.T) {
	srv := slacktest.New(t)
	sweptEstate(t)
	ap := bootedProvider(t, srv)

	res := listUsers(t, ap, map[string]any{"query": "sarah", "includeDeleted": true})

	users := dataOf(t, res)["users"].([]map[string]interface{})
	if len(users) != 1 {
		t.Fatalf("includeDeleted returned %d users, want 1", len(users))
	}
	entry := users[0]
	if entry["deleted"] != true || entry["reason"] != "deactivated" {
		t.Fatalf("departed user not marked: %+v", entry)
	}
	if _, ok := entry["deactivatedBetween"]; !ok {
		t.Fatalf("no dated interval on the entry: %+v", entry)
	}
}

func TestListUsersSaysUnsweptWhenItCannotKnow(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	res := listUsers(t, ap, map[string]any{"query": "zorp"})

	if dataOf(t, res)["reason"] != "unswept" {
		t.Fatalf("reason = %v, want unswept", dataOf(t, res)["reason"])
	}
	if est := coverageOf(t, res); est["swept"] != false {
		t.Fatalf("coverage claims a sweep that never ran: %+v", est)
	}
}

func TestListUsersSaysNeverSeenUnderACompletedSweep(t *testing.T) {
	srv := slacktest.New(t)
	sweptEstate(t)
	ap := bootedProvider(t, srv)

	res := listUsers(t, ap, map[string]any{"query": "zorp"})

	if dataOf(t, res)["reason"] != "never_seen" {
		t.Fatalf("reason = %v, want never_seen", dataOf(t, res)["reason"])
	}
	if est := coverageOf(t, res); est["swept"] != true {
		t.Fatalf("coverage lost the sweep: %+v", est)
	}
}

func TestListChannelsCarriesTheArchiveInterval(t *testing.T) {
	srv := slacktest.New(t)

	seedEstate(t, func(st *estate.Store) {
		var c1 slack.Channel
		c1.ID, c1.Name = "C1", "eng"
		if _, err := st.ObserveChannels([]slack.Channel{c1}, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
		c1.IsArchived = true
		if _, err := st.ObserveChannels([]slack.Channel{c1}, true, estate.SourceSweep, estateBase.Add(24*time.Hour)); err != nil {
			t.Fatalf("archive: %v", err)
		}
	})

	var archived slack.Channel
	archived.ID, archived.Name = "C1", "eng"
	archived.IsArchived = true
	srv.SeedChannels(archived)
	ap := bootedProvider(t, srv)

	res := listChannels(t, ap, map[string]any{"filter": "all", "includeArchived": true})

	channels := dataOf(t, res)["channels"].([]map[string]interface{})
	if len(channels) != 1 {
		t.Fatalf("got %d channels, want 1: %+v", len(channels), channels)
	}
	interval, ok := channels[0]["archivedBetween"].([]string)
	if !ok || len(interval) != 2 {
		t.Fatalf("archived channel carries no interval: %+v", channels[0])
	}
	if interval[0] != estateBase.Format(time.RFC3339) || interval[1] != estateBase.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("interval %v, want [%s, %s]", interval, estateBase.Format(time.RFC3339), estateBase.Add(24*time.Hour).Format(time.RFC3339))
	}
}

func TestListChannelsIncludeDeletedListsGoneChannels(t *testing.T) {
	srv := slacktest.New(t)

	seedEstate(t, func(st *estate.Store) {
		var c1, c2 slack.Channel
		c1.ID, c1.Name = "C1", "eng"
		c2.ID, c2.Name = "C2", "platform"
		if _, err := st.ObserveChannels([]slack.Channel{c1, c2}, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
		if _, err := st.ObserveChannels([]slack.Channel{c1}, true, estate.SourceSweep, estateBase.Add(24*time.Hour)); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	})

	var c1 slack.Channel
	c1.ID, c1.Name = "C1", "eng"
	c1.IsMember = true
	srv.SeedChannels(c1)
	ap := bootedProvider(t, srv)

	res := listChannels(t, ap, map[string]any{"filter": "all", "includeDeleted": true})

	tombstoned, ok := dataOf(t, res)["tombstoned"].([]map[string]interface{})
	if !ok || len(tombstoned) != 1 {
		t.Fatalf("gone channel not reported: %+v", res.Data)
	}
	entry := tombstoned[0]
	if entry["name"] != "platform" || entry["reason"] != "absent" {
		t.Fatalf("wrong tombstone entry: %+v", entry)
	}
	if _, ok := entry["absentBetween"]; !ok {
		t.Fatalf("no dated interval: %+v", entry)
	}
}
