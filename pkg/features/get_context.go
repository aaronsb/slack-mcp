package features

import (
	"context"
	"fmt"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// GetContext retrieves conversation history or thread context
var GetContext = &Feature{
	Name:        "get-context",
	Description: "Get conversation history or thread context around a specific message. Does NOT mark messages as read.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel name or ID",
			},
			"messageTs": map[string]interface{}{
				"type":        "string",
				"description": "Message timestamp to retrieve. Returns full content for standalone messages, or full thread for threaded messages. If omitted, gets recent channel messages.",
			},
			"count": map[string]interface{}{
				"type":        "number",
				"description": "Number of messages to fetch (default 10, max 100)",
				"default":     10,
			},
		},
		"required": []string{"channel"},
	},
	Handler: getContextHandler,
}

func getContextHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	channel := params["channel"].(string)

	messageTs := ""
	if ts, ok := params["messageTs"].(string); ok {
		messageTs = ts
	}

	count := 10
	if c, ok := params["count"].(float64); ok {
		count = int(c)
		if count < 1 {
			count = 1
		}
		if count > 100 {
			count = 100
		}
	}

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	// Resolve channel name to ID (also resolves usernames to DM channels)
	channelID := resolveChannelForSending(apiProvider, api, channel)
	if channelID == "" {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Could not find channel or user '%s'", channel),
			Guidance: "Use 'list-channels' to see available channels, or provide a username for DMs",
		}, nil
	}

	usersMap := apiProvider.ProvideUsersMap()
	var messages []slack.Message
	isThread := false

	// If a message timestamp is given, try thread replies first
	if messageTs != "" {
		threadMsgs, _, _, threadErr := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: messageTs,
			Limit:     count,
		})
		if threadErr == nil && len(threadMsgs) > 1 {
			// Actual thread with replies
			isThread = true
			messages = threadMsgs
		} else if threadErr == nil && len(threadMsgs) == 1 {
			// Standalone message (no replies) — return it directly
			messages = threadMsgs
		}
	}

	// Fall back to channel history (only if no messages found above)
	if len(messages) == 0 {
		if messageTs != "" {
			// Get messages around the specified timestamp
			halfCount := count/2 + 1
			beforeMsgs, err := api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
				ChannelID: channelID,
				Latest:    messageTs,
				Limit:     halfCount,
				Inclusive: true,
			})
			if err != nil {
				return &FeatureResult{
					Success: false,
					Message: fmt.Sprintf("Failed to fetch messages: %v", err),
				}, nil
			}

			afterMsgs, _ := api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
				ChannelID: channelID,
				Oldest:    messageTs,
				Limit:     count / 2,
				Inclusive: false,
			})

			// Combine: afterMsgs (newer) + beforeMsgs (older), both newest-first
			messages = append(afterMsgs.Messages, beforeMsgs.Messages...)
		} else {
			// Just get recent messages
			history, err := api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
				ChannelID: channelID,
				Limit:     count,
			})
			if err != nil {
				return &FeatureResult{
					Success: false,
					Message: fmt.Sprintf("Failed to fetch messages: %v", err),
				}, nil
			}
			messages = history.Messages
		}
	}

	// Format messages (reverse to oldest-first)
	formatted := make([]map[string]interface{}, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		userName := msg.User
		if user, ok := usersMap[msg.User]; ok {
			if user.RealName != "" {
				userName = user.RealName
			} else {
				userName = user.Name
			}
		}

		entry := map[string]interface{}{
			"ts":   msg.Timestamp,
			"user": userName,
			"text": msg.Text,
			"time": formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
		}

		if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
			entry["thread_ts"] = msg.ThreadTimestamp
			entry["is_reply"] = true
		}

		if msg.ReplyCount > 0 {
			entry["reply_count"] = msg.ReplyCount
		}

		formatted = append(formatted, entry)
	}

	// Resolve channel name for display
	channelName := resolveChannelName(ctx, apiProvider, channelID, channel)

	result := &FeatureResult{
		Success:     true,
		Message:     fmt.Sprintf("Got %d messages from %s", len(formatted), channelName),
		ResultCount: len(formatted),
		Data: map[string]interface{}{
			"channel":      channelName,
			"channelId":    channelID,
			"messages":     formatted,
			"messageCount": len(formatted),
		},
		NextActions: []string{
			fmt.Sprintf("Send a reply: send-message channel='%s'", channel),
			fmt.Sprintf("Search for related: search query='<topic>' in:'%s'", channel),
		},
	}

	if isThread {
		result.Data.(map[string]interface{})["isThread"] = true
		result.Data.(map[string]interface{})["threadTs"] = messageTs
		result.Guidance = fmt.Sprintf("Thread with %d messages", len(formatted))
	}

	return result, nil
}

func resolveChannelName(ctx context.Context, apiProvider *provider.ApiProvider, channelID string, fallback string) string {
	if name := apiProvider.ResolveChannelName(ctx, channelID); name != "" {
		return "#" + name
	}
	return fallback
}
