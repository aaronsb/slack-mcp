package features_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// A channel name the cache does not hold is asked of Slack's channel search
// once (ADR-015). Only a unique exact name resolves, through
// conversations.info; anything else comes back as candidates, never IDs.

// switcherFake answers search.modules.channels with scripted items and
// records each form, and serves conversations.info for channels the cache
// was never seeded with.
type switcherFake struct {
	srv   *slacktest.Server
	mu    sync.Mutex
	forms []url.Values
	// items answers every search; resp, when set, replaces the answer.
	items []map[string]any
	resp  any
	// remote are the channels conversations.info knows beyond the seed.
	remote map[string]map[string]any
}

func newSwitcherFake(t *testing.T) (*switcherFake, *provider.ApiProvider) {
	t.Helper()
	srv := slacktest.New(t)
	f := &switcherFake{srv: srv, remote: map[string]map[string]any{}}
	srv.Handle("search.modules.channels", func(r *http.Request) any {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.forms = append(f.forms, r.PostForm)
		if f.resp != nil {
			return f.resp
		}
		return map[string]any{"ok": true, "items": f.items, "pagination": map[string]any{"total_count": len(f.items)}}
	})
	srv.Handle("conversations.info", func(r *http.Request) any {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if ch, ok := f.remote[r.Form.Get("channel")]; ok {
			return map[string]any{"ok": true, "channel": ch}
		}
		return map[string]any{"ok": false, "error": "channel_not_found"}
	})
	ap := bootedProvider(t, srv)
	srv.ResetCalls()
	return f, ap
}

func hit(id, name string, member bool, purpose string) map[string]any {
	return map[string]any{"id": id, "name": name, "is_member": member, "is_private": false, "is_archived": false,
		"purpose": map[string]any{"value": purpose}}
}

func remoteChannel(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "is_channel": true, "is_member": false,
		"purpose": map[string]any{"value": "new launch work"}, "topic": map[string]any{"value": ""}}
}

func (f *switcherFake) searches() int { return f.srv.Calls("search.modules.channels") }

// A '#name' the cache lacks resolves through one search and
// conversations.info, and is cached: the second use asks Slack nothing.
func TestChannelMissResolvesAUniqueExactNameAndCachesIt(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "launch", false, "new launch work"), hit("C8", "launch-ops", true, "")}
	f.remote["C9"] = remoteChannel("C9", "launch")

	out := runTool(t, features.Messages, ap, map[string]any{"target": "#launch", "since": "1d"})
	if f.searches() != 1 || f.srv.Calls("conversations.info") != 1 {
		t.Fatalf("searches=%d info=%d, want 1 and 1\n%s", f.searches(), f.srv.Calls("conversations.info"), out)
	}
	form := f.forms[0]
	if form.Get("query") != "launch" || form.Get("module") != "channels" || form.Get("sort") != "score" || form.Get("count") != "100" {
		t.Fatalf("search form = %v", form)
	}
	if strings.Contains(out, "No channel") {
		t.Fatalf("resolved name reported as a miss:\n%s", out)
	}
	if ch, ok := ap.LookupChannelName("launch"); !ok || ch.ID != "C9" {
		t.Fatalf("#launch not cached after the lookup: %+v %v", ch, ok)
	}

	runTool(t, features.Messages, ap, map[string]any{"target": "#launch", "since": "1d"})
	if f.searches() != 1 {
		t.Fatalf("second use searched again: %d searches", f.searches())
	}
}

// More than one hit, none named exactly, is an answer: a write sends
// nothing and names the hits by '#name', never by ID.
func TestChannelMissOnAWriteNamesTheHitsAndSendsNothing(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "deploy-prod", false, ""), hit("C8", "deploy-staging", true, ""),
		hit("C7", "release-train", false, "every deploy, announced")}

	out := runTool(t, features.Say, ap, map[string]any{"to": "#deploy", "text": "rolling back"})
	if n := f.srv.Calls("chat.postMessage"); n != 0 {
		t.Fatalf("posted %d times on an inexact name\n%s", n, out)
	}
	if f.srv.Calls("conversations.info") != 0 {
		t.Fatalf("fetched a channel for an inexact name")
	}
	mustContain(t, out, "No channel is named exactly '#deploy', so nothing was done.",
		"#deploy-prod", "#deploy-staging", "#release-train (matched its purpose)")
	for _, id := range []string{"C9", "C8", "C7"} {
		if strings.Contains(out, id) {
			t.Fatalf("output leaks channel ID %s:\n%s", id, out)
		}
	}
}

