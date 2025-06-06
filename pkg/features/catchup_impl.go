package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"strings"
)

// catchUpHandlerImpl provides the real implementation with pagination
func catchUpHandlerImpl(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	channel := params["channel"].(string)
	since := "1d"
	if s, ok := params["since"].(string); ok {
		since = s
	}

	cursor := ""
	if c, ok := params["cursor"].(string); ok {
		cursor = c
	}

	limit := 20
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 50 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}
	}

	// Get the API provider
	provider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get Slack API client
	api, err := provider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	// Parse time period to get oldest timestamp
	oldest, err := parseTimePeriod(since)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Invalid time period: %v", err),
		}, nil
	}

	// Find channel by name using provider's cache or use ID directly
	cleanName := strings.TrimPrefix(channel, "#")
	channelID := provider.ResolveChannelID(cleanName)

	// If the resolved ID is the same as input, it means the channel wasn't found in cache
	if channelID == cleanName && !strings.HasPrefix(channelID, "C") && !strings.HasPrefix(channelID, "D") && !strings.HasPrefix(channelID, "G") {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Channel '%s' not found. Use list-channels to see available channels.", channel),
		}, nil
	}

	// Fetch messages from channel
	histParams := &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Oldest:    fmt.Sprintf("%d", oldest.Unix()),
		Limit:     limit,
		Cursor:    cursor,
	}

	resp, err := api.GetConversationHistoryContext(ctx, histParams)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to fetch channel history: %v", err),
		}, nil
	}

	// Analyze messages for importance
	importantItems := []map[string]interface{}{}
	stats := map[string]interface{}{
		"totalMessages": len(resp.Messages),
		"threads":       0,
		"mentions":      0,
		"reactions":     0,
	}

	usersMap := provider.ProvideUsersMap()

	for _, msg := range resp.Messages {
		item := analyzeMessage(msg, usersMap)
		if item != nil {
			importantItems = append(importantItems, item)
		}

		// Update stats
		if msg.ReplyCount > 0 {
			stats["threads"] = stats["threads"].(int) + 1
		}
		if strings.Contains(msg.Text, "<@") {
			stats["mentions"] = stats["mentions"].(int) + 1
		}
		if len(msg.Reactions) > 0 {
			stats["reactions"] = stats["reactions"].(int) + 1
		}
	}

	// Build response
	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"channel":        channel,
			"period":         since,
			"importantItems": importantItems,
			"statistics":     stats,
		},
		Message:     fmt.Sprintf("Found %d messages in #%s from the last %s", len(resp.Messages), channel, since),
		ResultCount: len(importantItems),
		Pagination: &Pagination{
			Cursor:     cursor,
			NextCursor: resp.ResponseMetaData.NextCursor,
			HasMore:    resp.HasMore,
			PageSize:   len(resp.Messages),
		},
	}

	// Add contextual guidance
	if len(importantItems) > 0 {
		result.Guidance = "💡 Found important items that may need your attention"
		result.NextActions = []string{
			"Use 'find-discussion' to explore specific threads",
			"Use 'check-my-mentions' to see mentions across all channels",
		}
	}

	if resp.HasMore {
		result.NextActions = append(result.NextActions,
			fmt.Sprintf("Use cursor='%s' to see more messages", resp.ResponseMetaData.NextCursor))
	}

	return result, nil
}

func analyzeMessage(msg slack.Message, usersMap map[string]slack.User) map[string]interface{} {
	// Skip if not important
	if !isImportantMessage(msg) {
		return nil
	}

	// Get user info
	userName := "unknown"
	if user, ok := usersMap[msg.User]; ok {
		userName = user.Name
		if user.RealName != "" {
			userName = user.RealName
		}
	}

	// Build item
	item := map[string]interface{}{
		"author":    userName,
		"message":   msg.Text,
		"timestamp": msg.Timestamp,
		"type":      "message",
	}

	// Categorize the message
	if msg.SubType == "channel_topic" || msg.SubType == "channel_purpose" {
		item["type"] = "channel_update"
	} else if strings.Contains(strings.ToLower(msg.Text), "decision") ||
		strings.Contains(strings.ToLower(msg.Text), "approved") ||
		strings.Contains(strings.ToLower(msg.Text), "agreed") {
		item["type"] = "decision"
	} else if msg.ReplyCount > 5 {
		item["type"] = "active_thread"
		item["replyCount"] = msg.ReplyCount
		item["lastReply"] = msg.LatestReply
	} else if len(msg.Reactions) > 0 {
		totalReactions := 0
		for _, r := range msg.Reactions {
			totalReactions += r.Count
		}
		item["type"] = "popular"
		item["reactions"] = totalReactions
	}

	return item
}
