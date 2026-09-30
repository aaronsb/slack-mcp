package features_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// Writes never auto-pick below an exact handle (ADR-005, ADR-009): a partial
// name fails with candidates and no Slack write, for say and for reactions.

func writeTargetServer(t *testing.T) *slacktest.Server {
	t.Helper()
	srv := slacktest.New(t)
	mk := func(id, handle, real string) slack.User {
		return slack.User{ID: id, Name: handle, RealName: real}
	}
	srv.SeedUsers(
		mk("U1", "bockeliea", "Aaron Bockelie"),
		mk("U100ALICE1", "alice", "Alice Martin"),
		mk("U100ALAN01", "alan", "Alan Turing"),
		mk("U100SALLY1", "sal", "Sal Mineo"),
	)
	srv.Handle("conversations.open", func(r *http.Request) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D9"}}
	})
	return srv
}

func sayTo(t *testing.T, srv *slacktest.Server, to string) *features.FeatureResult {
	t.Helper()
	res, err := features.WriteMessage.Handler(context.Background(), map[string]any{
		"_provider": srv.Provider(t), "channel": to, "message": "hi",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return res
}

func TestSayPartialNameFailsWithCandidatesAndNoWrite(t *testing.T) {
	srv := writeTargetServer(t)
	res := sayTo(t, srv, "@al")
	if res.Success {
		t.Fatalf("partial name must not send: %s", res.Message)
	}
	for _, want := range []string{"@alice", "Alice Martin", "@alan", "Alan Turing"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("error should list %q, got:\n%s", want, res.Message)
		}
	}
	if strings.Contains(res.Message, "U100") {
		t.Errorf("error leaks an ID:\n%s", res.Message)
	}
	if srv.Calls("chat.postMessage") != 0 || srv.Calls("conversations.open") != 0 {
		t.Errorf("no Slack write expected: post=%d open=%d",
			srv.Calls("chat.postMessage"), srv.Calls("conversations.open"))
	}
}

func TestSayUniqueFragmentIsNotAutoPicked(t *testing.T) {
	srv := writeTargetServer(t)
	res := sayTo(t, srv, "@turing")
	if res.Success || srv.Calls("chat.postMessage") != 0 || srv.Calls("conversations.open") != 0 {
		t.Fatalf("a lone fragment match must not send: %+v", res)
	}
	if !strings.Contains(res.Message, "@alan") {
		t.Errorf("the lone match should be offered as a candidate:\n%s", res.Message)
	}
}

func TestSayExactTargetsStillResolve(t *testing.T) {
	for _, to := range []string{"@alice", "alice", "@ALICE", "Alice Martin", "@Alice Martin", "U100ALICE1"} {
		t.Run(to, func(t *testing.T) {
			srv := writeTargetServer(t)
			res := sayTo(t, srv, to)
			if !res.Success {
				t.Fatalf("exact target %q should send: %s", to, res.Message)
			}
			if srv.Calls("chat.postMessage") != 1 {
				t.Errorf("want one post, got %d", srv.Calls("chat.postMessage"))
			}
		})
	}
}

func TestReactPartialNameFailsWithNoWrite(t *testing.T) {
	srv := writeTargetServer(t)
	res, err := features.React.Handler(context.Background(), map[string]any{
		"_provider": srv.Provider(t), "channel": "@al",
		"messageTs": "1786752114.508819", "emoji": "tada",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Success || !strings.Contains(res.Message, "@alice") || !strings.Contains(res.Message, "@alan") {
		t.Fatalf("want failure listing both candidates, got %+v", res)
	}
	if srv.Calls("reactions.add") != 0 || srv.Calls("conversations.open") != 0 {
		t.Errorf("no Slack write expected")
	}
}

func TestReactExactHandleStillResolves(t *testing.T) {
	srv := writeTargetServer(t)
	res, err := features.React.Handler(context.Background(), map[string]any{
		"_provider": srv.Provider(t), "channel": "@alan",
		"messageTs": "1786752114.508819", "emoji": "tada",
	})
	if err != nil || !res.Success {
		t.Fatalf("exact handle should react: %v %+v", err, res)
	}
	if srv.Calls("reactions.add") != 1 {
		t.Errorf("want one reaction, got %d", srv.Calls("reactions.add"))
	}
}
