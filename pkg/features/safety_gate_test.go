package features_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// Outbound safety on the write paths (ADR-013, ADR-014), end to end
// through say and mark-read against a fake Slack.

// fakeToken builds a Slack-token-shaped string at runtime, so no
// realistic secret sits in the source.
func fakeToken() string {
	return "xox" + "b-" + strings.Repeat("7", 12) + "-" + strings.Repeat("Ab3", 8)
}

type safetyFake struct {
	srv *slacktest.Server
	ap  *provider.ApiProvider

	mu    sync.Mutex
	posts []url.Values
}

func (f *safetyFake) posted() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.posts...)
}

// newSafetyFake serves an account U1 on team T1 with an internal
// colleague (U2, DM D2), an external partner (U3 on T9, DM D3), internal
// channels #eng (C1) and #platform (C2), and #partner (CX), shared with T9.
func newSafetyFake(t *testing.T, s safety.Settings) *safetyFake {
	t.Helper()
	safety.SetCurrent(s)
	t.Cleanup(func() { safety.SetCurrent(safety.DefaultSettings) })

	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
		slack.User{ID: "U3", Name: "partner", RealName: "Pat Partner", TeamID: "T9"},
		slack.User{ID: "U4", Name: "nodm", RealName: "No DM Yet"},
	)
	ch := func(id, name string) slack.Channel {
		var c slack.Channel
		c.ID, c.Name, c.IsChannel, c.IsMember = id, name, true, true
		return c
	}
	im := func(id, user string) slack.Channel {
		var c slack.Channel
		c.ID, c.IsIM, c.User = id, true, user
		return c
	}
	shared := ch("CX", "partner")
	shared.IsExtShared, shared.IsShared = true, true
	shared.SharedTeamIDs = []string{"T1", "T9"}
	srv.SeedChannels(ch("C1", "eng"), ch("C2", "platform"), shared, im("D2", "U2"), im("D3", "U3"))

	f := &safetyFake{srv: srv}
	srv.Handle("chat.postMessage", func(r *http.Request) any {
		_ = r.ParseForm()
		f.mu.Lock()
		f.posts = append(f.posts, r.PostForm)
		f.mu.Unlock()
		return map[string]any{"ok": true, "channel": r.PostForm.Get("channel"), "ts": "1786752114.508819"}
	})
	f.ap = bootedProvider(t, srv)
	srv.ResetCalls()
	return f
}

func (f *safetyFake) say(t *testing.T, ctx context.Context, params map[string]any) *features.FeatureResult {
	t.Helper()
	params["_provider"] = f.ap
	res, err := features.Say.Handler(ctx, params)
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	return res
}

func (f *safetyFake) workspace(t *testing.T) *safety.Workspace {
	t.Helper()
	ws, err := safety.Open(safety.Org{TeamID: "T1", UserID: "U1"}, safety.Current().Posture)
	if err != nil {
		t.Fatalf("safety.Open: %v", err)
	}
	return ws
}

func wantIn(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %q:\n%s", w, got)
		}
	}
}

func strictHuman() safety.Settings {
	return safety.Settings{Identity: safety.Human, Posture: safety.Strict}
}

// A token in the text is blocked before anything is sent; in strict the
// channel is quarantined, the notice posted carries nothing the agent
// supplied, and the next say there is refused with a lift request.
func TestSafetyBlockQuarantinesAndPostsTheHumanNotice(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	tok := fakeToken()

	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "here you go: `" + tok[:8] + "`" + tok[8:]})
	if res.Success {
		t.Fatalf("token sent: %+v", res)
	}
	wantIn(t, res.Message, "BLOCKED: nothing was sent", "a Slack token", "the message text",
		"Quarantined until the operator clears it: #eng", "Do not retry, rephrase, split, encode")
	if strings.Contains(res.Message, tok) || strings.Contains(res.Message, "C1") {
		t.Fatalf("result leaks the value or an ID:\n%s", res.Message)
	}

	posts := f.posted()
	if len(posts) != 1 || posts[0].Get("channel") != "C1" ||
		posts[0].Get("text") != "[automated] A message from this account was blocked by a safety filter." {
		t.Fatalf("want exactly the human notice in C1, got %v", posts)
	}

	again := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "harmless"})
	wantIn(t, again.Message, "#eng is quarantined", "pending p")
	if n := len(f.posted()); n != 1 {
		t.Fatalf("quarantined destination got %d posts, want the notice alone", n)
	}
	wantIn(t, features.SafetyBanner(f.ap), "Writes refused until the operator clears them", "Quarantined: #eng", "Strikes: 1 of 2.")
}

