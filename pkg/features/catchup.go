package features

import (
	"context"
	"fmt"
	"github.com/slack-go/slack"
	"time"
)

// CatchUpOnChannel provides intelligent channel catch-up functionality
var CatchUpOnChannel = &Feature{
	Name:        "catch-up-on-channel",
	Description: "Get a summary of what you missed in a channel - shows important messages, mentions, and key discussions",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel name (e.g., 'general' or 'engineering') or ID",
			},
			"since": map[string]interface{}{
				"type":        "string",
				"description": "Time period to check (e.g., '1h', '6h', '1d', '3d') or specific time",
				"default":     "1d",
			},
			"focus": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"all", "mentions", "threads", "important"},
				"description": "What to focus on in the summary",
				"default":     "all",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Pagination cursor from previous request",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of items to return (default: 20, max: 50)",
				"default":     20,
			},
		},
		"required": []string{"channel"},
	},
	Handler: catchUpHandlerImpl,
}

func catchUpHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// This is a placeholder - in real implementation, this would:
	// 1. Parse the time period
	// 2. Find the channel by name or ID
	// 3. Fetch messages since the specified time
	// 4. Analyze for important content:
	//    - Messages with many reactions
	//    - Threads with multiple participants
	//    - Messages containing decisions/announcements
	//    - User mentions
	// 5. Group and summarize the content
	// 6. Return structured results with guidance

	channel := params["channel"].(string)
	since := "1d"
	if s, ok := params["since"].(string); ok {
		since = s
	}

	// Mock response showing the semantic approach
	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"channel": channel,
			"period":  since,
			"summary": "Key discussions about Q1 planning and API redesign",
			"importantItems": []map[string]interface{}{
				{
					"type":        "announcement",
					"author":      "sarah.chen",
					"message":     "Team, we're moving the API v2 launch to next sprint",
					"reactions":   12,
					"timestamp":   "10:30 AM",
					"threadCount": 8,
				},
				{
					"type":      "mention",
					"author":    "john.doe",
					"message":   "@you Can you review the PR for the auth changes?",
					"timestamp": "2:15 PM",
					"urgent":    true,
				},
			},
			"statistics": map[string]interface{}{
				"totalMessages":    47,
				"activeThreads":    3,
				"participants":     12,
				"decisionsReached": 2,
			},
		},
		Message: fmt.Sprintf("Found 2 important items in #%s from the last %s", channel, since),
		NextActions: []string{
			"Use 'find-discussion' to see the full API v2 thread",
			"Use 'check-my-mentions' to see all pending mentions",
		},
		Guidance:    "💡 The API v2 discussion has 8 replies - this seems to be an active decision thread you might want to review",
		ResultCount: 2,
	}, nil
}

// Helper function to parse time duration
func parseTimePeriod(period string) (time.Time, error) {
	now := time.Now()

	// Handle relative times like "1h", "3d"
	switch period[len(period)-1] {
	case 'h':
		hours, err := time.ParseDuration(period)
		if err == nil {
			return now.Add(-hours), nil
		}
	case 'd':
		days := 1
		if len(period) > 1 {
			fmt.Sscanf(period, "%dd", &days)
		}
		return now.AddDate(0, 0, -days), nil
	case 'w':
		weeks := 1
		if len(period) > 1 {
			fmt.Sscanf(period, "%dw", &weeks)
		}
		return now.AddDate(0, 0, -weeks*7), nil
	}

	// Try parsing as absolute time
	t, err := time.Parse(time.RFC3339, period)
	if err != nil {
		return now.AddDate(0, 0, -1), nil // Default to 1 day
	}
	return t, nil
}

// Helper to identify important messages
func isImportantMessage(msg slack.Message) bool {
	// High reaction count
	if len(msg.Reactions) > 0 {
		totalReactions := 0
		for _, r := range msg.Reactions {
			totalReactions += r.Count
		}
		if totalReactions >= 5 {
			return true
		}
	}

	// Has many thread replies
	if msg.ReplyCount > 3 {
		return true
	}

	// Contains decision keywords
	decisionKeywords := []string{"decided", "decision", "will", "moving forward", "approved", "rejected"}
	for _, keyword := range decisionKeywords {
		// In real implementation, use better text analysis
		if containsWord(msg.Text, keyword) {
			return true
		}
	}

	return false
}

func containsWord(text, word string) bool {
	// Simplified - real implementation would use proper word boundary detection
	return len(text) > 0 // Placeholder
}
