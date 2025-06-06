package features

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PaceConversation helps the AI match human conversation pace and decide engagement level
var PaceConversation = &Feature{
	Name:        "pace-conversation",
	Description: "Analyze conversation timing and decide whether to actively engage or wait. Uses thinking time as natural pacing.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel or DM where conversation is happening",
			},
			"lastMessageTime": map[string]interface{}{
				"type":        "string",
				"description": "Timestamp of the last message sent/received",
			},
			"conversationContext": map[string]interface{}{
				"type":        "string",
				"description": "Brief context about the conversation state",
			},
			"thinkingFocus": map[string]interface{}{
				"type":        "string",
				"description": "What to think about while pacing (response strategy, context analysis, etc.)",
				"default":     "next_response",
			},
		},
		"required": []string{"channel", "lastMessageTime", "conversationContext"},
	},
	Handler: paceConversationHandler,
}

func paceConversationHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	channel := params["channel"].(string)
	lastMessageTime := params["lastMessageTime"].(string)
	conversationContext := params["conversationContext"].(string)
	thinkingFocus := "next_response"
	if tf, ok := params["thinkingFocus"].(string); ok {
		thinkingFocus = tf
	}

	// Parse the timestamp to understand timing
	// Slack timestamps are like "1749226415.169859" (unix timestamp with microseconds)
	var lastTime time.Time
	if strings.Contains(lastMessageTime, ".") {
		parts := strings.Split(lastMessageTime, ".")
		if len(parts) > 0 {
			var unixTime int64
			fmt.Sscanf(parts[0], "%d", &unixTime)
			lastTime = time.Unix(unixTime, 0)
		}
	}

	// Calculate time elapsed
	timeSinceLastMessage := time.Since(lastTime)

	// Determine conversation pace and engagement mode
	var mode string
	var guidance string
	var thinkingPrompt string
	var nextActions []string

	switch {
	case timeSinceLastMessage < 10*time.Second:
		// Very active conversation - quick pace
		mode = "active_engaged"
		guidance = "⚡ Active conversation detected. Quick responses expected."
		thinkingPrompt = fmt.Sprintf("Think briefly about: %s. The conversation is active, so a timely response is important.", thinkingFocus)
		nextActions = []string{
			fmt.Sprintf("Check for new messages: catch-up-on-channel channel='%s' since='30s'", channel),
			fmt.Sprintf("Send quick response: write-message channel='%s'", channel),
		}

	case timeSinceLastMessage < 30*time.Second:
		// Moderate pace - thoughtful exchange
		mode = "engaged_thoughtful"
		guidance = "💭 Conversation is active but thoughtful. Take a moment to consider your response."
		thinkingPrompt = fmt.Sprintf("Think about: %s. Consider the context: %s. The pace allows for a thoughtful response.", thinkingFocus, conversationContext)
		nextActions = []string{
			fmt.Sprintf("Read recent context: catch-up-on-channel channel='%s' since='2m'", channel),
			fmt.Sprintf("Monitor before responding: find-discussion channel='%s'", channel),
			fmt.Sprintf("Craft response: write-message channel='%s'", channel),
		}

	case timeSinceLastMessage < 60*time.Second:
		// Slowing down - transition phase
		mode = "transitioning"
		guidance = "🤔 Conversation pace is slowing. Consider if immediate response is needed."
		thinkingPrompt = fmt.Sprintf("Reflect on: %s. The conversation may be winding down. Consider: %s. Is a response still timely?", thinkingFocus, conversationContext)
		nextActions = []string{
			fmt.Sprintf("Check full context: catch-up-on-channel channel='%s' since='5m'", channel),
			fmt.Sprintf("Wait and monitor: find-discussion channel='%s'", channel),
			fmt.Sprintf("Consider if response is still needed: decide-next-action context='Conversation in %s has slowed - last message %v ago'", channel, timeSinceLastMessage),
		}

	case timeSinceLastMessage < 5*time.Minute:
		// Inactive - shift to reactive
		mode = "reactive_waiting"
		guidance = "⏸️ Conversation has paused. Shift to reactive mode - wait for them to re-engage."
		thinkingPrompt = fmt.Sprintf("The conversation has gone quiet. Reflect on: %s. Context: %s. It may be best to wait for them to respond rather than sending more messages.", thinkingFocus, conversationContext)
		nextActions = []string{
			"Monitor for activity: check-unreads",
			fmt.Sprintf("Check back later: catch-up-on-channel channel='%s' since='1h'", channel),
			"Focus on other conversations: check-unreads focus='all'",
		}

	default:
		// Dormant conversation
		mode = "dormant"
		guidance = "💤 Conversation is dormant. No immediate action needed."
		thinkingPrompt = fmt.Sprintf("This conversation from %v ago is dormant. Context: %s. Move on to active items.", timeSinceLastMessage, conversationContext)
		nextActions = []string{
			"Check other activity: check-unreads",
			"Review all channels: list-channels",
		}
	}

	// Build result with pacing guidance
	result := &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Conversation pace analyzed: %s mode", mode),
		Data: map[string]interface{}{
			"channel":              channel,
			"mode":                 mode,
			"timeSinceLastMessage": timeSinceLastMessage.String(),
			"thinkingPrompt":       thinkingPrompt,
			"lastMessageTime":      lastTime.Format("3:04 PM"),
		},
		Guidance:    guidance,
		NextActions: nextActions,
	}

	// Add mode-specific recommendations
	switch mode {
	case "active_engaged":
		result.Data.(map[string]interface{})["recommendation"] = "Respond quickly to maintain engagement"
	case "engaged_thoughtful":
		result.Data.(map[string]interface{})["recommendation"] = "Take time for quality response"
	case "transitioning":
		result.Data.(map[string]interface{})["recommendation"] = "Assess if response adds value"
	case "reactive_waiting":
		result.Data.(map[string]interface{})["recommendation"] = "Wait for them to re-engage"
	case "dormant":
		result.Data.(map[string]interface{})["recommendation"] = "Move to other active items"
	}

	// IMPORTANT: The thinkingPrompt gives the AI something to process, creating natural pacing
	// The AI should use this prompt to think about the conversation while time passes

	return result, nil
}
