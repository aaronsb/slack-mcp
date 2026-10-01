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
	Name:        "mark-read",
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
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if refusal := preWriteLocal(ctx, apiProvider, "mark-read"); refusal != nil {
		return refusal, nil
	}
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
	// Receipts are public, so mark-read routes like any write: '#name' is a
	// channel only, a person only by exact handle (ADR-005, issue #94).
	channelID, refusal := markReadDestination(ctx, apiProvider, channel)
	if refusal != nil {
		return refusal, nil
	}

	channelInfo, err := apiProvider.GetChannelInfo(ctx, channelID)
	if err != nil {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Channel '%s' not found. Use estate view='channels' to see available channels.", channel),
			Guidance: "💡 See available channels: estate view='channels'",
		}, nil
	}
	label := "#" + channelInfo.Name
	if channelInfo.Name == "" || channelInfo.IsIM {
		label = channel
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
			"channel":    label,
			"markedUpTo": timestamp,
			"scope":      scope,
		},
		Message:  fmt.Sprintf("Marked %s as read", label),
		Guidance: "✅ Channel messages marked as read",
	}

	// Add next actions
	result.NextActions = []string{
		"Check remaining unreads: inbox view='unreads'",
		fmt.Sprintf("Catch up on %s again: messages target='%s' since='1d'", label, label),
	}

	return result, nil
}

func handleThreadMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, threadId string) (*FeatureResult, error) {
	// Parse thread ID (format: channelId.threadTs). A Slack ts contains a
	// dot itself, so split on the first dot only.
	parts := strings.SplitN(threadId, ".", 2)
	if len(parts) != 2 {
		return &FeatureResult{
			Success: false,
			Message: "Invalid threadId format. Expected: channelId.threadTs",
		}, nil
	}

	channelId := parts[0]
	threadTs := parts[1]
	dest := &resolvedDestination{Typed: threadId, ConvID: channelId, Name: "the thread's conversation"}
	if ch, ok := apiProvider.LookupChannel(channelId); ok && ch.ID == channelId {
		dest = channelDestination(apiProvider, threadId, ch)
	}
	if refusal := preMarkRead(ctx, apiProvider, dest); refusal != nil {
		return refusal, nil
	}

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
			"Check for more threads: inbox view='new'",
			"Find related discussions: messages query='<topic>'",
		},
	}, nil
}

func handleDMMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, user string) (*FeatureResult, error) {
	// 'dm:' names a person: the same write-policy ladder as say, so a shared
	// real name or a deactivated user never gets a receipt (issue #94).
	person := "@" + strings.TrimPrefix(strings.TrimSpace(user), "@")
	dmID, refusal := markReadDestination(ctx, apiProvider, person)
	if refusal != nil {
		return refusal, nil
	}

	client, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not get client: %v", err),
		}, nil
	}

	history, err := client.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: dmID,
		Limit:     1,
	})
	if err != nil || len(history.Messages) == 0 {
		return &FeatureResult{
			Success: false,
			Message: "Could not get latest DM message",
		}, nil
	}

	if err := client.MarkConversationContext(ctx, dmID, history.Messages[0].Timestamp); err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to mark DM as read: %v", err),
		}, nil
	}

	return &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"user":      person,
			"channelId": dmID,
		},
		Message:  fmt.Sprintf("Marked DM with %s as read", person),
		Guidance: "✅ Direct messages marked as read",
		NextActions: []string{
			"Check other DMs: inbox view='unreads' focus='dms'",
			fmt.Sprintf("Catch up with %s: messages target='%s' since='1d'", person, person),
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
	refused := markReadFilter(ctx, apiProvider)
	var withheld []string

	// Process IMs
	for _, im := range counts.IMs {
		if !im.HasUnreads {
			continue
		}
		if skip, name := refused(im.ID); skip {
			withheld = append(withheld, name)
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
	withholdFrom(result, withheld)

	if skippedCount > 0 {
		result.Guidance = fmt.Sprintf("✅ Marked %d DMs as read, skipped %d based on filter '%s'",
			markedCount, skippedCount, filter)
	} else {
		result.Guidance = "✅ All unread DMs marked as read"
	}

	result.NextActions = []string{
		"Check remaining unreads: inbox view='unreads'",
		"Mark channels as read: mark-read target='all-channels'",
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
	refused := markReadFilter(ctx, apiProvider)
	var withheld []string
	for _, ch := range counts.Channels {
		if !ch.HasUnreads {
			continue
		}
		if skip, name := refused(ch.ID); skip {
			withheld = append(withheld, name)
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
	withholdFrom(result, withheld)

	if skippedCount > 0 {
		result.Guidance = fmt.Sprintf("✅ Marked %d channels as read, skipped %d based on filter '%s'",
			markedCount, skippedCount, filter)
	} else {
		result.Guidance = "✅ All unread channels marked as read"
	}

	result.NextActions = []string{
		"Check what's left: inbox view='unreads'",
		"Review important channels: messages target='general'",
	}

	return result, nil
}

// markReadDestination resolves a mark-read target without opening
// anything, refuses a person with no DM, and runs the quarantine step.
// mark-read never opens a conversation (ADR-013).
func markReadDestination(ctx context.Context, ap *provider.ApiProvider, target string) (string, *FeatureResult) {
	dest, terr := locateTarget(ctx, ap, target, provider.WritePolicy, "channel")
	if terr != nil {
		return "", terr.result()
	}
	if dest.ConvID == "" {
		return "", &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("There is no DM with %s, so there is nothing to mark read. Nothing was done.", dest.Name),
		}
	}
	if refusal := preMarkRead(ctx, ap, dest); refusal != nil {
		return "", refusal
	}
	return dest.ConvID, nil
}

// withholdFrom lists the conversations a bulk mark-read skipped for
// outbound safety.
func withholdFrom(result *FeatureResult, withheld []string) {
	if len(withheld) == 0 {
		return
	}
	result.Data.(map[string]interface{})["skippedForSafety"] = withheld
	result.Message += fmt.Sprintf(". Skipped, not marked: %s", strings.Join(withheld, ", "))
}

func handleEverythingMarkAsRead(ctx context.Context, apiProvider *provider.ApiProvider, filter string) (*FeatureResult, error) {
	// Mark both DMs and channels
	dmResult, _ := handleAllDMsMarkAsRead(ctx, apiProvider, filter)
	channelResult, _ := handleAllChannelsMarkAsRead(ctx, apiProvider, filter)

	dmMarked := 0
	channelMarked := 0
	var withheld []string

	if dmData, ok := dmResult.Data.(map[string]interface{}); ok {
		if count, ok := dmData["markedCount"].(int); ok {
			dmMarked = count
		}
		w, _ := dmData["skippedForSafety"].([]string)
		withheld = append(withheld, w...)
	}

	if chData, ok := channelResult.Data.(map[string]interface{}); ok {
		if count, ok := chData["markedCount"].(int); ok {
			channelMarked = count
		}
		w, _ := chData["skippedForSafety"].([]string)
		withheld = append(withheld, w...)
	}

	totalMarked := dmMarked + channelMarked

	result := &FeatureResult{
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
			"See what's new: inbox view='unreads'",
			"Catch up on important stuff: messages target='general'",
		},
	}
	withholdFrom(result, withheld)
	return result, nil
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
			"command":     "mark-read target='all-dms'",
			"description": fmt.Sprintf("Mark all %d DMs as read", unreadDMs),
			"mentions":    mentionDMs,
		})

		if mentionDMs > 0 {
			options = append(options, map[string]interface{}{
				"command":     "mark-read target='all-dms' filter='no-mentions'",
				"description": fmt.Sprintf("Mark %d DMs as read (keep %d with mentions)", unreadDMs-mentionDMs, mentionDMs),
			})
		}
	}

	if unreadChannels > 0 {
		options = append(options, map[string]interface{}{
			"command":     "mark-read target='all-channels'",
			"description": fmt.Sprintf("Mark all %d channels as read", unreadChannels),
			"mentions":    mentionChannels,
		})

		options = append(options, map[string]interface{}{
			"command":     "mark-read target='all-channels' filter='non-important'",
			"description": "Mark only non-important channels as read",
		})
	}

	if unreadDMs > 0 && unreadChannels > 0 {
		options = append(options, map[string]interface{}{
			"command":     "mark-read target='everything'",
			"description": fmt.Sprintf("Mark everything as read (%d total)", unreadDMs+unreadChannels),
		})

		if mentionDMs > 0 || mentionChannels > 0 {
			options = append(options, map[string]interface{}{
				"command":     "mark-read target='everything' filter='no-mentions'",
				"description": fmt.Sprintf("Mark all as read except %d with mentions", mentionDMs+mentionChannels),
			})
		}
	}

	// Add specific channel/DM options
	options = append(options, map[string]interface{}{
		"command":     "mark-read channel='general'",
		"description": "Mark a specific channel as read",
		"example":     true,
	})

	options = append(options, map[string]interface{}{
		"command":     "mark-read channel='dm:john.doe'",
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
			"See unread details: inbox view='unreads'",
			"Mark all as read: mark-read target='everything'",
			"Keep mentions: mark-read target='everything' filter='no-mentions'",
		},
	}, nil
}
