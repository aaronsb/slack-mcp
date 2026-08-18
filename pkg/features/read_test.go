package features_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

func read(t *testing.T, srv *slacktest.Server, params map[string]any) *features.FeatureResult {
	t.Helper()
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	params["_provider"] = ap
	res, err := features.Read.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return res
}

func channel(id, name string) slack.Channel {
	var ch slack.Channel
	ch.ID = id
	ch.Name = name
	ch.IsChannel = true
	ch.IsMember = true
	return ch
}

func dmWith(id, userID string) slack.Channel {
	var ch slack.Channel
	ch.ID = id
	ch.IsIM = true
	ch.User = userID
	return ch
}

// The point of the tool: a plain description instead of a pasted coordinate.
func TestReadResolvesAChannelByName(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"), channel("C2", "random"))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "deploy is rolling back", "1782246118.543969")},
		}
	})

	res := read(t, srv, map[string]any{"handle": "engineering"})
	if !res.Success {
		t.Fatalf("read failed: %s", res.Message)
	}

	d := res.Data.(map[string]any)
	if d["where"] != "#engineering" {
		t.Errorf("resolved to %v, want #engineering", d["where"])
	}
	msgs := d["messages"].([]map[string]any)
	if len(msgs) != 1 || msgs[0]["text"] != "deploy is rolling back" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

// A person's name resolves to the DM with them.
func TestReadResolvesAPersonToTheirDM(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(dmWith("D1", "U2"), channel("C1", "engineering"))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "got a minute?", "1782246118.543969")},
		}
	})

	res := read(t, srv, map[string]any{"handle": "sarah"})
	if !res.Success {
		t.Fatalf("read failed: %s", res.Message)
	}
	if got := res.Data.(map[string]any)["where"]; got != "@Sarah Chen" {
		t.Errorf("resolved to %v, want @Sarah Chen", got)
	}
}

// Ambiguity is an answer, not a failure — one round trip in the agent's loop
// instead of a human deciding which coordinate to paste.
func TestAmbiguousDescriptionReturnsCandidates(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(
		channel("C1", "deploy-alerts"),
		channel("C2", "deploy-runbook"),
		channel("C3", "random"),
	)

	res := read(t, srv, map[string]any{"handle": "deploy"})
	if !res.Success {
		t.Fatalf("an ambiguous description should answer, not fail: %s", res.Message)
	}

	d := res.Data.(map[string]any)
	if ambiguous, _ := d["ambiguous"].(bool); !ambiguous {
		t.Fatalf("expected candidates, got %+v", d)
	}
	options := d["candidates"].([]map[string]any)
	if len(options) != 2 {
		t.Fatalf("want 2 candidates, got %d: %+v", len(options), options)
	}

	// Every candidate must be usable without assembling anything.
	for _, o := range options {
		h, _ := o["handle"].(string)
		if !handle.Is(h) {
			t.Errorf("candidate %+v carries no usable handle", o)
		}
	}
}

// An exact name wins outright rather than being offered next to the substring
// matches it obviously beats.
func TestExactNameBeatsSubstringMatches(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(
		channel("C1", "deploy"),
		channel("C2", "deploy-alerts"),
		channel("C3", "deploy-runbook"),
	)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "exact", "1782246118.543969")},
		}
	})

	res := read(t, srv, map[string]any{"handle": "deploy"})
	d := res.Data.(map[string]any)
	if ambiguous, _ := d["ambiguous"].(bool); ambiguous {
		t.Fatalf("an exact match should resolve outright, got candidates: %+v", d["candidates"])
	}
	if d["where"] != "#deploy" {
		t.Errorf("resolved to %v, want #deploy", d["where"])
	}
}

// Not-found must say what it looked at, so the caller knows whether to widen.
func TestNotFoundReportsWhatWasSearched(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))

	res := read(t, srv, map[string]any{"handle": "nothing-like-this-exists"})
	if res.Success {
		t.Fatal("expected a miss")
	}
	d := res.Data.(map[string]any)
	if _, ok := d["searched"]; !ok {
		t.Error("a miss did not say what was searched; the caller cannot tell it from a wrong window")
	}
}

// A handle skips resolution entirely.
func TestReadFollowsAHandle(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("conversations.replies", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "just the one", "1782246118.543969")},
		}
	})

	res := read(t, srv, map[string]any{"handle": handle.Message("C1", "1782246118.543969")})
	if !res.Success {
		t.Fatalf("read failed: %s", res.Message)
	}
	d := res.Data.(map[string]any)
	if d["kind"] != "message" {
		t.Errorf("kind = %v, want message", d["kind"])
	}
	if d["where"] != "#engineering" {
		t.Errorf("where = %v, want #engineering", d["where"])
	}
}

// A message with replies is more useful read as its thread.
func TestReadOfAThreadedMessageReturnsTheThread(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("conversations.replies", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "deploy rollback?", "1782246118.543969"),
				slacktest.Message("U1", "on it", "1782246200.000000"),
			},
		}
	})

	res := read(t, srv, map[string]any{"handle": handle.Message("C1", "1782246118.543969")})
	d := res.Data.(map[string]any)
	if d["kind"] != "thread" {
		t.Errorf("kind = %v, want thread — a reply alone is rarely comprehensible", d["kind"])
	}
	if got := len(d["messages"].([]map[string]any)); got != 2 {
		t.Errorf("want the whole thread, got %d messages", got)
	}
}

// Reading must not advance the human's read marker.
func TestReadDoesNotMarkRead(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "hello", "1782246118.543969")},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	if _, err := features.Read.Handler(context.Background(), map[string]any{
		"_provider": ap, "handle": "engineering",
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	if got := srv.Calls("conversations.mark"); got != 0 {
		t.Errorf("read marked %d conversations read", got)
	}
}

// Every message read comes back with a handle, so acking needs no coordinates.
func TestReadReturnsHandlesForWhatItRead(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "hello", "1782246118.543969")},
		}
	})

	res := read(t, srv, map[string]any{"handle": "engineering"})
	msgs := res.Data.(map[string]any)["messages"].([]map[string]any)
	h, _ := msgs[0]["handle"].(string)
	if !handle.Is(h) {
		t.Errorf("message carries no handle: %+v", msgs[0])
	}
	if len(res.NextActions) == 0 {
		t.Error("read did not offer a way to acknowledge what it returned")
	}
}
