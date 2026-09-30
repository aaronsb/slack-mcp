package features_test

import (
	"context"
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
	srv := slacktest.New(t)
	srv.SeedChannels(channel("C1", "engineering"))

	var mu sync.Mutex
	seen := &[]pagedRequest{}
	srv.Handle("search.messages", func(r *http.Request) any {
		_ = r.ParseForm()
		mu.Lock()
		*seen = append(*seen, pagedRequest{r.Form.Get("query"), r.Form.Get("page"), r.Form.Get("count")})
		mu.Unlock()
		page := 1
		if p, err := strconv.Atoi(r.Form.Get("page")); err == nil {
			page = p
		}
		return map[string]any{
			"ok": true,
			"messages": map[string]any{
				"total":  pages * 2,
				"paging": map[string]any{"count": 2, "total": pages * 2, "page": page, "pages": pages},
				"matches": []any{
					aMatch("C1", "1782246118.543969", "deploy one"),
					aMatch("C1", "1782246119.543969", "deploy two"),
				},
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
		res, err := features.FindDiscussion.Handler(context.Background(), params)
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
		"query":   {"query": "rollback", "cursor": first.Pagination.NextCursor},
		"in":      {"query": "deploy", "in": []any{"engineering"}, "cursor": first.Pagination.NextCursor},
		"limit":   {"query": "deploy", "limit": float64(10), "cursor": first.Pagination.NextCursor},
		"garbage": {"query": "deploy", "cursor": "not-a-cursor"},
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
	if got[1].count != "100" && got[1].count != "" {
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
