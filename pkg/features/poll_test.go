package features_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
	"github.com/slack-go/slack"
)

// recent returns a Slack timestamp inside the first-look window.
func recent(offset time.Duration) string {
	return fmt.Sprintf("%d.000000", time.Now().Add(-offset).Unix())
}

func poll(t *testing.T, srv *slacktest.Server, params map[string]any) *features.FeatureResult {
	t.Helper()
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	if params == nil {
		params = map[string]any{}
	}
	params["_provider"] = ap

	res, err := features.Poll.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return res
}

func data(t *testing.T, res *features.FeatureResult) map[string]any {
	t.Helper()
	d, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("result data is %T, want a map", res.Data)
	}
	return d
}

// A monitor's viability is its idle cost. One tick that finds nothing must be
// one call to client.counts and no history fetches at all.
func TestIdleTickCostsOneCall(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		// Everything is old, so nothing falls inside the first-look window.
		return slacktest.Counts(
			[]any{slacktest.Conversation("C1", "1600000000.000000", "1600000000.000000", false, 0)},
			[]any{slacktest.Conversation("D1", "1600000000.000000", "1600000000.000000", false, 0)},
		)
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	if changed := data(t, res)["changed"].(bool); changed {
		t.Error("nothing moved, but poll reported a change")
	}
	if got := srv.Calls("client.counts"); got != 1 {
		t.Errorf("client.counts called %d times, want exactly 1", got)
	}
	if got := srv.Calls("conversations.history"); got != 0 {
		t.Errorf("conversations.history called %d times on an idle tick, want 0", got)
	}
}

// The premise of ADR-003: a conversation the human has already read still
// reports as changed, because detection reads `latest`, not `has_unreads`.
func TestReportsMovementTheHumanHasAlreadyRead(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)

	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			// has_unreads false and last_read == latest: fully read in Slack.
			[]any{slacktest.Conversation("C1", moved, moved, false, 0)},
			nil,
		)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "already read in Slack", moved)},
		}
	})

	res := poll(t, srv, nil)
	d := data(t, res)

	if !d["changed"].(bool) {
		t.Fatal("a conversation the human read reported no change; detection is coupled to read state")
	}
	events := d["events"].([]map[string]any)
	if len(events) != 1 || events[0]["preview"] != "already read in Slack" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

// Poll reads. It must not advance Slack's read marker.
func TestPollDoesNotMarkRead(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "1600000000.000000", moved, true, 1)}, nil)
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

	if got := srv.Calls("conversations.mark"); got != 0 {
		t.Errorf("poll marked %d conversations read; it must never touch the read marker", got)
	}
}

// Poll must not advance the agent's own position either — ack is a separate
// verb. Polling twice must report the same thing both times.
func TestPollIsIdempotent(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "1600000000.000000", moved, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "still here", moved)},
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
	second, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	if first.ResultCount != second.ResultCount || first.ResultCount != 1 {
		t.Errorf("poll is not idempotent: first %d events, second %d", first.ResultCount, second.ResultCount)
	}
}

// Events carry handles the model never has to assemble.
func TestEventsCarryDecodableHandles(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "1600000000.000000", moved, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "ship it", moved)},
		}
	})

	events := data(t, poll(t, srv, nil))["events"].([]map[string]any)
	if len(events) == 0 {
		t.Fatal("no events")
	}

	h, _ := events[0]["handle"].(string)
	ref, err := handle.Decode(h)
	if err != nil {
		t.Fatalf("event handle %q does not decode: %v", h, err)
	}
	if ref.Channel != "C1" || ref.TS != moved {
		t.Errorf("handle decoded to %+v, want channel C1 at %s", ref, moved)
	}

	// Nothing in the event should invite the model to build an ID itself.
	if _, leaked := events[0]["channelId"]; leaked {
		t.Error("event exposes a raw channel ID")
	}
}

