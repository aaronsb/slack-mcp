package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"strings"
)

// CheckUnreads provides a comprehensive view of all unread activity
var CheckUnreads = &Feature{
	Name:        "check-unreads",
	Description: "Get a summary of all your unread messages - DMs, mentions, and important channel activity",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"focus": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"all", "dms", "mentions", "channels"},
				"description": "What type of unreads to focus on",
				"default":     "all",
			},
			"includeChannels": map[string]interface{}{
				"type":        "boolean",
				"description": "Include unread channel messages (not just mentions)",
				"default":     false,
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum items per category (default: 10, max: 25)",
				"default":     10,
			},
		},
	},
	Handler: checkUnreadsSimple,
}

func checkUnreadsHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	focus := "all"
	if f, ok := params["focus"].(string); ok {
		focus = f
	}

	includeChannels := false
	if i, ok := params["includeChannels"].(bool); ok {
		includeChannels = i
	}

	limit := 10
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 25 {
			limit = 25
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
	usersMap := provider.ProvideUsersMap()

	// Initialize result categories
	unreads := map[string]interface{}{
		"dms":      []map[string]interface{}{},
		"mentions": []map[string]interface{}{},
		"channels": []map[string]interface{}{},
	}

	stats := map[string]interface{}{
		"totalDMs":      0,
		"totalMentions": 0,
		"totalChannels": 0,
		"urgent":        0,
	}

	// Get all conversations
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

	// Process each channel for unreads
	for _, channel := range channels {
		// Skip if no unread messages
		if channel.UnreadCount == 0 {
			continue
		}

		// Check channel type
		isDM := channel.IsIM || channel.IsMpIM

		// Skip channels if not requested
		if !isDM && !includeChannels && focus == "dms" {
			continue
		}

		// Get recent messages
		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: channel.ID,
			Limit:     10,
		}

		resp, err := api.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			continue
		}

		// Process DMs
		if isDM && (focus == "all" || focus == "dms") {
			for i, msg := range resp.Messages {
				if i >= limit {
					break
				}

				// Skip if from self
				if msg.User == currentUserID {
					continue
				}

				authorName := getUserName(msg.User, usersMap)
				isUrgent := categorizeUrgency(msg.Text) == "high"

				if isUrgent {
					stats["urgent"] = stats["urgent"].(int) + 1
				}

				dm := map[string]interface{}{
					"type":        "dm",
					"author":      authorName,
					"message":     msg.Text,
					"timestamp":   formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
					"channelId":   channel.ID,
					"unreadCount": channel.UnreadCount,
					"urgent":      isUrgent,
				}

				unreads["dms"] = append(unreads["dms"].([]map[string]interface{}), dm)
				stats["totalDMs"] = stats["totalDMs"].(int) + 1
			}
		}

		// Process mentions in channels
		if !isDM && (focus == "all" || focus == "mentions") {
			mentionPattern := fmt.Sprintf("<@%s>", currentUserID)

			for _, msg := range resp.Messages {
				if strings.Contains(msg.Text, mentionPattern) {
					authorName := getUserName(msg.User, usersMap)
					isUrgent := categorizeUrgency(msg.Text) == "high"

					if isUrgent {
						stats["urgent"] = stats["urgent"].(int) + 1
					}

					mention := map[string]interface{}{
						"type":      "mention",
						"channel":   channel.Name,
						"author":    authorName,
						"message":   msg.Text,
						"timestamp": formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
						"channelId": channel.ID,
						"threadId":  fmt.Sprintf("%s:%s", channel.ID, msg.Timestamp),
						"urgent":    isUrgent,
					}

					unreads["mentions"] = append(unreads["mentions"].([]map[string]interface{}), mention)
					stats["totalMentions"] = stats["totalMentions"].(int) + 1

					if len(unreads["mentions"].([]map[string]interface{})) >= limit {
						break
					}
				}
			}
		}

		// Process general channel unreads if requested
		if !isDM && includeChannels && (focus == "all" || focus == "channels") {
			if channel.UnreadCount > 0 {
				channelInfo := map[string]interface{}{
					"type":        "channel",
					"channel":     channel.Name,
					"channelId":   channel.ID,
					"unreadCount": channel.UnreadCount,
					"lastMessage": "Multiple unread messages",
				}

				// Get preview of last message
				if len(resp.Messages) > 0 {
					lastMsg := resp.Messages[0]
					authorName := getUserName(lastMsg.User, usersMap)
					channelInfo["lastMessage"] = fmt.Sprintf("%s: %s", authorName, truncateMessage(lastMsg.Text, 100))
					channelInfo["timestamp"] = formatTimestamp(parseSlackTimestamp(lastMsg.Timestamp))
				}

				unreads["channels"] = append(unreads["channels"].([]map[string]interface{}), channelInfo)
				stats["totalChannels"] = stats["totalChannels"].(int) + 1

				if len(unreads["channels"].([]map[string]interface{})) >= limit {
					break
				}
			}
		}
	}

	// Build result
	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"unreads": unreads,
			"stats":   stats,
			"focus":   focus,
		},
		Message: fmt.Sprintf("Found %d DMs, %d mentions, %d channels with unreads",
			stats["totalDMs"], stats["totalMentions"], stats["totalChannels"]),
		ResultCount: stats["totalDMs"].(int) + stats["totalMentions"].(int) + stats["totalChannels"].(int),
	}

	// Add guidance based on findings
	if stats["urgent"].(int) > 0 {
		result.Guidance = fmt.Sprintf("🚨 You have %d urgent items that need immediate attention", stats["urgent"].(int))
	} else if stats["totalDMs"].(int) > 0 {
		result.Guidance = fmt.Sprintf("💬 You have %d unread DMs to catch up on", stats["totalDMs"].(int))
	} else if stats["totalMentions"].(int) > 0 {
		result.Guidance = fmt.Sprintf("📢 You have %d mentions to review", stats["totalMentions"].(int))
	} else {
		result.Guidance = "✅ You're all caught up!"
	}

	// Add next actions
	result.NextActions = []string{}
	if stats["totalDMs"].(int) > 0 {
		result.NextActions = append(result.NextActions, "Use 'catch-up-on-channel' with a DM channel ID to see full conversation")
	}
	if stats["totalMentions"].(int) > 0 {
		result.NextActions = append(result.NextActions, "Use 'find-discussion' with threadId to see full thread context")
	}

	return result, nil
}

func getUserName(userID string, usersMap map[string]slack.User) string {
	if user, ok := usersMap[userID]; ok {
		if user.RealName != "" {
			return user.RealName
		}
		return user.Name
	}
	return "Unknown User"
}

func truncateMessage(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
