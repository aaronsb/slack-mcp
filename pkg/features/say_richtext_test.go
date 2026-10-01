package features_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// say authors rich_text (#98): every text post carries one rich_text block
// plus the normalized mrkdwn as its text fallback, and a post Slack rejects
// for its blocks is retried exactly once as text alone.

// postRecorder captures every chat.postMessage form and answers with the
// scripted responses in order, repeating the last one.
type postRecorder struct {
	mu    sync.Mutex
	forms []url.Values
}

func (p *postRecorder) form(i int) url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forms[i]
}

func recordPosts(srv *slacktest.Server, responses ...map[string]any) *postRecorder {
	rec := &postRecorder{}
	srv.Handle("chat.postMessage", func(r *http.Request) any {
		_ = r.ParseForm()
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.forms = append(rec.forms, r.PostForm)
		i := min(len(rec.forms), len(responses)) - 1
		return responses[i]
	})
	return rec
}

var (
	postOK        = map[string]any{"ok": true, "channel": "C1", "ts": "1782246300.000000"}
	invalidBlocks = map[string]any{"ok": false, "error": "invalid_blocks"}
)

func say(t *testing.T, ap *provider.ApiProvider, params map[string]any) (*features.FeatureResult, string) {
	t.Helper()
	params["_provider"] = ap
	res, err := features.Say.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	return res, features.FormatResult(features.Say.Name, res)
}

// blocksOf decodes the form's blocks field.
func blocksOf(t *testing.T, form url.Values) []map[string]any {
	t.Helper()
	raw := form.Get("blocks")
	if raw == "" {
		return nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("blocks field is not JSON: %v\n%s", err, raw)
	}
	return blocks
}

func assertOneRichTextBlock(t *testing.T, form url.Values) {
	t.Helper()
	blocks := blocksOf(t, form)
	if len(blocks) != 1 || blocks[0]["type"] != "rich_text" {
		t.Fatalf("want one rich_text block, got %v", form.Get("blocks"))
	}
}

func assertNoLeaks(t *testing.T, srv *slacktest.Server, out string) {
	t.Helper()
	if strings.Contains(out, "C1") {
		t.Fatalf("internal ID leaked:\n%s", out)
	}
	if srv.Calls("conversations.mark") != 0 {
		t.Fatalf("say fired a read receipt")
	}
}

func TestSayPostsRichTextWithMrkdwnFallback(t *testing.T) {
	srv := slacktest.New(t)
	rec := recordPosts(srv, postOK)
	ap := bootedProvider(t, srv)

	res, out := say(t, ap, map[string]any{"to": "#eng", "text": "**Ship** it: [notes](https://x.io)"})
	if srv.Calls("chat.postMessage") != 1 {
		t.Fatalf("want one post, got %d", srv.Calls("chat.postMessage"))
	}
	form := rec.form(0)
	assertOneRichTextBlock(t, form)
	if got := form.Get("text"); got != "*Ship* it: <https://x.io|notes>" {
		t.Fatalf("fallback text not normalized mrkdwn: %q", got)
	}
	section := blocksOf(t, form)[0]["elements"].([]any)[0].(map[string]any)
	raw, _ := json.Marshal(section["elements"])
	if !strings.Contains(string(raw), `"bold":true`) || !strings.Contains(string(raw), `"type":"link"`) {
		t.Fatalf("block lacks bold/link: %s", raw)
	}

	data := res.Data.(map[string]any)
	if data["rendering"] != "rich_text" || data["blocksRejected"] != false {
		t.Fatalf("rendering not reported: %v", data)
	}
	if strings.Contains(out, "plain text") {
		t.Fatalf("a clean rich_text post mentions the fallback:\n%s", out)
	}
	assertNoLeaks(t, srv, out)
}

