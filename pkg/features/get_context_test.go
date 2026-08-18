package features_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// Drives a real feature handler end to end against the fake host. This is the
// path every tool test takes, so if it breaks, the harness is broken rather
// than the tool.
func TestGetContextReadsChannelHistory(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "rolling back the deploy", "1782246200.000000"),
				slacktest.Message("U1", "what happened?", "1782246118.543969"),
			},
		}
	})

	res, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": srv.Provider(t),
		"channel":   "eng",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Success {
		t.Fatalf("handler failed: %s", res.Message)
	}

	data := res.Data.(map[string]any)
	msgs := data["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}

	// Formatted oldest-first, with user IDs resolved through the users cache.
	if msgs[0]["text"] != "what happened?" {
		t.Errorf("messages not oldest-first: got %q first", msgs[0]["text"])
	}
	if msgs[1]["user"] != "Sarah Chen" {
		t.Errorf("user ID not resolved to a name: got %v", msgs[1]["user"])
	}
}

// get-context documents that it does not mark messages as read. Assert the
// behaviour rather than trusting the description — that gap is exactly what
// ADR-003 was written about.
func TestGetContextDoesNotMarkRead(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)

	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.ResetCalls()

	res, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": ap,
		"channel":   "eng",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Success {
		t.Fatalf("handler failed: %s", res.Message)
	}

	if got := srv.Calls("conversations.mark"); got != 0 {
		t.Errorf("get-context marked %d conversations read; it must not touch the read marker", got)
	}
}

// A thread request must come back as a thread, not as channel history.
func TestGetContextReturnsThread(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.replies", func(*http.Request) any {
		root := slacktest.Message("U2", "deploy rollback?", "1782246118.543969")
		root["reply_count"] = 2
		root["latest_reply"] = "1782246300.000000"
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				root,
				slacktest.Message("U1", "on it", "1782246300.000000"),
			},
		}
	})

	res, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": srv.Provider(t),
		"channel":   "eng",
		"messageTs": "1782246118.543969",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	data := res.Data.(map[string]any)
	if isThread, _ := data["isThread"].(bool); !isThread {
		t.Errorf("thread request not flagged as a thread: %+v", data)
	}
	if got := len(data["messages"].([]map[string]any)); got != 2 {
		t.Errorf("want 2 thread messages, got %d", got)
	}
}
