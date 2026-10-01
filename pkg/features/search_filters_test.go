package features_test

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

func filterSearch(t *testing.T, params map[string]any) (*features.FeatureResult, *[]pagedRequest) {
	t.Helper()
	run, reqs := pagedSearchWith(t, 1)
	return run(params), reqs
}

func lastQuery(reqs *[]pagedRequest) string {
	if len(*reqs) == 0 {
		return ""
	}
	return (*reqs)[len(*reqs)-1].query
}

func copyWith(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestEachFilterComposesItsSlackSyntax(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"in channel", map[string]any{"in": "#engineering"}, "in:#engineering"},
		{"from", map[string]any{"from": []any{"sarah"}}, "from:@schen"},
		{"after", map[string]any{"after": "2026-09-01"}, "after:2026-09-01"},
		{"before", map[string]any{"before": "2026-09-10"}, "before:2026-09-10"},
		{"has link", map[string]any{"has": []any{"link"}}, "has:link"},
		{"has pin", map[string]any{"has": []any{"pin"}}, "has:pin"},
		{"has emoji", map[string]any{"has": []any{":eyes:"}}, "has::eyes:"},
		{"thread", map[string]any{"thread": true}, "is:thread"},
		{"all", map[string]any{"in": []any{"#engineering"}, "from": []any{"sarah"}, "after": "2026-09-01", "before": "2026-09-10", "has": []any{"link"}, "thread": true},
			"deploy after:2026-09-01 before:2026-09-10 in:#engineering from:@schen has:link is:thread"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.params["query"] = "deploy"
			res, reqs := filterSearch(t, c.params)
			if !res.Success {
				t.Fatalf("refused: %s %s", res.Message, res.Guidance)
			}
			q := lastQuery(reqs)
			if !strings.HasPrefix(q, "deploy") {
				t.Errorf("raw query not first and verbatim: %q", q)
			}
			if !strings.Contains(q, c.want) {
				t.Errorf("query %q lacks %q", q, c.want)
			}
		})
	}
}

func TestNamesResolveAndOutputShowsNamesOnly(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	var queries []string
	srv.Handle("search.messages", func(r *http.Request) any {
		_ = r.ParseForm()
		queries = append(queries, r.Form.Get("query"))
		return map[string]any{"ok": true, "messages": map[string]any{"total": 1, "matches": []any{aMatch("C1", "1782246118.543969", "deploy")}}}
	})
	srv.Handle("conversations.open", func(*http.Request) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D9"}}
	})
	ap := bootedProvider(t, srv)

	out := runTool(t, features.Messages, ap, map[string]any{
		"query": "deploy", "in": []any{"engineering", "@sarah"}, "from": []any{"sarah"},
	})
	for _, want := range []string{"Sent to Slack: deploy", "in:#engineering", "in:@schen", "from:@schen"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(queries) != 1 || strings.Contains(queries[0], "D9") || strings.Contains(queries[0], "C1") {
		t.Errorf("query sent to Slack: %q", queries)
	}
	ids := regexp.MustCompile(`\b(C1|U2|D9)\b`)
	for _, line := range strings.Split(out, "\n") {
		if (strings.Contains(line, "Sent to Slack") || strings.Contains(line, "messages query=") || strings.Contains(line, " -> ")) && ids.MatchString(line) {
			t.Errorf("ID leaked into %q", line)
		}
	}
	if srv.Calls("conversations.mark") != 0 {
		t.Errorf("a search marked something read")
	}
}

func TestEchoListsEveryFilter(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	srv.Handle("search.messages", func(*http.Request) any {
		return map[string]any{"ok": true, "messages": map[string]any{"total": 0, "matches": []any{}}}
	})
	ap := bootedProvider(t, srv)
	out := runTool(t, features.Messages, ap, map[string]any{
		"query": "x", "in": []any{"#engineering"}, "after": "2026-09-01", "has": []any{"link"}, "thread": true,
	})
	for _, want := range []string{"after=2026-09-01", "has=[link]", "in=[#engineering]", "thread=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("echo lacks %q:\n%s", want, out)
		}
	}
}

func TestAfterReplacesTheWindowOnceAndPins(t *testing.T) {
	_, reqs := filterSearch(t, map[string]any{"query": "deploy", "after": "2026-09-01"})
	if n := strings.Count(lastQuery(reqs), "after:"); n != 1 {
		t.Errorf("want one after:, got %d in %q", n, lastQuery(reqs))
	}

	run, reqs2 := pagedSearchFunc(t, func(int, pagedRequest) (int, bool) { return 1, false })
	run(map[string]any{"query": "deploy", "after": "2026-09-01"})
	if len(*reqs2) != 1 {
		t.Errorf("an explicit after widened: %d searches", len(*reqs2))
	}
	run(map[string]any{"query": "deploy after:2026-01-01"})
	q := (*reqs2)[len(*reqs2)-1].query
	if strings.Count(q, "after:") != 1 || len(*reqs2) != 2 {
		t.Errorf("raw after: should suppress auto-after and not widen: %q (%d searches)", q, len(*reqs2))
	}
}

