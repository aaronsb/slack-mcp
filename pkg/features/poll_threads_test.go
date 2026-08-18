package features_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
)

// quietCounts is a workspace where no conversation has moved, so anything the
// tick reports came from the thread path.
func quietCounts() any {
	return slacktest.Counts(
		[]any{slacktest.Conversation("C1", "1600000000.000000", "1600000000.000000", false, 0)},
		nil,
	)
}

// A thread can gain twenty replies without its channel's `latest` moving, so
// the conversation tick is blind to it. The thread feed is what sees it.
func TestThreadRepliesAreReportedWhenTheChannelLooksQuiet(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return slacktest.ThreadView(slacktest.Thread("C1", "1782246118.543969", "1786752114.508819", 9, 2))
	})

	d := data(t, poll(t, srv, nil))
	events := d["events"].([]map[string]any)

	if len(events) != 1 {
		t.Fatalf("want one thread event, got %+v", events)
	}
	if events[0]["kind"] != "thread" {
		t.Errorf("kind = %v, want thread", events[0]["kind"])
	}

	// The handle must open the whole thread, not one reply out of context.
	ref, err := handle.Decode(events[0]["handle"].(string))
	if err != nil {
		t.Fatalf("thread handle does not decode: %v", err)
	}
	if ref.Kind != handle.KindThread || ref.TS != "1782246118.543969" {
		t.Errorf("handle points at %+v, want the thread root", ref)
	}
}

// The feed is unreads-only: once the person reads a thread in their own client
// it disappears from it. A tracked thread must keep reporting anyway, which is
// what conversations.replies is for.
func TestATrackedThreadKeepsReportingAfterTheFeedDropsIt(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	// Empty feed — the human has read everything in their client.
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return slacktest.ThreadView()
	})
	srv.Handle("conversations.replies", func(*http.Request) any {
		root := slacktest.Message("U2", "deploy rollback?", "1782246118.543969")
		root["thread_ts"] = "1782246118.543969"
		root["reply_count"] = 9
		root["latest_reply"] = "1786752999.000000" // newer than what was acked
		return map[string]any{"ok": true, "messages": []any{root}}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	store, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("watermark.Open: %v", err)
	}
	store.AckThread("C1", "1782246118.543969", "1786752114.508819", time.Now())
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	events := res.Data.(map[string]any)["events"].([]map[string]any)
	if len(events) != 1 || events[0]["kind"] != "thread" {
		t.Fatalf("a tracked thread stopped reporting once the feed dropped it: %+v", events)
	}
}

// A thread already acknowledged at its newest reply must not be reported again.
func TestAnAcknowledgedThreadIsNotReported(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return slacktest.ThreadView(slacktest.Thread("C1", "1782246118.543969", "1786752114.508819", 9, 2))
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	store, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("watermark.Open: %v", err)
	}
	store.AckThread("C1", "1782246118.543969", "1786752114.508819", time.Now())
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ResultCount != 0 {
		t.Errorf("an acknowledged thread was reported again: %+v", res.Data)
	}
}

// Acking the thread handle poll hands out must actually stop it repeating.
func TestAckingAThreadHandleStopsItRepeating(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return slacktest.ThreadView(slacktest.Thread("C1", "1782246118.543969", "1786752114.508819", 9, 2))
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
		t.Fatalf("expected one thread event, got %d", len(events))
	}

	ack(t, ap, map[string]any{"handle": events[0]["handle"]})

	second, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if second.ResultCount != 0 {
		t.Errorf("thread still reported after its handle was acked: %+v", second.Data)
	}
}

// A failed thread feed must not be reported as a complete tick.
func TestAFailedThreadFeedMakesTheTickIncomplete(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return map[string]any{"ok": false, "error": "ratelimited"}
	})

	coverage := data(t, poll(t, srv, nil))["coverage"].(map[string]any)
	if coverage["complete"].(bool) {
		t.Error("coverage claims complete while the thread feed failed")
	}
}

// The thread half must not cost a request per conversation.
func TestThreadTickCostsOneFeedCall(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("client.counts", func(*http.Request) any { return quietCounts() })
	srv.Handle("subscriptions.thread.getView", func(*http.Request) any {
		return slacktest.ThreadView()
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	if _, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap}); err != nil {
		t.Fatalf("poll: %v", err)
	}

	if got := srv.Calls("subscriptions.thread.getView"); got != 1 {
		t.Errorf("thread feed called %d times, want 1", got)
	}
	if got := srv.Calls("conversations.replies"); got != 0 {
		t.Errorf("conversations.replies called %d times with nothing tracked, want 0", got)
	}
}
