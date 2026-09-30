package features_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// pagedRequest is what Slack received for one search.messages call.
type pagedRequest struct{ query, page, count string }

// pagedSearchWith mocks a search.messages whose paging says there are `pages`
// pages, recording the query, page, and count of every request.
func pagedSearchWith(t *testing.T, pages int) (func(map[string]any) *features.FeatureResult, *[]pagedRequest) {
	t.Helper()
	return pagedSearchFunc(t, func(int, pagedRequest) (int, bool) { return pages, true })
}

// pagedSearchFunc is pagedSearchWith with the response decided per request:
// respond gets the 0-based request number and returns the page count to report
// and whether the response carries matches.
func pagedSearchFunc(t *testing.T, respond func(n int, req pagedRequest) (pages int, matches bool)) (func(map[string]any) *features.FeatureResult, *[]pagedRequest) {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))

	var mu sync.Mutex
	seen := &[]pagedRequest{}
	srv.Handle("search.messages", func(r *http.Request) any {
		_ = r.ParseForm()
		mu.Lock()
		req := pagedRequest{r.Form.Get("query"), r.Form.Get("page"), r.Form.Get("count")}
		n := len(*seen)
		*seen = append(*seen, req)
		mu.Unlock()
		pages, hasMatches := respond(n, req)
		matches := []any{}
		if hasMatches {
			matches = []any{
				aMatch("C1", "1782246118.543969", "deploy one"),
				aMatch("C1", "1782246119.543969", "deploy two"),
			}
		}
		page := 1
		if p, err := strconv.Atoi(r.Form.Get("page")); err == nil {
			page = p
		}
		return map[string]any{
			"ok": true,
			"messages": map[string]any{
				"total":   pages * 2,
				"paging":  map[string]any{"count": 2, "total": pages * 2, "page": page, "pages": pages},
				"matches": matches,
			},
		}
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)

	return func(params map[string]any) *features.FeatureResult {
		params["_provider"] = ap
		res, err := features.Messages.Handler(context.Background(), params)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		return res
	}, seen
}

func TestSearchPagesThroughTheCursor(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)

	first := run(map[string]any{"query": "deploy", "timeframe": "3d"})
	if first.Pagination == nil || !first.Pagination.HasMore || first.Pagination.NextCursor == "" {
		t.Fatalf("a search with pages > 1 returned no cursor: %+v", first.Pagination)
	}

	second := run(map[string]any{"query": "deploy", "timeframe": "3d", "cursor": first.Pagination.NextCursor})
	if !second.Success {
		t.Fatalf("page 2 refused: %s", second.Message)
	}
	got := *reqs
	if len(got) != 2 {
		t.Fatalf("want 2 requests, got %d", len(got))
	}
	if got[1].page != "2" {
		t.Errorf("cursor requested page %q, want 2", got[1].page)
	}
	if got[1].query != got[0].query {
		t.Errorf("page 2 ran %q, page 1 ran %q", got[1].query, got[0].query)
	}
	if cov := second.Data.(map[string]any)["coverage"].(map[string]any); cov["page"] != 2 {
		t.Errorf("coverage page = %v, want 2", cov["page"])
	}
	if out := features.FormatResult("search", second); !strings.Contains(out, "cursor='") {
		t.Errorf("page 2 of 3 renders no next cursor:\n%s", out)
	}
}

func TestSearchLastPageHasNoCursor(t *testing.T) {
	run, _ := pagedSearchWith(t, 2)
	first := run(map[string]any{"query": "deploy", "timeframe": "3d"})
	last := run(map[string]any{"query": "deploy", "timeframe": "3d", "cursor": first.Pagination.NextCursor})
	if last.Pagination != nil {
		t.Errorf("final page offers a cursor: %+v", last.Pagination)
	}
}