func TestInvalidFiltersAreRefusedWithoutSearching(t *testing.T) {
	cases := map[string]map[string]any{
		"after+timeframe": {"after": "2026-09-01", "timeframe": "3d"},
		"after>before":    {"after": "2026-09-10", "before": "2026-09-01"},
		"bad has":         {"has": []any{"star"}},
		"bad date":        {"after": "yesterday"},
		"raw after":       {"query": "x after:2026-01-01", "after": "2026-09-01"},
		"raw from":        {"query": "x from:@bob", "from": []any{"sarah"}},
		"raw in":          {"query": "x in:#eng", "in": "#engineering"},
		"raw has":         {"query": "x has:link", "has": []any{"pin"}},
		"raw is":          {"query": "x is:thread", "thread": true},
		"id in":           {"in": "C0123ABCDEF"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := p["query"]; !ok {
				p["query"] = "x"
			}
			res, reqs := filterSearch(t, p)
			if res.Success {
				t.Errorf("accepted: %+v", p)
			}
			if len(*reqs) != 0 {
				t.Errorf("searched %d time(s)", len(*reqs))
			}
		})
	}
	res, _ := filterSearch(t, map[string]any{"query": "x", "has": []any{"star"}})
	if !strings.Contains(res.Guidance, "link, pin") {
		t.Errorf("has error does not list accepted values: %q", res.Guidance)
	}
}

func TestUnresolvableNamesFailWithCandidatesAndNoSearch(t *testing.T) {
	res, reqs := filterSearch(t, map[string]any{"query": "x", "in": "#enginering"})
	if res.Success || !strings.Contains(res.Message, "enginering") || len(*reqs) != 0 {
		t.Errorf("misspelled channel: success=%v msg=%q searches=%d", res.Success, res.Message, len(*reqs))
	}
	res, reqs = filterSearch(t, map[string]any{"query": "x", "in": "@zorptangle"})
	if res.Message == "" || len(*reqs) != 0 {
		t.Errorf("unknown person in in=: %q searches=%d", res.Message, len(*reqs))
	}
	res, reqs = filterSearch(t, map[string]any{"query": "x", "from": []any{"zorptangle"}})
	if _, ok := res.Data.(map[string]any)["unresolved"]; !ok || len(*reqs) != 0 {
		t.Errorf("unknown from: no candidate report, searches=%d", len(*reqs))
	}
}

func TestEachFilterChangesTheCursorDigest(t *testing.T) {
	base := map[string]any{"query": "deploy", "timeframe": "3d"}
	variants := map[string]map[string]any{
		"in":     {"in": "#engineering"},
		"before": {"before": "2026-09-10"},
		"has":    {"has": []any{"link"}},
		"thread": {"thread": true},
		"from":   {"from": []any{"sarah"}},
	}
	for name, extra := range variants {
		t.Run(name, func(t *testing.T) {
			run, _ := pagedSearchWith(t, 3)
			first := run(copyWith(base, nil))
			if first.Pagination == nil {
				t.Fatal("no cursor")
			}
			p := copyWith(base, extra)
			p["cursor"] = first.Pagination.NextCursor
			if res := run(p); res.Success || !strings.Contains(res.Message, "different search") {
				t.Errorf("adding %s did not invalidate the cursor: %q", name, res.Message)
			}
		})
	}
	t.Run("after", func(t *testing.T) {
		run, _ := pagedSearchWith(t, 3)
		first := run(map[string]any{"query": "deploy", "after": "2026-09-01"})
		p := map[string]any{"query": "deploy", "after": "2026-09-02", "cursor": first.Pagination.NextCursor}
		if res := run(p); res.Success || !strings.Contains(res.Message, "different search") {
			t.Errorf("changed after did not invalidate the cursor: %q", res.Message)
		}
		ok := run(map[string]any{"query": "deploy", "after": "2026-09-01", "cursor": first.Pagination.NextCursor})
		if !ok.Success {
			t.Errorf("same filters refused: %s", ok.Message)
		}
	})
}

func TestOldVersionCursorIsRefused(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)
	old := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"p":2,"c":100,"s":"2026-09-01","d":"abc"}`))
	res := run(map[string]any{"query": "deploy", "cursor": old})
	if res.Success || !strings.Contains(res.Message, "older version") || len(*reqs) != 0 {
		t.Errorf("old cursor: success=%v msg=%q searches=%d", res.Success, res.Message, len(*reqs))
	}
}

func TestFiltersWorkInBatch(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))
	var queries []string
	srv.Handle("search.messages", func(r *http.Request) any {
		_ = r.ParseForm()
		queries = append(queries, r.Form.Get("query"))
		return map[string]any{"ok": true, "messages": map[string]any{"total": 0, "matches": []any{}}}
	})
	ap := bootedProvider(t, srv)
	p := map[string]any{"query": "x", "from": []any{"sarah"}, "has": []any{"link"}, "after": "2026-09-01"}
	runTool(t, features.Messages, ap, copyWith(p, nil))
	runTool(t, features.Batch, ap, map[string]any{"commands": []any{
		map[string]any{"tool": "messages", "params": copyWith(p, nil)},
	}})
	if len(queries) != 2 || queries[0] != queries[1] {
		t.Errorf("batch query differs from direct: %q", queries)
	}
}
