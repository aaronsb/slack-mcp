package features_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

func ack(t *testing.T, ap *provider.ApiProvider, params map[string]any) *features.FeatureResult {
	t.Helper()
	params["_provider"] = ap
	res, err := features.Ack.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	return res
}

// The whole point of the split: acknowledging is invisible to Slack.
func TestAckDoesNotMarkReadInSlack(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	res := ack(t, ap, map[string]any{"handle": handle.Message("C1", "1782246118.543969")})
	if !res.Success {
		t.Fatalf("ack failed: %s", res.Message)
	}

	if got := srv.Calls("conversations.mark"); got != 0 {
		t.Errorf("ack called conversations.mark %d times; it must never touch the read marker", got)
	}
	if got := srv.Calls("client.counts"); got != 0 {
		t.Errorf("ack made %d unnecessary API calls; it only writes local state", got)
	}
}

// Acknowledging is what stops poll repeating itself.
func TestAckStopsPollRepeating(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", moved, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "ship it", moved)},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	first, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	events := first.Data.(map[string]any)["events"].([]map[string]any)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}

	ack(t, ap, map[string]any{"handle": events[0]["handle"]})

	second, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if second.ResultCount != 0 {
		t.Errorf("poll still reported %d events after they were acknowledged", second.ResultCount)
	}
}

// A conversation handle comes from a backlog or unreadable event — the cases
// where poll could NOT show the messages. Acknowledging one would move the
// position past messages nobody has seen.
func TestAckRefusesWholeConversations(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	res := ack(t, ap, map[string]any{"handle": handle.Conversation("C1")})
	if res.Success {
		t.Fatal("ack accepted a whole-conversation handle; that skips messages nobody saw")
	}
	rejected := res.Data.(map[string]any)["rejected"].([]map[string]any)
	if len(rejected) != 1 {
		t.Errorf("expected the handle to be reported as rejected, got %+v", rejected)
	}
}

// Several handles at once, since a poll returns many events.
func TestAckAcceptsABatch(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	res := ack(t, ap, map[string]any{"handles": []any{
		handle.Message("C1", "1782246118.543969"),
		handle.Message("C2", "1782246119.000000"),
	}})
	if !res.Success || res.ResultCount != 2 {
		t.Errorf("batch ack recorded %d of 2: %s", res.ResultCount, res.Message)
	}
}

// Garbage must be reported rather than silently counted as acknowledged.
func TestAckRejectsNonHandles(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	res := ack(t, ap, map[string]any{"handles": []any{
		"C0B74M52CQ6:1782246118.543969", // the old composed-ID form
		handle.Message("C1", "1782246118.543969"),
	}})

	if res.ResultCount != 1 {
		t.Errorf("acknowledged %d; only the real handle should count", res.ResultCount)
	}
	if len(res.Data.(map[string]any)["rejected"].([]map[string]any)) != 1 {
		t.Error("the composed ID was not reported as rejected")
	}
}

// Acknowledging a scope must not advance another scope's position.
func TestAckIsScoped(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", moved, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "hello", moved)},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	ack(t, ap, map[string]any{
		"handle": handle.Message("C1", moved),
		"scope":  "monitor",
	})

	other, err := features.Poll.Handler(context.Background(), map[string]any{
		"_provider": ap,
		"scope":     "briefing",
	})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if other.ResultCount == 0 {
		t.Error("acknowledging in one scope advanced another scope's position")
	}
}

func TestAckWithNothingToDoSaysSo(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	res := ack(t, ap, map[string]any{})
	if res.Success {
		t.Error("ack with no handles reported success")
	}
}
