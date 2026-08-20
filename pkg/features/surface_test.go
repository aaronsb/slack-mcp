package features_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
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

func TestV2SurfaceIsExactlyEightTools(t *testing.T) {
	want := []string{"inbox", "messages", "estate", "say", "dismiss", "mark-read", "auth", "download"}
	tools := []*features.Feature{
		features.Inbox, features.Messages, features.EstateViews, features.Say,
		features.Dismiss, features.MarkAsRead, features.Auth, features.Download,
	}
	for i, f := range tools {
		if f.Name != want[i] {
			t.Fatalf("tool %d named %q, want %q", i, f.Name, want[i])
		}
	}
}