// A first look has nothing recorded, so every conversation counts as unseen.
// It must be bounded, and must say that it was.
func TestFirstLookIsBoundedAndSaysSo(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "0", recent(time.Hour), true, 1),   // inside the window
				slacktest.Conversation("C2", "0", "1600000000.000000", true, 1), // years old
			},
			nil,
		)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "recent", recent(time.Hour))},
		}
	})

	d := data(t, poll(t, srv, nil))
	coverage := d["coverage"].(map[string]any)

	if !coverage["firstLook"].(bool) {
		t.Error("a fresh watermark did not report itself as a first look")
	}
	if got := coverage["conversationsMoved"].(int); got != 1 {
		t.Errorf("moved = %d, want 1 — the years-old conversation should fall outside the window", got)
	}
	if coverage["conversationsScanned"].(int) != 2 {
		t.Error("coverage should report everything scanned, not only what was returned")
	}
}

// A conversation already acknowledged at its newest message must be skipped
// outright — no history fetch. Without this only the first-look window is under
// test, and dropping the watermark comparison entirely would go unnoticed.
func TestAcknowledgedConversationIsNotRefetched(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "0", moved, true, 1), // acknowledged below
				slacktest.Conversation("C2", "0", moved, true, 1), // still unseen
			},
			nil,
		)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "new", moved)},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	// Record C1 as seen up to its newest message, the way ack will.
	store, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("watermark.Open: %v", err)
	}
	store.Ack("C1", moved, time.Now())
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv.ResetCalls()
	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	coverage := data(t, res)["coverage"].(map[string]any)
	if got := coverage["conversationsMoved"].(int); got != 1 {
		t.Errorf("moved = %d, want 1 — the acknowledged conversation should be skipped", got)
	}
	if got := srv.Calls("conversations.history"); got != 1 {
		t.Errorf("conversations.history called %d times, want 1 — the acknowledged conversation was refetched", got)
	}
}

// Slack caps a history page and reports has_more. Ignoring it drops the OLDEST
// messages in the range — exactly the ones the agent has not seen — and does so
// silently. The tick must page, and must report when it stopped early.
func TestMoreHistoryThanOnePageIsNotLostSilently(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", moved, true, 1)}, nil)
	})

	pages := 0
	srv.Handle("conversations.history", func(*http.Request) any {
		pages++
		return map[string]any{
			"ok": true, "has_more": true,
			"messages":          []any{slacktest.Message("U2", fmt.Sprintf("page %d", pages), moved)},
			"response_metadata": map[string]any{"next_cursor": fmt.Sprintf("c%d", pages)},
		}
	})

	d := data(t, poll(t, srv, nil))
	coverage := d["coverage"].(map[string]any)

	if pages < 2 {
		t.Errorf("history fetched %d page(s); has_more was ignored", pages)
	}
	if coverage["conversationsPartial"].(int) == 0 {
		t.Error("a conversation with more history than one tick fetches was not reported partial")
	}
	if coverage["complete"].(bool) {
		t.Error("coverage claims complete while messages were left unfetched")
	}
}

// A conversation whose newest message predates the first-look window produces
// no event, so it can never be acked and stays unknown forever. Flagging it as
// a first look would make every future tick claim to be one, permanently.
func TestOldUnknownConversationDoesNotStickFirstLook(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "0", moved, true, 1),               // acked below
				slacktest.Conversation("C2", "0", "1600000000.000000", true, 1), // years old, never ackable
			},
			nil,
		)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "new", moved)},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	// Steady state: the agent has acked everything it was ever shown.
	store, err := watermark.Open("Praecipio", watermark.DefaultScope)
	if err != nil {
		t.Fatalf("watermark.Open: %v", err)
	}
	store.Ack("C1", moved, time.Now())
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	coverage := data(t, res)["coverage"].(map[string]any)
	if coverage["firstLook"].(bool) {
		t.Error("a steady-state tick reported firstLook because an old conversation can never be acked")
	}
}

