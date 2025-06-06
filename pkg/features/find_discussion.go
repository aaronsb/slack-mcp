package features

import (
	"context"
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
		},
		"required": []string{},
	},
	Handler: findDiscussionHandler,
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
		return getThreadContextImpl(ctx, params, threadId)
	}

	// Otherwise, search for discussions
	if query == "" {
		return &FeatureResult{
			Success: false,
			Message: "Please provide either a search query or a threadId",
		}, nil
	}
	
	// Implementation
	result, err := searchMessagesImpl(ctx, params, query)
	return result, err
}