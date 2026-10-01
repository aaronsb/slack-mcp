package features

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// widenTo is the window a search falls back to when the first attempt finds
// nothing. An empty result and a badly chosen window are indistinguishable to
// the caller, so rather than return the first and let them guess, search widens
// once and says that it did.
const widenTo = 365 * 24 * time.Hour

func searchUsingOfficialAPI(ctx context.Context, p *provider.ApiProvider, query string, params map[string]interface{}) (*FeatureResult, error) {
	api, err := p.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get Slack client: %v", err),
		}, nil
	}

	timeframe, pinned := params["timeframe"].(string)
	if !pinned || timeframe == "" {
		timeframe = "1w"
	}

	people := stringList(params["from"])
	filters, refusal := parseSearchFilters(ctx, p, query, params)
	if refusal != nil {
		return refusal, nil
	}
	channels := filters.channels

	// Resolve every person before rendering the query: a guessed handle in
	// from: makes Slack return an empty result indistinguishable from "this
	// person said nothing" (ADR-005; the ladder runs on cached state only).
	resolvedFrom := make([]string, 0, len(people))
	fromResolutions := make([]map[string]interface{}, 0, len(people))
	var unresolved []provider.PersonResolution
	for _, person := range people {
		r := p.ResolvePerson(person)
		if !r.Resolved {
			// A departed person's history is still searchable: a unique
			// tombstoned match resolves for this read path. The refusal
			// stands for writes and for genuine ambiguity.
			if r.Reason == "tombstoned" && len(r.Candidates) == 1 {
				resolvedFrom = append(resolvedFrom, r.Candidates[0].Handle)
				fromResolutions = append(fromResolutions, map[string]interface{}{
					"input": r.Input, "handle": r.Candidates[0].Handle, "via": "tombstoned",
				})
				continue
			}
			unresolved = append(unresolved, r)
			continue
		}
		resolvedFrom = append(resolvedFrom, r.Handle)
		fromResolutions = append(fromResolutions, map[string]interface{}{
			"input": r.Input, "handle": r.Handle, "via": r.Via,
		})
	}
	if len(unresolved) > 0 {
		return unresolvedPeopleResult(p, unresolved), nil
	}
	people = resolvedFrom

	count := searchDefaultCount
	limit, limited := explicitLimit(params)
	if limited {
		count = limit
	}
	digest := searchDigest(query, filters, people)
	page := 1
	widened := false
	continuing := false
	cursorIn := ""

	var built string
	var since time.Time
	if raw, _ := params["cursor"].(string); strings.TrimSpace(raw) != "" {
		cursorIn = raw
		cur, err := decodeSearchCursor(raw)
		if err == errCursorVersion {
			return &FeatureResult{
				Success:  false,
				Message:  "That cursor came from an older version of search.",
				Guidance: "Rerun the search without cursor= to get a current one.",
			}, nil
		}
		if err != nil {
			return &FeatureResult{
				Success:  false,
				Message:  "That cursor is not a search cursor.",
				Guidance: "Cursors come from the previous page of this same query; drop cursor= to start the search over.",
			}, nil
		}
		if cur.Digest != digest || (limited && limit != cur.Count) ||
			(pinned && strings.ToLower(strings.TrimSpace(timeframe)) != cur.Timeframe) {
			return &FeatureResult{
				Success: false,
				Message: "That cursor belongs to a different search: the query, in:, from:, limit, or timeframe changed since it was issued.",
				Guidance: "Pass the cursor back with exactly the same query, in, from, limit, and timeframe as the page that returned it, " +
					"or drop cursor= to start this search over.",
			}, nil
		}
		// The cursor pins the window that produced it, so page 2 of a search
		// that widened stays in the widened window, and nothing re-widens.
		if cur.Before != "" {
			filters.before = cur.Before
		}
		since = time.Time{}
		if !cur.Unbounded {
			since, _ = time.Parse("2006-01-02", cur.Since)
		}
		page, count, widened, continuing = cur.Page, cur.Count, cur.Widened, true
		built = buildQueryFrom(query, since, filters, people)
	} else {
		since = searchStart(timeframe, filters)
		built = buildQueryFrom(query, since, filters, people)
	}

	messages, err := runSearch(ctx, api, built, page, count)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Search failed: %v", err),
		}, nil
	}

	if !continuing && len(messages.Matches) == 0 && !pinned && !filters.explicitWindow() {
		// Nothing in the default window. Widen once rather than handing back an
		// empty result the caller cannot distinguish from a wrong window.
		widened = true
		since = time.Now().Add(-widenTo)
		built = buildQueryFrom(query, since, filters, people)
		messages, err = runSearch(ctx, api, built, page, count)
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Search failed while widening: %v", err),
			}, nil
		}
	}

	render := newBodyRenderer(p)
	ext := &externalNamer{}
	results := make([]map[string]interface{}, 0, len(messages.Matches))

	for _, match := range messages.Matches {
		who := userLabel(p, match.User, ext)

		entry := map[string]interface{}{
			// A handle, not a channel ID and a timestamp for the caller to
			// reassemble. Composing "channel:ts" was the format ADR-003 retired.
			"handle":    handle.Message(match.Channel.ID, match.Timestamp),
			"where":     searchLabel(p, match.Channel.ID),
			"who":       who,
			"when":      formatTimestamp(parseSlackTimestamp(match.Timestamp)),
			"text":      render(match.Text),
			"permalink": match.Permalink,
		}

		if match.Previous.Timestamp != "" || match.Next.Timestamp != "" {
			entry["inThread"] = true
		}
		if len(match.Attachments) > 0 {
			entry["hasAttachments"] = true
		}

		results = append(results, entry)
	}
	// Fetched newest-first so the cap keeps the newest; rendered oldest-first
	// so the list reads as a timeline (ADR-011).
	reverseItems(results)

	pages := messages.Paging.Pages
	shownPage := page
	if messages.Paging.Page > 0 {
		shownPage = messages.Paging.Page
	}
	// Slack stops paging at searchMaxPage however many pages it reports; a
	// cursor past it would be refused, and the tail beyond it is unreachable.
	reachable := pages
	if reachable > searchMaxPage {
		reachable = searchMaxPage
	}
	capped := pages > searchMaxPage
	hasMore := shownPage < reachable

	coverage := map[string]interface{}{
		"page":         shownPage,
		"pages":        pages,
		"window":       describeSearchWindow(since, widened, pinned),
		"widened":      widened,
		"channels":     channelNames(channels),
		"from":         people,
		"totalMatches": messages.Total,
		"returned":     len(results),
		// Same meaning as read: complete means this response ends the result
		// set, so the last page of a paged search is complete. It is false when
		// more pages follow, when Slack's page cap hides the tail, or when Slack
		// omitted paging and reported more matches than it returned.
		"complete": !hasMore && !capped && (pages > 1 || messages.Total <= len(results)),
	}
	if !since.IsZero() {
		coverage["searchedSince"] = since.Format("2006-01-02")
	}
	if capped {
		coverage["reachablePages"] = reachable
	}
	if len(fromResolutions) > 0 {
		coverage["fromResolved"] = fromResolutions
	}
	if len(filters.inResolved) > 0 {
		coverage["inResolved"] = filters.inResolved
	}

	result := &FeatureResult{
		Success:     true,
		ResultCount: len(results),
		Data: map[string]interface{}{
			"query":          query,
			"effectiveQuery": built,
			"results":        results,
			"coverage":       coverage,
		},
	}

	if pages > 1 {
		result.EchoSuffix = fmt.Sprintf(" page=%d/%d", shownPage, reachable)
	}

	if hasMore {
		next := searchCursor{Page: shownPage + 1, Count: count, Widened: widened, Digest: digest, Before: filters.before}
		if since.IsZero() {
			next.Unbounded = true
		} else {
			next.Since = since.Format("2006-01-02")
		}
		if pinned {
			next.Timeframe = strings.ToLower(strings.TrimSpace(timeframe))
		}
		result.Pagination = &Pagination{
			Cursor:     cursorIn,
			NextCursor: next.encode(),
			HasMore:    true,
			PageSize:   len(results),
			TotalCount: messages.Total,
		}
	}

	if len(results) == 0 {
		if query == "" {
			result.Message = fmt.Sprintf("Nothing matched the filters (%s).", coverage["window"])
		} else {
			result.Message = fmt.Sprintf("Nothing matching %q (%s).", query, coverage["window"])
		}
		if continuing {
			result.Guidance = fmt.Sprintf("Page %d came back empty: the result set shrank since the cursor was issued (matches were deleted or edited). "+
				"Rerun the search without cursor= to start over.", page)
			result.NextActions = []string{"Start over: messages query='<same terms>'"}
			return result, nil
		}
		result.Guidance = fmt.Sprintf(
			"Searched %s. Slack search only covers channels you are in — a message in a channel you have not joined will not appear.",
			coverage["window"])
		result.NextActions = []string{
			"Try different terms: messages query='<other terms>'",
			"Narrow to one place: messages target='<channel or person>'",
		}
		return result, nil
	}

	if query == "" {
		result.Message = fmt.Sprintf("%d results for the filters.", len(results))
	} else {
		result.Message = fmt.Sprintf("%d results for %q.", len(results), query)
	}
	result.NextActions = []string{
		"Read one in full: messages target='<handle from a result>'",
	}
	if pages > 1 {
		result.Guidance = fmt.Sprintf("Page %d of %d (%d matches in all); pages run newest to oldest.", shownPage, reachable, messages.Total)
		if capped && !hasMore {
			result.Guidance += fmt.Sprintf(" Slack stops search at page %d, so the older matches are unreachable: narrow the search with a shorter timeframe, in=, or from=.", searchMaxPage)
		}
	} else if messages.Total > len(results) {
		result.Guidance = fmt.Sprintf("Showing the newest %d of %d matches.", len(results), messages.Total)
	}
	if widened {
		result.Guidance = strings.TrimSpace(result.Guidance +
			fmt.Sprintf(" Nothing matched in the last %s, so this widened to %s.", timeframe, coverage["window"]))
	}

	return result, nil
}

