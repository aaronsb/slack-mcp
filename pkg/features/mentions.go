package features

import (
	"context"
	"fmt"
)

// CheckMyMentions finds all unread mentions requiring attention
var CheckMyMentions = &Feature{
	Name:        "check-my-mentions",
	Description: "See all your unread mentions across channels, grouped by urgency and context",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"urgencyFilter": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"all", "urgent", "questions", "fyi"},
				"description": "Filter mentions by urgency level",
				"default":     "all",
			},
			"includeResolved": map[string]interface{}{
				"type":        "boolean",
				"description": "Include mentions that have already been responded to",
				"default":     false,
			},
			"timeframe": map[string]interface{}{
				"type":        "string",
				"description": "How far back to check (e.g., '1d', '3d', '1w')",
				"default":     "3d",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Pagination cursor from previous request",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum mentions per page (default: 20, max: 50)",
				"default":     20,
			},
		},
	},
	Handler: checkMentionsReal,
}

func checkMentionsHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	urgencyFilter := "all"
	if u, ok := params["urgencyFilter"].(string); ok {
		urgencyFilter = u
	}

	includeResolved := false
	if i, ok := params["includeResolved"].(bool); ok {
		includeResolved = i
	}

	// Mock response demonstrating semantic grouping
	mentions := []map[string]interface{}{
		{
			"urgency":   "high",
			"type":      "direct_question",
			"channel":   "engineering",
			"author":    "lead.dev",
			"message":   "@you Can you review the auth PR before EOD? Blocking deployment",
			"timestamp": "2 hours ago",
			"threadId":  "1234.5678",
			"responded": false,
			"context":   "Part of critical security update discussion",
		},
		{
			"urgency":   "medium",
			"type":      "request",
			"channel":   "product",
			"author":    "pm.sarah",
			"message":   "@you What's your estimate for the user dashboard feature?",
			"timestamp": "Yesterday at 3:30 PM",
			"threadId":  "1234.5679",
			"responded": false,
			"context":   "Q2 planning thread with multiple stakeholders",
		},
		{
			"urgency":   "low",
			"type":      "fyi",
			"channel":   "general",
			"author":    "hr.team",
			"message":   "Reminder @channel: Team lunch is at noon tomorrow!",
			"timestamp": "Yesterday at 9:00 AM",
			"threadId":  "",
			"responded": false,
			"context":   "General announcement",
		},
	}

	// Filter based on urgency if requested
	filteredMentions := mentions
	if urgencyFilter != "all" {
		filtered := []map[string]interface{}{}
		for _, m := range mentions {
			if !includeResolved && m["responded"].(bool) {
				continue
			}
			// Add filtering logic based on urgencyFilter
			filtered = append(filtered, m)
		}
		filteredMentions = filtered
	}

	// Group by urgency
	urgentCount := 0
	for _, m := range filteredMentions {
		if m["urgency"] == "high" {
			urgentCount++
		}
	}

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"mentions": filteredMentions,
			"summary": map[string]interface{}{
				"total":         len(filteredMentions),
				"urgent":        urgentCount,
				"needsResponse": 2,
				"channels":      []string{"engineering", "product", "general"},
			},
		},
		Message: fmt.Sprintf("You have %d unread mentions (%d urgent)", len(filteredMentions), urgentCount),
		NextActions: []string{
			"Use 'find-discussion' with threadId to see full context",
			"Use 'catch-up-on-channel' to see related discussions",
		},
		Guidance:    "🚨 You have 1 urgent mention about a blocking PR review that needs immediate attention",
		ResultCount: len(filteredMentions),
	}, nil
}
