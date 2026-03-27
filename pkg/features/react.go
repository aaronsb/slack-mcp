package features

import (
	"context"
	"fmt"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// React adds or removes emoji reactions on messages
var React = &Feature{
	Name:        "react",
	Description: "Add or remove an emoji reaction on a message",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel name or ID containing the message",
			},
			"messageTs": map[string]interface{}{
				"type":        "string",
				"description": "Timestamp of the message to react to",
			},
			"emoji": map[string]interface{}{
				"type":        "string",
				"description": "Emoji name without colons (e.g., 'thumbsup', 'heart', 'eyes')",
			},
			"remove": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, remove the reaction instead of adding it",
				"default":     false,
			},
		},
		"required": []string{"channel", "messageTs", "emoji"},
	},
	Handler: reactHandler,
}

func reactHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	channel := params["channel"].(string)
	messageTs := params["messageTs"].(string)
	emoji := params["emoji"].(string)

	remove := false
	if r, ok := params["remove"].(bool); ok {
		remove = r
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
			Message:  fmt.Sprintf("Could not find channel '%s'", channel),
			Guidance: "Use 'list-channels' to see available channels",
		}, nil
	}

	// Normalize emoji name - strip colons if provided
	emojiName := strings.Trim(emoji, ":")

	ref := slack.NewRefToMessage(channelID, messageTs)

	action := "added"
	if remove {
		err = api.RemoveReactionContext(ctx, emojiName, ref)
		action = "removed"
	} else {
		err = api.AddReactionContext(ctx, emojiName, ref)
	}

	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to %s reaction: %v", action[:len(action)-2], err),
		}, nil
	}

	channelName := resolveChannelName(ctx, apiProvider, channelID, channel)

	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Reaction :%s: %s in %s", emojiName, action, channelName),
		Data: map[string]interface{}{
			"action":    action,
			"emoji":     emojiName,
			"channel":   channelName,
			"channelId": channelID,
			"messageTs": messageTs,
		},
	}, nil
}