// conversationsRead must count conversations actually reached, not conversations
// selected, or a low limit reports breadth the tick never had.
func TestLimitIsReportedRatherThanHidden(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "0", moved, true, 1),
				slacktest.Conversation("C2", "0", moved, true, 1),
			},
			nil,
		)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "one", moved),
				slacktest.Message("U2", "two", moved),
			},
		}
	})

	d := data(t, poll(t, srv, map[string]any{"limit": float64(1)}))
	coverage := d["coverage"].(map[string]any)

	if got := len(d["events"].([]map[string]any)); got != 1 {
		t.Errorf("limit=1 returned %d events", got)
	}
	if !coverage["limitReached"].(bool) {
		t.Error("the event limit was hit without being reported")
	}
	if coverage["complete"].(bool) {
		t.Error("coverage claims complete while the limit truncated the tick")
	}
	if got := coverage["conversationsRead"].(int); got != 1 {
		t.Errorf("conversationsRead = %d; only one conversation was actually reached", got)
	}
}

// Naming must cost no requests. Resolving per conversation would put an
// unbounded number of conversations.info calls inside a tick sold as cheap.
func TestNamingCostsNoRequests(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("CZZZ", "0", moved, true, 1)}, nil)
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
	srv.ResetCalls()

	// CZZZ is deliberately not in the seeded channel cache, so a fetching
	// resolver would reach for it here.
	if _, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap}); err != nil {
		t.Fatalf("poll: %v", err)
	}

	if got := srv.Calls("conversations.info"); got != 0 {
		t.Errorf("conversations.info called %d times during a tick; naming must come from cache", got)
	}
}

// Slack never populates Name on IM channel objects, so a DM has to resolve
// through the user it is with or it shows a raw ID.
func TestDirectMessagesAreNamedAfterTheirUser(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)

	dm := slack.Channel{}
	dm.ID = "D1"
	dm.IsIM = true
	dm.User = "U2"
	srv.SeedChannels(dm)

	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(nil, []any{slacktest.Conversation("D1", "0", moved, true, 1)})
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "ping", moved)},
		}
	})

	events := data(t, poll(t, srv, nil))["events"].([]map[string]any)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if got := events[0]["where"]; got != "@Sarah Chen" {
		t.Errorf("DM named %q; want the user it is with, not a raw ID", got)
	}
}

// Events must not carry raw coordinates alongside the handle — a coordinate in
// the output is one the model will try to reuse.
func TestEventsCarryNoRawCoordinates(t *testing.T) {
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

	events := data(t, poll(t, srv, nil))["events"].([]map[string]any)
	for _, key := range []string{"ts", "channelId", "channel", "timestamp", "thread_ts"} {
		if _, leaked := events[0][key]; leaked {
			t.Errorf("event exposes raw coordinate %q", key)
		}
	}
}

// Slack's `inclusive` applies to both ends of a range. The fetch needs it for
// `latest`, which also re-includes the message sitting exactly on the
// watermark — already seen, and otherwise re-reported on every tick forever.
func TestTheAcknowledgedMessageIsNotReReported(t *testing.T) {
	srv := slacktest.New(t)
	seen := recent(2 * time.Hour)
	fresh := recent(time.Minute)

	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", fresh, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		// Slack returns the boundary message when inclusive is set.
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "new one", fresh),
				slacktest.Message("U2", "already acknowledged", seen),
			},
		}
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
	store.Ack("C1", seen, time.Now())
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	events := data(t, res)["events"].([]map[string]any)
	for _, e := range events {
		if e["preview"] == "already acknowledged" {
			t.Fatal("the message on the watermark was re-reported; it would come back on every tick")
		}
	}
	if len(events) != 1 {
		t.Errorf("want only the new message, got %d events", len(events))
	}
}