func TestSearchRefusesACursorFromADifferentSearch(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)
	first := run(map[string]any{"query": "deploy", "timeframe": "3d"})

	cases := map[string]map[string]any{
		"query":     {"query": "rollback", "cursor": first.Pagination.NextCursor},
		"in":        {"query": "deploy", "in": []any{"engineering"}, "cursor": first.Pagination.NextCursor},
		"limit":     {"query": "deploy", "limit": float64(10), "cursor": first.Pagination.NextCursor},
		"garbage":   {"query": "deploy", "cursor": "not-a-cursor"},
		"from":      {"query": "deploy", "from": []any{"sarah"}, "cursor": first.Pagination.NextCursor},
		"timeframe": {"query": "deploy", "timeframe": "30d", "cursor": first.Pagination.NextCursor},
	}
	for name, params := range cases {
		if res := run(params); res.Success {
			t.Errorf("%s: mismatched cursor was accepted", name)
		}
	}
	if len(*reqs) != 1 {
		t.Errorf("a refused cursor still reached Slack: %d requests", len(*reqs))
	}
}

func TestSearchLimitIsTheSlackPageSize(t *testing.T) {
	run, reqs := pagedSearchWith(t, 2)
	run(map[string]any{"query": "deploy", "timeframe": "3d", "limit": float64(25)})
	run(map[string]any{"query": "deploy", "timeframe": "3d", "limit": float64(5000)})
	got := *reqs
	if got[0].count != "25" {
		t.Errorf("limit 25 sent count=%q", got[0].count)
	}
	if got[1].count != "100" {
		t.Errorf("limit above Slack's max sent count=%q, want 100", got[1].count)
	}
}

// The cursor pins the window that produced it, so page 2 never drifts to a
// different window (and never re-widens).
func TestSearchCursorPinsTheWindow(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)
	first := run(map[string]any{"query": "deploy"})
	run(map[string]any{"query": "deploy", "cursor": first.Pagination.NextCursor})
	got := *reqs
	if len(got) != 2 || got[0].query != got[1].query {
		t.Errorf("window drifted between pages: %+v", got)
	}
}

