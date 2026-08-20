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

	channels := stringList(params["in"])
	people := stringList(params["from"])

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

	built, since := buildQuery(p, query, timeframe, channels, people)
	messages, err := runSearch(ctx, api, built)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Search failed: %v", err),
		}, nil
	}

	widened := false
	if len(messages.Matches) == 0 && !pinned {
		// Nothing in the default window. Widen once rather than handing back an
		// empty result the caller cannot distinguish from a wrong window.
		widened = true
		since = time.Now().Add(-widenTo)
		built, _ = buildQueryFrom(p, query, since, channels, people)
		messages, err = runSearch(ctx, api, built)
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Search failed while widening: %v", err),
			}, nil
		}
	}

	render := newBodyRenderer(p)
	results := make([]map[string]interface{}, 0, len(messages.Matches))

	for _, match := range messages.Matches {
		who := userLabel(p, match.User)

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

	coverage := map[string]interface{}{
		"searchedSince": since.Format("2006-01-02"),
		"window":        describeSearchWindow(since, widened, pinned),
		"widened":       widened,
		"channels":      channels,
		"from":          people,
		"totalMatches":  messages.Total,
		"returned":      len(results),
		"complete":      messages.Total <= len(results),
	}
	if len(fromResolutions) > 0 {
		coverage["fromResolved"] = fromResolutions
	}

	result := &FeatureResult{
		Success:     true,
		ResultCount: len(results),
		Data: map[string]interface{}{
			"query":    query,
			"results":  results,
			"coverage": coverage,
		},
	}

	if len(results) == 0 {
		result.Message = fmt.Sprintf("Nothing matching %q since %s.", query, since.Format("2006-01-02"))
		result.Guidance = fmt.Sprintf(
			"Searched %s. Slack search only covers channels you are in — a message in a channel you have not joined will not appear.",
			coverage["window"])
		result.NextActions = []string{
			"Try different terms: messages query='<other terms>'",
			"Narrow to one place: messages target='<channel or person>'",
		}
		return result, nil
	}

	result.Message = fmt.Sprintf("%d results for %q.", len(results), query)
	result.NextActions = []string{
		"Read one in full: messages target='<handle from a result>'",
	}
	if messages.Total > len(results) {
		result.Guidance = fmt.Sprintf("Showing %d of %d matches, newest first.", len(results), messages.Total)
	}
	if widened {
		result.Guidance = strings.TrimSpace(result.Guidance +
			fmt.Sprintf(" Nothing matched in the last %s, so this widened to %s.", timeframe, coverage["window"]))
	}

	return result, nil
}

func runSearch(ctx context.Context, api *slack.Client, query string) (*slack.SearchMessages, error) {
	searchParams := slack.NewSearchParameters()
	searchParams.Sort = "timestamp"
	searchParams.SortDirection = "desc"
	searchParams.Count = 100
	searchParams.Page = 1

	log.Printf("search: %s", query)
	return api.SearchMessagesContext(ctx, query, searchParams)
}

// buildQuery renders a search, returning the query and the point it searches
// back to.
func buildQuery(p *provider.ApiProvider, query, timeframe string, channels, people []string) (string, time.Time) {
	return buildQueryFrom(p, query, timeframeStart(timeframe), channels, people)
}

func buildQueryFrom(p *provider.ApiProvider, query string, since time.Time, channels, people []string) (string, time.Time) {
	parts := []string{query}

	// An absolute date, because Slack's search grammar takes one. The previous
	// form rendered "after:-30d", which Slack does not parse as a relative
	// offset — so the filter was quietly ignored and results came back from
	// outside the window the caller asked for.
	parts = append(parts, "after:"+since.Format("2006-01-02"))

	for _, ch := range channels {
		name := strings.TrimPrefix(strings.TrimSpace(ch), "#")
		if name == "" {
			continue
		}
		parts = append(parts, "in:#"+name)
	}
	for _, person := range people {
		name := strings.TrimPrefix(strings.TrimSpace(person), "@")
		if name == "" {
			continue
		}
		parts = append(parts, "from:@"+name)
	}

	return strings.Join(parts, " "), since
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
