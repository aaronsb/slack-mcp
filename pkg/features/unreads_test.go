package features_test

import (
	"net/http"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// A DM's unread count is the messages after its last_read, each marked,
// and a channel's mentions leave out your own join event.
func TestUnreadsCountFromLastReadAndSkipJoins(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(dmWith("D1", "U2"), channel("C1", "engineering"))
	srv.Handle("conversations.history", func(r *http.Request) any {
		_ = r.ParseForm()
		switch r.Form.Get("channel") {
		case "D1":
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				slacktest.Message("U2", "third", "1782246300.000000"),
				slacktest.Message("U2", "second", "1782246200.000000"),
				slacktest.Message("U2", "first", "1782246100.000000"),
			}}
		case "C1":
			join := slacktest.Message("U1", "<@U1> has joined the channel", "1782246250.000000")
			join["subtype"] = "channel_join"
			return map[string]any{"ok": true, "has_more": false, "messages": []any{
				join,
				slacktest.Message("U2", "<@U1> real ask", "1782246200.000000"),
			}}
		}
		return nil
	})
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{slacktest.Conversation("C1", "1782246000.000000", "1782246250.000000", true, 2)},
			[]any{slacktest.Conversation("D1", "1782246150.000000", "1782246300.000000", true, 0)},
		)
	})

	res := run(t, srv, features.CheckUnreads, map[string]any{})
	unreads := res.Data.(map[string]any)["unreads"].(map[string]any)

	dm := unreads["dms"].([]map[string]any)[0]
	if got := dm["unreadCount"]; got != 2 {
		t.Errorf("unreadCount = %v, want 2 (messages after last_read)", got)
	}
	var flags []bool
	for _, m := range dm["messages"].([]map[string]any) {
		flags = append(flags, m["unread"].(bool))
	}
	if len(flags) != 3 || flags[0] || !flags[1] || !flags[2] {
		t.Errorf("unread flags oldest first = %v, want [false true true]", flags)
	}

	mentions := unreads["mentions"].([]map[string]any)
	if len(mentions) != 1 || mentions[0]["message"] != "@Aaron Bockelie (you) real ask" {
		t.Errorf("mentions = %v, want only the real ask", mentions)
	}
}
