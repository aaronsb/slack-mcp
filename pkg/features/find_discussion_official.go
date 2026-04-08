package features

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// searchUsingOfficialAPI uses the official Slack search.messages API
func searchUsingOfficialAPI(ctx context.Context, p *provider.ApiProvider, query string, params map[string]interface{}) (*FeatureResult, error) {
	// Get Slack client
	api, err := p.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get Slack client: %v", err),
		}, nil
	}

	// Build search parameters
	searchParams := slack.NewSearchParameters()
	searchParams.Sort = "timestamp"
	searchParams.SortDirection = "desc"
	searchParams.Count = 100
	searchParams.Page = 1

	// Apply filters from params
	if timeframe, ok := params["timeframe"].(string); ok {
		// Convert our timeframe format to Slack's date filter
		dateFilter := parseTimeframeToDateFilter(timeframe)
		query = query + " " + dateFilter
	}

	if channels, ok := params["in"].([]string); ok && len(channels) > 0 {
		// Convert channel names to IDs
		channelIDs := []string{}
		for _, ch := range channels {
			channelID := p.ResolveChannelID(ch)
			channelIDs = append(channelIDs, channelID)
		}
		query = query + " in:" + strings.Join(channelIDs, ",")
	}

	if users, ok := params["from"].([]string); ok && len(users) > 0 {
		query = query + " from:" + strings.Join(users, ",")
	}

	// Log the search
	log.Printf("Official API search query: %s", query)

	// Perform search
	messages, err := api.SearchMessagesContext(ctx, query, searchParams)
	if err != nil {
		log.Printf("Official API search error: %v", err)
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Search failed: %v", err),
		}, nil
	}

	log.Printf("Official API search results - Total: %d, Matches: %d", messages.Total, len(messages.Matches))

	// Convert to our format
	discussions := []map[string]interface{}{}
	usersMap := p.ProvideUsersMap()

	for _, match := range messages.Matches {
		// Get channel info
		channelName := p.ResolveChannelName(ctx, match.Channel.ID)
		if channelName == "" {
			channelName = match.Channel.Name
		}

		// Get user info
		userName := "unknown"
		if user, ok := usersMap[match.User]; ok {
			userName = user.Name
			if user.RealName != "" {
				userName = user.RealName
			}
		}

		discussion := map[string]interface{}{
			"type":      match.Type,
			"channel":   channelName,
			"channelId": match.Channel.ID,
			"user":      userName,
			"text":      match.Text,
			"timestamp": match.Timestamp,
			"permalink": match.Permalink,
		}

		// Check if it's part of a thread
		if match.Previous.Timestamp != "" || match.Previous2.Timestamp != "" ||
			match.Next.Timestamp != "" || match.Next2.Timestamp != "" {
			discussion["type"] = "thread"
			discussion["threadId"] = fmt.Sprintf("%s:%s", match.Channel.ID, match.Timestamp)
		}

		discussions = append(discussions, discussion)
	}

	result := &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Found %d discussions matching '%s'", len(discussions), query),
		Data: map[string]interface{}{
			"query":       query,
			"discussions": discussions,
			"searchMeta": map[string]interface{}{
				"totalMatches": messages.Total,
				"returned":     len(discussions),
				"timeframe":    params["timeframe"],
			},
		},
		ResultCount: len(discussions),
	}

	// Add next actions based on results
	if len(discussions) > 0 {
		result.NextActions = []string{}

		// Suggest get-context for the first result to get full message content
		first := discussions[0]
		result.NextActions = append(result.NextActions,
			fmt.Sprintf("Full message: get-context channel='%s' messageTs='%s'", first["channel"], first["timestamp"]))

		// Also suggest catch-up on channels with found content
		channelsSeen := map[string]bool{}
		for _, d := range discussions {
			ch := d["channel"].(string)
			if !channelsSeen[ch] && len(channelsSeen) < 2 {
				channelsSeen[ch] = true
				result.NextActions = append(result.NextActions,
					fmt.Sprintf("Read context: catch-up channel='%s' since='1d'", ch))
			}
		}

		result.Guidance = fmt.Sprintf("Found %d discussions about '%s'. Use get-context with a message timestamp to retrieve full content.", len(discussions), query)
	} else {
		result.Guidance = "No results found. Try different search terms or browse channels."
		result.NextActions = []string{
			"check-unreads",
			"list-channels filter='member-only'",
			"catch-up channel='general' since='1d'",
			"search query='<different_terms>'",
		}
	}

	return result, nil
}

// parseTimeframeToDateFilter converts our timeframe format to Slack's date filter
func parseTimeframeToDateFilter(timeframe string) string {
	timeframe = strings.TrimSpace(timeframe)

	if strings.HasSuffix(timeframe, "d") {
		var d int
		if _, err := fmt.Sscanf(timeframe, "%dd", &d); err == nil && d > 0 {
			return fmt.Sprintf("after:-%dd", d)
		}
	} else if strings.HasSuffix(timeframe, "w") {
		var w int
		if _, err := fmt.Sscanf(timeframe, "%dw", &w); err == nil && w > 0 {
			return fmt.Sprintf("after:-%dd", w*7)
		}
	} else if strings.HasSuffix(timeframe, "m") {
		var m int
		if _, err := fmt.Sscanf(timeframe, "%dm", &m); err == nil && m > 0 {
			return fmt.Sprintf("after:-%dd", m*30)
		}
	}

	return "after:-30d"
}
