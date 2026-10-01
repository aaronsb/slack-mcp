package provider_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// The form POST is the transport the drafts API (scheduled send, #101)
// needs: form-encoded body, the same session headers as every internal call.
func TestPostFormInternalAPISendsAFormWithSessionHeaders(t *testing.T) {
	srv := slacktest.New(t)
	var (
		contentType, auth, cookie string
		form                      url.Values
	)
	srv.Handle("drafts.list", func(r *http.Request) any {
		contentType, auth, cookie = r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Get("Cookie")
		_ = r.ParseForm()
		form = r.PostForm
		return map[string]any{"ok": true, "drafts": []any{}}
	})
	ap := srv.Provider(t)

	var res struct {
		OK     bool  `json:"ok"`
		Drafts []any `json:"drafts"`
	}
	err := ap.ProvideInternalClient().PostFormInternalAPI(context.Background(), "/api/drafts.list",
		url.Values{"is_active": {"true"}, "limit": {"100"}}, &res)
	if err != nil {
		t.Fatalf("PostFormInternalAPI: %v", err)
	}
	if !res.OK {
		t.Fatalf("response not decoded: %+v", res)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %q", contentType)
	}
	if auth != "Bearer xoxc-test" || cookie != "d=xoxd-test" {
		t.Fatalf("session headers missing: auth=%q cookie=%q", auth, cookie)
	}
	if form.Get("is_active") != "true" || form.Get("limit") != "100" {
		t.Fatalf("form not sent: %v", form)
	}
	if srv.Calls("conversations.mark") != 0 {
		t.Fatalf("a form POST fired a read receipt")
	}
}
