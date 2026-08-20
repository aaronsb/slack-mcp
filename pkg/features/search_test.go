package features_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// capturedSearch records the query string Slack actually received, which is
// the only way to tell an applied filter from a dropped one.
type capturedSearch struct {
	mu      sync.Mutex
	queries []string
}

func (c *capturedSearch) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := r.ParseForm(); err == nil && r.Form.Get("query") != "" {
		c.queries = append(c.queries, r.Form.Get("query"))
		return
	}
	if body, err := io.ReadAll(r.Body); err == nil {
		c.queries = append(c.queries, string(body))
	}
}

func (c *capturedSearch) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queries) == 0 {
		return ""
	}
	return c.queries[len(c.queries)-1]
}

func (c *capturedSearch) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queries)
}

func searchWith(t *testing.T, params map[string]any, matches []any) (*features.FeatureResult, *capturedSearch) {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))

	cap := &capturedSearch{}
	srv.Handle("search.messages", func(r *http.Request) any {
		cap.record(r)
		return map[string]any{
			"ok": true,
			"messages": map[string]any{
				"total":   len(matches),
				"matches": matches,
			},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	params["_provider"] = ap
	res, err := features.FindDiscussion.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return res, cap
}

func aMatch(channelID, ts, text string) map[string]any {
	return map[string]any{
		"type":      "message",
		"user":      "U2",
		"text":      text,
		"ts":        ts,
		"permalink": "https://example.slack.com/archives/" + channelID + "/p" + ts,
		"channel":   map[string]any{"id": channelID, "name": "engineering"},
	}
}

// MCP delivers arrays as []interface{}. Asserting []string meant `in:` was
// silently dropped, so a caller narrowing a search got a workspace-wide one
// back with no sign the narrowing had gone.
func TestChannelFilterIsActuallyApplied(t *testing.T) {
	_, cap := searchWith(t, map[string]any{
		"query": "deploy",
		"in":    []any{"engineering"},
	}, []any{aMatch("C1", "1782246118.543969", "deploy rollback")})

	if q := cap.last(); !strings.Contains(q, "in:#engineering") {
		t.Errorf("query %q carries no in: filter; the channel narrowing was dropped", q)
	}
}

// A person named any way — display name, real name, fragment — resolves to
// their actual handle before the query renders (ADR-005). Passing the input
// through verbatim made Slack return an empty result indistinguishable from
// "this person said nothing".
func TestPersonFilterResolvesToTheRealHandle(t *testing.T) {
	res, cap := searchWith(t, map[string]any{
		"query": "deploy",
		"from":  []any{"sarah"},
	}, []any{aMatch("C1", "1782246118.543969", "deploy rollback")})

	if q := cap.last(); !strings.Contains(q, "from:@schen") {
		t.Errorf("query %q does not carry the resolved handle", q)
	}
	coverage := res.Data.(map[string]any)["coverage"].(map[string]any)
	resolved, ok := coverage["fromResolved"].([]map[string]any)
	if !ok || len(resolved) != 1 || resolved[0]["handle"] != "schen" {
		t.Errorf("coverage does not say how the person resolved: %+v", coverage["fromResolved"])
	}
}

// An unresolvable person stops the search before it runs: a doomed from:
// filter must never masquerade as an empty result.
func TestAnUnresolvablePersonReturnsInsteadOfSearching(t *testing.T) {
	res, cap := searchWith(t, map[string]any{
		"query": "deploy",
		"from":  []any{"zorptangle"},
	}, []any{aMatch("C1", "1782246118.543969", "deploy rollback")})

	if cap.count() != 0 {
		t.Errorf("search ran %d time(s) with an unresolved from:", cap.count())
	}
	unresolved, ok := res.Data.(map[string]any)["unresolved"].([]map[string]any)
	if !ok || len(unresolved) != 1 {
		t.Fatalf("no unresolved report: %+v", res.Data)
	}
	// The test fixture has no completed sweep, so the honest reason is
	// unswept — absence cannot be asserted.
	if unresolved[0]["reason"] != "unswept" {
		t.Errorf("reason = %v, want unswept", unresolved[0]["reason"])
	}
}

// Slack's search grammar takes a date. The previous code emitted "after:-30d",
// which Slack does not parse as a relative offset — so the window was ignored
// and results came back from outside it.
func TestWindowIsAnAbsoluteDate(t *testing.T) {
	_, cap := searchWith(t, map[string]any{
		"query":     "deploy",
		"timeframe": "3d",
	}, []any{aMatch("C1", "1782246118.543969", "deploy")})

	q := cap.last()
	if strings.Contains(q, "after:-") {
		t.Errorf("query %q uses a relative offset Slack does not parse", q)
	}
	if !strings.Contains(q, "after:20") {
		t.Errorf("query %q carries no absolute after: date", q)
	}
}

// An empty result and a badly chosen window are indistinguishable to a caller,
// so search widens once rather than making them guess.
func TestEmptyResultWidensAndSaysSo(t *testing.T) {
	res, cap := searchWith(t, map[string]any{"query": "nothing matches this"}, nil)

	if cap.count() < 2 {
		t.Errorf("search ran %d time(s); an empty first attempt should widen", cap.count())
	}
	coverage := res.Data.(map[string]any)["coverage"].(map[string]any)
	if !coverage["widened"].(bool) {
		t.Error("search widened without reporting it")
	}
}

// A caller who pinned a window meant it.
func TestAPinnedWindowIsNotWidened(t *testing.T) {
	res, cap := searchWith(t, map[string]any{
		"query":     "nothing matches this",
		"timeframe": "3d",
	}, nil)

	if cap.count() != 1 {
		t.Errorf("search ran %d times despite an explicit timeframe", cap.count())
	}
	coverage := res.Data.(map[string]any)["coverage"].(map[string]any)
	if coverage["widened"].(bool) {
		t.Error("an explicitly requested window was widened anyway")
	}
}

// Coverage says what was actually searched, so a miss is legible.
func TestCoverageSaysWhatWasSearched(t *testing.T) {
	res, _ := searchWith(t, map[string]any{
		"query": "deploy",
		"in":    []any{"engineering"},
	}, []any{aMatch("C1", "1782246118.543969", "deploy")})

	coverage := res.Data.(map[string]any)["coverage"].(map[string]any)
	for _, key := range []string{"searchedSince", "window", "widened", "channels", "totalMatches", "returned", "complete"} {
		if _, ok := coverage[key]; !ok {
			t.Errorf("coverage has no %q field", key)
		}
	}
	if got := coverage["channels"].([]string); len(got) != 1 || got[0] != "engineering" {
		t.Errorf("coverage does not record the channel filter: %+v", got)
	}
}

// Results carry handles, not the channel:ts form ADR-003 retired.
func TestResultsCarryHandlesNotComposedIDs(t *testing.T) {
	res, _ := searchWith(t, map[string]any{"query": "deploy"},
		[]any{aMatch("C1", "1782246118.543969", "deploy rollback")})

	results := res.Data.(map[string]any)["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}

	h, _ := results[0]["handle"].(string)
	ref, err := handle.Decode(h)
	if err != nil {
		t.Fatalf("result handle %q does not decode: %v", h, err)
	}
	if ref.Channel != "C1" {
		t.Errorf("handle decoded to %+v", ref)
	}

	for _, leaked := range []string{"channelId", "threadId", "timestamp"} {
		if _, ok := results[0][leaked]; ok {
			t.Errorf("result exposes raw coordinate %q", leaked)
		}
	}
}
