package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"log"
	"strings"
)

// WriteMessage sends a message to a channel or DM
var WriteMessage = &Feature{
	Name:        "write-message",
	Description: "Send a message to a channel or direct message conversation",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel name, DM username, or channel/DM ID to send to",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Message text to send",
			},
			"threadTs": map[string]interface{}{
				"type":        "string",
				"description": "Thread timestamp to reply to (optional)",
			},
		},
		"required": []string{"channel", "message"},
	},
	Handler: writeMessageHandler,
}

func writeMessageHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	channel := params["channel"].(string)
	message := params["message"].(string)
	threadTs := ""
	if ts, ok := params["threadTs"].(string); ok {
		threadTs = ts
	}

	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get Slack API client
	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	// Resolve channel name to ID
	channelID := resolveChannelForSending(apiProvider, api, channel)
	if channelID == "" {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Could not find channel or user '%s'", channel),
			Guidance: "💡 Use 'list-channels' to see available channels or provide a username for DMs",
		}, nil
	}

	// Prepare message options
	options := []slack.MsgOption{
		slack.MsgOptionText(message, false),
	}

	// Add thread timestamp if replying to a thread
	if threadTs != "" {
		options = append(options, slack.MsgOptionTS(threadTs))
	}

	// Send the message
	channelID, timestamp, err := api.PostMessageContext(ctx, channelID, options...)
	if err != nil {
		log.Printf("Failed to send message: %v", err)
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Failed to send message: %v", err),
			Guidance: "⚠️ Check if you have permission to post in this channel",
		}, nil
	}

	// Build response with message details
	result := &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Message sent successfully to %s", channel),
		Data: map[string]interface{}{
			"channel":   channel,
			"channelId": channelID,
			"timestamp": timestamp,
			"threadTs":  threadTs,
			"message":   message,
		},
	}

	// Add next actions with semantic flow
	if threadTs == "" {
		// New message - provide context-aware follow-ups
		result.NextActions = []string{
			fmt.Sprintf("Read conversation context: catch-up-on-channel channel='%s' since='1h'", channel),
			fmt.Sprintf("Monitor for responses: find-discussion threadId='%s:%s'", channelID, timestamp),
			fmt.Sprintf("Reply to your message: write-message channel='%s' threadTs='%s'", channel, timestamp),
		}
		result.Guidance = "💡 Your message was sent. Use catch-up to see recent context or monitor for responses."
	} else {
		// Thread reply - focus on thread context
		result.NextActions = []string{
			fmt.Sprintf("Read full thread: find-discussion threadId='%s:%s'", channelID, threadTs),
			fmt.Sprintf("Continue thread: write-message channel='%s' threadTs='%s'", channel, threadTs),
			fmt.Sprintf("See channel context: catch-up-on-channel channel='%s' since='4h'", channel),
		}
		result.Guidance = "💬 Reply sent to thread. Check the full discussion for context."
	}

	return result, nil
}

func resolveChannelForSending(apiProvider *provider.ApiProvider, api *slack.Client, channel string) string {
	// First try provider's cache for channel names
	cleanName := strings.TrimPrefix(channel, "#")
	if channelID := apiProvider.ResolveChannelID(cleanName); strings.HasPrefix(channelID, "C") || strings.HasPrefix(channelID, "G") {
		return channelID
	}

	// If it looks like a channel ID already, return it
	if strings.HasPrefix(channel, "C") || strings.HasPrefix(channel, "D") || strings.HasPrefix(channel, "G") {
		return channel
	}

	// Try to resolve as a username for DM
	usersMap := apiProvider.ProvideUsersMap()

	// Clean up username
	cleanUser := strings.TrimPrefix(channel, "@")

	// Look for user by name or real name
	var userID string
	for uid, user := range usersMap {
		if user.Name == cleanUser || user.RealName == cleanUser ||
			strings.EqualFold(user.Name, cleanUser) ||
			strings.EqualFold(user.RealName, cleanUser) {
			userID = uid
			break
		}
	}

	if userID == "" {
		// Try partial name match as last resort
		lowerClean := strings.ToLower(cleanUser)
		for uid, user := range usersMap {
			if strings.Contains(strings.ToLower(user.Name), lowerClean) ||
				strings.Contains(strings.ToLower(user.RealName), lowerClean) {
				userID = uid
				break
			}
		}
	}

	if userID != "" {
		// Open DM conversation with user
		channel, _, _, err := api.OpenConversation(&slack.OpenConversationParameters{
			Users: []string{userID},
		})
		if err != nil {
			log.Printf("Failed to open DM with user %s: %v", cleanUser, err)
			return ""
		}
		return channel.ID
	}

	return ""
}
