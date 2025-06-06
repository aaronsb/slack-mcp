package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"strings"
)

// catchUpHandlerImpl provides the real implementation with auto-pagination for recent timeframes
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

	// Determine if we should auto-follow cursors
	shouldAutoCursor := shouldAutoFollowCursor(since, cursor)
	
	// Collect all messages if auto-cursoring
	allMessages := []slack.Message{}
	importantItems := []map[string]interface{}{}
	stats := map[string]interface{}{
		"totalMessages": 0,
		"threads":       0,
		"mentions":      0,
		"reactions":     0,
	}
	
	usersMap := provider.ProvideUsersMap()
	currentCursor := cursor
	hasMore := true
	pageCount := 0
	maxPages := 10
	
	// For recent timeframes, be more aggressive
	if isRecentTimeframe(since) && cursor == "" {
		maxPages = 20
	}

	for hasMore && pageCount < maxPages {
		// Fetch messages from channel
		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: channelID,
			Oldest:    fmt.Sprintf("%d", oldest.Unix()),
			Limit:     limit,
			Cursor:    currentCursor,
		}

		resp, err := api.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Failed to fetch channel history: %v", err),
			}, nil
		}

		// Process messages
		for _, msg := range resp.Messages {
			allMessages = append(allMessages, msg)
			
			// Analyze each message
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

		stats["totalMessages"] = len(allMessages)
		pageCount++

		// Decide whether to continue
		if !shouldAutoCursor || !resp.HasMore || cursor != "" {
			// Stop if: not auto-cursoring, no more data, or user provided specific cursor
			hasMore = false
			currentCursor = resp.ResponseMetaData.NextCursor
		} else {
			// Check if we've gone back far enough for recent timeframes
			if len(allMessages) > 0 && isRecentTimeframe(since) {
				oldestFetched := allMessages[len(allMessages)-1]
				msgTime := parseSlackTimestamp(oldestFetched.Timestamp)
				if msgTime.Before(oldest) {
					hasMore = false
				}
			}
			currentCursor = resp.ResponseMetaData.NextCursor
			if currentCursor == "" {
				hasMore = false
			}
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
		Message:     fmt.Sprintf("Found %d messages in #%s from the last %s", len(allMessages), channel, since),
		ResultCount: len(importantItems),
		Pagination: &Pagination{
			Cursor:     cursor,
			NextCursor: currentCursor,
			HasMore:    hasMore && pageCount >= maxPages,
			PageSize:   len(allMessages),
		},
	}

	// Add auto-cursor info if we did multiple pages
	if pageCount > 1 && cursor == "" {
		result.Data.(map[string]interface{})["pagesTraversed"] = pageCount
		result.Message += fmt.Sprintf(" (auto-fetched %d pages)", pageCount)
	}

	// Apply count-based windowing rules
	totalMsgCount := stats["totalMessages"].(int)
	
	// Add contextual guidance based on message count
	if totalMsgCount == 0 {
		result.Guidance = "✅ No activity in this time period"
		result.NextActions = []string{
			"Try a longer timeframe: catch-up-on-channel channel='"+channel+"' since='1w'",
			"Check other channels: list-channels filter='with-unreads'",
			"Search for older discussions: find-discussion query='<topic>' in:"+channel+" timeframe='1m'",
		}
	} else if totalMsgCount <= 3 {
		// 1-3 messages: Full consumption = auto-mark read
		result.Guidance = "💬 Full content displayed - marking as read"
		result.NextActions = []string{
			"Messages auto-marked as read (full consumption)",
			"Check mentions across channels: check-my-mentions",
			"Plan your response: decide-next-action context='Caught up on "+channel+"'",
		}
		// TODO: Actually mark as read
	} else if totalMsgCount <= 15 {
		// 4-15 messages: Thorough review = auto-mark read  
		result.Guidance = "🔍 Thorough review complete - marking as read"
		result.NextActions = []string{
			"Messages auto-marked as read (thorough review)",
			"Check mentions across channels: check-my-mentions",
			"Plan your response: decide-next-action context='Caught up on "+channel+"'",
		}
		// TODO: Actually mark as read
	} else if totalMsgCount <= 50 {
		// 16-50 messages: Triage only = preserve unread
		result.Guidance = "📋 Triage view - important items highlighted (unread preserved)"
		result.NextActions = []string{
			"Review highlighted items above",
		}
		if currentCursor != "" {
			result.NextActions = append(result.NextActions,
				fmt.Sprintf("Continue with more messages: catch-up-on-channel channel='%s' cursor='%s'", channel, currentCursor))
		}
		result.NextActions = append(result.NextActions,
			"Mark specific items as read: mark-as-read channel='"+channel+"'")
	} else {
		// 50+ messages: Surface scan = preserve unread
		result.Guidance = "📡 High volume detected - showing surface scan only"
		result.NextActions = []string{}
		
		if currentCursor != "" {
			result.NextActions = append(result.NextActions,
				fmt.Sprintf("🔄 Continue reading next batch: catch-up-on-channel channel='%s' cursor='%s'", channel, currentCursor))
		}
		
		result.NextActions = append(result.NextActions,
			"Filter by importance: catch-up-on-channel channel='"+channel+"' focus='important'",
			"Search for specific topics: find-discussion query='<topic>' in:"+channel)
		
		// Add semantic prompt for high volume
		if hasMore {
			result.Data.(map[string]interface{})["semanticPrompt"] = fmt.Sprintf(
				"High message volume (%d+ messages). To continue systematic review, use the cursor to fetch the next batch. "+
				"The OODA loop recommends breaking large volumes into manageable chunks for proper orientation.",
				totalMsgCount)
		}
	}
	
	// Add thread and mention suggestions if found
	if len(importantItems) > 0 {
		hasThreads := false
		for _, item := range importantItems {
			if item["type"] == "active_thread" || item["replyCount"] != nil {
				hasThreads = true
				break
			}
		}
		
		// Add contextual search for any message count with important items
		if hasThreads || len(importantItems) < 3 {
			result.NextActions = append(result.NextActions,
				"For specific topics: find-discussion query='<topic>' in:"+channel)
		}
		
		if hasThreads {
			result.NextActions = append(result.NextActions,
				"Join active conversation: write-message channel='"+channel+"' threadTs='<thread>'")
		}
	}

	return result, nil
}

// shouldAutoFollowCursor determines if we should automatically page through results
func shouldAutoFollowCursor(timeframe string, manualCursor string) bool {
	// Never auto-cursor if user provided a manual cursor
	if manualCursor != "" {
		return false
	}
	
	// Auto-cursor for recent timeframes
	return isRecentTimeframe(timeframe)
}

// isRecentTimeframe checks if the timeframe is recent enough to warrant auto-cursoring
func isRecentTimeframe(timeframe string) bool {
	// Auto-cursor for anything less than 1 day
	if strings.HasSuffix(timeframe, "m") || strings.HasSuffix(timeframe, "h") {
		return true
	}
	
	// Check for "1d" or less
	if strings.HasSuffix(timeframe, "d") {
		days := 1
		fmt.Sscanf(timeframe, "%dd", &days)
		return days <= 1
	}
	
	return false
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