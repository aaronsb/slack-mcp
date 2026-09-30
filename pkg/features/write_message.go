package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"log"
)

// WriteMessage sends a message to a channel or DM
var WriteMessage = &Feature{
	Name:        "say",
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

	// Route the target by prefix; a person resolves only exactly (ADR-005)
	channelID, terr := resolveTarget(ctx, apiProvider, channel, provider.WritePolicy)
	if terr != nil {
		return terr.result(), nil
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
			fmt.Sprintf("Read conversation context: messages target='%s' since='1h'", channel),
			fmt.Sprintf("Monitor for responses: messages target='%s' since='30m'", channel),
			fmt.Sprintf("Reply to your message: say to='%s' thread='%s'", channel, timestamp),
		}
		result.Guidance = "💡 Your message was sent."
	} else {
		// Thread reply - focus on thread context
		result.NextActions = []string{
			fmt.Sprintf("Read full thread: messages target='%s' around='%s'", channel, threadTs),
			fmt.Sprintf("Continue thread: say to='%s' thread='%s'", channel, threadTs),
			fmt.Sprintf("See channel context: messages target='%s' since='4h'", channel),
		}
		result.Guidance = "💬 Reply sent to thread. Check the full discussion for context."
	}

	return result, nil
}
