package features_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// The encounter observer rides the render paths (ADR-008 stage 2): a read
// that already fetched messages appends author/conversation/bucket lines to
// the attention ledger, no text, hour resolution for the session's own user
// only.
func TestGetContextObservesEncounters(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("conversations.history", func(*http.Request) any {
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{
				slacktest.Message("U2", "rolling back the deploy", "1782246200.000000"),
				slacktest.Message("U1", "what happened?", "1782246118.543969"),
			},
		}
	})

	ap := srv.Provider(t)
	res, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": ap,
		"channel":   "eng",
	})
	if err != nil || !res.Success {
		t.Fatalf("handler: %v / %+v", err, res)
	}

	lines := attentionLines(t)
	if len(lines) != 2 {
		t.Fatalf("want 2 encounter lines, got %d: %v", len(lines), lines)
	}
	var sawSelf, sawOther bool
	for _, ln := range lines {
		var e map[string]any
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("bad encounter line %q: %v", ln, err)
		}
		if e["kind"] != "encounter" || e["conv"] != "C1" || e["day"] != "2026-06-23" {
			t.Fatalf("wrong encounter shape: %v", e)
		}
		if txt, ok := e["text"]; ok {
			t.Fatalf("message text leaked into the attention ledger: %v", txt)
		}
		switch e["user"] {
		case "U1": // the session's own user (slacktest auth.test)
			sawSelf = true
			if _, ok := e["hour"]; !ok {
				t.Fatalf("self encounter has no hour bucket: %v", e)
			}
		case "U2":
			sawOther = true
			if _, ok := e["hour"]; ok {
				t.Fatalf("colleague encounter carries an hour bucket: %v", e)
			}
		}
	}
	if !sawSelf || !sawOther {
		t.Fatalf("missing encounters: self=%v other=%v", sawSelf, sawOther)
	}

	// The same read on the same provider appends nothing — the buckets are
	// already seen. (A fresh provider would be dropped by the writer
	// election instead; per-bucket dedup itself is covered in pkg/estate.)
	if _, err := features.GetContext.Handler(context.Background(), map[string]any{
		"_provider": ap,
		"channel":   "eng",
	}); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if again := attentionLines(t); len(again) != 2 {
		t.Fatalf("re-read appended encounters: %d lines", len(again))
	}
}

func attentionLines(t *testing.T) []string {
	t.Helper()
	pattern := filepath.Join(os.Getenv("XDG_DATA_HOME"), "slack-mcp", "ledger", "*", "attention-*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) != 1 {
		t.Fatalf("attention ledger not found (%v): %v", err, matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read attention ledger: %v", err)
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
