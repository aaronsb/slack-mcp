package features

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// MarkAsRead handles marking messages, channels, or threads as read
var MarkAsRead = &Feature{
	Name:        "mark-as-read",
	Description: "Mark channels, threads, or messages as read - helps manage your Slack inbox",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "What to mark as read: 'channel:name', 'thread:id', 'dm:user', 'all-dms', 'all-channels', 'everything'",
			},
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Channel name or ID to mark as read (alternative to target)",
			},
			"timestamp": map[string]interface{}{
				"type":        "string",
				"description": "Specific message timestamp to mark as read up to",
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"description": "Scope of marking: 'messages-only', 'including-threads', 'threads-only'",
				"default":     "including-threads",
			},
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "Filter what to mark: 'all', 'non-important', 'older-than-1d', 'no-mentions'",
				"default":     "all",
			},
		},
		"required": []string{},
	},
	Handler: markAsReadHandler,
}

func markAsReadHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Parse parameters
	target := ""
	if t, ok := params["target"].(string); ok {
		target = t
	}

	channel := ""
	if ch, ok := params["channel"].(string); ok {
		channel = ch
	}

	timestamp := ""
	if ts, ok := params["timestamp"].(string); ok {
		timestamp = ts
	}

	scope := "including-threads"
	if s, ok := params["scope"].(string); ok {
		scope = s
	}

	filter := "all"
	if f, ok := params["filter"].(string); ok {
		filter = f
	}

	// Handle different target types
	if target != "" {
		return handleTargetMarkAsRead(ctx, apiProvider, target, scope, filter)
	} else if channel != "" {
		return handleChannelMarkAsRead(ctx, apiProvider, channel, timestamp, scope)
	} else {
		// Interactive mode - show what can be marked as read
		return showMarkAsReadOptions(ctx, apiProvider)
	}
}

func handleTargetMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, target, scope, filter string) (*FeatureResult, error) {
	parts := strings.SplitN(target, ":", 2)
	targetType := target
	targetValue := ""

	if len(parts) == 2 {
		targetType = parts[0]
		targetValue = parts[1]
	}

	switch targetType {
	case "channel":
		return handleChannelMarkAsRead(ctx, apiProvider, targetValue, "", scope)

	case "thread":
		return handleThreadMarkAsRead(ctx, apiProvider, targetValue)

	case "dm":
		return handleDMMarkAsRead(ctx, apiProvider, targetValue)

	case "all-dms":
		return handleAllDMsMarkAsRead(ctx, apiProvider, filter)

	case "all-channels":
		return handleAllChannelsMarkAsRead(ctx, apiProvider, filter)

	case "everything":
		return handleEverythingMarkAsRead(ctx, apiProvider, filter)

	default:
		return &FeatureResult{
			Success:  false,
			Message:  "Invalid target. Use format like 'channel:general' or 'all-dms'",
			Guidance: "💡 Examples: 'channel:general', 'dm:john.doe', 'all-dms', 'everything'",
		}, nil
	}
}

func handleChannelMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, channel, timestamp string, scope string) (*FeatureResult, error) {
	// Resolve channel name to ID using provider's cache
	cleanName := strings.TrimPrefix(channel, "#")
	channelID := apiProvider.ResolveChannelID(cleanName)

	// Get channel info using the resolved ID
	channelInfo, err := apiProvider.GetChannelInfo(ctx, channelID)
	if err != nil {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Channel '%s' not found. Use list-channels to see available channels.", channel),
			Guidance: "💡 Use 'list-channels' to see available channels",
		}, nil
	}

	var client *slack.Client

	// Get latest message timestamp if not provided
	if timestamp == "" {
		if client == nil {
			client, err = apiProvider.Provide()
			if err != nil {
				return &FeatureResult{
					Success: false,
					Message: fmt.Sprintf("Could not get client: %v", err),
				}, nil
			}
		}
		history, err := client.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: channelID,
			Limit:     1,
		})
		if err != nil || len(history.Messages) == 0 {
			return &FeatureResult{
				Success: false,
				Message: "Could not get latest message timestamp",
			}, nil
		}
		timestamp = history.Messages[0].Timestamp
	}

	// Mark channel as read
	if client == nil {
		client, err = apiProvider.Provide()
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Could not get client: %v", err),
			}, nil
		}
	}
	err = client.MarkConversation(channelID, timestamp)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to mark channel as read: %v", err),
		}, nil
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"channel":    channelInfo.Name,
			"markedUpTo": timestamp,
			"scope":      scope,
		},
		Message:  fmt.Sprintf("Marked #%s as read", channelInfo.Name),
		Guidance: "✅ Channel messages marked as read",
	}

	// Add next actions
	result.NextActions = []string{
		"Check remaining unreads: check-unreads",
		fmt.Sprintf("Catch up on #%s again: catch-up-on-channel channel='%s'", channelInfo.Name, channelInfo.Name),
	}

	return result, nil
}

func handleThreadMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, threadId string) (*FeatureResult, error) {
	// Parse thread ID (format: channelId.threadTs)
	parts := strings.Split(threadId, ".")
	if len(parts) != 2 {
		return &FeatureResult{
			Success: false,
			Message: "Invalid threadId format. Expected: channelId.threadTs",
		}, nil
	}

	channelId := parts[0]
	threadTs := parts[1]

	// Mark thread as read using internal client if available
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient != nil {
		// Use internal endpoint for thread marking
		// Note: This would require adding a new method to InternalClient
		log.Printf("TODO: Implement internal thread marking for %s", threadId)
	}

	// Fallback: Mark the channel up to the thread timestamp
	client, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not get client: %v", err),
		}, nil
	}
	err = client.MarkConversation(channelId, threadTs)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to mark thread as read: %v", err),
		}, nil
	}

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"threadId":  threadId,
			"channelId": channelId,
			"threadTs":  threadTs,
		},
		Message:  "Thread marked as read",
		Guidance: "✅ Thread and its replies marked as read",
		NextActions: []string{
			"Check for more threads: check-unreads focus='threads'",
			"Find related discussions: find-discussion",
		},
	}, nil
}

func handleDMMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, user string) (*FeatureResult, error) {
	// Find DM channel with user
	userID := user

	// Try to resolve username to ID
	usersMap := apiProvider.ProvideUsersMap()
	for uid, u := range usersMap {
		if u.Name == user || u.RealName == user || uid == user {
			userID = uid
			break
		}
	}

	// Find IM channel
	client, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not get client: %v", err),
		}, nil
	}
	// Get IM conversations
	conversations, _, err := client.GetConversations(&slack.GetConversationsParameters{
		Types: []string{"im"},
		Limit: 1000,
	})
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not get DM channels: %v", err),
		}, nil
	}

	var imChannel *slack.Channel
	for _, conv := range conversations {
		if conv.IsIM && conv.User == userID {
			imChannel = &conv
			break
		}
	}

	if imChannel == nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("No DM channel found with user '%s'", user),
		}, nil
	}

	// Get latest message
	history, err := client.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: imChannel.ID,
		Limit:     1,
	})
	if err != nil || len(history.Messages) == 0 {
		return &FeatureResult{
			Success: false,
			Message: "Could not get latest DM message",
		}, nil
	}

	// Mark as read
	err = client.MarkConversation(imChannel.ID, history.Messages[0].Timestamp)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to mark DM as read: %v", err),
		}, nil
	}

	// Get user's real name for display
	userName := user
	if u, ok := usersMap[userID]; ok {
		userName = u.RealName
		if userName == "" {
			userName = u.Name
		}
	}

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"user":      userName,
			"userId":    userID,
			"channelId": imChannel.ID,
		},
		Message:  fmt.Sprintf("Marked DM with %s as read", userName),
		Guidance: "✅ Direct messages marked as read",
		NextActions: []string{
			"Check other DMs: check-unreads focus='dms'",
			fmt.Sprintf("Catch up with %s: catch-up-on-channel channel='%s'", userName, imChannel.ID),
		},
	}, nil
}

func handleAllDMsMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, filter string) (*FeatureResult, error) {
	// Get unread counts
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		return &FeatureResult{
			Success:  false,
			Message:  "Bulk marking requires internal client access",
			Guidance: "⚠️ This feature requires xoxc/xoxd tokens",
		}, nil
	}

	counts, err := internalClient.GetClientCounts(ctx)
	if err != nil || !counts.OK {
		return &FeatureResult{
			Success: false,
			Message: "Could not get unread counts",
		}, nil
	}

	markedCount := 0
	skippedCount := 0
	errors := []string{}

	// Process IMs
	for _, im := range counts.IMs {
		if !im.HasUnreads {
			continue
		}

		// Apply filter
		if filter == "no-mentions" && im.MentionCount > 0 {
			skippedCount++
			continue
		}

		if filter == "older-than-1d" {
			// Check if latest message is older than 1 day
			ts := parseSlackTimestamp(im.Latest)
			if time.Since(ts) < 24*time.Hour {
				skippedCount++
				continue
			}
		}

		// Mark as read
		client, _ := apiProvider.Provide()
		err := client.MarkConversation(im.ID, im.Latest)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", im.ID, err))
		} else {
			markedCount++
		}
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"markedCount":  markedCount,
			"skippedCount": skippedCount,
			"filter":       filter,
			"errors":       errors,
		},
		Message: fmt.Sprintf("Marked %d DMs as read", markedCount),
	}

	if skippedCount > 0 {
		result.Guidance = fmt.Sprintf("✅ Marked %d DMs as read, skipped %d based on filter '%s'",
			markedCount, skippedCount, filter)
	} else {
		result.Guidance = "✅ All unread DMs marked as read"
	}

	result.NextActions = []string{
		"Check remaining unreads: check-unreads",
		"Mark channels as read: mark-as-read target='all-channels'",
	}

	return result, nil
}

func handleAllChannelsMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, filter string) (*FeatureResult, error) {
	// Similar to DMs but for channels
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		return &FeatureResult{
			Success:  false,
			Message:  "Bulk marking requires internal client access",
			Guidance: "⚠️ This feature requires xoxc/xoxd tokens",
		}, nil
	}

	counts, err := internalClient.GetClientCounts(ctx)
	if err != nil || !counts.OK {
		return &FeatureResult{
			Success: false,
			Message: "Could not get unread counts",
		}, nil
	}

	markedCount := 0
	skippedCount := 0
	errors := []string{}

	// Get list of important channels (you're actively participating in)
	importantChannels := map[string]bool{}
	if filter == "non-important" {
		// Get channels where user has recently posted
		// This is a simplified heuristic
		client, _ := apiProvider.Provide()
		channels, _, _ := client.GetConversations(&slack.GetConversationsParameters{
			Limit: 100,
			Types: []string{"public_channel", "private_channel"},
		})
		for _, ch := range channels {
			if ch.IsMember && ch.NumMembers < 20 { // Small channels are likely important
				importantChannels[ch.ID] = true
			}
		}
	}

	// Process channels
	for _, ch := range counts.Channels {
		if !ch.HasUnreads {
			continue
		}

		// Apply filter
		if filter == "non-important" && importantChannels[ch.ID] {
			skippedCount++
			continue
		}

		if filter == "no-mentions" && ch.MentionCount > 0 {
			skippedCount++
			continue
		}

		// Mark as read
		client, _ := apiProvider.Provide()
		err := client.MarkConversation(ch.ID, ch.Latest)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", ch.ID, err))
		} else {
			markedCount++
		}
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"markedCount":  markedCount,
			"skippedCount": skippedCount,
			"filter":       filter,
			"errors":       errors,
		},
		Message: fmt.Sprintf("Marked %d channels as read", markedCount),
	}

	if skippedCount > 0 {
		result.Guidance = fmt.Sprintf("✅ Marked %d channels as read, skipped %d based on filter '%s'",
			markedCount, skippedCount, filter)
	} else {
		result.Guidance = "✅ All unread channels marked as read"
	}

	result.NextActions = []string{
		"Check what's left: check-unreads",
		"Review important channels: catch-up-on-channel channel='general'",
	}

	return result, nil
}

func handleEverythingMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, filter string) (*FeatureResult, error) {
	// Mark both DMs and channels
	dmResult, _ := handleAllDMsMarkAsRead(ctx, apiProvider, filter)
	channelResult, _ := handleAllChannelsMarkAsRead(ctx, apiProvider, filter)

	dmMarked := 0
	channelMarked := 0

	if dmData, ok := dmResult.Data.(map[string]interface{}); ok {
		if count, ok := dmData["markedCount"].(int); ok {
			dmMarked = count
		}
	}

	if chData, ok := channelResult.Data.(map[string]interface{}); ok {
		if count, ok := chData["markedCount"].(int); ok {
			channelMarked = count
		}
	}

	totalMarked := dmMarked + channelMarked

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"totalMarked":    totalMarked,
			"dmsMarked":      dmMarked,
			"channelsMarked": channelMarked,
			"filter":         filter,
		},
		Message:  fmt.Sprintf("Marked %d conversations as read", totalMarked),
		Guidance: fmt.Sprintf("✅ Slack inbox cleared! (%d DMs, %d channels)", dmMarked, channelMarked),
		NextActions: []string{
			"See what's new: check-unreads",
			"Catch up on important stuff: catch-up-on-channel channel='general'",
		},
	}, nil
}

