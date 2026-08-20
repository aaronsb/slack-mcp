package features_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// Slack Connect externals are visible to users.info and invisible to
// users.list, so the bulk caches never learn them and their mention tags
// rendered raw forever. The tag resolver now repairs the miss on demand —
// one bounded users.info per stranger, patched into the cache and estate.

func externalUserInfo(t *testing.T) func(*http.Request) any {
	t.Helper()
	return func(r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("users.info form: %v", err)
		}
		id := r.Form.Get("user")
		return map[string]any{
			"ok": true,
			"user": map[string]any{
				"id":   id,
				"name": "ext-" + strings.ToLower(id),
				"profile": map[string]any{
					"real_name":    "External " + id,
					"display_name": "External " + id,
				},
			},
		}
	}
}

func TestUnknownUserTagsRepairThroughUsersInfo(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("users.info", externalUserInfo(t))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{map[string]any{
				"type": "message", "user": "U2", "ts": "1782246200.000000",
				"text": "intro from <@UEXTCONN1> re the partner call",
			}},
		}
	})
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	res, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := features.FormatResult("messages", res)
	if !strings.Contains(out, "@External UEXTCONN1") {
		t.Fatalf("external mention did not repair:\n%s", out)
	}
	if strings.Contains(out, "<@UEXTCONN1>") {
		t.Fatalf("raw tag survived a successful repair:\n%s", out)
	}
	if calls := srv.Calls("users.info"); calls != 1 {
		t.Fatalf("repair cost %d users.info calls, want 1", calls)
	}

	// The patch persists: a second read renders from the cache.
	srv.ResetCalls()
	if _, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	}); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if calls := srv.Calls("users.info"); calls != 0 {
		t.Fatalf("second render cost %d users.info calls, want 0", calls)
	}
}

func TestTagRepairIsBudgeted(t *testing.T) {
	mentions := make([]string, 0, 8)
	for i := 1; i <= 8; i++ {
		mentions = append(mentions, fmt.Sprintf("<@UBUDGET%02d>", i))
	}
	srv := slacktest.New(t)
	srv.Handle("users.info", externalUserInfo(t))
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{map[string]any{
				"type": "message", "user": "U2", "ts": "1782246200.000000",
				"text": "cc " + strings.Join(mentions, " "),
			}},
		}
	})
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	res, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := features.FormatResult("messages", res)
	if calls := srv.Calls("users.info"); calls != 5 {
		t.Fatalf("budget did not hold: %d users.info calls, want 5", calls)
	}
	if !strings.Contains(out, "@External UBUDGET01") {
		t.Fatalf("budgeted repairs did not render:\n%s", out)
	}
	if !strings.Contains(out, "<@UBUDGET08>") {
		t.Fatalf("over-budget tag should stay raw for the repair queue:\n%s", out)
	}
}
