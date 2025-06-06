package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"strings"
)

// checkMentionsReal provides real implementation by scanning channels
func checkMentionsReal(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	urgencyFilter := "all"
	if u, ok := params["urgencyFilter"].(string); ok {
		urgencyFilter = u
	}

	includeResolved := false
	if i, ok := params["includeResolved"].(bool); ok {
		includeResolved = i
	}

	timeframe := "3d"
	if t, ok := params["timeframe"].(string); ok {
		timeframe = t
	}

	limit := 20
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 50 {
			limit = 50
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

	// Get current user info
	authTest, err := api.AuthTest()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get user info: %v", err),
		}, nil
	}
	currentUserID := authTest.UserID

	// Parse time period
	oldest, err := parseTimePeriod(timeframe)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Invalid time period: %v", err),
		}, nil
	}

	// Get list of channels user is member of
	channels, _, err := api.GetConversations(&slack.GetConversationsParameters{
		Types: []string{"public_channel", "private_channel", "mpim", "im"},
		Limit: 200,
	})
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get channels: %v", err),
		}, nil
	}

	// Scan channels for mentions
	mentions := []map[string]interface{}{}
	channelSet := make(map[string]bool)
	urgentCount := 0
	needsResponse := 0
	totalScanned := 0

	usersMap := provider.ProvideUsersMap()
	mentionPattern := fmt.Sprintf("<@%s>", currentUserID)

	// Limit channels to scan based on activity
	for _, channel := range channels {
		if totalScanned >= 10 && len(mentions) >= limit {
			break // Stop if we have enough mentions
		}

		// Skip archived channels
		if channel.IsArchived {
			continue
		}

		// Get recent messages from channel
		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: channel.ID,
			Oldest:    fmt.Sprintf("%d", oldest.Unix()),
			Limit:     50, // Check last 50 messages per channel
		}

		resp, err := api.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			continue // Skip channels we can't access
		}

		totalScanned++

		// Look for mentions in messages
		for _, msg := range resp.Messages {
			// Check if message mentions the user
			if !strings.Contains(msg.Text, mentionPattern) {
				continue
			}

			// Skip if message is from current user (self-mention)
			if msg.User == currentUserID {
				continue
			}

			// Get channel name
			channelName := channel.Name
			if channelName == "" {
				channelName = channel.ID
			}
			channelSet[channelName] = true

			// Get author info
			authorName := "unknown"
			if user, ok := usersMap[msg.User]; ok {
				authorName = user.Name
				if user.RealName != "" {
					authorName = user.RealName
				}
			}

			// Parse timestamp
			msgTime := parseSlackTimestamp(msg.Timestamp)

			// Determine urgency and type
			urgency := categorizeUrgency(msg.Text)
			msgType := categorizeMessageType(msg.Text)

			if urgency == "high" {
				urgentCount++
			}

			// Check if in a thread (has replies)
			responded := msg.ReplyCount > 0 && checkIfUserReplied(api, channel.ID, msg.Timestamp, currentUserID)

			if !includeResolved && responded {
				continue
			}

			if !responded && (msgType == "direct_question" || msgType == "request") {
				needsResponse++
			}

			mention := map[string]interface{}{
				"urgency":   urgency,
				"type":      msgType,
				"channel":   channelName,
				"author":    authorName,
				"message":   msg.Text,
				"timestamp": formatTimestamp(msgTime),
				"threadId":  fmt.Sprintf("%s:%s", channel.ID, msg.Timestamp),
				"responded": responded,
				"context":   fmt.Sprintf("Channel: #%s", channelName),
			}

			// Apply urgency filter
			if urgencyFilter == "all" || urgencyFilter == urgency {
				mentions = append(mentions, mention)
				if len(mentions) >= limit {
					break
				}
			}
		}
	}

	// Build channels list
	channelsList := []string{}
	for ch := range channelSet {
		channelsList = append(channelsList, ch)
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"mentions": mentions,
			"summary": map[string]interface{}{
				"total":           len(mentions),
				"urgent":          urgentCount,
				"needsResponse":   needsResponse,
				"channels":        channelsList,
				"channelsScanned": totalScanned,
			},
		},
		Message:     fmt.Sprintf("Found %d mentions across %d channels", len(mentions), totalScanned),
		ResultCount: len(mentions),
	}

	// Add guidance
	if urgentCount > 0 {
		result.Guidance = fmt.Sprintf("🚨 You have %d urgent mention(s) that need immediate attention", urgentCount)
	} else if needsResponse > 0 {
		result.Guidance = fmt.Sprintf("📋 You have %d mention(s) that need a response", needsResponse)
	} else if len(mentions) == 0 {
		result.Guidance = "✅ No pending mentions found"
	}

	result.NextActions = []string{
		"Use 'find-discussion' with threadId to see full thread context",
		"Use 'catch-up-on-channel' to see activity in specific channels",
	}

	if totalScanned < len(channels) {
		result.NextActions = append(result.NextActions,
			fmt.Sprintf("Note: Scanned %d of %d channels. Some mentions might be in unscanned channels.", totalScanned, len(channels)))
	}

	return result, nil
}

// checkIfUserReplied checks if user has replied in a thread
func checkIfUserReplied(api *slack.Client, channelID, threadTS, userID string) bool {
	// Get thread replies
	msgs, _, _, err := api.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: channelID,
		Timestamp: threadTS,
		Limit:     100,
	})

	if err != nil {
		return false
	}

	// Check if user has replied
	for _, msg := range msgs {
		if msg.User == userID {
			return true
		}
	}

	return false
}

func categorizeUrgency(text string) string {
	lowText := strings.ToLower(text)

	// High urgency keywords
	highKeywords := []string{"urgent", "asap", "blocking", "critical", "emergency", "immediately", "eod", "end of day", "today", "now"}
	for _, keyword := range highKeywords {
		if strings.Contains(lowText, keyword) {
			return "high"
		}
	}

	// Question indicators
	if strings.Contains(text, "?") || strings.Contains(lowText, "can you") || strings.Contains(lowText, "could you") || strings.Contains(lowText, "would you") {
		return "medium"
	}

	return "low"
}

func categorizeMessageType(text string) string {
	lowText := strings.ToLower(text)

	if strings.Contains(text, "?") {
		return "direct_question"
	}

	requestKeywords := []string{"can you", "could you", "please", "need", "want", "would you", "will you"}
	for _, keyword := range requestKeywords {
		if strings.Contains(lowText, keyword) {
			return "request"
		}
	}

	if strings.Contains(lowText, "fyi") || strings.Contains(lowText, "reminder") || strings.Contains(lowText, "heads up") {
		return "fyi"
	}

	return "mention"
}
