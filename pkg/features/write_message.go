package features

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
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

	broadcast, _ := params["broadcast"].(bool)
	broadcast = broadcast && threadTs != ""

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

	// Route the target by prefix; a person resolves only exactly (ADR-005).
	// Nothing is opened yet: a person with no DM gets one only after
	// preSend passes.
	dest, terr := resolveWriteDestination(ctx, apiProvider, channel, "to")
	if terr != nil {
		return terr.result(), nil
	}

	// Author the post as a rich_text block with the mrkdwn text as its
	// fallback (#98): the block is how Slack's own composer sends a typed
	// message; the text carries notifications, search, and old clients.
	fallback := text.NormalizeMrkdwn(message)
	out := &outbound{Thread: threadTs, Broadcast: broadcast, Text: message, Fallback: fallback}
	var blocks []slack.Block
	if rt := text.ToRichText(message); len(rt.Elements) > 0 {
		blocks = []slack.Block{rt}
		out.RichText = rt
	}
	if refusal := preSend(ctx, apiProvider, dest, out); refusal != nil {
		return refusal, nil
	}

	channelID, terr := openDestination(ctx, apiProvider, dest)
	if terr != nil {
		return terr.result(), nil
	}

	post := func(withBlocks bool) (string, string, error) {
		options := []slack.MsgOption{slack.MsgOptionText(fallback, false)}
		if withBlocks {
			options = append(options, slack.MsgOptionBlocks(blocks...))
		}
		if threadTs != "" {
			options = append(options, slack.MsgOptionTS(threadTs))
			if broadcast {
				options = append(options, slack.MsgOptionBroadcast())
			}
		}
		return api.PostMessageContext(ctx, channelID, options...)
	}

	// A block validation error means Slack rejected the converter's output
	// and posted nothing, so one retry as text alone cannot double-post. Any
	// other error, or a second failure, is reported as-is.
	rendering := "text"
	blocksRejected := false
	var postedChannel, timestamp string
	if len(blocks) > 0 {
		rendering = "rich_text"
		postedChannel, timestamp, err = post(true)
		if rejected, detail := blocksRejection(err); rejected {
			log.Printf("Slack rejected rich_text blocks (%v %v); retrying once as text", err, detail)
			blocksRejected, rendering = true, "text"
			postedChannel, timestamp, err = post(false)
		}
	} else {
		postedChannel, timestamp, err = post(false)
	}
	if err != nil {
		log.Printf("Failed to send message: %v", err)
		msg := fmt.Sprintf("Failed to send message: %v", err)
		guidance := "⚠️ Check if you have permission to post in this channel"
		if blocksRejected {
			msg = fmt.Sprintf("Failed to send message: Slack rejected the rich-text block, and the mrkdwn text retry failed too: %v", err)
			guidance = "⚠️ Nothing was posted. Both attempts were refused; the error above names the second refusal."
		}
		return &FeatureResult{
			Success:  false,
			Message:  msg,
			Guidance: guidance,
		}, nil
	}

	// Build response with message details
	result := &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Message sent successfully to %s", channel),
		Data: map[string]interface{}{
			"channel":   channel,
			"channelId": postedChannel,
			"timestamp": timestamp,
			"threadTs":  threadTs,
			"message":   message,
			"broadcast": broadcast,
			// rendering is how the post went out: "rich_text" (blocks plus
			// text fallback) or "text"; blocksRejected marks the retry.
			"rendering":      rendering,
			"blocksRejected": blocksRejected,
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

// blockRejections are the chat.postMessage errors that mean Slack refused
// the blocks themselves at validation, before posting anything.
var blockRejections = map[string]bool{
	"invalid_blocks":        true,
	"invalid_blocks_format": true,
	"msg_blocks_too_long":   true,
}

// blocksRejection reports whether Slack refused a post for its blocks, with
// the response metadata messages that name the offending element.
func blocksRejection(err error) (bool, []string) {
	var se slack.SlackErrorResponse
	if errors.As(err, &se) && blockRejections[se.Err] {
		return true, se.ResponseMetadata.Messages
	}
	return false, nil
}