// Two hits both named exactly (one archived, say) do not resolve a write.
func TestChannelMissWithTwoExactNamesDoesNotResolve(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "launch", false, ""), hit("C8", "Launch", false, "")}

	out := runTool(t, features.Say, ap, map[string]any{"to": "#launch", "text": "go"})
	if f.srv.Calls("chat.postMessage") != 0 || f.srv.Calls("conversations.info") != 0 {
		t.Fatalf("resolved an ambiguous exact name\n%s", out)
	}
	mustContain(t, out, "No channel is named exactly '#launch'")
}

// A name that found nothing is said plainly and not asked again for a while.
func TestChannelMissWithNoHitsIsRememberedBriefly(t *testing.T) {
	f, ap := newSwitcherFake(t)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "#nowhere", "since": "1d"})
	mustContain(t, out, "No channel named '#nowhere' is visible to you: Slack's channel search found none")
	runTool(t, features.Messages, ap, map[string]any{"target": "#Nowhere", "since": "1d"})
	if f.searches() != 1 {
		t.Fatalf("searches = %d, want 1: a fresh miss was asked again", f.searches())
	}
}

// A search Slack refuses degrades to the old miss, says why, and is not
// retried in a loop.
func TestChannelMissDegradesWhenTheSearchFails(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.resp = map[string]any{"ok": false, "error": "unknown_method"}

	out := runTool(t, features.Messages, ap, map[string]any{"target": "#launch", "since": "1d"})
	mustContain(t, out, "No channel named '#launch' is known", "Slack's channel search could not be reached")
	if f.searches() != 1 {
		t.Fatalf("searches = %d, want 1", f.searches())
	}
}

// A rate limit pauses every lookup for the time Slack names.
func TestChannelMissHonorsARateLimit(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.resp = slacktest.Response{Status: http.StatusTooManyRequests, Body: "{}", Header: map[string]string{"Retry-After": "120"}}

	out := runTool(t, features.Messages, ap, map[string]any{"target": "#launch", "since": "1d"})
	mustContain(t, out, "rate-limited for another 2m0s")
	out = runTool(t, features.Messages, ap, map[string]any{"target": "#other", "since": "1d"})
	mustContain(t, out, "rate-limited")
	if f.searches() != 1 {
		t.Fatalf("searches = %d, want 1: a lookup ran during the pause", f.searches())
	}
}

// On the read path one channel-shaped word that matched nothing cached
// reaches the search; hits come back as candidates with handles.
func TestReadMissOffersSearchHitsAsCandidates(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "deploy-prod", false, ""), hit("C7", "release-train", false, "every deploy")}

	out := runTool(t, features.Messages, ap, map[string]any{"target": "deploy"})
	mustContain(t, out, "No channel is named exactly \"deploy\"; Slack's channel search found 2.", "#deploy-prod", "#release-train")
	if strings.Contains(out, "C9") || strings.Contains(out, "C7") {
		t.Fatalf("candidates leak IDs:\n%s", out)
	}
}

// A multi-word description never reaches the search.
func TestReadMissOfAPhraseDoesNotSearch(t *testing.T) {
	f, ap := newSwitcherFake(t)

	out := runTool(t, features.Messages, ap, map[string]any{"target": "the thread about nothing"})
	mustContain(t, out, "Nothing here matches")
	if f.searches() != 0 {
		t.Fatalf("a phrase reached the channel search")
	}
}

// A channel already cached never reaches the search.
func TestCachedChannelDoesNotSearch(t *testing.T) {
	f, ap := newSwitcherFake(t)
	runTool(t, features.Messages, ap, map[string]any{"target": "#eng", "since": "1d"})
	if f.searches() != 0 {
		t.Fatalf("a cached channel reached the channel search")
	}
}

