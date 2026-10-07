package features

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// emojiNamePattern is the shape of a Slack emoji name, with an optional
// skin tone, checked before the reaction reaches the safety gate.
var emojiNamePattern = regexp.MustCompile(`^[a-z0-9_+'-]+(::skin-tone-[2-6])?$`)

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
	channel, _ := params["channel"].(string)
	messageTs, _ := params["messageTs"].(string)
	emoji, _ := params["emoji"].(string)
	if channel == "" || messageTs == "" || emoji == "" {
		return &FeatureResult{
			Success: false,
			Message: "A reaction needs a channel, a messageTs, and an emoji.",
		}, nil
	}

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

	// The emoji name is checked first: every local refusal comes before
	// the destination lookup, which may ask Slack's channel search
	// (ADR-015).
	emojiName := strings.Trim(emoji, ":")
	if !emojiNamePattern.MatchString(emojiName) {
		return &FeatureResult{
			Success: false,
			Message: "emoji must be an emoji name: lowercase letters, digits, and _ + ' -, optionally ::skin-tone-2 to ::skin-tone-6 ('thumbsup'). Nothing was sent.",
		}, nil
	}

	// A reaction is a write: route by prefix, a person only exactly
	// (ADR-005), then through preSend like any say (ADR-013). A reaction
	// lands on a message that exists, so a person with no DM has nothing
	// to react to and no DM is opened for one.
	dest, terr := resolveWriteDestination(ctx, apiProvider, channel, "to")
	if terr != nil {
		return terr.result(), nil
	}
	if dest.ConvID == "" {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("There is no DM with %s, so there is no message there to react to. Nothing was sent.", dest.Name),
		}, nil
	}

	out := &outbound{Emoji: emojiName, Remove: remove, MessageTs: messageTs}
	if refusal := preSend(ctx, apiProvider, dest, out); refusal != nil {
		return refusal, nil
	}
	channelID, terr := openDestination(ctx, apiProvider, dest)
	if terr != nil {
		return terr.result(), nil
	}

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
