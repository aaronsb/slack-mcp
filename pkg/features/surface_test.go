package features_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// The v2 surface (ADR-009) is thin dispatch over the v1 handlers: these
// tests pin the routing, the RenderAs formatter hand-off, and the echo
// line that states the effective invocation.

func runTool(t *testing.T, f *features.Feature, ap *provider.ApiProvider, params map[string]any) string {
	t.Helper()
	params["_provider"] = ap
	res, err := f.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	return features.FormatResult(f.Name, res)
}

func TestInboxRoutesViewsAndEchoesTheInvocation(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Inbox, ap, map[string]any{"view": "unreads", "limit": float64(5)})
	if !strings.Contains(out, "`inbox view='unreads' limit=5`") {
		t.Fatalf("echo line missing:\n%s", out)
	}

	out = runTool(t, features.Inbox, ap, map[string]any{"view": "bogus"})
	if !strings.Contains(out, "Available views: new") {
		t.Fatalf("unknown view not named:\n%s", out)
	}
}

func TestMessagesRoutesByAddressingMode(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "hello there", "1782246200.000000")},
		}
	})
	ap := bootedProvider(t, srv)

	// target+since → catch-up's renderer.
	out := runTool(t, features.Messages, ap, map[string]any{"target": "#eng", "since": "1d"})
	if !strings.Contains(out, "`messages target='#eng' since=1d`") {
		t.Fatalf("catch-up echo missing:\n%s", out)
	}
	if !strings.Contains(out, "hello there") {
		t.Fatalf("catch-up content missing:\n%s", out)
	}

	// target alone → read's renderer.
	out = runTool(t, features.Messages, ap, map[string]any{"target": "#eng"})
	if !strings.Contains(out, "`messages target='#eng'`") {
		t.Fatalf("read echo missing:\n%s", out)
	}
	if !strings.Contains(out, "hello there") {
		t.Fatalf("read content missing:\n%s", out)
	}

	// no address → the error names all modes.
	out = runTool(t, features.Messages, ap, map[string]any{})
	if !strings.Contains(out, "needs an address") {
		t.Fatalf("missing-address error absent:\n%s", out)
	}
}

func TestMessagesQueryRoutesToSearch(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("search.messages", func(*http.Request) any {
		return map[string]any{
			"ok":       true,
			"messages": map[string]any{"total": 0, "matches": []any{}},
		}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{"query": "deploy failure"})
	if !strings.Contains(out, "`messages query='deploy failure'`") {
		t.Fatalf("search echo missing:\n%s", out)
	}
	if srv.Calls("search.messages") == 0 {
		t.Fatalf("query did not reach search")
	}
}

func TestSayPostsAMessageThroughTheWritePath(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("chat.postMessage", func(*http.Request) any {
		return map[string]any{"ok": true, "channel": "C1", "ts": "1782246300.000000"}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "shipping v2"})
	if srv.Calls("chat.postMessage") != 1 {
		t.Fatalf("say did not post: %d calls", srv.Calls("chat.postMessage"))
	}
	if !strings.Contains(out, "`say to='#eng'`") {
		t.Fatalf("say echo missing:\n%s", out)
	}

	out = runTool(t, features.Say, ap, map[string]any{"to": "#eng"})
	if !strings.Contains(out, "needs text=") {
		t.Fatalf("modeless say not rejected:\n%s", out)
	}
}

func TestEstatePeopleViewServesTheDirectory(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	out := estateViewOut(t, ap, map[string]any{"view": "people", "person": "sarah"})
	if !strings.Contains(out, "`estate view='people' query='sarah'`") {
		t.Fatalf("people echo missing:\n%s", out)
	}
	if !strings.Contains(out, "Sarah Chen") {
		t.Fatalf("directory result missing:\n%s", out)
	}
}

func TestMessagesSinceResolvesAPerson(t *testing.T) {
	srv := slacktest.New(t)
	var dm slack.Channel
	dm.ID = "D1"
	dm.IsIM = true
	dm.User = "U2"
	srv.SeedChannels(dm)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "lunch?", "1782246200.000000")},
		}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "@schen", "since": "1d"})
	if !strings.Contains(out, "`messages target='@schen' since=1d`") {
		t.Fatalf("echo missing:\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Fatalf("person target failed to resolve to the DM:\n%s", out)
	}
}

func TestLadderResolvesMe(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	out := estateViewOut(t, ap, map[string]any{"view": "person", "person": "me"})
	if !strings.Contains(out, "person view — Aaron Bockelie") {
		t.Fatalf("'me' did not resolve to the session identity:\n%s", out)
	}
	if !strings.Contains(out, "observed encounters in the window") {
		t.Fatalf("thin-coverage line missing:\n%s", out)
	}
}

func TestMessagesSinceMissRendersCandidates(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "@nosuchperson", "since": "1d"})
	if !strings.Contains(out, "Could not resolve") {
		t.Fatalf("miss not rendered:\n%s", out)
	}
	if !strings.Contains(out, "`messages target='@nosuchperson' since=1d`") {
		t.Fatalf("miss result lost the echo:\n%s", out)
	}
}