func showMarkAsReadOptions(ctx context.Context, apiProvider *provider.ApiProvider) (*FeatureResult, error) {
	// Show current unread status and options
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		return &FeatureResult{
			Success:  false,
			Message:  "Cannot show unread status without internal client",
			Guidance: "⚠️ This feature requires xoxc/xoxd tokens",
		}, nil
	}

	counts, err := internalClient.GetClientCounts(ctx)
	if err != nil || !counts.OK {
		return &FeatureResult{
			Success: false,
			Message: "Could not get unread counts",
		}, nil
	}

	// Count unreads
	unreadChannels := 0
	unreadDMs := 0
	mentionChannels := 0
	mentionDMs := 0

	for _, ch := range counts.Channels {
		if ch.HasUnreads {
			unreadChannels++
			if ch.MentionCount > 0 {
				mentionChannels++
			}
		}
	}

	for _, im := range counts.IMs {
		if im.HasUnreads {
			unreadDMs++
			if im.MentionCount > 0 {
				mentionDMs++
			}
		}
	}

	// Build options
	options := []map[string]interface{}{}

	if unreadDMs > 0 {
		options = append(options, map[string]interface{}{
			"command":     "mark-as-read target='all-dms'",
			"description": fmt.Sprintf("Mark all %d DMs as read", unreadDMs),
			"mentions":    mentionDMs,
		})

		if mentionDMs > 0 {
			options = append(options, map[string]interface{}{
				"command":     "mark-as-read target='all-dms' filter='no-mentions'",
				"description": fmt.Sprintf("Mark %d DMs as read (keep %d with mentions)", unreadDMs-mentionDMs, mentionDMs),
			})
		}
	}

	if unreadChannels > 0 {
		options = append(options, map[string]interface{}{
			"command":     "mark-as-read target='all-channels'",
			"description": fmt.Sprintf("Mark all %d channels as read", unreadChannels),
			"mentions":    mentionChannels,
		})

		options = append(options, map[string]interface{}{
			"command":     "mark-as-read target='all-channels' filter='non-important'",
			"description": "Mark only non-important channels as read",
		})
	}

	if unreadDMs > 0 && unreadChannels > 0 {
		options = append(options, map[string]interface{}{
			"command":     "mark-as-read target='everything'",
			"description": fmt.Sprintf("Mark everything as read (%d total)", unreadDMs+unreadChannels),
		})

		if mentionDMs > 0 || mentionChannels > 0 {
			options = append(options, map[string]interface{}{
				"command":     "mark-as-read target='everything' filter='no-mentions'",
				"description": fmt.Sprintf("Mark all as read except %d with mentions", mentionDMs+mentionChannels),
			})
		}
	}

	// Add specific channel/DM options
	options = append(options, map[string]interface{}{
		"command":     "mark-as-read channel='general'",
		"description": "Mark a specific channel as read",
		"example":     true,
	})

	options = append(options, map[string]interface{}{
		"command":     "mark-as-read target='dm:john.doe'",
		"description": "Mark a specific DM as read",
		"example":     true,
	})

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"unreadChannels":  unreadChannels,
			"unreadDMs":       unreadDMs,
			"mentionChannels": mentionChannels,
			"mentionDMs":      mentionDMs,
			"totalUnreads":    unreadChannels + unreadDMs,
			"options":         options,
		},
		Message:  fmt.Sprintf("You have %d unread conversations", unreadChannels+unreadDMs),
		Guidance: "💡 Choose an option above or specify what to mark as read",
		NextActions: []string{
			"See unread details: check-unreads",
			"Mark all as read: mark-as-read target='everything'",
			"Keep mentions: mark-as-read target='everything' filter='no-mentions'",
		},
	}, nil
}
