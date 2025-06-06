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

func findDiscussionHandlerImpl(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	query := ""
	if q, ok := params["query"].(string); ok {
		query = q
	}

	threadId := ""
	if t, ok := params["threadId"].(string); ok {
		threadId = t
	}

	// Handle thread context retrieval
	if threadId != "" {
		return getThreadContextImpl(ctx, params, threadId)
	}

	// Validate query
	if query == "" {
		return &FeatureResult{
			Success:  false,
			Message:  "Please provide either a search query or a threadId",
			Guidance: "💡 Try something like 'find discussions about API design' or 'recent pricing decisions'",
		}, nil
	}

	// Parse optional parameters
	channels := []string{}
	if ch, ok := params["in"].([]interface{}); ok {
		for _, c := range ch {
			if str, ok := c.(string); ok {
				channels = append(channels, str)
			}
		}
	}

	users := []string{}
	if u, ok := params["from"].([]interface{}); ok {
		for _, user := range u {
			if str, ok := user.(string); ok {
				users = append(users, str)
			}
		}
	}

	timeframe := "1m"
	if tf, ok := params["timeframe"].(string); ok {
		timeframe = tf
	}

	// Use internal search endpoint for better results
	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Use internal search endpoint for better results
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		return &FeatureResult{
			Success:  false,
			Message:  "Search functionality not available",
			Guidance: "⚠️ Internal search requires xoxc/xoxd tokens",
		}, nil
	}

	// Build search query with filters
	searchQuery := query

	// Add channel filters if specified
	if len(channels) > 0 {
		// Resolve channel names to IDs using provider's cache
		channelIDs := []string{}
		for _, ch := range channels {
			// Use provider's cache to resolve channel name to ID
			cleanName := strings.TrimPrefix(ch, "#")
			if channelID := apiProvider.ResolveChannelID(cleanName); strings.HasPrefix(channelID, "C") || strings.HasPrefix(channelID, "D") || strings.HasPrefix(channelID, "G") {
				channelIDs = append(channelIDs, channelID)
			}
		}
		if len(channelIDs) > 0 {
			searchQuery += " in:" + strings.Join(channelIDs, ",")
		}
	}

	// Add user filters if specified
	if len(users) > 0 {
		searchQuery += " from:" + strings.Join(users, ",")
	}

	// Add timeframe filter
	searchQuery += " " + parseTimeframeToSearchFilter(timeframe)

	// Perform search
	searchResp, err := internalClient.SearchMessages(ctx, searchQuery, nil)
	if err != nil {
		log.Printf("Search error: %v", err)
		return &FeatureResult{
			Success:  false,
			Message:  "Search failed",
			Guidance: "⚠️ Try simplifying your search query or checking your connection",
		}, nil
	}

	if !searchResp.OK {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Search error: %s", searchResp.Error),
		}, nil
	}

	// Process and enrich results
	discussions := []map[string]interface{}{}

	for _, msg := range searchResp.Messages.Results {
		// Get channel info
		channelName := msg.Channel.Name
		if channelName == "" {
			channelName = msg.Channel.ID
		}

		// Get user info
		userName := msg.User
		if user, ok := apiProvider.ProvideUsersMap()[msg.User]; ok {
			userName = user.RealName
			if userName == "" {
				userName = user.Name
			}
		}

		// Parse timestamp
		ts := msg.Timestamp
		msgTime := parseSlackTimestamp(ts)
		timeAgo := humanizeTime(msgTime)

		// Detect discussion type
		discussionType := "message"
		if strings.Contains(strings.ToLower(msg.Text), "decision") ||
			strings.Contains(strings.ToLower(msg.Text), "decided") ||
			strings.Contains(strings.ToLower(msg.Text), "approved") {
			discussionType = "decision"
		} else if strings.Contains(msg.Permalink, "thread_ts") {
			discussionType = "thread"
		}

		// Extract key points from message
		keyPoints := extractKeyPoints(msg.Text)

		discussion := map[string]interface{}{
			"type":      discussionType,
			"channel":   channelName,
			"channelId": msg.Channel.ID,
			"author":    userName,
			"authorId":  msg.User,
			"message":   msg.Text,
			"timestamp": timeAgo,
			"permalink": msg.Permalink,
			"threadId":  extractThreadId(msg.Permalink),
			"keyPoints": keyPoints,
		}

		discussions = append(discussions, discussion)
	}

	// Build response
	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"query":       query,
			"discussions": discussions,
			"searchMeta": map[string]interface{}{
				"totalMatches": searchResp.Messages.Total,
				"returned":     len(discussions),
				"timeframe":    timeframe,
			},
		},
		Message:     fmt.Sprintf("Found %d discussions matching '%s'", len(discussions), query),
		ResultCount: len(discussions),
	}

	// Add helpful next actions based on results
	if len(discussions) > 0 {
		result.NextActions = []string{}

		// Suggest viewing threads
		threadCount := 0
		for _, d := range discussions {
			if d["type"] == "thread" && threadCount < 2 {
				if threadId := d["threadId"].(string); threadId != "" {
					result.NextActions = append(result.NextActions,
						fmt.Sprintf("View full thread: find-discussion threadId='%s'", threadId))
					threadCount++
				}
			}
		}

		// Suggest catching up on active channels
		channelsSeen := map[string]bool{}
		for _, d := range discussions {
			ch := d["channel"].(string)
			if !channelsSeen[ch] && len(result.NextActions) < 4 {
				channelsSeen[ch] = true
				result.NextActions = append(result.NextActions,
					fmt.Sprintf("Catch up on #%s: catch-up-on-channel channel='%s'", ch, ch))
			}
		}

		result.Guidance = "💡 Click on permalinks to view messages in Slack, or use threadId to see full conversations"
	} else {
		result.Guidance = "🔍 Try broadening your search or checking different timeframes"
		result.NextActions = []string{
			"Try a broader search term",
			"Check unreads instead: check-unreads",
			"Browse recent activity: catch-up-on-channel channel='general'",
		}
	}

	return result, nil
}