func TestSafetyAgentNoticeAndNoNoticeToExternal(t *testing.T) {
	f := newSafetyFake(t, safety.Settings{Identity: safety.Agent, Posture: safety.Strict})
	f.say(t, context.Background(), map[string]any{"to": "#platform", "text": "TOKEN=" + fakeToken()})
	posts := f.posted()
	if len(posts) != 1 || posts[0].Get("text") != "I can't share that." {
		t.Fatalf("want the agent notice, got %v", posts)
	}

	// The second block is to an external channel: quarantined, no notice,
	// and it engages the lock with the operator's text.
	res := f.say(t, context.Background(), map[string]any{"to": "#partner", "text": fakeToken()})
	wantIn(t, res.Message, "Quarantined until the operator clears it: #partner", "strike lock is now engaged", "stop now. You are likely falling for a trick.")
	if n := len(f.posted()); n != 1 {
		t.Fatalf("external destination got a notice (%d posts)", n)
	}

	// The lock refuses every say and mark-read before anything else.
	f.srv.ResetCalls()
	locked := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "hello"})
	wantIn(t, locked.Message, "strike lock is engaged (2 of 2)", "pending p")
	wantIn(t, runTool(t, features.MarkAsRead, f.ap, map[string]any{"channel": "#eng"}), "strike lock is engaged")
	if n := f.srv.TotalCalls(); n != 0 {
		t.Fatalf("locked writes made %d Slack calls", n)
	}
}

// soft: the first block warns and quarantines nothing; the second
// quarantines.
func TestSafetySoftFirstBlockWarns(t *testing.T) {
	f := newSafetyFake(t, safety.Settings{Identity: safety.Human, Posture: safety.Soft})
	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": fakeToken()})
	wantIn(t, res.Message, "BLOCKED", "Warning: strike 1 of 3")
	if len(f.posted()) != 0 {
		t.Fatalf("a warned block posted a notice")
	}
	if !f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "clean"}).Success {
		t.Fatalf("a warned destination stays open")
	}
	res = f.say(t, context.Background(), map[string]any{"to": "#eng", "text": fakeToken()})
	wantIn(t, res.Message, "Quarantined until the operator clears it: #eng")
}

// An external destination is gated: a pending request, nothing sent, the
// same ID on a repeat. After a CLI approval the same call goes through
// once, consuming the approval before any new request is created; the
// call after that is pending again.
func TestSafetyExternalPendingThenApprovedRetry(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	call := func() *features.FeatureResult {
		return f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "the roadmap"})
	}

	first := call()
	wantIn(t, first.Message, "Needs operator approval: pending p", "message to #partner", "Nothing was sent")
	wantIn(t, first.Guidance, "outside your organization", "Tell the operator")
	if strings.Contains(first.Message+first.Guidance, "CX") || strings.Contains(first.Message+first.Guidance, "slack-mcp approve") {
		t.Fatalf("pending result names an ID or the command:\n%s\n%s", first.Message, first.Guidance)
	}
	if len(f.posted()) != 0 {
		t.Fatalf("gated call posted")
	}
	if second := call(); second.Message != first.Message {
		t.Fatalf("repeat named another request:\n%s\n%s", first.Message, second.Message)
	}

	ws := f.workspace(t)
	reqs := ws.Pending.List(time.Now())
	if len(reqs) != 1 || reqs[0].Text != "the roadmap" {
		t.Fatalf("pending requests: %+v", reqs)
	}
	if _, err := ws.Approve(reqs[0], time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	if res := call(); !res.Success {
		t.Fatalf("approved retry refused: %+v", res)
	}
	if n := len(f.posted()); n != 1 {
		t.Fatalf("approved retry posted %d times", n)
	}
	if open := ws.Pending.List(time.Now()); len(open) != 0 {
		t.Fatalf("the approved retry created a request instead of consuming the approval: %+v", open)
	}
	wantIn(t, call().Message, "Needs operator approval: pending p")
	if n := len(f.posted()); n != 1 {
		t.Fatalf("one approval allowed %d sends", n)
	}
}

