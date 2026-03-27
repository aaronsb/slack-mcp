package features

import (
	"context"
	"fmt"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// ListUsers searches for users by name
var ListUsers = &Feature{
	Name:        "list-users",
	Description: "Search for Slack users by name. Returns matching display names, usernames, and IDs. Use this to find someone before sending a DM or checking their activity.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query — matches against display name, username, or email prefix",
			},
			"includesBots": map[string]interface{}{
				"type":        "boolean",
				"description": "Include bot/app users in results (default false)",
				"default":     false,
			},
		},
		"required": []string{"query"},
	},
	Handler: listUsersHandler,
}

func listUsersHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	query := params["query"].(string)
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return &FeatureResult{
			Success:  false,
			Message:  "Query must be at least 2 characters",
			Guidance: "Provide a name or partial name to search for, e.g. list-users query='clayton'",
		}, nil
	}
	queryLower := strings.ToLower(query)

	includeBots := false
	if b, ok := params["includesBots"].(bool); ok {
		includeBots = b
	}

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	usersMap := apiProvider.ProvideUsersMap()

	var matches []map[string]interface{}

	for _, user := range usersMap {
		if user.Deleted {
			continue
		}
		if !includeBots && user.IsBot {
			continue
		}

		nameLower := strings.ToLower(user.Name)
		realNameLower := strings.ToLower(user.RealName)
		displayNameLower := strings.ToLower(user.Profile.DisplayName)

		if strings.Contains(realNameLower, queryLower) ||
			strings.Contains(nameLower, queryLower) ||
			strings.Contains(displayNameLower, queryLower) {

			displayName := user.RealName
			if displayName == "" {
				displayName = user.Name
			}

			entry := map[string]interface{}{
				"displayName": displayName,
				"username":    user.Name,
				"id":          user.ID,
			}

			if user.Profile.DisplayName != "" && user.Profile.DisplayName != user.RealName {
				entry["profileDisplayName"] = user.Profile.DisplayName
			}
			if user.Profile.Title != "" {
				entry["title"] = user.Profile.Title
			}
			if user.IsBot {
				entry["isBot"] = true
			}

			matches = append(matches, entry)
		}
	}

	if len(matches) == 0 {
		return &FeatureResult{
			Success:  true,
			Message:  fmt.Sprintf("No users found matching '%s'", query),
			Data:     map[string]interface{}{"users": []map[string]interface{}{}},
			Guidance: "Try a different spelling or a shorter query. Use list-users query='<first name>' for broad matches.",
		}, nil
	}

	return &FeatureResult{
		Success:     true,
		Message:     fmt.Sprintf("Found %d user(s) matching '%s'", len(matches), query),
		ResultCount: len(matches),
		Data: map[string]interface{}{
			"users": matches,
		},
		NextActions: []string{
			"Send a DM: send-message channel='<displayName>' message='...'",
			"See DM history: get-context channel='<displayName>'",
		},
	}, nil
}