func getThreadContextImpl(ctx context.Context, params map[string]interface{}, threadId string) (*FeatureResult, error) {
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

	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get channel info
	channelInfo, err := apiProvider.GetChannelInfo(ctx, channelId)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not find channel: %v", err),
		}, nil
	}

	channelName := channelInfo.Name
	if channelName == "" {
		channelName = channelId
	}

	// Get thread messages
	client, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not get client: %v", err),
		}, nil
	}
	msgs, _, _, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: channelId,
		Timestamp: threadTs,
		Limit:     100,
	})

	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not retrieve thread: %v", err),
		}, nil
	}

	// Process messages
	threadMessages := []map[string]interface{}{}
	var originalMessage map[string]interface{}
	participants := map[string]bool{}
	hasDecision := false
	var decisionMessage map[string]interface{}

	for i, msg := range msgs {
		// Get user info
		userName := msg.User
		if user, ok := apiProvider.ProvideUsersMap()[msg.User]; ok {
			userName = user.RealName
			if userName == "" {
				userName = user.Name
			}
		}
		participants[userName] = true

		// Parse timestamp
		msgTime := parseSlackTimestamp(msg.Timestamp)

		// Check for decision markers
		isDecision := false
		if strings.Contains(strings.ToLower(msg.Text), "decision:") ||
			strings.Contains(strings.ToLower(msg.Text), "decided:") ||
			strings.Contains(strings.ToLower(msg.Text), "approved:") ||
			strings.Contains(strings.ToLower(msg.Text), "conclusion:") {
			isDecision = true
			hasDecision = true
		}

		msgData := map[string]interface{}{
			"author":     userName,
			"authorId":   msg.User,
			"message":    msg.Text,
			"timestamp":  msgTime.Format("Jan 2 at 3:04 PM"),
			"reactions":  formatReactions(msg.Reactions),
			"isDecision": isDecision,
		}

		if i == 0 {
			originalMessage = msgData
		} else {
			threadMessages = append(threadMessages, msgData)
		}

		if isDecision && decisionMessage == nil {
			decisionMessage = msgData
		}
	}

	// Build participant list
	participantList := []string{}
	for p := range participants {
		participantList = append(participantList, p)
	}

	result := &FeatureResult{
		Success: true,
		Data: map[string]interface{}{
			"threadId":        threadId,
			"channel":         channelName,
			"channelId":       channelId,
			"originalMessage": originalMessage,
			"replies":         threadMessages,
			"replyCount":      len(threadMessages),
			"participants":    participantList,
			"hasDecision":     hasDecision,
			"decision":        decisionMessage,
		},
		Message:     fmt.Sprintf("Thread in #%s with %d replies", channelName, len(threadMessages)),
		ResultCount: len(msgs),
	}

	// Add guidance based on thread content
	if hasDecision {
		result.Guidance = "📌 This thread contains a decision that may require follow-up action"
	} else if len(threadMessages) > 10 {
		result.Guidance = "💬 This is an active discussion - consider if it needs resolution"
	} else {
		result.Guidance = "💡 This thread might benefit from a summary or decision"
	}

	// Suggest next actions
	result.NextActions = []string{
		fmt.Sprintf("Mark thread as read: mark-as-read channel='%s' timestamp='%s'", channelId, threadTs),
		fmt.Sprintf("See recent activity in #%s: catch-up-on-channel channel='%s'", channelName, channelName),
		"Find related discussions: find-discussion query='...'",
	}

	return result, nil
}