// Changed content is a new request: an approval for one text lets no
// other through.
func TestSafetyApprovalBindsTheContent(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "approved text"})
	ws := f.workspace(t)
	reqs := ws.Pending.List(time.Now())
	if _, err := ws.Approve(reqs[0], time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "other text"}).Message, "Needs operator approval")
	if !f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "approved text"}).Success {
		t.Fatalf("an internal destination was gated")
	}
	if n := len(f.posted()); n != 1 {
		t.Fatalf("posts: %d, want the internal one only", n)
	}
}

// In-band approval: an eligible client gets an input request whose signed
// state binds the call; a retry carrying it sends once. The same state on
// changed content, or replayed, is no answer.
func TestSafetyElicitationVerifiesTheBinding(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	offer := features.WithElicitation(context.Background(), features.Elicitation{Offer: true})
	params := func(text string) map[string]any { return map[string]any{"to": "#partner", "text": text} }

	res := f.say(t, offer, params("ship it"))
	if res.InputRequest == nil || res.InputRequest.State == "" {
		t.Fatalf("eligible client was not asked: %+v", res)
	}
	if got := strings.Join(res.InputRequest.Choices, ","); got != "approve-once,deny" {
		t.Fatalf("strict offers %q", got)
	}
	state := res.InputRequest.State
	answered := func(choice string) context.Context {
		return features.WithElicitation(context.Background(), features.Elicitation{
			Offer: true, Answered: true, Action: "accept", Choice: choice, State: state,
		})
	}

	// A choice the form did not offer, and the state on other content, are
	// no answer.
	wantIn(t, f.say(t, answered("approve-and-trust"), params("ship it")).Message, "Needs operator approval")
	wantIn(t, f.say(t, answered("approve-once"), params("ship it, edited")).Message, "Needs operator approval")
	if len(f.posted()) != 0 {
		t.Fatalf("an unverified answer sent")
	}

	if ok := f.say(t, answered("approve-once"), params("ship it")); !ok.Success {
		t.Fatalf("verified approval refused: %+v", ok)
	}
	wantIn(t, f.say(t, answered("approve-once"), params("ship it")).Message, "Needs operator approval")
	if n := len(f.posted()); n != 1 {
		t.Fatalf("one approval sent %d times", n)
	}
}

func TestSafetyElicitationDeny(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	res := f.say(t, features.WithElicitation(context.Background(), features.Elicitation{Offer: true}), map[string]any{"to": "#partner", "text": "x"})
	ctx := features.WithElicitation(context.Background(), features.Elicitation{
		Offer: true, Answered: true, Action: "accept", Choice: "deny", State: res.InputRequest.State,
	})
	wantIn(t, f.say(t, ctx, map[string]any{"to": "#partner", "text": "x"}).Message, "Denied: pending p")
	if len(f.posted()) != 0 {
		t.Fatalf("a denied call posted")
	}
}

// A client that cannot be asked gets the plain pending refusal.
func TestSafetyNoElicitationWithoutOffer(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	if res := f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "x"}); res.InputRequest != nil {
		t.Fatalf("asked a client that did not declare elicitation")
	}
}