// When Slack has more hits than one page and none on it is named exactly,
// the miss says only the top page was checked.
func TestChannelMissSaysWhenOnlyTheTopPageWasChecked(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.resp = map[string]any{"ok": true, "items": []map[string]any{hit("C9", "sales-east", false, "")},
		"pagination": map[string]any{"total_count": 940}}

	out := runTool(t, features.Say, ap, map[string]any{"to": "#sales", "text": "q3"})
	mustContain(t, out, "No channel among the top 1 of 940 search hits is named exactly '#sales', so nothing was done.")
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("posted on a truncated inexact answer")
	}
}

// A '#' target that cannot be a channel name is refused locally: nothing
// the caller typed reaches Slack's search.
func TestChannelMissOfANonChannelNameMakesNoSearch(t *testing.T) {
	f, ap := newSwitcherFake(t)
	for _, to := range []string{"#", "#two words", "#" + strings.Repeat("x", 81), "#xoxb-not-a-name!"} {
		out := runTool(t, features.Say, ap, map[string]any{"to": to, "text": "hi"})
		mustContain(t, out, "is known, so nothing was done.")
	}
	if f.searches() != 0 {
		t.Fatalf("a non-channel name reached the search %d times", f.searches())
	}
}

// A rate limit that names no wait pauses for the default.
func TestChannelMissRateLimitWithoutRetryAfterPausesForTheDefault(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.resp = slacktest.Response{Status: http.StatusTooManyRequests, Body: "{}"}

	out := runTool(t, features.Messages, ap, map[string]any{"target": "#launch", "since": "1d"})
	mustContain(t, out, "rate-limited for another 30s")
	if f.searches() != 1 {
		t.Fatalf("searches = %d, want 1", f.searches())
	}
}

// An exact hit whose record cannot be read is reported, not resolved.
func TestChannelMissWhoseRecordCannotBeReadIsReported(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "launch", false, "")}

	out := runTool(t, features.Say, ap, map[string]any{"to": "#launch", "text": "go"})
	mustContain(t, out, "Slack's channel search found #launch, but reading it failed")
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("posted to a channel whose record failed to load")
	}
}

// mark-read on an inexact name marks nothing.
func TestMarkReadOnAnInexactNameMarksNothing(t *testing.T) {
	f, ap := newSwitcherFake(t)
	f.items = []map[string]any{hit("C9", "deploy-prod", true, "")}

	out := runTool(t, features.MarkAsRead, ap, map[string]any{"channel": "#deploy"})
	mustContain(t, out, "No channel is named exactly '#deploy'")
	if n := f.srv.Calls("conversations.mark"); n != 0 {
		t.Fatalf("marked %d times on an inexact name", n)
	}
}

// A destination found through the search still passes the scanner and the
// quarantine: a token to it is blocked and nothing reaches the channel.
func TestRemotelyResolvedDestinationIsStillScanned(t *testing.T) {
	f := newSafetyFakeWith(t, strictHuman(), func(srv *slacktest.Server) {
		srv.Handle("search.modules.channels", func(*http.Request) any {
			return map[string]any{"ok": true, "items": []map[string]any{hit("C9", "launch", true, "")}}
		})
	})
	f.srv.Handle("conversations.info", func(r *http.Request) any {
		_ = r.ParseForm()
		if r.Form.Get("channel") == "C9" {
			return map[string]any{"ok": true, "channel": slack.Channel{GroupConversation: slack.GroupConversation{
				Name: "launch", Conversation: slack.Conversation{ID: "C9"}}, IsChannel: true, IsMember: true}}
		}
		return map[string]any{"ok": false, "error": "channel_not_found"}
	})

	res := f.say(t, context.Background(), map[string]any{"to": "#launch", "text": "key " + fakeToken()})
	if res.Success {
		t.Fatalf("token sent to a remotely resolved channel: %+v", res)
	}
	wantIn(t, res.Message, "BLOCKED: nothing was sent", "Quarantined until the operator clears it: #launch")
	for _, p := range f.posted() {
		if p.Get("channel") == "C9" && strings.Contains(p.Get("text"), "key") {
			t.Fatalf("the agent's text reached the channel: %v", p)
		}
	}
}