func TestSayRetriesOnceAsTextWhenBlocksAreRejected(t *testing.T) {
	srv := slacktest.New(t)
	rec := recordPosts(srv, invalidBlocks, postOK)
	ap := bootedProvider(t, srv)

	res, out := say(t, ap, map[string]any{"to": "#eng", "text": "*bold* move"})
	if n := srv.Calls("chat.postMessage"); n != 2 {
		t.Fatalf("want 2 posts (blocks, then text), got %d", n)
	}
	assertOneRichTextBlock(t, rec.form(0))
	retry := rec.form(1)
	if _, has := retry["blocks"]; has {
		t.Fatalf("retry still carried blocks: %v", retry)
	}
	if retry.Get("text") != "*bold* move" {
		t.Fatalf("retry text: %q", retry.Get("text"))
	}
	if !res.Success {
		t.Fatalf("retry succeeded but result failed: %s", out)
	}
	data := res.Data.(map[string]any)
	if data["rendering"] != "text" || data["blocksRejected"] != true {
		t.Fatalf("fallback not reported in data: %v", data)
	}
	if !strings.Contains(out, "Posted as plain text") {
		t.Fatalf("output does not name the fallback:\n%s", out)
	}
	assertNoLeaks(t, srv, out)
}

func TestSayReportsASecondFailureWithoutRetryingAgain(t *testing.T) {
	srv := slacktest.New(t)
	recordPosts(srv, invalidBlocks, invalidBlocks)
	ap := bootedProvider(t, srv)

	res, out := say(t, ap, map[string]any{"to": "#eng", "text": "hello"})
	if n := srv.Calls("chat.postMessage"); n != 2 {
		t.Fatalf("want exactly 2 posts, got %d", n)
	}
	if res.Success {
		t.Fatalf("double failure reported as success:\n%s", out)
	}
	if !strings.Contains(out, "rejected the rich-text formatting") || !strings.Contains(out, "retry failed") {
		t.Fatalf("failure does not explain the retry:\n%s", out)
	}
	assertNoLeaks(t, srv, out)
}

func TestSayDoesNotRetryOtherErrors(t *testing.T) {
	srv := slacktest.New(t)
	recordPosts(srv, map[string]any{"ok": false, "error": "not_in_channel"})
	ap := bootedProvider(t, srv)

	res, out := say(t, ap, map[string]any{"to": "#eng", "text": "hello"})
	if n := srv.Calls("chat.postMessage"); n != 1 {
		t.Fatalf("a non-block error was retried: %d posts", n)
	}
	if res.Success || !strings.Contains(out, "not_in_channel") {
		t.Fatalf("error not reported as-is:\n%s", out)
	}
}

func TestSayThreadReplyAndBroadcastCarryBlocks(t *testing.T) {
	srv := slacktest.New(t)
	rec := recordPosts(srv, postOK)
	ap := bootedProvider(t, srv)

	_, out := say(t, ap, map[string]any{
		"to": "#eng", "text": "Decision: *ship Friday*",
		"thread": "1782246118.543969", "broadcast": true,
	})
	form := rec.form(0)
	assertOneRichTextBlock(t, form)
	if form.Get("thread_ts") != "1782246118.543969" || form.Get("reply_broadcast") != "true" {
		t.Fatalf("thread/broadcast lost alongside blocks: %v", form)
	}
	if form.Get("text") != "Decision: *ship Friday*" {
		t.Fatalf("fallback text: %q", form.Get("text"))
	}
	assertNoLeaks(t, srv, out)
}

func TestSayWhitespaceTextPostsWithoutBlocks(t *testing.T) {
	srv := slacktest.New(t)
	rec := recordPosts(srv, postOK)
	ap := bootedProvider(t, srv)

	res, _ := say(t, ap, map[string]any{"to": "#eng", "text": "   "})
	if srv.Calls("chat.postMessage") != 1 {
		t.Fatalf("whitespace post: %d calls", srv.Calls("chat.postMessage"))
	}
	if _, has := rec.form(0)["blocks"]; has {
		t.Fatalf("an empty rich_text block was sent: %v", rec.form(0))
	}
	if res.Data.(map[string]any)["rendering"] != "text" {
		t.Fatalf("rendering: %v", res.Data)
	}
}

func TestSayReactionSendsNoBlocks(t *testing.T) {
	srv := slacktest.New(t)
	recordPosts(srv, postOK)
	ap := bootedProvider(t, srv)

	_, out := say(t, ap, map[string]any{"to": "#eng", "emoji": "thumbsup", "messageTs": "1782246118.543969"})
	if srv.Calls("chat.postMessage") != 0 || srv.Calls("reactions.add") != 1 {
		t.Fatalf("reaction path changed: post=%d react=%d", srv.Calls("chat.postMessage"), srv.Calls("reactions.add"))
	}
	assertNoLeaks(t, srv, out)
}