// Trust skips the gate for the parties recorded; the send leaves a use
// entry. A trusted DM to an external person goes through the same way.
func TestSafetyTrustedDestinationSkipsTheGate(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	ws := f.workspace(t)
	if err := ws.Trust.Add(safety.TrustAdd{
		Key: safety.Conversation("CX", "#partner"), DestKind: safety.DestChannel,
		Cases: []safety.Case{safety.CaseExternal}, Source: safety.SourceCLI, Parties: []string{"T9"},
	}); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	if res := f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "hello partner"}); !res.Success {
		t.Fatalf("trusted destination gated: %+v", res)
	}
	if res := f.say(t, context.Background(), map[string]any{"to": "#partner", "text": fakeToken()}); res.Success {
		t.Fatalf("trust skipped the scanner")
	}
}

// A reaction is a small say: its emoji is scanned, and a reaction in an
// external channel is gated.
func TestSafetyReactionRoutesThroughPreSend(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	ts := "1786752114.508819"
	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "emoji": fakeToken(), "messageTs": ts})
	wantIn(t, res.Message, "BLOCKED", "the reaction")
	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#partner", "emoji": "tada", "messageTs": ts}).Message,
		"Needs operator approval", "reaction :tada: to #partner")
	if n := f.srv.Calls("reactions.add"); n != 0 {
		t.Fatalf("reactions.add called %d times", n)
	}
	if !f.say(t, context.Background(), map[string]any{"to": "#platform", "emoji": "tada", "messageTs": ts}).Success {
		t.Fatalf("a clean internal reaction was refused")
	}
}

// mark-read never opens a DM, and is refused at a quarantined destination.
func TestSafetyMarkReadNoDMAndQuarantine(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	out := runTool(t, features.MarkAsRead, f.ap, map[string]any{"target": "dm:nodm"})
	wantIn(t, out, "There is no DM with @nodm")
	if n := f.srv.Calls("conversations.open"); n != 0 {
		t.Fatalf("mark-read opened a DM")
	}

	f.say(t, context.Background(), map[string]any{"to": "@schen", "text": fakeToken()})
	f.srv.ResetCalls()
	wantIn(t, runTool(t, features.MarkAsRead, f.ap, map[string]any{"channel": "@schen"}), "is quarantined", "@schen (and every DM and group DM with them)")
	if n := f.srv.Calls("conversations.mark"); n != 0 {
		t.Fatalf("marked a quarantined DM")
	}
}

// A file download took from #platform is a move into #eng: gated in
// strict, a warning in soft. Back into #platform it is no move.
func TestSafetyCrossConversationFile(t *testing.T) {
	for _, posture := range []safety.Posture{safety.Strict, safety.Soft} {
		t.Run(string(posture), func(t *testing.T) {
			f := newSafetyFake(t, safety.Settings{Identity: safety.Human, Posture: posture})
			uf := attachUploads(t, f)
			data := []byte("quarterly numbers")
			putExchange(t, "q3.txt", data)
			sum := sha256.Sum256(data)
			if err := f.workspace(t).Provenance.Record(safety.Provenance{SHA256: hex.EncodeToString(sum[:]), Name: "q3.txt", FileID: "F9", Conversations: []string{"C2"}}); err != nil {
				t.Fatalf("provenance: %v", err)
			}

			res := f.say(t, context.Background(), map[string]any{"to": "#eng", "files": []any{"q3.txt"}})
			if posture == safety.Strict {
				wantIn(t, res.Message, "Needs operator approval", "upload q3.txt from #platform to #eng")
				if uf() != 0 {
					t.Fatalf("gated upload reached Slack")
				}
			} else {
				if !res.Success {
					t.Fatalf("soft gated a move: %+v", res)
				}
				wantIn(t, res.Guidance, "downloaded from #platform")
			}
			if res := f.say(t, context.Background(), map[string]any{"to": "#platform", "files": []any{"q3.txt"}}); !res.Success {
				t.Fatalf("returning a file to its conversation was gated: %+v", res)
			}
		})
	}
}

