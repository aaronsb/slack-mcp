package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// ListChannels provides channel listing with smart caching
var ListChannels = &Feature{
	Name:        "list-channels",
	Description: "List available channels from cache - use forceRefresh to update the cache",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "Filter channels by type: 'all', 'public', 'private', 'dm', 'group-dm', 'member-only', 'with-unreads'",
				"default":     "all",
			},
			"search": map[string]interface{}{
				"type":        "string",
				"description": "Search for channels by name (partial match)",
			},
			"forceRefresh": map[string]interface{}{
				"type":        "boolean",
				"description": "Force refresh the channel cache from Slack (rate-limited)",
				"default":     false,
			},
			"includeArchived": map[string]interface{}{
				"type":        "boolean",
				"description": "Include archived channels",
				"default":     false,
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum channels to return (default: 100, max: 500)",
				"default":     100,
			},
		},
		"required": []string{},
	},
	Handler: listChannelsHandler,
}

func listChannelsHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Parse parameters
	filter := "all"
	if f, ok := params["filter"].(string); ok {
		filter = f
	}

	search := ""
	if s, ok := params["search"].(string); ok {
		search = strings.ToLower(s)
	}

	forceRefresh := false
	if f, ok := params["forceRefresh"].(bool); ok {
		forceRefresh = f
	}

	includeArchived := false
	if i, ok := params["includeArchived"].(bool); ok {
		includeArchived = i
	}

	limit := 100
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 500 {
			limit = 500
		}
		if limit < 1 {
			limit = 1
		}
	}

	// Handle cache refresh if requested
	if forceRefresh {
		refreshResult, err := apiProvider.RefreshChannelCache(ctx)
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Failed to refresh channel cache: %v", err),
			}, nil
		}

		if !refreshResult.Allowed {
			return &FeatureResult{
				Success: true,
				Data: map[string]interface{}{
					"refreshStatus": "rate-limited",
					"nextRefreshIn": fmt.Sprintf("%.0f seconds", refreshResult.RetryAfter.Seconds()),
					"lastRefresh":   refreshResult.LastRefresh.Format(time.RFC3339),
				},
				Message: fmt.Sprintf("Cache refresh rate-limited. Next refresh available in %.0f seconds",
					refreshResult.RetryAfter.Seconds()),
				Guidance: "⏳ Channel cache was recently refreshed. Showing cached data.",
			}, nil
		}
	}

	// Get channels from cache
	channels := apiProvider.GetCachedChannels()
	cacheInfo := apiProvider.GetCacheInfo()

	// Filter and process channels
	filteredChannels := []map[string]interface{}{}

	for _, ch := range channels {
		// Skip archived if not requested
		if ch.IsArchived && !includeArchived {
			continue
		}

		// Apply search filter
		if search != "" {
			nameMatch := strings.Contains(strings.ToLower(ch.Name), search)
			purposeMatch := strings.Contains(strings.ToLower(ch.Purpose.Value), search)
			if !nameMatch && !purposeMatch {
				continue
			}
		}

		// Apply type filter
		skip := false
		switch filter {
		case "public":
			skip = ch.IsPrivate || ch.IsIM || ch.IsMpIM
		case "private":
			skip = !ch.IsPrivate || ch.IsIM || ch.IsMpIM
		case "dm":
			skip = !ch.IsIM
		case "group-dm":
			skip = !ch.IsMpIM
		case "member-only":
			skip = !ch.IsMember
		case "with-unreads":
			// This would require checking unread counts
			// For now, we'll include all (can be enhanced later)
		}

		if skip {
			continue
		}

		// Build channel info
		channelType := "public"
		displayName := fmt.Sprintf("#%s", ch.Name)

		if ch.IsIM {
			channelType = "dm"
			// Try to resolve DM name
			if ch.User != "" {
				if user, ok := apiProvider.ProvideUsersMap()[ch.User]; ok {
					name := user.RealName
					if name == "" {
						name = user.Name
					}
					displayName = fmt.Sprintf("DM: %s", name)
				}
			}
		} else if ch.IsMpIM {
			channelType = "group-dm"
			displayName = fmt.Sprintf("Group: %s", ch.Name)
		} else if ch.IsPrivate {
			channelType = "private"
			displayName = fmt.Sprintf("🔒#%s", ch.Name)
		}

		channelInfo := map[string]interface{}{
			"name":        ch.Name,
			"displayName": displayName,
			"type":        channelType,
			"isMember":    ch.IsMember,
			"isArchived":  ch.IsArchived,
		}

		// Add optional fields
		if ch.Purpose.Value != "" {
			channelInfo["purpose"] = ch.Purpose.Value
		}
		if ch.NumMembers > 0 {
			channelInfo["memberCount"] = ch.NumMembers
		}

		filteredChannels = append(filteredChannels, channelInfo)
	}

	// Sort by name
	sort.Slice(filteredChannels, func(i, j int) bool {
		return filteredChannels[i]["displayName"].(string) < filteredChannels[j]["displayName"].(string)
	})

	// Apply limit
	totalFound := len(filteredChannels)
	if len(filteredChannels) > limit {
		filteredChannels = filteredChannels[:limit]
	}

	// Build summary
	summary := map[string]interface{}{
		"totalCached": len(channels),
		"totalFound":  totalFound,
		"returned":    len(filteredChannels),
		"lastRefresh": cacheInfo.LastRefresh.Format(time.RFC3339),
		"cacheAge":    fmt.Sprintf("%.0f minutes", time.Since(cacheInfo.LastRefresh).Minutes()),
	}

	// Count by type
	typeCounts := map[string]int{
		"public":   0,
		"private":  0,
		"dm":       0,
		"group-dm": 0,
		"archived": 0,
	}

	for _, ch := range filteredChannels {
		typeCounts[ch["type"].(string)]++
		if ch["isArchived"].(bool) {
			typeCounts["archived"]++
		}
	}
	summary["byType"] = typeCounts

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"channels": filteredChannels,
			"filter":   filter,
			"summary":  summary,
		},
		Message:     fmt.Sprintf("Found %d channels (showing %d)", totalFound, len(filteredChannels)),
		ResultCount: len(filteredChannels),
	}

	// Add guidance
	if len(filteredChannels) == 0 {
		result.Guidance = "🔍 No channels found matching your criteria"
		result.NextActions = []string{
			"Try a different filter: list-channels filter='all'",
			"Force refresh the cache: list-channels forceRefresh=true",
		}
	} else {
		if time.Since(cacheInfo.LastRefresh) > 30*time.Minute {
			result.Guidance = fmt.Sprintf("📋 Channel cache is %.0f minutes old. Consider refreshing if needed.",
				time.Since(cacheInfo.LastRefresh).Minutes())
		} else {
			result.Guidance = "✅ Showing channels from recent cache"
		}

		result.NextActions = []string{
			"Catch up on a channel: catch-up-on-channel channel='[name]'",
			"Search for specific channels: list-channels search='engineering'",
			"Refresh cache: list-channels forceRefresh=true",
		}
	}

	return result, nil
}
