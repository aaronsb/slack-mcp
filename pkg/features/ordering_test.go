package features_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// ADR-011: every rendered message list is oldest-first. Slack hands every
// fixture here newest-first, the way the real API does, and each test reads
// the rendered list top to bottom expecting the conversation in the order it
// happened.

func texts(items []map[string]any, key string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it[key].(string)
		out = append(out, s)
	}
	return out
}

func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d is %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

func run(t *testing.T, srv *slacktest.Server, f *features.Feature, params map[string]any) *features.FeatureResult {
	t.Helper()
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	params["_provider"] = ap
	res, err := f.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	if !res.Success {
		t.Fatalf("%s failed: %s", f.Name, res.Message)
	}
	return res
}

func TestSinceWindowReadsOldestFirst(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "third", "1782246300.000000"),
				slacktest.Message("U2", "second", "1782246200.000000"),
				slacktest.Message("U2", "first", "1782246100.000000"),
			},
		}
	})

	res := run(t, srv, features.CatchUpOnChannel, map[string]any{"channel": "engineering", "since": "1d"})
	items := res.Data.(map[string]any)["importantItems"].([]map[string]any)
	assertOrder(t, texts(items, "message"), []string{"first", "second", "third"})
}

func TestSearchResultsReadOldestFirst(t *testing.T) {
	res, _ := searchWith(t, map[string]any{"query": "deploy"}, []any{
		aMatch("C1", "1782246300.000000", "deploy third"),
		aMatch("C1", "1782246200.000000", "deploy second"),
		aMatch("C1", "1782246100.000000", "deploy first"),
	})
	results := res.Data.(map[string]any)["results"].([]map[string]any)
	assertOrder(t, texts(results, "text"), []string{"deploy first", "deploy second", "deploy third"})
}

// The mentions scan walks channels one at a time. Grouped by channel, a
// reader sees two half-timelines; sorted, one.
func TestMentionsAreOneTimelineOldestFirst(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"), channel("C2", "random"))
	srv.Handle("conversations.history", func(r *http.Request) any {
		_ = r.ParseForm()
		switch r.Form.Get("channel") {
		case "C1":
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				slacktest.Message("U2", "<@U1> engineering late", "1782246300.000000"),
				slacktest.Message("U2", "<@U1> engineering early", "1782246100.000000"),
			}}
		case "C2":
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				slacktest.Message("U2", "<@U1> random middle", "1782246200.000000"),
			}}
		}
		return nil
	})

	res := run(t, srv, features.CheckMyMentions, map[string]any{"timeframe": "3d"})
	mentions := res.Data.(map[string]any)["mentions"].([]map[string]any)
	assertOrder(t, texts(mentions, "message"),
		[]string{"@Aaron Bockelie (you) engineering early", "@Aaron Bockelie (you) random middle", "@Aaron Bockelie (you) engineering late"})
}

func TestUnreadPreviewsReadOldestFirst(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(dmWith("D1", "U2"), channel("C1", "engineering"))
	srv.Handle("conversations.history", func(r *http.Request) any {
		_ = r.ParseForm()
		switch r.Form.Get("channel") {
		case "D1":
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				slacktest.Message("U2", "dm later", "1782246300.000000"),
				slacktest.Message("U2", "dm earlier", "1782246100.000000"),
			}}
		case "C1":
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				slacktest.Message("U2", "<@U1> channel later", "1782246300.000000"),
				slacktest.Message("U2", "<@U1> channel earlier", "1782246100.000000"),
			}}
		}
		return nil
	})

	// The default counts fixture allows one mention in C1; two are needed to
	// observe an order.
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{slacktest.Conversation("C1", "1782246000.000000", "1782246300.000000", true, 2)},
			[]any{slacktest.Conversation("D1", "1782246000.000000", "1782246300.000000", true, 0)},
		)
	})

	res := run(t, srv, features.CheckUnreads, map[string]any{})
	unreads := res.Data.(map[string]any)["unreads"].(map[string]any)

	dms := unreads["dms"].([]map[string]any)
	if len(dms) != 1 {
		t.Fatalf("want one DM, got %d", len(dms))
	}
	assertOrder(t, texts(dms[0]["messages"].([]map[string]any), "text"), []string{"dm earlier", "dm later"})

	mentions := unreads["mentions"].([]map[string]any)
	assertOrder(t, texts(mentions, "message"), []string{"@Aaron Bockelie (you) channel earlier", "@Aaron Bockelie (you) channel later"})
}
