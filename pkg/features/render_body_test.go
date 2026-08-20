package features_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// The rendered body is the agent's whole view of a message (ADR-004):
// mentions resolve to names, the agent's own user is marked, departed users
// answer with their dated exit, and what cannot resolve stays visibly raw.
func TestMessageBodiesResolveTheirTags(t *testing.T) {
	srv := slacktest.New(t)

	// U0GONE001 existed and was deactivated; only the fold remembers.
	seedEstate(t, func(st *estate.Store) {
		gone := slack.User{ID: "U0GONE001", Name: "leaver", RealName: "Lee Ver", Deleted: true}
		if _, err := st.ObserveUsers([]slack.User{gone}, false, estate.SourceTraffic,
			time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	})

	body := "hey <@U2>, <@U1> and <@U0GONE001> — see <#C1|eng> <!here> (mystery: <@U0MYSTERY9>)"
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{slacktest.Message("U2", body, "1782246118.543969")},
		}
	})

	ap := bootedProvider(t, srv)
	res, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": ap, "channel": "eng",
	})
	if err != nil {
		t.Fatalf("get-context: %v", err)
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(raw)

	if !strings.Contains(out, "@Sarah Chen") {
		t.Fatalf("mention did not resolve: %s", out)
	}
	if !strings.Contains(out, "@Aaron Bockelie (you)") {
		t.Fatalf("self-mention not marked: %s", out)
	}
	if !strings.Contains(out, "@Lee Ver (deactivated 2026-05-12)") {
		t.Fatalf("departed mention lost its dated exit: %s", out)
	}
	if !strings.Contains(out, "#eng") {
		t.Fatalf("channel tag did not resolve: %s", out)
	}
	if !strings.Contains(out, "@here") {
		t.Fatalf("broadcast tag did not rewrite: %s", out)
	}
	if !strings.Contains(out, "\\u003c@U0MYSTERY9\\u003e") && !strings.Contains(out, "<@U0MYSTERY9>") {
		t.Fatalf("unresolvable tag was hidden instead of left visible: %s", out)
	}
}