func runSearch(ctx context.Context, api *slack.Client, query string, page, count int) (*slack.SearchMessages, error) {
	searchParams := slack.NewSearchParameters()
	searchParams.Sort = "timestamp"
	searchParams.SortDirection = "desc"
	searchParams.Count = count
	searchParams.Page = page

	log.Printf("search: %s (page %d, count %d)", query, page, count)
	return api.SearchMessagesContext(ctx, query, searchParams)
}

// searchStart is where the window begins: an explicit after=, the zero time
// when only before= or raw date text bounds it, else the timeframe.
func searchStart(timeframe string, f *searchFilters) time.Time {
	switch {
	case f.after != "":
		t, _ := time.Parse("2006-01-02", f.after)
		return t
	case f.before != "" || f.rawDatesSet:
		return time.Time{}
	}
	return timeframeStart(timeframe)
}

// buildQueryFrom composes the query Slack receives: the raw text verbatim,
// then the structured filters, names only. The zero time means no after:.
func buildQueryFrom(query string, since time.Time, f *searchFilters, people []string) string {
	parts := []string{query}

	// An absolute date, because Slack's search grammar takes one; "after:-30d"
	// is not parsed as a relative offset and was quietly ignored.
	if !since.IsZero() {
		parts = append(parts, "after:"+since.Format("2006-01-02"))
	}
	if f.before != "" {
		parts = append(parts, "before:"+f.before)
	}
	for _, ch := range f.channels {
		parts = append(parts, "in:"+ch)
	}
	for _, person := range people {
		name := strings.TrimPrefix(strings.TrimSpace(person), "@")
		if name == "" {
			continue
		}
		parts = append(parts, "from:@"+name)
	}
	for _, h := range f.has {
		parts = append(parts, "has:"+h)
	}
	if f.thread {
		parts = append(parts, "is:thread")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// timeframeStart turns "3d", "2w", "1m", "1y" into the point to search back to.
// An unrecognised value falls back to a week rather than silently searching a
// month, which is what the previous default did.
func timeframeStart(timeframe string) time.Time {
	now := time.Now()
	timeframe = strings.TrimSpace(strings.ToLower(timeframe))
	if timeframe == "" {
		return now.AddDate(0, 0, -7)
	}

	unit := timeframe[len(timeframe)-1]
	var n int
	if _, err := fmt.Sscanf(timeframe, "%d", &n); err != nil || n <= 0 {
		return now.AddDate(0, 0, -7)
	}

	switch unit {
	case 'd':
		return now.AddDate(0, 0, -n)
	case 'w':
		return now.AddDate(0, 0, -n*7)
	case 'm':
		return now.AddDate(0, -n, 0)
	case 'y':
		return now.AddDate(-n, 0, 0)
	}
	return now.AddDate(0, 0, -7)
}

func describeSearchWindow(since time.Time, widened, pinned bool) string {
	if since.IsZero() {
		return "all dates, bounded only by the date filters in the query"
	}
	days := int(time.Since(since).Hours() / 24)
	switch {
	case widened:
		return fmt.Sprintf("the last %d days, after nothing matched in the requested window", days)
	case pinned:
		return fmt.Sprintf("the last %d days, as requested", days)
	default:
		return fmt.Sprintf("the last %d days", days)
	}
}

// stringList accepts what an MCP host actually delivers.
//
// Arguments arrive as decoded JSON, so an array is []interface{} — never
// []string. Asserting []string meant the in: and from: filters silently never
// applied, and a caller narrowing a search got a workspace-wide one back with
// no indication the narrowing had been dropped.
func stringList(v interface{}) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(list) == "" {
			return nil
		}
		return []string{list}
	}
	return nil
}

// searchLabel names a conversation for a search result, accepting the raw ID
// when the cache has no name — a search result is a pointer, and read reports
// the naming gap when it opens one.
func searchLabel(p *provider.ApiProvider, channelID string) string {
	label, _ := conversationLabel(p, channelID)
	return label
}

// channelNames drops the # from channel tokens, keeping @handle for DMs.
func channelNames(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, strings.TrimPrefix(t, "#"))
	}
	return out
}