func TestSinceModeAcceptsAnInboxHandle(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "group update", "1782246200.000000")},
		}
	})
	ap := bootedProvider(t, srv)

	h := handle.Conversation("C1")
	out := runTool(t, features.Messages, ap, map[string]any{"target": h, "since": "1d"})
	if strings.Contains(out, "not found") {
		t.Fatalf("event handle rejected as a since-mode target:\n%s", out)
	}
	if !strings.Contains(out, "group update") {
		t.Fatalf("handle target did not reach the conversation:\n%s", out)
	}
}

func TestFiltersAloneAreASearch(t *testing.T) {
	srv := slacktest.New(t)
	var q string
	srv.Handle("search.messages", func(r *http.Request) any {
		_ = r.ParseForm()
		q = r.Form.Get("query")
		return map[string]any{"ok": true, "messages": map[string]any{"total": 0, "matches": []any{}}}
	})
	ap := bootedProvider(t, srv)
	out := runTool(t, features.Messages, ap, map[string]any{"from": []any{"sarah"}, "after": "2026-09-01"})
	if q != "after:2026-09-01 from:@schen" {
		t.Errorf("query sent: %q", q)
	}
	if strings.Contains(out, `""`) || !strings.Contains(out, "Nothing matched the filters") || !strings.Contains(out, "from=[sarah]") {
		t.Errorf("render:\n%s", out)
	}
}

func TestTargetWithFiltersIsRefused(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("search.messages", func(*http.Request) any {
		return map[string]any{"ok": true, "messages": map[string]any{"total": 0, "matches": []any{}}}
	})
	ap := bootedProvider(t, srv)
	for _, p := range []map[string]any{
		{"target": "#eng", "from": []any{"sarah"}},
		{"target": "#eng", "query": "x", "has": []any{"link"}},
	} {
		out := runTool(t, features.Messages, ap, p)
		if !strings.Contains(out, "cannot be combined") || !strings.Contains(out, "in='#channel'") {
			t.Errorf("not refused:\n%s", out)
		}
	}
	if srv.Calls("search.messages") != 0 {
		t.Errorf("a refused call searched")
	}
}

func TestFiltersOnlyEchoIsClean(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("search.messages", func(*http.Request) any {
		return map[string]any{"ok": true, "messages": map[string]any{"total": 0, "matches": []any{}}}
	})
	ap := bootedProvider(t, srv)
	out := runTool(t, features.Messages, ap, map[string]any{"after": "7d", "in": []any{"#engineering"}, "thread": false, "has": []any{}})
	if !strings.Contains(out, "`messages after=7d in=[#engineering]`") {
		t.Errorf("echo:\n%s", out)
	}
}

func TestTargetWithQueryIsRefused(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)
	out := runTool(t, features.Messages, ap, map[string]any{"target": "#eng", "query": "x"})
	if !strings.Contains(out, "cannot be combined") || !strings.Contains(out, "in='#channel'") {
		t.Errorf("not refused:\n%s", out)
	}
	if srv.Calls("search.messages") != 0 {
		t.Errorf("searched")
	}
}

// A bare '@' target names a person, so it reads that person's DM through
// the read-policy ladder, as since= does. '@me' is the self-DM, not every
// conversation whose name contains "me".
func TestBareAtMeReadsTheSelfDM(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedUsers(slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"})
	var self slack.Channel
	self.ID, self.IsIM, self.User = "D1", true, "U1"
	srv.SeedChannels(self, channel("C1", "meetings"), channel("C2", "memes"))
	var read []string
	srv.Handle("conversations.history", func(r *http.Request) any {
		_ = r.ParseForm()
		read = append(read, r.Form.Get("channel"))
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U1", "note to self", "1782246200.000000")},
		}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "@me"})
	if strings.Contains(out, "matches") || !strings.Contains(out, "note to self") {
		t.Fatalf("@me did not read the self-DM:\n%s", out)
	}
	if len(read) != 1 || read[0] != "D1" {
		t.Errorf("read %v, want only the self-DM", read)
	}
}

// A DM the cache has not seen yet is still read by '@handle', as since=
// reads it, rather than reported missing.
func TestBareAtReadsAnUncachedDM(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedUsers(slack.User{ID: "U2", Name: "schen", RealName: "Sam Chen"})
	srv.Handle("conversations.open", func(*http.Request) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D9"}}
	})
	var read []string
	srv.Handle("conversations.history", func(r *http.Request) any {
		_ = r.ParseForm()
		read = append(read, r.Form.Get("channel"))
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "lunch?", "1782246200.000000")},
		}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "@schen"})
	if !strings.Contains(out, "lunch?") || len(read) != 1 || read[0] != "D9" {
		t.Fatalf("@schen did not read the DM (read %v):\n%s", read, out)
	}
}
