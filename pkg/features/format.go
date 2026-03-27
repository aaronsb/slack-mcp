package features

import (
	"fmt"
	"strings"
)

// FormatResult converts a FeatureResult into token-efficient markdown
// optimized for AI consumption. Returns compact, scannable text
// instead of raw JSON.
func FormatResult(toolName string, result *FeatureResult) string {
	if !result.Success {
		return formatError(result)
	}

	switch toolName {
	case "check-unreads":
		return formatUnreads(result)
	case "check-mentions":
		return formatMentions(result)
	case "list-channels":
		return formatChannels(result)
	case "list-users":
		return formatUsers(result)
	case "catch-up":
		return formatCatchUp(result)
	case "get-context":
		return formatContext(result)
	case "search":
		return formatSearch(result)
	case "send-message":
		return formatSendMessage(result)
	case "mark-read":
		return formatMarkRead(result)
	case "react":
		return formatReact(result)
	case "check-timing":
		return formatTiming(result)
	case "auth-setup":
		return formatAuthSetup(result)
	default:
		return formatGeneric(result)
	}
}

// --- Helpers ---

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func dataMap(result *FeatureResult) map[string]interface{} {
	if m, ok := result.Data.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func asList(v interface{}) []map[string]interface{} {
	if items, ok := v.([]map[string]interface{}); ok {
		return items
	}
	// Handle []interface{} from JSON
	if items, ok := v.([]interface{}); ok {
		result := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]interface{}); ok {
				result = append(result, m)
			}
		}
		return result
	}
	return nil
}

