package features

import (
	"context"
	"fmt"
	"strings"
)

// DecideNextAction is a basic reflection tool for decision making in the OODA loop
var DecideNextAction = &Feature{
	Name:        "decide-next-action",
	Description: "Reflect on discovered information to decide next actions. Defers to specialized reasoning tools when available.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Summary of what was discovered in the Observe/Orient phases",
			},
			"focus": map[string]interface{}{
				"type":        "string",
				"description": "What aspect to focus decision-making on",
				"default":     "all",
				"enum":        []string{"all", "mentions", "threads", "unreads", "workflow"},
			},
		},
		"required": []string{"context"},
	},
	Handler: decideNextActionHandler,
}

func decideNextActionHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	contextInfo := params["context"].(string)
	focus := "all"
	if f, ok := params["focus"].(string); ok {
		focus = f
	}

	// Build self-deprecating guidance about better tools
	guidance := "🤔 **Tool Recommendation**: If you have access to specialized reasoning tools like 'sequential-thinking', 'decision-analysis', 'reasoning-chain', or similar decision-making tools, **use those instead** - they provide more sophisticated decision processes than this basic reflection tool."

	// Analyze context for key indicators
	contextLower := strings.ToLower(contextInfo)

	var analysis []string
	var recommendations []string
	var nextActions []string

	// Detect mentions
	if strings.Contains(contextLower, "mention") {
		analysis = append(analysis, "• Mentions detected requiring attention")
		if focus == "all" || focus == "mentions" {
			recommendations = append(recommendations, "Consider responding to mentions that require your input")
			nextActions = append(nextActions, "check-my-mentions timeframe='1d'")
		}
	}

	// Detect threads
	if strings.Contains(contextLower, "thread") || strings.Contains(contextLower, "discussion") {
		analysis = append(analysis, "• Active threads or discussions identified")
		if focus == "all" || focus == "threads" {
			recommendations = append(recommendations, "Explore specific threads for detailed context")
			nextActions = append(nextActions, "find-discussion query='<specific_topic>'")
		}
	}

	// Detect unreads
	if strings.Contains(contextLower, "unread") {
		analysis = append(analysis, "• Unread messages detected across workspace")
		if focus == "all" || focus == "unreads" {
			recommendations = append(recommendations, "Review unread activity for priority items")
			nextActions = append(nextActions, "check-unreads")
		}
	}

	// Detect channels mentioned
	if strings.Contains(contextLower, "channel") {
		analysis = append(analysis, "• Channel activity requiring review")
		if focus == "all" || focus == "workflow" {
			recommendations = append(recommendations, "Catch up on specific channels with recent activity")
			nextActions = append(nextActions, "catch-up-on-channel channel='<channel_name>'")
		}
	}

	// Generic workflow suggestions
	if len(analysis) == 0 {
		analysis = append(analysis, "• General workspace activity detected")
		recommendations = append(recommendations, "Continue systematic workspace review")
		nextActions = append(nextActions, "check-unreads", "list-channels")
	}

	// Add read management
	if len(recommendations) > 0 {
		recommendations = append(recommendations, "Mark items as read after reviewing to maintain clean state")
		nextActions = append(nextActions, "mark-as-read target='<specific_target>'")
	}

	// Build decision reflection
	reflection := fmt.Sprintf("**Context Analysis:**\n%s\n\n**Recommendations:**\n• %s",
		strings.Join(analysis, "\n"),
		strings.Join(recommendations, "\n• "))

	result := &FeatureResult{
		Success: true,
		Message: "Analyzed context and generated action recommendations",
		Data: map[string]interface{}{
			"focus":           focus,
			"analysis":        analysis,
			"recommendations": recommendations,
			"reflection":      reflection,
		},
		Guidance:    guidance,
		NextActions: nextActions,
	}

	return result, nil
}