// A conversation lost to a fetch error is a conversation whose messages went
// unreported, so the tick is not complete.
func TestAFailedConversationMakesTheTickIncomplete(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", moved, true, 1)}, nil)
	})
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{"ok": false, "error": "channel_not_found"}
	})

	coverage := data(t, poll(t, srv, nil))["coverage"].(map[string]any)
	if coverage["conversationsFailed"].(int) == 0 {
		t.Error("a failed fetch was not counted")
	}
	if coverage["complete"].(bool) {
		t.Error("coverage claims complete while a conversation could not be read")
	}
}

// A backlog larger than one tick reads must be reported as a backlog, not as a
// slice of the newest messages with the oldest silently missing.
func TestOverflowReportsBacklogRatherThanTheNewestSlice(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "0", moved, true, 1)}, nil)
	})
	page := 0
	srv.Handle("conversations.history", func(*http.Request) any {
		page++
		return map[string]any{
			"ok": true, "has_more": true,
			"messages":          []any{slacktest.Message("U2", fmt.Sprintf("newest slice %d", page), moved)},
			"response_metadata": map[string]any{"next_cursor": fmt.Sprintf("c%d", page)},
		}
	})

	d := data(t, poll(t, srv, nil))
	events := d["events"].([]map[string]any)

	if len(events) != 1 || events[0]["kind"] != "backlog" {
		t.Fatalf("want a single backlog event, got %+v", events)
	}
	for _, e := range events {
		if s, _ := e["preview"].(string); strings.Contains(s, "newest slice") {
			t.Error("returned the newest slice, whose missing half is the half nearest the watermark")
		}
	}
	if d["coverage"].(map[string]any)["complete"].(bool) {
		t.Error("coverage claims complete while a backlog went unread")
	}
}

// Coverage is a field, not prose, so a caller can detect an incomplete tick.
func TestCoverageIsStructured(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts([]any{slacktest.Conversation("C1", "1600000000.000000", "1600000000.000000", false, 0)}, nil)
	})

	coverage := data(t, poll(t, srv, nil))["coverage"].(map[string]any)
	for _, key := range []string{"conversationsScanned", "conversationsMoved", "complete", "firstLook", "window"} {
		if _, ok := coverage[key]; !ok {
			t.Errorf("coverage has no %q field", key)
		}
	}
}

// One unreadable conversation must not cost the rest of the tick.
func TestOneBadConversationDoesNotLoseTheTick(t *testing.T) {
	srv := slacktest.New(t)
	moved := recent(time.Hour)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "0", moved, true, 1),
				slacktest.Conversation("C2", "0", moved, true, 1),
			},
			nil,
		)
	})
	calls := 0
	srv.Handle("conversations.history", func(*http.Request) any {
		calls++
		if calls == 1 {
			return map[string]any{"ok": false, "error": "channel_not_found"}
		}
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", "survived", moved)},
		}
	})

	events := data(t, poll(t, srv, nil))["events"].([]map[string]any)

	var sawUnreadable, sawSurvivor bool
	for _, e := range events {
		if e["kind"] == "unreadable" {
			sawUnreadable = true
		}
		if e["preview"] == "survived" {
			sawSurvivor = true
		}
	}
	if !sawUnreadable {
		t.Error("the failing conversation was dropped silently instead of reported")
	}
	if !sawSurvivor {
		t.Error("one failing conversation lost the rest of the tick")
	}
}

// Scopes keep two agents from consuming each other's position.
func TestScopeSeparatesPositions(t *testing.T) {
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

	a, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap, "scope": "monitor"})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	b, err := features.Poll.Handler(context.Background(), map[string]any{"_provider": ap, "scope": "briefing"})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	if a.ResultCount == 0 || b.ResultCount == 0 {
		t.Errorf("scopes shared a position: monitor got %d events, briefing got %d", a.ResultCount, b.ResultCount)
	}
	if watermark.DefaultScope == "monitor" {
		t.Fatal("test assumes the default scope differs from the ones used here")
	}
}