// Helper functions

func parseTimeframeToSearchFilter(timeframe string) string {
	// Convert friendly timeframes to Slack search syntax
	switch timeframe {
	case "today":
		return "during:today"
	case "yesterday":
		return "during:yesterday"
	case "1d":
		return "after:-1d"
	case "3d":
		return "after:-3d"
	case "1w", "week":
		return "after:-7d"
	case "2w":
		return "after:-14d"
	case "1m", "month":
		return "after:-30d"
	case "3m":
		return "after:-90d"
	default:
		// Try to parse as a month name
		if month := parseMonthName(timeframe); month != "" {
			return "during:" + month
		}
		return "after:-30d" // Default to last month
	}
}

func parseMonthName(s string) string {
	s = strings.ToLower(s)
	months := map[string]string{
		"january": "january", "jan": "january",
		"february": "february", "feb": "february",
		"march": "march", "mar": "march",
		"april": "april", "apr": "april",
		"may":  "may",
		"june": "june", "jun": "june",
		"july": "july", "jul": "july",
		"august": "august", "aug": "august",
		"september": "september", "sep": "september", "sept": "september",
		"october": "october", "oct": "october",
		"november": "november", "nov": "november",
		"december": "december", "dec": "december",
	}
	return months[s]
}

func extractThreadId(permalink string) string {
	// Extract thread ID from permalink
	// Format: https://workspace.slack.com/archives/C123/p1234567890123456?thread_ts=1234567890.123456
	if strings.Contains(permalink, "thread_ts=") {
		parts := strings.Split(permalink, "thread_ts=")
		if len(parts) > 1 {
			threadTs := parts[1]
			// Extract channel ID
			if strings.Contains(permalink, "/archives/") {
				archiveParts := strings.Split(permalink, "/archives/")
				if len(archiveParts) > 1 {
					channelParts := strings.Split(archiveParts[1], "/")
					if len(channelParts) > 0 {
						return channelParts[0] + "." + threadTs
					}
				}
			}
		}
	}
	return ""
}

func extractKeyPoints(text string) []string {
	// Simple key point extraction
	points := []string{}

	// Look for bullet points
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "•") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") {
			point := strings.TrimPrefix(line, "•")
			point = strings.TrimPrefix(point, "-")
			point = strings.TrimPrefix(point, "*")
			point = strings.TrimSpace(point)
			if len(point) > 10 && len(point) < 100 {
				points = append(points, point)
			}
		}
	}

	// If no bullet points, look for key phrases
	if len(points) == 0 {
		keyPhrases := []string{"decided", "will", "should", "must", "need to", "plan to", "agreed"}
		sentences := strings.Split(text, ". ")
		for _, sentence := range sentences {
			lower := strings.ToLower(sentence)
			for _, phrase := range keyPhrases {
				if strings.Contains(lower, phrase) && len(sentence) < 100 {
					points = append(points, strings.TrimSpace(sentence))
					break
				}
			}
			if len(points) >= 3 {
				break
			}
		}
	}

	return points
}

func formatReactions(reactions []slack.ItemReaction) []string {
	formatted := []string{}
	for _, r := range reactions {
		formatted = append(formatted, fmt.Sprintf("%s:%d", r.Name, r.Count))
	}
	return formatted
}

func humanizeTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	if diff < time.Minute {
		return "just now"
	} else if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	} else if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	} else if diff < 48*time.Hour {
		return "yesterday"
	} else if diff < 7*24*time.Hour {
		days := int(diff.Hours() / 24)
		return fmt.Sprintf("%d days ago", days)
	} else if diff < 30*24*time.Hour {
		weeks := int(diff.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}

	return t.Format("Jan 2, 2006")
}