func str(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func num(m map[string]interface{}, key string) int {
	if v, ok := m[key].(int); ok {
		return v
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func footer(result *FeatureResult) string {
	var parts []string
	if result.Guidance != "" {
		parts = append(parts, result.Guidance)
	}
	if len(result.NextActions) > 0 {
		parts = append(parts, "**Next:** "+strings.Join(result.NextActions, " | "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n---\n" + strings.Join(parts, "\n")
}

// --- Error ---

func formatError(result *FeatureResult) string {
	s := "**Error:** " + result.Message
	if result.Guidance != "" {
		s += "\n" + result.Guidance
	}
	return s
}

// --- check-unreads ---

func formatUnreads(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder

	stats := data["stats"]
	statsMap, _ := stats.(map[string]interface{})

	b.WriteString(fmt.Sprintf("## Unreads\n\n"))

	unreads, _ := data["unreads"].(map[string]interface{})
	if unreads == nil {
		b.WriteString("No unreads.")
		return b.String()
	}

	// DMs
	dms := asList(unreads["dms"])
	if len(dms) > 0 {
		b.WriteString(fmt.Sprintf("### DMs (%d)\n\n", len(dms)))
		for _, dm := range dms {
			author := str(dm, "author")
			count := num(dm, "unreadCount")
			urgent := ""
			if v, ok := dm["urgent"].(bool); ok && v {
				urgent = " [URGENT]"
			}
			b.WriteString(fmt.Sprintf("**%s** (%d unread)%s\n", author, count, urgent))

			messages := asList(dm["messages"])
			limit := 5
			if len(messages) < limit {
				limit = len(messages)
			}
			for _, msg := range messages[:limit] {
				user := str(msg, "user")
				text := truncate(str(msg, "text"), 120)
				ts := str(msg, "timestamp")
				if text == "" {
					text = "(attachment/empty)"
				}
				b.WriteString(fmt.Sprintf("  %s | %s: %s\n", ts, user, text))
			}
			if len(messages) > 5 {
				b.WriteString(fmt.Sprintf("  +%d more messages\n", len(messages)-5))
			}
			b.WriteString("\n")
		}
	}

	// Mentions
	mentions := asList(unreads["mentions"])
	if len(mentions) > 0 {
		b.WriteString(fmt.Sprintf("### Mentions (%d)\n\n", len(mentions)))
		for _, m := range mentions {
			channel := str(m, "channel")
			author := str(m, "author")
			text := truncate(str(m, "message"), 100)
			ts := str(m, "timestamp")
			urgent := ""
			if v, ok := m["urgent"].(bool); ok && v {
				urgent = " [URGENT]"
			}
			b.WriteString(fmt.Sprintf("#%s | %s | %s%s\n  %s\n\n", channel, author, ts, urgent, text))
		}
	}

	// Thread unreads
	if threads, ok := data["threadUnreads"].(map[string]interface{}); ok {
		total := num(threads, "total")
		mentionCount := num(threads, "mentions")
		if total > 0 {
			b.WriteString(fmt.Sprintf("### Threads: %d unread (%d with mentions)\n\n", total, mentionCount))
		}
	}

	// Summary line
	if statsMap != nil {
		urgentCount := num(statsMap, "urgent")
		if urgentCount > 0 {
			b.WriteString(fmt.Sprintf("**%d urgent item(s) need attention.**\n", urgentCount))
		}
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- check-mentions ---

func formatMentions(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	mentions := asList(data["mentions"])

	b.WriteString(fmt.Sprintf("## Mentions (%d)\n\n", len(mentions)))

	for _, m := range mentions {
		channel := str(m, "channel")
		author := str(m, "author")
		text := truncate(str(m, "message"), 100)
		ts := str(m, "timestamp")
		urgency := str(m, "urgency")
		responded := ""
		if v, ok := m["responded"].(bool); ok && v {
			responded = " [replied]"
		}
		tag := ""
		if urgency == "high" {
			tag = " [URGENT]"
		} else if urgency == "medium" {
			tag = " [?]"
		}

		b.WriteString(fmt.Sprintf("#%s | %s | %s%s%s\n  %s\n\n", channel, author, ts, tag, responded, text))
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- list-channels ---

func formatChannels(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	channels := asList(data["channels"])

	b.WriteString(fmt.Sprintf("## Channels (%d)\n\n", len(channels)))

	for _, ch := range channels {
		display := str(ch, "displayName")
		purpose := truncate(str(ch, "purpose"), 60)
		member := ""
		if v, ok := ch["isMember"].(bool); ok && v {
			member = " [member]"
		}
		if purpose != "" {
			b.WriteString(fmt.Sprintf("%s%s — %s\n", display, member, purpose))
		} else {
			b.WriteString(fmt.Sprintf("%s%s\n", display, member))
		}
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- list-users ---

func formatUsers(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	users := asList(data["users"])

	b.WriteString(fmt.Sprintf("## Users (%d)\n\n", len(users)))

	for _, u := range users {
		display := str(u, "displayName")
		username := str(u, "username")
		title := str(u, "title")
		line := fmt.Sprintf("**%s** (@%s)", display, username)
		if title != "" {
			line += " — " + title
		}
		if v, ok := u["isBot"].(bool); ok && v {
			line += " [bot]"
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- catch-up ---

func formatCatchUp(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	channel := str(data, "channel")
	messages := asList(data["messages"])

	b.WriteString(fmt.Sprintf("## %s (%d messages)\n\n", channel, len(messages)))

	for _, msg := range messages {
		user := str(msg, "user")
		text := str(msg, "text")
		ts := str(msg, "time")
		if ts == "" {
			ts = str(msg, "timestamp")
		}

		// Show thread indicator
		replyCount := num(msg, "replyCount")
		threadTag := ""
		if replyCount > 0 {
			threadTag = fmt.Sprintf(" [%d replies]", replyCount)
		}

		b.WriteString(fmt.Sprintf("**%s** (%s)%s\n%s\n\n", user, ts, threadTag, text))
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- get-context ---

func formatContext(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	channel := str(data, "channel")
	messages := asList(data["messages"])

	header := channel
	if v, ok := data["isThread"].(bool); ok && v {
		header = fmt.Sprintf("Thread in %s", channel)
	}
	b.WriteString(fmt.Sprintf("## %s (%d messages)\n\n", header, len(messages)))

	for _, msg := range messages {
		user := str(msg, "user")
		text := str(msg, "text")
		ts := str(msg, "time")
		if ts == "" {
			ts = str(msg, "timestamp")
		}

		replyTag := ""
		if v, ok := msg["is_reply"].(bool); ok && v {
			replyTag = " ↩"
		}
		replyCount := num(msg, "reply_count")
		if replyCount > 0 {
			replyTag = fmt.Sprintf(" [%d replies]", replyCount)
		}

		b.WriteString(fmt.Sprintf("**%s** (%s)%s\n%s\n\n", user, ts, replyTag, text))
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- search ---

func formatSearch(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	messages := asList(data["messages"])
	query := str(data, "query")

	b.WriteString(fmt.Sprintf("## Search: \"%s\" (%d results)\n\n", query, len(messages)))

	for _, msg := range messages {
		channel := str(msg, "channel")
		user := str(msg, "user")
		text := truncate(str(msg, "text"), 120)
		ts := str(msg, "timestamp")

		b.WriteString(fmt.Sprintf("#%s | %s | %s\n  %s\n\n", channel, user, ts, text))
	}

	b.WriteString(footer(result))
	return b.String()
}

// --- send-message ---

func formatSendMessage(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return result.Message
	}

	channel := str(data, "channel")
	s := fmt.Sprintf("Message sent to %s.", channel)
	s += footer(result)
	return s
}

// --- mark-read ---

func formatMarkRead(result *FeatureResult) string {
	return result.Message + footer(result)
}

// --- react ---

func formatReact(result *FeatureResult) string {
	return result.Message + footer(result)
}

// --- check-timing ---

func formatTiming(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return formatGeneric(result)
	}

	var b strings.Builder
	b.WriteString("## Conversation Timing\n\n")

	if analysis, ok := data["analysis"].(map[string]interface{}); ok {
		for k, v := range analysis {
			b.WriteString(fmt.Sprintf("**%s:** %v\n", k, v))
		}
	}

	b.WriteString("\n" + result.Message)
	b.WriteString(footer(result))
	return b.String()
}

// --- auth-setup ---

func formatAuthSetup(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return result.Message
	}

	status := str(data, "status")
	switch status {
	case "connected":
		return fmt.Sprintf("Connected to **%s** as **%s**.", str(data, "workspace"), str(data, "user"))
	case "waiting":
		url := str(data, "url")
		if url != "" {
			return fmt.Sprintf("Setup running at %s — complete the flow in your browser.", url)
		}
		return "Setup in progress — waiting for browser flow to complete."
	case "cleared":
		return "Credentials cleared." + footer(result)
	default:
		return result.Message + footer(result)
	}
}

// --- Generic fallback ---

func formatGeneric(result *FeatureResult) string {
	s := result.Message
	s += footer(result)
	return s
}
