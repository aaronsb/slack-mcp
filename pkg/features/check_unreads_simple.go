package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"log"
)

// checkUnreadsSimple provides a clean implementation using internal endpoints
func checkUnreadsSimple(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	focus := "all"
	if f, ok := params["focus"].(string); ok {
		focus = f
	}

	includeChannels := false
	if i, ok := params["includeChannels"].(bool); ok {
		includeChannels = i
	}

	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get internal client
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		log.Println("Internal client not available, cannot get unread counts")
		return &FeatureResult{
			Success: false,
			Message: "Unable to fetch unread counts",
		}, nil
	}

	// Get counts from internal endpoint
	counts, err := internalClient.GetClientCounts(ctx)
	if err != nil {
		log.Printf("Failed to get client counts: %v", err)
		return &FeatureResult{
			Success: false,
			Message: "Unable to fetch unread counts",
		}, nil
	}

	if !counts.OK {
		return &FeatureResult{
			Success: false,
			Message: "Unable to fetch unread counts",
		}, nil
	}

	// Build summary based on internal data
	unreads := map[string]interface{}{
		"dms":      []map[string]interface{}{},
		"mentions": []map[string]interface{}{},
		"channels": []map[string]interface{}{},
	}

	stats := map[string]interface{}{
		"totalDMs":      counts.ChannelBadges.DMs,
		"totalMentions": 0,
		"totalChannels": 0,
		"urgent":        0,
	}

	// Count mentions across all channels
	for _, ch := range counts.Channels {
		if ch.MentionCount > 0 {
			stats["totalMentions"] = stats["totalMentions"].(int) + ch.MentionCount
		}
		if ch.HasUnreads {
			stats["totalChannels"] = stats["totalChannels"].(int) + 1
		}
	}

	// Add thread mentions
	stats["totalMentions"] = stats["totalMentions"].(int) + counts.Threads.MentionCount

	// Store channel ID mapping for catch-up-on-channel
	channelMapping := make(map[string]string) // name -> ID

	// Process DMs summary
	if focus == "all" || focus == "dms" {
		dmCount := 0
		for _, im := range counts.IMs {
			if im.HasUnreads && im.MentionCount > 0 && dmCount < 5 {
				// Resolve DM name
				dmName := "Unknown"
				if info, err := apiProvider.GetChannelInfo(ctx, im.ID); err == nil {
					if info.User != "" {
						// Regular DM - get user name
						if user, ok := apiProvider.ProvideUsersMap()[info.User]; ok {
							dmName = user.Name
							if user.RealName != "" {
								dmName = user.RealName
							}
						}
					} else if info.Name != "" {
						// Bot DM or app
						dmName = info.Name
					}
				}
				
				dm := map[string]interface{}{
					"type":        "dm",
					"from":        dmName,
					"unreadCount": im.MentionCount,
				}
				unreads["dms"] = append(unreads["dms"].([]map[string]interface{}), dm)
				channelMapping[dmName] = im.ID
				dmCount++
			}
		}
	}

	// Process mentions summary
	if focus == "all" || focus == "mentions" {
		mentionCount := 0
		
		// Channels with mentions
		for _, ch := range counts.Channels {
			if ch.MentionCount > 0 && mentionCount < 10 {
				channelName := apiProvider.ResolveChannelName(ctx, ch.ID)
				mention := map[string]interface{}{
					"type":         "channel_mention",
					"channel":      channelName,
					"mentionCount": ch.MentionCount,
				}
				unreads["mentions"] = append(unreads["mentions"].([]map[string]interface{}), mention)
				channelMapping[channelName] = ch.ID
				mentionCount++
			}
		}
	}

	// Process channel unreads if requested
	if includeChannels && (focus == "all" || focus == "channels") {
		channelCount := 0
		for _, ch := range counts.Channels {
			if ch.HasUnreads && channelCount < 10 {
				channelName := apiProvider.ResolveChannelName(ctx, ch.ID)
				channelData := map[string]interface{}{
					"type":    "channel",
					"channel": channelName,
				}
				unreads["channels"] = append(unreads["channels"].([]map[string]interface{}), channelData)
				channelMapping[channelName] = ch.ID
				channelCount++
			}
		}
	}

	// TODO: Store mapping for other tools to use
	_ = channelMapping

	// Build result
	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"unreads":       unreads,
			"stats":         stats,
			"focus":         focus,
			"source":        "internal_api",
			"channelBadges": counts.ChannelBadges,
		},
		Message: fmt.Sprintf("Found %d DMs, %d mentions, %d channels with unreads",
			stats["totalDMs"], stats["totalMentions"], stats["totalChannels"]),
		ResultCount: len(unreads["dms"].([]map[string]interface{})) + 
			len(unreads["mentions"].([]map[string]interface{})) + 
			len(unreads["channels"].([]map[string]interface{})),
	}

	// Add thread info
	if counts.Threads.UnreadCount > 0 {
		result.Data.(map[string]interface{})["threadUnreads"] = map[string]interface{}{
			"total":    counts.Threads.UnreadCount,
			"mentions": counts.Threads.MentionCount,
		}
		result.Message += fmt.Sprintf(" (+ %d thread unreads)", counts.Threads.UnreadCount)
	}

	// Add guidance
	if stats["totalMentions"].(int) > 10 {
		result.Guidance = fmt.Sprintf("🚨 You have %d mentions that need attention", stats["totalMentions"].(int))
	} else if stats["totalDMs"].(int) > 5 {
		result.Guidance = fmt.Sprintf("💬 You have %d DMs with unreads", stats["totalDMs"].(int))
	} else if stats["totalMentions"].(int) > 0 {
		result.Guidance = fmt.Sprintf("📢 You have %d mentions to review", stats["totalMentions"].(int))
	} else if stats["totalChannels"].(int) > 0 {
		result.Guidance = fmt.Sprintf("📌 %d channels have new messages", stats["totalChannels"].(int))
	} else {
		result.Guidance = "✅ You're all caught up!"
	}

	// Add next actions - using channel names, not IDs
	result.NextActions = []string{}
	if len(unreads["mentions"].([]map[string]interface{})) > 0 {
		// Get first channel name as example
		firstMention := unreads["mentions"].([]map[string]interface{})[0]
		channelName := firstMention["channel"].(string)
		result.NextActions = append(result.NextActions, 
			fmt.Sprintf("Use 'catch-up-on-channel' with channel='%s' to see the messages", channelName))
	}
	if len(unreads["dms"].([]map[string]interface{})) > 0 {
		firstDM := unreads["dms"].([]map[string]interface{})[0]
		dmName := firstDM["from"].(string)
		result.NextActions = append(result.NextActions, 
			fmt.Sprintf("Check DM from %s - they have %d unread messages", dmName, firstDM["unreadCount"]))
	}

	return result, nil
}