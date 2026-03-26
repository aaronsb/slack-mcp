package features

import (
	"fmt"
)

// WorkflowManager provides dynamic next action suggestions based on context
type WorkflowManager struct {
	personality string
	features    map[string]bool
}

// NewWorkflowManager creates a workflow manager for a personality
func NewWorkflowManager(personality string, availableFeatures []string) *WorkflowManager {
	features := make(map[string]bool)
	for _, f := range availableFeatures {
		features[f] = true
	}

	return &WorkflowManager{
		personality: personality,
		features:    features,
	}
}

// GetNextActions suggests next actions based on tool and result context
func (wm *WorkflowManager) GetNextActions(toolName string, result *FeatureResult, context map[string]interface{}) []string {
	actions := []string{}

	// Only suggest available features
	suggest := func(action string) {
		// Parse tool name from action string (before first space)
		toolPart := action
		for i, ch := range action {
			if ch == ' ' {
				toolPart = action[:i]
				break
			}
		}

		if wm.features[toolPart] {
			actions = append(actions, action)
		}
	}

	switch toolName {
	case "check-unreads":
		// Analyze unread data
		if data, ok := result.Data.(map[string]interface{}); ok {
			if mentions, ok := data["stats"].(map[string]interface{}); ok {
				if totalMentions, _ := mentions["totalMentions"].(int); totalMentions > 0 {
					suggest("check-my-mentions")
				}
			}

			// Suggest specific DMs if present
			if unreads, ok := data["unreads"].(map[string]interface{}); ok {
				if dms, ok := unreads["dms"].([]map[string]interface{}); ok && len(dms) > 0 {
					if dm := dms[0]; dm["channel"] != nil {
						suggest(fmt.Sprintf("catch-up-on-channel channel='%s'", dm["channel"]))
					}
				}
			}

			// If many unreads, suggest bulk clearing
			if stats, ok := data["stats"].(map[string]interface{}); ok {
				if total, _ := stats["totalChannels"].(int); total > 10 {
					suggest("mark-as-read target='all-channels' filter='no-mentions'")
				}
			}
		}

	case "catch-up-on-channel":
		// Based on what was found
		if data, ok := result.Data.(map[string]interface{}); ok {
			// If has important items with threads
			if items, ok := data["importantItems"].([]map[string]interface{}); ok {
				for _, item := range items {
					if item["type"] == "thread" || item["type"] == "decision" {
						suggest("find-discussion query='[topic from thread]'")
						break
					}
				}
			}

			// If pagination available
			if pagination, ok := data["pagination"].(map[string]interface{}); ok {
				if hasMore, _ := pagination["hasMore"].(bool); hasMore {
					if cursor, ok := pagination["nextCursor"].(string); ok && cursor != "" {
						channel := data["channel"].(string)
						suggest(fmt.Sprintf("catch-up-on-channel channel='%s' cursor='%s'", channel, cursor))
					}
				}
			}

			// Always offer to mark as read after catching up
			if channel, ok := data["channel"].(string); ok {
				suggest(fmt.Sprintf("mark-as-read channel='%s'", channel))
			}
		}

		// General suggestions
		suggest("check-my-mentions")

	case "find-discussion":
		if data, ok := result.Data.(map[string]interface{}); ok {
			discussions := data["discussions"].([]map[string]interface{})

			if len(discussions) > 0 {
				// Suggest viewing threads
				for i, disc := range discussions {
					if i >= 2 { // Limit to 2 thread suggestions
						break
					}
					if threadId, ok := disc["threadId"].(string); ok && threadId != "" {
						suggest(fmt.Sprintf("find-discussion threadId='%s'", threadId))
					}
				}

				// Suggest catching up on active channels
				seen := make(map[string]bool)
				for _, disc := range discussions {
					if channel, ok := disc["channel"].(string); ok && !seen[channel] {
						seen[channel] = true
						suggest(fmt.Sprintf("catch-up-on-channel channel='%s'", channel))
						if len(seen) >= 2 { // Limit channel suggestions
							break
						}
					}
				}
			} else {
				// No results - suggest alternatives
				suggest("list-channels search='[related-term]'")
				suggest("check-unreads")
			}
		}

	case "check-my-mentions":
		// Based on urgency and context
		if data, ok := result.Data.(map[string]interface{}); ok {
			if mentions, ok := data["mentions"].([]map[string]interface{}); ok && len(mentions) > 0 {
				// Most urgent first
				if mention := mentions[0]; mention["channel"] != nil {
					suggest(fmt.Sprintf("catch-up-on-channel channel='%s'", mention["channel"]))
				}

				// If it's a thread
				if mention := mentions[0]; mention["threadId"] != nil {
					suggest(fmt.Sprintf("find-discussion threadId='%s'", mention["threadId"]))
				}
			}
		}

		// After reviewing mentions
		suggest("mark-as-read target='all-channels' filter='no-mentions'")

	case "mark-as-read":
		// After marking, check what's left
		suggest("check-unreads")

		// If selectively marked, check mentions
		if _, ok := result.Data.(map[string]interface{}); ok {
			if filter, ok := context["filter"].(string); ok && filter == "no-mentions" {
				suggest("check-my-mentions")
			}
		}

		// Continue with important channels
		suggest("catch-up-on-channel channel='general'")

	case "list-channels":
		// Based on search results
		if data, ok := result.Data.(map[string]interface{}); ok {
			if channels, ok := data["channels"].([]map[string]interface{}); ok && len(channels) > 0 {
				// Suggest first few channels
				for i, ch := range channels {
					if i >= 3 { // Limit suggestions
						break
					}
					if name, ok := ch["name"].(string); ok {
						suggest(fmt.Sprintf("catch-up-on-channel channel='%s'", name))
					}
				}
			}

			// If cache is old
			if summary, ok := data["summary"].(map[string]interface{}); ok {
				if _, ok := summary["cacheAge"].(string); ok {
					// Parse age and suggest refresh if > 1 hour
					suggest("list-channels forceRefresh=true")
				}
			}
		}

		suggest("check-unreads")
		// Search as a secondary option after primary OODA actions
		if data, ok := result.Data.(map[string]interface{}); ok {
			if channels, ok := data["channels"].([]map[string]interface{}); ok && len(channels) > 10 {
				suggest("Can't find a channel? find-discussion query='[topic]' to search across all")
			}
		}
	}

	// Limit to 4 suggestions max
	if len(actions) > 4 {
		actions = actions[:4]
	}

	return actions
}

// GetWorkflowSteps returns steps for predefined workflows
func (wm *WorkflowManager) GetWorkflowSteps(workflow string) []string {
	switch workflow {
	case "morning-review":
		return []string{
			"check-unreads",
			"check-my-mentions",
			"catch-up-on-channel channel='general'",
			"mark-as-read target='everything' filter='no-mentions'",
		}

	case "research-topic":
		return []string{
			"find-discussion query='[topic]'",
			"list-channels search='[related-channel]'",
			"catch-up-on-channel channel='[found-channel]'",
		}

	case "inbox-zero":
		return []string{
			"check-unreads",
			"check-my-mentions",
			"mark-as-read target='everything' filter='no-mentions'",
			"check-unreads", // Verify
		}

	default:
		return []string{}
	}
}
