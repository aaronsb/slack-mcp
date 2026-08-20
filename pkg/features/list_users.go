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
			"includeDeleted": map[string]interface{}{
				"type":        "boolean",
				"description": "Include deactivated and deleted users as dated facts — when they left, and what they were called (default false)",
				"default":     false,
			},
		},
		"required": []string{"query"},
	},
	Handler: listUsersHandler,
}

func listUsersHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	query, _ := params["query"].(string)
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return &FeatureResult{
			Success:  false,
			Message:  "Query must be at least 2 characters",
			Guidance: "Provide a name or partial name to search for, e.g. estate view='people' person='clayton'",
		}, nil
	}
	queryLower := strings.ToLower(query)

	includeBots := false
	if b, ok := params["includesBots"].(bool); ok {
		includeBots = b
	}

	includeDeleted := false
	if b, ok := params["includeDeleted"].(bool); ok {
		includeDeleted = b
	}

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	usersMap := apiProvider.ProvideUsersMap()
	estateUsers := apiProvider.EstateUsers()

	var matches []map[string]interface{}
	matchedIDs := make(map[string]bool)

	for _, user := range usersMap {
		if user.Deleted && !includeDeleted {
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
			if user.Deleted {
				entry["deleted"] = true
				if rec, ok := estateUsers[user.ID]; ok && rec.Gone != nil {
					entry["reason"] = rec.Gone.Reason
					entry[goneIntervalKey(rec.Gone)] = goneInterval(rec.Gone)
				}
			}

			matches = append(matches, entry)
			matchedIDs[user.ID] = true
		}
	}

	// Tombstoned users the snapshot lost live only in the fold; with
	// includeDeleted they join the results as dated facts.
	if includeDeleted {
		for _, entry := range tombstonedUserMatches(apiProvider, queryLower) {
			if id, _ := entry["id"].(string); !matchedIDs[id] {
				matches = append(matches, entry)
				matchedIDs[id] = true
			}
		}
	}

	coverage := estateCoverage(apiProvider)
	swept := apiProvider.EstateLastFullSweep()

	if len(matches) == 0 {
		data := map[string]interface{}{
			"users":    []map[string]interface{}{},
			"coverage": coverage,
		}

		// A departed person answers as a dated fact, never as a silent gap
		// (ADR-007). Beyond that, "not in the estate" and "never swept" are
		// different claims and the response says which one it makes.
		if tombstoned := tombstonedUserMatches(apiProvider, queryLower); len(tombstoned) > 0 {
			data["tombstoned"] = tombstoned
			return &FeatureResult{
				Success:     true,
				Message:     fmt.Sprintf("No active users match '%s'; %d tombstoned user(s) did", query, len(tombstoned)),
				ResultCount: len(tombstoned),
				Data:        data,
				Guidance:    "These users existed and are gone; each entry carries the interval in which they left.",
			}, nil
		}

		if swept.IsZero() {
			data["reason"] = "unswept"
			return &FeatureResult{
				Success:  true,
				Message:  fmt.Sprintf("No users found matching '%s'", query),
				Data:     data,
				Guidance: "No full sweep yet — this user may exist unseen. Try another spelling, or estate view='people' person='<first name>'.",
			}, nil
		}

		data["reason"] = "never_seen"
		return &FeatureResult{
			Success:  true,
			Message:  fmt.Sprintf("No users found matching '%s' — not in the estate as of %s", query, swept.Format("2006-01-02")),
			Data:     data,
			Guidance: "Try a different spelling or a shorter query. Use estate view='people' person='<first name>' for broad matches.",
		}, nil
	}

	return &FeatureResult{
		Success:     true,
		Message:     fmt.Sprintf("Found %d user(s) matching '%s'", len(matches), query),
		ResultCount: len(matches),
		Data: map[string]interface{}{
			"users":    matches,
			"coverage": coverage,
		},
		NextActions: []string{
			"Send a DM: say to='<displayName>' text='...'",
			"See DM history: messages target='<displayName>'",
		},
	}, nil
}
