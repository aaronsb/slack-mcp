package features_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// say broadcast (#97): a thread reply can also go to the channel.

func broadcastServer(t *testing.T) (*slacktest.Server, *url.Values) {
	t.Helper()
	srv := slacktest.New(t)
	form := &url.Values{}
	srv.Handle("chat.postMessage", func(r *http.Request) any {
		_ = r.ParseForm()
		*form = r.PostForm
		return map[string]any{"ok": true, "channel": "C1", "ts": "1782246300.000000"}
	})
	return srv, form
}

func TestSayBroadcastSendsReplyBroadcastWithTheThread(t *testing.T) {
	srv, form := broadcastServer(t)
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "Decision: ship Friday",
		"thread": "1782246118.543969", "broadcast": true,
	})
	if srv.Calls("chat.postMessage") != 1 {
		t.Fatalf("expected one post, got %d", srv.Calls("chat.postMessage"))
	}
	if form.Get("reply_broadcast") != "true" || form.Get("thread_ts") != "1782246118.543969" {
		t.Fatalf("form lacks reply_broadcast/thread_ts: %v", *form)
	}
	if !strings.Contains(out, "broadcast=true") {
		t.Fatalf("echo lacks broadcast=true:\n%s", out)
	}
	if !strings.Contains(out, "also sent to #eng") {
		t.Fatalf("output wording missing:\n%s", out)
	}
	if strings.Contains(out, "C1") {
		t.Fatalf("internal ID leaked:\n%s", out)
	}
	if srv.Calls("conversations.mark") != 0 {
		t.Fatalf("broadcast fired a read receipt")
	}
}

func TestSayThreadReplyWithoutBroadcastDoesNotBroadcast(t *testing.T) {
	srv, form := broadcastServer(t)
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "ack", "thread": "1782246118.543969",
	})
	if srv.Calls("chat.postMessage") != 1 {
		t.Fatalf("reply did not post")
	}
	if _, present := (*form)["reply_broadcast"]; present {
		t.Fatalf("reply_broadcast sent without broadcast: %v", *form)
	}
	if strings.Contains(out, "broadcast") {
		t.Fatalf("plain reply mentions broadcast:\n%s", out)
	}
}

func TestSayBroadcastWithoutThreadIsRejectedBeforeAnyCall(t *testing.T) {
	srv, _ := broadcastServer(t)
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "hello", "broadcast": true,
	})
	if srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("rejected broadcast still posted")
	}
	if !strings.Contains(out, "broadcast needs thread=") {
		t.Fatalf("rejection unclear:\n%s", out)
	}
}

func TestSayBroadcastWithEmojiIsRejectedBeforeAnyCall(t *testing.T) {
	srv, _ := broadcastServer(t)
	ap := bootedProvider(t, srv)
	srv.Handle("reactions.add", func(*http.Request) any { return map[string]any{"ok": true} })

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "emoji": "thumbsup", "messageTs": "1782246118.543969",
		"thread": "1782246118.543969", "broadcast": true,
	})
	if srv.Calls("reactions.add") != 0 || srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("rejected broadcast+emoji still wrote")
	}
	if !strings.Contains(out, "broadcast") || !strings.Contains(out, "reaction") {
		t.Fatalf("rejection unclear:\n%s", out)
	}
}