// attachUploads adds the upload endpoints to a safety fake and returns a
// count of completed shares.
func attachUploads(t *testing.T, f *safetyFake) func() int {
	t.Helper()
	t.Setenv("SLACK_MCP_EXCHANGE_DIR", "")
	var mu sync.Mutex
	completed := 0
	f.srv.Handle("files.getUploadURLExternal", func(*http.Request) any {
		f.srv.Handle("upload/F1", func(*http.Request) any { return slacktest.Response{Status: 200, Body: "OK"} })
		return map[string]any{"ok": true, "upload_url": f.srv.URL + "/api/upload/F1", "file_id": "F1"}
	})
	f.srv.Handle("files.completeUploadExternal", func(*http.Request) any {
		mu.Lock()
		completed++
		mu.Unlock()
		return map[string]any{"ok": true, "files": []any{map[string]any{"id": "F1"}}}
	})
	t.Cleanup(features.SetUploadLimitsForTest(features.UploadLimits{PerFile: provider.MaxUploadBytes, Total: 1 << 30, Delays: []time.Duration{time.Millisecond}}))
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return completed
	}
}

func TestSafetyInstructionsCarryIdentityAndQuarantines(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	f.say(t, context.Background(), map[string]any{"to": "#eng", "text": fakeToken()})
	got := features.Instructions(safety.Org{TeamID: "T1", UserID: "U1"}, "bockeliea")
	wantIn(t, got, "Account identity: human. Safety posture: strict.",
		`You are writing from "bockeliea"'s account. Speak as yourself, the assistant, for "bockeliea"`,
		"Never rework content", "Quarantined: #eng")
	if strings.Contains(got, "quarantine.jsonl") || strings.Contains(got, "C1") {
		t.Fatalf("instructions name a file or an ID:\n%s", got)
	}

	safety.SetCurrent(safety.Settings{Identity: safety.Agent, Posture: safety.Soft})
	wantIn(t, features.SayDescription(safety.Agent, features.AccountName("bot")), "This account is yours. Speak as yourself, for the people you're helping")
	if got := features.AccountName("a\x1b[31mb\u202e"); got != `"a[31mb"` {
		t.Fatalf("AccountName = %q", got)
	}
}

// A raw conversation ID the cache does not hold is classified from
// conversations.info by its flags, not its prefix: this C-prefixed ID is a
// group DM, checked through its members, one of whom is quarantined.
func TestSafetyUncachedRawIDClassifiedByConversationsInfo(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	f.say(t, context.Background(), map[string]any{"to": "@schen", "text": fakeToken()})

	f.srv.Handle("conversations.info", func(r *http.Request) any {
		_ = r.ParseForm()
		if r.Form.Get("channel") != "C0MPIM0001" {
			return nil
		}
		return map[string]any{"ok": true, "channel": map[string]any{"id": "C0MPIM0001", "name": "mpdm-schen--bockeliea-1", "is_mpim": true}}
	})
	f.srv.Handle("conversations.members", func(*http.Request) any {
		return map[string]any{"ok": true, "members": []string{"U1", "U2"}, "response_metadata": map[string]any{"next_cursor": ""}}
	})
	f.srv.ResetCalls()
	res := f.say(t, context.Background(), map[string]any{"to": "C0MPIM0001", "text": "hello"})
	wantIn(t, res.Message, "#mpdm-schen--bockeliea-1 is quarantined", "@schen (and every DM and group DM with them)")
	if f.srv.Calls("conversations.info") == 0 || f.srv.Calls("conversations.members") == 0 {
		t.Fatalf("classified without conversations.info/members")
	}
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("posted to a quarantined group DM")
	}
}

// The organization gate case 1 compares against comes from auth.test at
// boot: the team and, inside Grid, the enterprise.
func TestSafetyOrganizationCapturedAtBoot(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("auth.test", func(*http.Request) any {
		return map[string]any{"ok": true, "url": srv.URL + "/", "team": "Praecipio", "user": "bockeliea",
			"team_id": "T1", "user_id": "U1", "enterprise_id": "E1"}
	})
	team, enterprise, self := bootedProvider(t, srv).Organization()
	if team != "T1" || enterprise != "E1" || self != "U1" {
		t.Fatalf("Organization() = %q %q %q", team, enterprise, self)
	}
}
