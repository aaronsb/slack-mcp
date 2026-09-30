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

	// Resolve channel name to ID
	channelID, terr := resolveChannelForSending(apiProvider, api, channel)
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

// isChannelID checks if a string looks like a Slack channel/DM/group ID
// (capital letter followed by uppercase alphanumeric, no spaces)
func isChannelID(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != 'C' && s[0] != 'D' && s[0] != 'G' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// targetError is why a send target did not resolve: a message for the caller
// and a guidance line naming what to try instead. Candidates are people named
// by handle and display name, never IDs.
type targetError struct {
	Message  string
	Guidance string
}

func (e *targetError) Error() string { return e.Message }

// result renders the failure as a FeatureResult. No Slack write has happened.
func (e *targetError) result() *FeatureResult {
	return &FeatureResult{Success: false, Message: e.Message, Guidance: e.Guidance}
}

// resolveChannelForSending maps a channel or person to a conversation ID.
// Channels and IDs resolve as before. A person resolves only on an exact
// handle, user ID, or exact real/display name (ADR-005, ADR-009): below that
// the answer is a *targetError listing candidates, because a message sent to
// the wrong person cannot be unsent, and a scheduled one fires unwatched.
func resolveChannelForSending(apiProvider *provider.ApiProvider, api *slack.Client, channel string) (string, *targetError) {
	// First try provider's resolver (includes on-demand display name resolution)
	cleanName := strings.TrimPrefix(channel, "#")
	if channelID := apiProvider.ResolveChannelID(cleanName); channelID != cleanName {
		return channelID, nil
	}

	// If it already looks like a channel ID, return it
	if isChannelID(channel) {
		return channel, nil
	}

	cleanUser := strings.TrimPrefix(channel, "@")
	res := apiProvider.ResolvePerson(channel)

	if !res.Resolved || !exactWriteTarget(res.Via) {
		return "", unresolvedTarget(channel, res)
	}

	// Open DM conversation with user
	dm, _, _, err := api.OpenConversation(&slack.OpenConversationParameters{
		Users: []string{res.UserID},
	})
	if err != nil {
		log.Printf("Failed to open DM with user %s: %v", cleanUser, err)
		return "", &targetError{
			Message:  fmt.Sprintf("Could not open a conversation with '%s'", channel),
			Guidance: "Nothing was sent. Retry, or check the handle: estate view='people'",
		}
	}
	return dm.ID, nil
}

// exactWriteTarget reports whether a ladder rung is exact enough to act on
// without asking. Fragment matches ("unique-match") are a read convenience,
// not a write target.
func exactWriteTarget(via string) bool {
	switch via {
	case "user-id", "exact-handle", "unique-name":
		return true
	}
	return false
}

func unresolvedTarget(input string, res provider.PersonResolution) *targetError {
	cands := res.Candidates
	if res.Resolved {
		// A lone fragment match: surface it as the one candidate, unsent.
		cands = []provider.PersonCandidate{{Handle: res.Handle, DisplayName: res.DisplayName}}
	}
	if len(cands) == 0 {
		return &targetError{
			Message:  fmt.Sprintf("Could not find channel or user '%s'", input),
			Guidance: "Nothing was sent. See available channels: estate view='channels' — or provide an exact @handle for DMs",
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "'%s' is not an exact handle or name, so nothing was sent. Candidates:", input)
	for _, c := range cands {
		fmt.Fprintf(&b, "\n  @%s", c.Handle)
		if c.DisplayName != "" && c.DisplayName != c.Handle {
			fmt.Fprintf(&b, " (%s)", c.DisplayName)
		}
		if c.Title != "" {
			fmt.Fprintf(&b, ", %s", c.Title)
		}
		if c.Deleted {
			b.WriteString(" [deactivated]")
		}
	}
	return &targetError{
		Message:  b.String(),
		Guidance: "Retry with the exact @handle of the person you mean, e.g. to='@" + cands[0].Handle + "'",
	}
}
