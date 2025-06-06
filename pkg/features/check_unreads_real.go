package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
	"log"
	"strings"
)

// checkUnreadsReal uses internal Slack endpoints to get accurate unread counts
func checkUnreadsReal(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	focus := "all"
	if f, ok := params["focus"].(string); ok {
		focus = f
	}

	includeChannels := false
	if i, ok := params["includeChannels"].(bool); ok {
		includeChannels = i
	}

	limit := 10
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 25 {
			limit = 25
		}
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
		// Fallback to the original implementation
		log.Println("Internal client not available, falling back to standard API")
		return checkUnreadsHandler(ctx, params)
	}

	// Get client counts using internal endpoint
	counts, err := internalClient.GetClientCounts(ctx)
	if err != nil {
		log.Printf("Failed to get client counts: %v, falling back to standard API", err)
		return checkUnreadsHandler(ctx, params)
	}

	if !counts.OK {
		log.Printf("Client counts request failed: %s, falling back to standard API", counts.Error)
		return checkUnreadsHandler(ctx, params)
	}

	// Get standard Slack client for additional info
	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	// Get current user info
	authTest, err := api.AuthTest()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to get user info: %v", err),
		}, nil
	}
	currentUserID := authTest.UserID
	usersMap := apiProvider.ProvideUsersMap()

	// Initialize result categories
	unreads := map[string]interface{}{
		"dms":      []map[string]interface{}{},
		"mentions": []map[string]interface{}{},
		"channels": []map[string]interface{}{},
	}

	stats := map[string]interface{}{
		"totalDMs":      0,
		"totalMentions": 0,
		"totalChannels": 0,
		"urgent":        0,
	}

	// Process DMs from internal counts
	if focus == "all" || focus == "dms" {
		dmCount := 0
		for _, im := range counts.IMs {
			if im.HasUnreads && im.MentionCount > 0 && dmCount < limit {
				// Get channel info for user details
				info, err := api.GetConversationInfo(&slack.GetConversationInfoInput{
					ChannelID: im.ID,
				})
				if err != nil {
					log.Printf("Failed to get DM info for %s: %v", im.ID, err)
					continue
				}

				// Get last message for preview
				histParams := &slack.GetConversationHistoryParameters{
					ChannelID: im.ID,
					Limit:     1,
				}
				resp, err := api.GetConversationHistoryContext(ctx, histParams)
				if err != nil {
					log.Printf("Failed to get DM history for %s: %v", im.ID, err)
					continue
				}

				if len(resp.Messages) > 0 {
					msg := resp.Messages[0]
					authorName := getUserName(info.User, usersMap)
					isUrgent := categorizeUrgency(msg.Text) == "high"

					if isUrgent {
						stats["urgent"] = stats["urgent"].(int) + 1
					}

					dm := map[string]interface{}{
						"type":        "dm",
						"author":      authorName,
						"message":     msg.Text,
						"timestamp":   formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
						"channelId":   im.ID,
						"unreadCount": im.MentionCount,
						"urgent":      isUrgent,
					}

					unreads["dms"] = append(unreads["dms"].([]map[string]interface{}), dm)
					stats["totalDMs"] = stats["totalDMs"].(int) + 1
					dmCount++
				}
			}
		}
	}

	// Process channels with mentions
	if focus == "all" || focus == "mentions" {
		mentionCount := 0
		
		// First check MPIMs for mentions
		for _, mpim := range counts.MPIMs {
			if mpim.MentionCount > 0 && mentionCount < limit {
				// Get channel info
				info, err := api.GetConversationInfo(&slack.GetConversationInfoInput{
					ChannelID: mpim.ID,
				})
				if err != nil {
					log.Printf("Failed to get MPIM info for %s: %v", mpim.ID, err)
					continue
				}

				// Get recent messages to find mentions
				histParams := &slack.GetConversationHistoryParameters{
					ChannelID: mpim.ID,
					Limit:     10,
				}
				resp, err := api.GetConversationHistoryContext(ctx, histParams)
				if err != nil {
					log.Printf("Failed to get MPIM history for %s: %v", mpim.ID, err)
					continue
				}

				mentionPattern := fmt.Sprintf("<@%s>", currentUserID)
				for _, msg := range resp.Messages {
					if strings.Contains(msg.Text, mentionPattern) {
						authorName := getUserName(msg.User, usersMap)
						isUrgent := categorizeUrgency(msg.Text) == "high"

						if isUrgent {
							stats["urgent"] = stats["urgent"].(int) + 1
						}

						mention := map[string]interface{}{
							"type":      "mention",
							"channel":   info.Name,
							"author":    authorName,
							"message":   msg.Text,
							"timestamp": formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
							"channelId": mpim.ID,
							"threadId":  fmt.Sprintf("%s:%s", mpim.ID, msg.Timestamp),
							"urgent":    isUrgent,
						}

						unreads["mentions"] = append(unreads["mentions"].([]map[string]interface{}), mention)
						stats["totalMentions"] = stats["totalMentions"].(int) + 1
						mentionCount++
						break
					}
				}
			}
		}
		
		// Then check regular channels
		for _, ch := range counts.Channels {
			if ch.MentionCount > 0 && mentionCount < limit {
				// Get channel info
				info, err := api.GetConversationInfo(&slack.GetConversationInfoInput{
					ChannelID: ch.ID,
				})
				if err != nil {
					log.Printf("Failed to get channel info for %s: %v", ch.ID, err)
					continue
				}

				// Get recent messages to find mentions
				histParams := &slack.GetConversationHistoryParameters{
					ChannelID: ch.ID,
					Limit:     20,
				}
				resp, err := api.GetConversationHistoryContext(ctx, histParams)
				if err != nil {
					log.Printf("Failed to get channel history for %s: %v", ch.ID, err)
					continue
				}

				mentionPattern := fmt.Sprintf("<@%s>", currentUserID)
				foundMentions := 0

				for _, msg := range resp.Messages {
					if strings.Contains(msg.Text, mentionPattern) {
						authorName := getUserName(msg.User, usersMap)
						isUrgent := categorizeUrgency(msg.Text) == "high"

						if isUrgent {
							stats["urgent"] = stats["urgent"].(int) + 1
						}

						mention := map[string]interface{}{
							"type":      "mention",
							"channel":   info.Name,
							"author":    authorName,
							"message":   msg.Text,
							"timestamp": formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
							"channelId": ch.ID,
							"threadId":  fmt.Sprintf("%s:%s", ch.ID, msg.Timestamp),
							"urgent":    isUrgent,
						}

						unreads["mentions"] = append(unreads["mentions"].([]map[string]interface{}), mention)
						stats["totalMentions"] = stats["totalMentions"].(int) + 1
						mentionCount++
						foundMentions++

						if foundMentions >= ch.MentionCount || mentionCount >= limit {
							break
						}
					}
				}
			}
		}
	}

	// Process general channel unreads if requested
	if includeChannels && (focus == "all" || focus == "channels") {
		channelCount := 0
		for _, ch := range counts.Channels {
			if ch.HasUnreads && channelCount < limit {
				// Get channel details
				info, err := api.GetConversationInfo(&slack.GetConversationInfoInput{
					ChannelID: ch.ID,
				})
				if err != nil {
					log.Printf("Failed to get channel info for %s: %v", ch.ID, err)
					continue
				}

				// Skip DMs
				if info.IsIM || info.IsMpIM {
					continue
				}

				channelData := map[string]interface{}{
					"type":        "channel",
					"channel":     info.Name,
					"channelId":   ch.ID,
					"hasUnreads":  ch.HasUnreads,
					"lastMessage": "Multiple unread messages",
				}

				// Get preview of last message
				histParams := &slack.GetConversationHistoryParameters{
					ChannelID: ch.ID,
					Limit:     1,
				}
				resp, err := api.GetConversationHistoryContext(ctx, histParams)
				if err == nil && len(resp.Messages) > 0 {
					lastMsg := resp.Messages[0]
					authorName := getUserName(lastMsg.User, usersMap)
					channelData["lastMessage"] = fmt.Sprintf("%s: %s", authorName, truncateMessage(lastMsg.Text, 100))
					channelData["timestamp"] = formatTimestamp(parseSlackTimestamp(lastMsg.Timestamp))
				}

				unreads["channels"] = append(unreads["channels"].([]map[string]interface{}), channelData)
				stats["totalChannels"] = stats["totalChannels"].(int) + 1
				channelCount++
			}
		}
	}

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
		ResultCount: stats["totalDMs"].(int) + stats["totalMentions"].(int) + stats["totalChannels"].(int),
	}

	// Add thread unreads if available
	if counts.Threads.UnreadCount > 0 {
		result.Data.(map[string]interface{})["threadUnreads"] = map[string]interface{}{
			"total":    counts.Threads.UnreadCount,
			"mentions": counts.Threads.MentionCount,
		}
		result.Message += fmt.Sprintf(" (+ %d thread unreads)", counts.Threads.UnreadCount)
	}

	// Add guidance based on findings
	if stats["urgent"].(int) > 0 {
		result.Guidance = fmt.Sprintf("🚨 You have %d urgent items that need immediate attention", stats["urgent"].(int))
	} else if stats["totalDMs"].(int) > 0 {
		result.Guidance = fmt.Sprintf("💬 You have %d unread DMs to catch up on", stats["totalDMs"].(int))
	} else if stats["totalMentions"].(int) > 0 {
		result.Guidance = fmt.Sprintf("📢 You have %d mentions to review", stats["totalMentions"].(int))
	} else {
		result.Guidance = "✅ You're all caught up!"
	}

	// Add next actions
	result.NextActions = []string{}
	if stats["totalDMs"].(int) > 0 {
		result.NextActions = append(result.NextActions, "Use 'catch-up-on-channel' with a DM channel ID to see full conversation")
	}
	if stats["totalMentions"].(int) > 0 {
		result.NextActions = append(result.NextActions, "Use 'find-discussion' with threadId to see full thread context")
	}

	return result, nil
}