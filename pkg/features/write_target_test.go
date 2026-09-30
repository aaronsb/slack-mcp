package features_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
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

// Exact handles, '@me', and a literal user ID resolve — through the ladder,
// with the DM opened once, whether or not the '@' is there.
func TestSayExactTargetsStillResolve(t *testing.T) {
	for _, to := range []string{"@alice", "alice", "@ALICE", "U100ALICE1", "@me"} {
		t.Run(to, func(t *testing.T) {
			srv := writeTargetServer(t)
			res := sayTo(t, srv, to)
			if !res.Success {
				t.Fatalf("exact target %q should send: %s", to, res.Message)
			}
			if srv.Calls("chat.postMessage") != 1 || srv.Calls("conversations.open") != 1 {
				t.Errorf("want one open and one post, got open=%d post=%d",
					srv.Calls("conversations.open"), srv.Calls("chat.postMessage"))
			}
		})
	}
}

// ADR-005: a write auto-resolves only on an exact handle. An exact real name
// is offered back as the single candidate with its handle, unsent.
func TestSayExactRealNameOffersTheHandle(t *testing.T) {
	for _, to := range []string{"Alice Martin", "@Alice Martin"} {
		t.Run(to, func(t *testing.T) {
			srv := writeTargetServer(t)
			res := sayTo(t, srv, to)
			if res.Success {
				t.Fatalf("a real name must not send: %s", res.Message)
			}
			if !strings.Contains(res.Message, "Did you mean @alice (Alice Martin)?") ||
				!strings.Contains(res.Guidance, "to='@alice'") {
				t.Errorf("want the handle offered, got:\n%s\n%s", res.Message, res.Guidance)
			}
			assertNoWrite(t, srv)
		})
	}
}

func assertNoWrite(t *testing.T, srv *slacktest.Server) {
	t.Helper()
	for _, m := range []string{"chat.postMessage", "conversations.open", "reactions.add"} {
		if n := srv.Calls(m); n != 0 {
			t.Errorf("no Slack write expected, %s was called %d times", m, n)
		}
	}
}

// ambiguityServer holds the shapes the bare-word path used to mis-route:
// duplicate real names, a deactivated user, and a handle that is also a
// channel name with an open DM.
func ambiguityServer(t *testing.T) (*slacktest.Server, *[]string) {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U100JOHNA1", Name: "jsmith", RealName: "John Smith"},
		slack.User{ID: "U100JOHNB1", Name: "johns", RealName: "John Smith"},
		slack.User{ID: "U100GONE01", Name: "gone", RealName: "Gone Person", Deleted: true},
		slack.User{ID: "U100ENG001", Name: "eng", RealName: "Eng Bot"},
	)
	var eng, dm slack.Channel
	eng.ID, eng.Name, eng.IsChannel, eng.IsMember = "C1", "eng", true, true
	dm.ID, dm.IsIM, dm.User = "D77", true, "U100ENG001"
	srv.SeedChannels(eng, dm)

	posted := &[]string{}
	srv.Handle("chat.postMessage", func(r *http.Request) any {
		_ = r.ParseForm()
		*posted = append(*posted, r.FormValue("channel"))
		return map[string]any{"ok": true, "channel": r.FormValue("channel"), "ts": "1786752114.508819"}
	})
	srv.Handle("conversations.open", func(r *http.Request) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D9"}}
	})
	return srv, posted
}

