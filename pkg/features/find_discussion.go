package features

import (
	"context"
	"fmt"
)

// FindDiscussion searches for conversations using natural language
var FindDiscussion = &Feature{
	Name:        "find-discussion",
	Description: "Search for conversations using natural language - finds threads, decisions, and related discussions",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "What you're looking for (e.g., 'API redesign discussion', 'decision about pricing')",
			},
			"in": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Specific channels to search in (optional, searches all if not specified)",
			},
			"from": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Specific people to search messages from (optional)",
			},
			"timeframe": map[string]interface{}{
				"type":        "string",
				"description": "Time period to search (e.g., 'last week', '1m', 'january')",
				"default":     "1m",
			},
			"threadId": map[string]interface{}{
				"type":        "string",
				"description": "Specific thread ID to retrieve full context (optional)",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Pagination cursor from previous request",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum results per page (default: 10, max: 25)",
				"default":     10,
			},
		},
		"required": []string{},
	},
	Handler: findDiscussionHandlerImpl,
}

func findDiscussionHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	query := ""
	if q, ok := params["query"].(string); ok {
		query = q
	}

	threadId := ""
	if t, ok := params["threadId"].(string); ok {
		threadId = t
	}

	// If threadId is provided, return full thread context
	if threadId != "" {
		return getThreadContext(threadId)
	}

	// Otherwise, search for discussions
	if query == "" {
		return &FeatureResult{
			Success: false,
			Message: "Please provide either a search query or a threadId",
		}, nil
	}

	// Mock semantic search results
	discussions := []map[string]interface{}{
		{
			"relevance":    0.95,
			"type":         "decision_thread",
			"channel":      "engineering",
			"title":        "API v2 Architecture Decision",
			"summary":      "Team decided to use GraphQL for v2 API after discussing REST limitations",
			"participants": []string{"lead.dev", "architect", "you"},
			"messageCount": 23,
			"decision":     "Approved: GraphQL implementation with 2-sprint timeline",
			"timestamp":    "3 days ago",
			"threadId":     "1234.5680",
			"keyPoints": []string{
				"GraphQL chosen for flexibility",
				"2-sprint implementation timeline",
				"Breaking changes documented",
			},
		},
		{
			"relevance":    0.78,
			"type":         "discussion",
			"channel":      "product",
			"title":        "API Documentation Standards",
			"summary":      "Discussion about improving API documentation and examples",
			"participants": []string{"pm.sarah", "tech.writer", "lead.dev"},
			"messageCount": 12,
			"timestamp":    "1 week ago",
			"threadId":     "1234.5681",
			"keyPoints": []string{
				"Need better examples",
				"OpenAPI spec updates required",
				"Documentation review process",
			},
		},
	}

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"query":       query,
			"discussions": discussions,
			"searchMeta": map[string]interface{}{
				"channelsSearched": 15,
				"timeframe":        "last month",
				"totalMatches":     2,
			},
		},
		Message: fmt.Sprintf("Found %d discussions matching '%s'", len(discussions), query),
		NextActions: []string{
			"Use 'find-discussion' with threadId='1234.5680' to see the full GraphQL decision thread",
			"Use 'catch-up-on-channel' channel='engineering' to see recent related activity",
		},
		Guidance:    "💡 The GraphQL decision thread has a final decision and might be what you're looking for",
		ResultCount: len(discussions),
	}, nil
}

func getThreadContext(threadId string) (*FeatureResult, error) {
	// Mock full thread retrieval
	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"threadId": threadId,
			"channel":  "engineering",
			"originalMessage": map[string]interface{}{
				"author":    "lead.dev",
				"message":   "Team, we need to decide on the API architecture for v2...",
				"timestamp": "3 days ago at 10:00 AM",
			},
			"replies": []map[string]interface{}{
				{
					"author":    "architect",
					"message":   "I propose we consider GraphQL for these reasons...",
					"timestamp": "3 days ago at 10:15 AM",
					"reactions": []string{"👍:5", "🤔:2"},
				},
				{
					"author":    "you",
					"message":   "GraphQL would help with our mobile client efficiency...",
					"timestamp": "3 days ago at 10:30 AM",
				},
				// More replies...
			},
			"decision": map[string]interface{}{
				"made":      true,
				"summary":   "GraphQL approved with 2-sprint timeline",
				"author":    "lead.dev",
				"timestamp": "3 days ago at 4:00 PM",
			},
		},
		Message: "Retrieved full thread context with 23 messages",
		NextActions: []string{
			"Use 'browse-team-activity' to see what else the team is working on",
			"Use 'find-discussion' query='GraphQL implementation' to find related discussions",
		},
		Guidance: "📌 This thread contains a final decision that affects the API v2 timeline",
	}, nil
}
