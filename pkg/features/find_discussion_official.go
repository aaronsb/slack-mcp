package features

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

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
		
		// Primary action: catch up on channels with found content
		channelsSeen := map[string]bool{}
		for _, d := range discussions {
			ch := d["channel"].(string)
			if !channelsSeen[ch] && len(channelsSeen) < 2 {
				channelsSeen[ch] = true
				result.NextActions = append(result.NextActions,
					fmt.Sprintf("Orient - Read context: catch-up-on-channel channel='%s' since='1d'", ch))
			}
		}

		// Decide phase suggestion
		result.NextActions = append(result.NextActions,
			fmt.Sprintf("Decide - Plan response: decide-next-action context='Found %d discussions about %s'", len(discussions), query))

		result.Guidance = "🔍 Found what you're looking for? Return to OODA flow: Orient with context → Decide on action → Act if needed"
	} else {
		result.Guidance = "🔍 No results found. Return to systematic discovery through OODA flow"
		result.NextActions = []string{
			"Observe - Start fresh: check-unreads",
			"Observe - Browse channels: list-channels filter='member-only'",
			"Orient - Recent activity: catch-up-on-channel channel='general' since='1d'",
			"Try different search: find-discussion query='<different_terms>'",
		}
	}

	return result, nil
}

// parseTimeframeToDateFilter converts our timeframe format to Slack's date filter
func parseTimeframeToDateFilter(timeframe string) string {
	// Parse common formats
	if strings.HasSuffix(timeframe, "d") {
		days := strings.TrimSuffix(timeframe, "d")
		if d, err := time.ParseDuration(days + "h"); err == nil {
			days := int(d.Hours() / 24)
			return fmt.Sprintf("after:-%dd", days)
		}
	} else if strings.HasSuffix(timeframe, "w") {
		weeks := strings.TrimSuffix(timeframe, "w")
		if w, err := time.ParseDuration(weeks + "h"); err == nil {
			days := int(w.Hours() / 24 / 7) * 7
			return fmt.Sprintf("after:-%dd", days)
		}
	} else if strings.HasSuffix(timeframe, "m") {
		months := strings.TrimSuffix(timeframe, "m")
		if m, err := time.ParseDuration(months + "h"); err == nil {
			days := int(m.Hours() / 24 / 30) * 30
			return fmt.Sprintf("after:-%dd", days)
		}
	}
	
	// Default to 30 days
	return "after:-30d"
}