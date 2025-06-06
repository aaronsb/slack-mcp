package features

import (
	"context"
	"fmt"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

func searchMessagesImpl(ctx context.Context, params map[string]interface{}, query string) (*FeatureResult, error) {
	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Use official Slack API for search
	return searchUsingOfficialAPI(ctx, apiProvider, query, params)
}

func getThreadContextImpl(ctx context.Context, params map[string]interface{}, threadId string) (*FeatureResult, error) {
	// Parse thread ID (format: channelId:threadTs)
	parts := strings.Split(threadId, ":")
	if len(parts) != 2 {
		return &FeatureResult{
			Success: false,
			Message: "Invalid threadId format. Expected: channelId:threadTs",
		}, nil
	}

	channelId := parts[0]
	threadTs := parts[1]

	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get channel info
	channelInfo, err := apiProvider.GetChannelInfo(ctx, channelId)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not find channel: %v", err),
		}, nil
	}

	channelName := channelInfo.Name
	if channelName == "" {
		channelName = channelId
	}

	// Get Slack client
	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get Slack client: %v", err),
		}, nil
	}

	// Get conversation replies
	params_conv := &slack.GetConversationRepliesParameters{
		ChannelID: channelId,
		Timestamp: threadTs,
		Limit:     100,
	}

	replies, _, _, err := api.GetConversationRepliesContext(ctx, params_conv)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get thread: %v", err),
		}, nil
	}

	// Process messages
	messages := []map[string]interface{}{}
	usersMap := apiProvider.ProvideUsersMap()

	for _, msg := range replies {
		// Get user info
		userName := "unknown"
		if user, ok := usersMap[msg.User]; ok {
			userName = user.Name
			if user.RealName != "" {
				userName = user.RealName
			}
		}

		// Parse timestamp
		msgTime := parseSlackTimestamp(msg.Timestamp)
		timeAgo := formatTimestamp(msgTime)

		message := map[string]interface{}{
			"user":      userName,
			"text":      msg.Text,
			"timestamp": timeAgo,
			"ts":        msg.Timestamp,
		}

		messages = append(messages, message)
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"threadId": threadId,
			"channel":  channelName,
			"messages": messages,
			"threadMeta": map[string]interface{}{
				"messageCount": len(messages),
				"participants": getUniqueParticipants(replies, usersMap),
			},
		},
		Message:     fmt.Sprintf("Found thread with %d messages", len(messages)),
		ResultCount: len(messages),
	}

	// Add next actions
	result.NextActions = []string{
		fmt.Sprintf("Reply to thread: write-message channel='%s' threadTs='%s'", channelName, threadTs),
		fmt.Sprintf("View channel context: catch-up-on-channel channel='%s'", channelName),
		"Mark thread as read: mark-as-read channel='" + channelName + "'",
	}
	result.Guidance = "💬 Thread loaded. You can reply or explore the channel context."

	return result, nil
}

// Helper functions
func parseTimeframeToSearchFilter(timeframe string) string {
	// Convert to days
	days := 30
	if strings.HasSuffix(timeframe, "d") {
		if d, err := fmt.Sscanf(timeframe, "%dd", &days); err == nil && d > 0 {
			days = d
		}
	} else if strings.HasSuffix(timeframe, "w") {
		var weeks int
		if w, err := fmt.Sscanf(timeframe, "%dw", &weeks); err == nil && w > 0 {
			days = weeks * 7
		}
	} else if strings.HasSuffix(timeframe, "m") {
		var months int
		if m, err := fmt.Sscanf(timeframe, "%dm", &months); err == nil && m > 0 {
			days = months * 30
		}
	}

	return fmt.Sprintf("after:-%dd", days)
}

func extractThreadId(permalink string) string {
	// Extract thread_ts from permalink if present
	if strings.Contains(permalink, "thread_ts=") {
		parts := strings.Split(permalink, "thread_ts=")
		if len(parts) > 1 {
			ts := strings.Split(parts[1], "&")[0]
			// Extract channel ID from permalink
			if strings.Contains(permalink, "/archives/") {
				channelParts := strings.Split(permalink, "/archives/")
				if len(channelParts) > 1 {
					channelId := strings.Split(channelParts[1], "/")[0]
					return channelId + ":" + ts
				}
			}
		}
	}
	return ""
}

func extractKeyPoints(text string) []string {
	// Simple key point extraction
	points := []string{}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) > 10 && len(points) < 3 {
			if len(line) > 100 {
				line = line[:100] + "..."
			}
			points = append(points, line)
		}
	}
	return points
}

func getUniqueParticipants(messages []slack.Message, usersMap map[string]slack.User) []string {
	seen := map[string]bool{}
	participants := []string{}

	for _, msg := range messages {
		if !seen[msg.User] {
			seen[msg.User] = true
			userName := msg.User
			if user, ok := usersMap[msg.User]; ok {
				userName = user.RealName
				if userName == "" {
					userName = user.Name
				}
			}
			participants = append(participants, userName)
		}
	}

	return participants
}