// cursorOf builds a cursor from raw JSON, so a test can forge fields the
// encoder would never produce.
func cursorOf(t *testing.T, v map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// validCursor is a cursor for search "deploy" that the decoder accepts, with
// overrides applied.
func validCursor(t *testing.T, over map[string]any) string {
	t.Helper()
	c := map[string]any{"v": 1, "p": 2, "c": 100, "s": "2026-01-02", "d": features.SearchDigestForTest("deploy", nil, nil)}
	for k, v := range over {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return cursorOf(t, c)
}

// A search that widened keeps the widened window on page 2 and does not
// widen again: exactly one request, carrying the same after: date.
func TestSearchPage2KeepsTheWidenedWindowWithoutWideningAgain(t *testing.T) {
	run, reqs := pagedSearchFunc(t, func(n int, _ pagedRequest) (int, bool) {
		if n == 0 {
			return 0, false // the default window finds nothing
		}
		return 3, true
	})
	first := run(map[string]any{"query": "deploy"})
	if first.Pagination == nil || first.Pagination.NextCursor == "" {
		t.Fatalf("widened search with pages > 1 returned no cursor: %+v", first.Pagination)
	}
	if len(*reqs) != 2 {
		t.Fatalf("page 1 made %d requests, want narrow + widen", len(*reqs))
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.Pagination.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var cur map[string]any
	_ = json.Unmarshal(raw, &cur)
	if cur["w"] != true {
		t.Errorf("cursor does not record the widening: %s", raw)
	}

	second := run(map[string]any{"query": "deploy", "cursor": first.Pagination.NextCursor})
	if !second.Success {
		t.Fatalf("page 2 refused: %s", second.Message)
	}
	got := *reqs
	if len(got) != 3 {
		t.Fatalf("page 2 made %d requests, want exactly 1", len(got)-2)
	}
	if got[2].query != got[1].query {
		t.Errorf("page 2 ran %q, the widened page 1 ran %q", got[2].query, got[1].query)
	}
	if got[2].page != "2" {
		t.Errorf("page 2 requested page %q", got[2].page)
	}
}

// The cursor's window wins over today's: a cursor carrying a past date sends
// that date, not a fresh one.
func TestSearchCursorSendsItsPinnedDate(t *testing.T) {
	run, reqs := pagedSearchWith(t, 5)
	res := run(map[string]any{"query": "deploy", "cursor": validCursor(t, map[string]any{"s": "2026-01-02"})})
	if !res.Success {
		t.Fatalf("refused: %s", res.Message)
	}
	got := *reqs
	if len(got) != 1 || !strings.Contains(got[0].query, "after:2026-01-02") {
		t.Errorf("page 2 did not send the pinned date: %+v", got)
	}
}

// Slack stops paging at page 100: the last reachable page offers no cursor,
// says the tail is unreachable, and is not complete.
func TestSearchStopsAtSlacksPageCap(t *testing.T) {
	run, reqs := pagedSearchWith(t, 150)

	before := run(map[string]any{"query": "deploy", "cursor": validCursor(t, map[string]any{"p": 99})})
	if before.Pagination == nil || before.Pagination.NextCursor == "" {
		t.Fatalf("page 99 of 150 should offer page 100: %+v", before.Pagination)
	}

	last := run(map[string]any{"query": "deploy", "cursor": validCursor(t, map[string]any{"p": 100})})
	if !last.Success {
		t.Fatalf("page 100 refused: %s", last.Message)
	}
	if last.Pagination != nil {
		t.Errorf("page 100 offers a cursor Slack would reject: %+v", last.Pagination)
	}
	cov := last.Data.(map[string]any)["coverage"].(map[string]any)
	if cov["complete"] != false {
		t.Errorf("a capped search reports complete = %v", cov["complete"])
	}
	if !strings.Contains(last.Guidance, "narrow") {
		t.Errorf("guidance does not tell the caller to narrow: %q", last.Guidance)
	}
	if len(*reqs) != 2 {
		t.Errorf("unexpected requests: %d", len(*reqs))
	}
}

// Fields the decoder checks, reached by encoding bad JSON as a valid
// base64url string so it gets past the envelope.
func TestSearchRefusesATamperedCursor(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)
	cases := map[string]string{
		"page zero":    validCursor(t, map[string]any{"p": 0}),
		"page too big": validCursor(t, map[string]any{"p": 101}),
		"count zero":   validCursor(t, map[string]any{"c": 0}),
		"count big":    validCursor(t, map[string]any{"c": 101}),
		"no digest":    validCursor(t, map[string]any{"d": nil}),
		"bad date":     validCursor(t, map[string]any{"s": "yesterday"}),
		"no date":      validCursor(t, map[string]any{"s": nil}),
	}
	for name, cur := range cases {
		if res := run(map[string]any{"query": "deploy", "cursor": cur}); res.Success {
			t.Errorf("%s: tampered cursor was accepted", name)
		}
	}
	if len(*reqs) != 0 {
		t.Errorf("a refused cursor reached Slack: %d requests", len(*reqs))
	}
}

func TestSearchNamesAnOlderCursorVersion(t *testing.T) {
	run, reqs := pagedSearchWith(t, 3)
	res := run(map[string]any{"query": "deploy", "cursor": validCursor(t, map[string]any{"v": 0})})
	if res.Success || !strings.Contains(res.Message, "older version") {
		t.Errorf("an old-version cursor was not named as such: %+v", res)
	}
	if len(*reqs) != 0 {
		t.Errorf("a refused cursor reached Slack")
	}
}

// '#eng' and 'eng' are the same search, so a cursor from one continues with
// the other.
func TestSearchCursorIgnoresChannelNameDecoration(t *testing.T) {
	run, _ := pagedSearchWith(t, 3)
	first := run(map[string]any{"query": "deploy", "in": []any{"#engineering"}})
	second := run(map[string]any{"query": "deploy", "in": []any{"engineering"}, "cursor": first.Pagination.NextCursor})
	if !second.Success {
		t.Errorf("#engineering and engineering are different searches: %s", second.Message)
	}
}

// The echo shows what ran: the clamped limit and the page landed on.
func TestMessagesQueryEchoShowsEffectiveLimitAndPage(t *testing.T) {
	run, _ := pagedSearchWith(t, 3)
	res := run(map[string]any{"query": "deploy", "timeframe": "3d", "limit": float64(5000)})
	for _, want := range []string{"limit=100", "page=1/3"} {
		if !strings.Contains(res.Echo, want) {
			t.Errorf("echo %q lacks %q", res.Echo, want)
		}
	}
	if strings.Contains(res.Echo, "5000") {
		t.Errorf("echo shows the raw limit: %q", res.Echo)
	}
}
