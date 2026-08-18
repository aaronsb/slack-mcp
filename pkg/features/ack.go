package features

import (
	"context"
	"fmt"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
)

// Ack records that this agent has seen something.
//
// This is not mark-read. Slack's read marker belongs to the person and is
// shared with their own client; moving it tells colleagues their message was
// read and clears the badge on their phone. A watermark belongs to this agent,
// is invisible to everyone, and only decides what poll reports next time.
var Ack = &Feature{
	Name: "ack",
	Description: "Record that you have seen what poll reported, so it is not reported again. " +
		"Invisible to everyone — this does NOT mark anything read in Slack, and does not " +
		"clear anyone's unread badge. Use mark-read for that, and only when the person " +
		"has actually read it.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"handle": map[string]interface{}{
				"type":        "string",
				"description": "A handle from a poll event.",
			},
			"handles": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Several handles from poll events, acknowledged together.",
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"description": "The scope whose position to advance. Must match the poll that produced the handles.",
				"default":     watermark.DefaultScope,
			},
		},
		"required": []string{},
	},
	Handler: ackHandler,
}

func ackHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	scope := watermark.DefaultScope
	if s, ok := params["scope"].(string); ok && s != "" {
		scope = s
	}

	var raw []string
	if h, ok := params["handle"].(string); ok && h != "" {
		raw = append(raw, h)
	}
	if list, ok := params["handles"].([]interface{}); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" {
				raw = append(raw, s)
			}
		}
	}
	if len(raw) == 0 {
		return &FeatureResult{
			Success:  false,
			Message:  "Nothing to acknowledge.",
			Guidance: "Pass a handle from a poll event, or several as handles.",
		}, nil
	}

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available"}, nil
	}

	identity := apiProvider.ProvideIdentity()
	if identity == nil || identity.Team == "" {
		return &FeatureResult{
			Success: false,
			Message: "Could not determine which workspace this is; a position cannot be stored without it.",
		}, nil
	}

	store, err := watermark.Open(identity.Team, scope)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not open the watermark store: %v", err),
		}, nil
	}

	now := time.Now()
	acked := 0
	threads := 0
	var rejected []map[string]interface{}

	for _, h := range raw {
		ref, err := handle.Decode(h)
		if err != nil {
			rejected = append(rejected, map[string]interface{}{
				"handle": h,
				"reason": "not a handle from a poll event",
			})
			continue
		}

		switch ref.Kind {
		case handle.KindMessage:
			store.Ack(ref.Channel, ref.TS, now)
			acked++

		case handle.KindThread:
			// A thread handle names the root, not the reply that was read.
			// Recording the root is the conservative choice: it can only cause
			// a reply to be reported again, never skipped.
			store.AckThread(ref.Channel, ref.TS, ref.TS, now)
			threads++

		case handle.KindConversation:
			// Conversation handles come from backlog and unreadable events —
			// the cases where poll could NOT show the messages. Acknowledging
			// one would move the position past messages nobody has seen, which
			// is the silent loss this whole design exists to prevent.
			rejected = append(rejected, map[string]interface{}{
				"handle": h,
				"reason": "this is a whole conversation whose messages were not shown; read it before acknowledging",
			})
		}
	}

	if acked == 0 && threads == 0 {
		return &FeatureResult{
			Success:  false,
			Message:  "Nothing was acknowledged.",
			Data:     map[string]interface{}{"rejected": rejected},
			Guidance: "Acknowledge the handles on individual events, not on a whole conversation.",
		}, nil
	}

	if err := store.Save(); err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Acknowledged nothing: the position could not be saved: %v", err),
		}, nil
	}

	result := &FeatureResult{
		Success:     true,
		ResultCount: acked + threads,
		Message:     fmt.Sprintf("Acknowledged %d.", acked+threads),
		Data: map[string]interface{}{
			"acknowledged":      acked,
			"threads":           threads,
			"rejected":          rejected,
			"markedReadInSlack": false,
		},
		NextActions: []string{
			"See what is still outstanding: poll",
		},
	}

	if len(rejected) > 0 {
		result.Guidance = fmt.Sprintf("%d handle(s) were not acknowledged; see rejected.", len(rejected))
	}

	return result, nil
}