func sayWith(t *testing.T, ap *provider.ApiProvider, to string) *features.FeatureResult {
	t.Helper()
	res, err := features.WriteMessage.Handler(context.Background(), map[string]any{
		"_provider": ap, "channel": to, "message": "hi",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return res
}

// A bare real name shared by two people used to DM whichever the users map
// iterated first. It now fails with both, and nothing is opened or sent.
func TestSayBareDuplicateRealNameFails(t *testing.T) {
	srv, _ := ambiguityServer(t)
	ap := bootedProvider(t, srv)
	srv.ResetCalls()

	res := sayWith(t, ap, "John Smith")
	if res.Success {
		t.Fatalf("an ambiguous bare name must not send: %s", res.Message)
	}
	for _, want := range []string{"@jsmith", "@johns"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("want candidate %s, got:\n%s", want, res.Message)
		}
	}
	assertNoWrite(t, srv)
}

// A deactivated user is never a write target, by bare name or by handle.
func TestSayDeactivatedUserFails(t *testing.T) {
	for _, to := range []string{"Gone Person", "gone", "@gone"} {
		t.Run(to, func(t *testing.T) {
			srv, _ := ambiguityServer(t)
			ap := bootedProvider(t, srv)
			srv.ResetCalls()

			if res := sayWith(t, ap, to); res.Success {
				t.Fatalf("a deactivated user must not be messaged: %s", res.Message)
			}
			assertNoWrite(t, srv)
		})
	}
}

// '#eng' is the channel even when a user's handle is 'eng' and their DM is
// indexed; '@eng' is that person's DM; a bare 'eng' names both and asks.
func TestSayChannelAndHandleShareAName(t *testing.T) {
	cases := []struct {
		to, wantChannel string
	}{
		{"#eng", "C1"},
		{"@eng", "D77"},
	}
	for _, c := range cases {
		t.Run(c.to, func(t *testing.T) {
			srv, posted := ambiguityServer(t)
			ap := bootedProvider(t, srv)
			srv.ResetCalls()

			res := sayWith(t, ap, c.to)
			if !res.Success {
				t.Fatalf("%s should send: %s", c.to, res.Message)
			}
			if len(*posted) != 1 || (*posted)[0] != c.wantChannel {
				t.Errorf("%s posted to %v, want [%s]", c.to, *posted, c.wantChannel)
			}
		})
	}

	t.Run("bare", func(t *testing.T) {
		srv, _ := ambiguityServer(t)
		ap := bootedProvider(t, srv)
		srv.ResetCalls()

		res := sayWith(t, ap, "eng")
		if res.Success {
			t.Fatalf("a bare word naming a channel and a person must ask: %s", res.Message)
		}
		if !strings.Contains(res.Guidance, "to='#eng'") || !strings.Contains(res.Guidance, "to='@eng'") {
			t.Errorf("want both readings offered, got: %s", res.Guidance)
		}
		assertNoWrite(t, srv)
	})
}

// An all-caps word is not a conversation ID: it goes to the ladder, which
// finds no one, and nothing is posted.
func TestSayAllCapsWordIsNotAnID(t *testing.T) {
	srv := writeTargetServer(t)
	res := sayTo(t, srv, "DAVE")
	if res.Success {
		t.Fatalf("DAVE is not a channel ID: %s", res.Message)
	}
	assertNoWrite(t, srv)
}

func TestReactBareAmbiguousNameFailsWithNoWrite(t *testing.T) {
	srv, _ := ambiguityServer(t)
	ap := bootedProvider(t, srv)
	srv.ResetCalls()

	res, err := features.React.Handler(context.Background(), map[string]any{
		"_provider": ap, "channel": "John Smith",
		"messageTs": "1786752114.508819", "emoji": "tada",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Success {
		t.Fatalf("an ambiguous bare name must not react: %s", res.Message)
	}
	assertNoWrite(t, srv)
}

// Reads keep ADR-005's read policy: around= takes a unique fragment like
// since= does, and a miss renders the same candidate view.
func TestMessagesAroundReadsAUniqueFragment(t *testing.T) {
	srv := writeTargetServer(t)
	ap := bootedProvider(t, srv)
	srv.ResetCalls()

	out := runTool(t, features.Messages, ap, map[string]any{"target": "@turing", "around": "1786752114.508819"})
	if strings.Contains(out, "not an exact handle") || strings.Contains(out, "Could not resolve") {
		t.Fatalf("a unique fragment should resolve on a read:\n%s", out)
	}
	if srv.Calls("conversations.replies")+srv.Calls("conversations.history") == 0 {
		t.Errorf("the read never reached the conversation")
	}
	if srv.Calls("chat.postMessage") != 0 {
		t.Errorf("a read must not post")
	}
}

func TestMessagesAroundMissMatchesSinceMiss(t *testing.T) {
	srv := writeTargetServer(t)
	ap := bootedProvider(t, srv)

	around := runTool(t, features.Messages, ap, map[string]any{"target": "@nosuchperson", "around": "1786752114.508819"})
	since := runTool(t, features.Messages, ap, map[string]any{"target": "@nosuchperson", "since": "1d"})
	for name, out := range map[string]string{"around": around, "since": since} {
		if !strings.Contains(out, "Could not resolve") {
			t.Errorf("%s miss not rendered as a candidate view:\n%s", name, out)
		}
	}
	if !strings.Contains(around, "`messages target='@nosuchperson' around=1786752114.508819`") {
		t.Errorf("around miss lost the echo:\n%s", around)
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
