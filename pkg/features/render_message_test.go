package features_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// The renderMessage seam (#63): every render path goes through one
// normalizer, so a blocks-only announcement (#45) gets a body and an
// author everywhere, not just in the paths someone remembered to fix.

func blocksOnlyMessage(ts string) map[string]any {
	return map[string]any{
		"type":     "message",
		"user":     "",
		"username": "Workflow Builder",
		"ts":       ts,
		"text":     "",
		"reactions": []any{
			map[string]any{"name": "tada", "count": 33, "users": []any{"U1"}},
		},
		"blocks": []any{
			map[string]any{
				"type": "header",
				"text": map[string]any{"type": "plain_text", "text": "Big Announcement"},
			},
			map[string]any{
				"type": "rich_text",
				"elements": []any{
					map[string]any{
						"type": "rich_text_section",
						"elements": []any{
							map[string]any{"type": "text", "text": "Welcome "},
							map[string]any{"type": "user", "user_id": "U2"},
							map[string]any{"type": "text", "text": " to the team!"},
						},
					},
				},
			},
		},
	}
}

func TestBlocksOnlyMessagesRenderWithBodyAndAuthor(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{blocksOnlyMessage("1782246200.000000")},
		}
	})
	ap := srv.Provider(t)

	// catch-up path (messages target+since)
	res, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng", "since": "1d",
	})
	if err != nil {
		t.Fatalf("catch-up: %v", err)
	}
	out := features.FormatResult("messages", res)
	if !strings.Contains(out, "Big Announcement") {
		t.Fatalf("blocks body missing from catch-up:\n%s", out)
	}
	if !strings.Contains(out, "Welcome @Sarah Chen to the team!") {
		t.Fatalf("rich-text user element not resolved:\n%s", out)
	}
	if !strings.Contains(out, "Workflow Builder (app)") {
		t.Fatalf("app author missing:\n%s", out)
	}
	if strings.Contains(out, "unknown") {
		t.Fatalf("author rendered as unknown:\n%s", out)
	}

	// read path (bare target)
	res, err = features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out = features.FormatResult("messages", res)
	if !strings.Contains(out, "Big Announcement") || !strings.Contains(out, "Workflow Builder (app)") {
		t.Fatalf("blocks body or author missing from read:\n%s", out)
	}
}

func TestAttachmentFallbackFillsEmptyBodies(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{map[string]any{
				"type": "message", "user": "U2", "ts": "1782246200.000000", "text": "",
				"attachments": []any{map[string]any{"fallback": "Q3 numbers attached: revenue up 12%"}},
			}},
		}
	})
	ap := srv.Provider(t)

	res, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := features.FormatResult("messages", res)
	if !strings.Contains(out, "Q3 numbers attached") {
		t.Fatalf("attachment fallback missing:\n%s", out)
	}
}

func TestUnresolvedTagsAreReportedNotJustRaw(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{map[string]any{
				"type": "message", "user": "U2", "ts": "1782246200.000000",
				"text": "ping <@UNOTINWORKSPACE9> about this",
			}},
		}
	})
	ap := srv.Provider(t)

	res, err := features.Messages.Handler(context.Background(), map[string]any{
		"_provider": ap, "target": "#eng",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	data := res.Data.(map[string]interface{})
	msgs := data["messages"].([]map[string]interface{})
	unresolved, ok := msgs[0]["unresolved"].([]string)
	if !ok || len(unresolved) != 1 || unresolved[0] != "<@UNOTINWORKSPACE9>" {
		t.Fatalf("unresolved tag not reported: %+v", msgs[0])
	}
	out := features.FormatResult("messages", res)
	if !strings.Contains(out, "<@UNOTINWORKSPACE9>") {
		t.Fatalf("raw tag vanished from the body:\n%s", out)
	}
}
