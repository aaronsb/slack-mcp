package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
	"github.com/slack-go/slack"
)

// firstLookWindow bounds the first poll against a fresh watermark.
//
// With nothing recorded, every conversation counts as unseen, and reporting all
// of them would bury the caller. Seeding from Slack's last_read would fix that
// by reintroducing exactly the coupling ADR-003 removes — a first look would
// show nothing whenever the human had already caught up in their own client.
// So the first look is bounded by time instead, and says so in its coverage.
const firstLookWindow = 24 * time.Hour

// maxHydrated caps how many conversations one tick fetches history for. Beyond
// this the tick reports the movement without the messages, and says so, rather
// than silently truncating.
const maxHydrated = 20

// Poll reports what changed since the agent last acknowledged.
var Poll = &Feature{
	Name: "poll",
	Description: "What changed since you last looked, across every channel and DM. " +
		"Takes no arguments. Reads only — never advances your Slack read marker, and " +
		"never advances this agent's position either; call 'ack' for that.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"scope": map[string]interface{}{
				"type": "string",
				"description": "Separates agents that should not share a position. " +
					"Omit unless you know you need it.",
				"default": watermark.DefaultScope,
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum events to return (default 50).",
				"default":     50,
			},
		},
		"required": []string{},
	},
	Handler: pollHandler,
}

// conversation is one entry from client.counts, flattened across the three
// kinds the endpoint reports separately.
type conversation struct {
	ID       string
	Latest   string
	Mentions int
	Kind     string // "channel", "dm", "group"
}

func pollHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	scope := watermark.DefaultScope
	if s, ok := params["scope"].(string); ok && s != "" {
		scope = s
	}

	limit := 50
	if l, ok := params["limit"].(float64); ok && int(l) > 0 {
		limit = int(l)
	}

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available"}, nil
	}

	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	internal := apiProvider.ProvideInternalClient()
	if internal == nil {
		return &FeatureResult{
			Success:  false,
			Message:  "Change detection needs the internal Slack endpoints, which are unavailable.",
			Guidance: "Run auth-setup to re-authenticate.",
		}, nil
	}

	identity := apiProvider.ProvideIdentity()
	if identity == nil || identity.Team == "" {
		return &FeatureResult{
			Success: false,
			Message: "Could not determine which workspace this is; a position cannot be stored without it.",
		}, nil
	}

	store, err := watermark.Open(identity.Team, scope)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not open the watermark store: %v", err),
		}, nil
	}

	// One call covers every conversation, read and unread alike.
	counts, err := internal.GetClientCounts(ctx)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to read conversation state: %v", err),
		}, nil
	}
	if !counts.OK {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Slack rejected the conversation-state request: %s", counts.Error),
		}, nil
	}

	all := flattenCounts(counts)
	now := time.Now()
	cutoff := now.Add(-firstLookWindow)

	var moved []conversation
	firstLook := false
	for _, c := range all {
		if !store.Changed(c.ID, c.Latest) {
			continue
		}
		if !store.Known(c.ID) {
			// Nothing recorded for this conversation. Bound it by time rather
			// than reporting its whole history.
			firstLook = true
			if parseSlackTimestamp(c.Latest).Before(cutoff) {
				continue
			}
		}
		moved = append(moved, c)
	}

	// Newest first, so a truncated tick keeps the most recent movement.
	sort.Slice(moved, func(i, j int) bool {
		return parseSlackTimestamp(moved[i].Latest).After(parseSlackTimestamp(moved[j].Latest))
	})

	hydrated := moved
	truncated := 0
	if len(hydrated) > maxHydrated {
		truncated = len(hydrated) - maxHydrated
		hydrated = hydrated[:maxHydrated]
	}

	usersMap := apiProvider.ProvideUsersMap()
	events := make([]map[string]interface{}, 0, limit)

	for _, c := range hydrated {
		oldest := ""
		if w, ok := store.Conversation(c.ID); ok {
			oldest = w.LastShownTS
		} else {
			oldest = fmt.Sprintf("%d.000000", cutoff.Unix())
		}

		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: c.ID,
			Oldest:    oldest,
			Inclusive: false,
			Limit:     30,
		}
		resp, err := api.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			// One unreadable conversation must not lose the rest of the tick.
			events = append(events, map[string]interface{}{
				"handle": handle.Conversation(c.ID),
				"kind":   "unreadable",
				"where":  describeConversation(ctx, apiProvider, c),
				"error":  err.Error(),
			})
			continue
		}

		name := describeConversation(ctx, apiProvider, c)
		for _, msg := range resp.Messages {
			if len(events) >= limit {
				break
			}
			events = append(events, buildEvent(c, msg, name, usersMap))
		}
		if len(events) >= limit {
			break
		}
	}

	threadHint := counts.Threads.UnreadCountByChannel

	result := &FeatureResult{
		Success:     true,
		ResultCount: len(events),
		Data: map[string]interface{}{
			"changed": len(events) > 0,
			"events":  events,
			"coverage": map[string]interface{}{
				"conversationsScanned": len(all),
				"conversationsMoved":   len(moved),
				"conversationsRead":    len(hydrated),
				"complete":             truncated == 0 && len(events) < limit,
				"firstLook":            firstLook,
				"window":               describeWindow(firstLook),
			},
			"threadActivityByChannel": threadHint,
		},
	}

	switch {
	case len(events) == 0:
		result.Message = fmt.Sprintf("Nothing new across %d conversations.", len(all))
		result.Guidance = "Nothing moved past your position. Poll again later."
	default:
		result.Message = fmt.Sprintf("%d new messages across %d conversations.", len(events), len(hydrated))
		result.NextActions = []string{
			"Read one in full: read handle='<handle from an event>'",
			"Record that you have seen these: ack handle='<handle>'",
		}
	}

	if truncated > 0 {
		result.Guidance = fmt.Sprintf(
			"%d further conversations moved but were not read this tick. Ack what you have seen, then poll again.",
			truncated)
	}
	if firstLook {
		result.Guidance = strings.TrimSpace(result.Guidance + " This is a first look, bounded to the last 24 hours; " +
			"ack to set your position, after which polls are unbounded.")
	}
	if len(threadHint) > 0 {
		result.NextActions = append(result.NextActions,
			"Threads moved in some channels; thread reporting arrives with the thread tick.")
	}

	return result, nil
}

// flattenCounts merges the three conversation kinds client.counts reports
// separately. Every entry carries `latest` whether or not it has unreads.
func flattenCounts(counts *provider.ClientCountsResponse) []conversation {
	out := make([]conversation, 0, len(counts.Channels)+len(counts.IMs)+len(counts.MPIMs))
	for _, c := range counts.Channels {
		out = append(out, conversation{ID: c.ID, Latest: c.Latest, Mentions: c.MentionCount, Kind: "channel"})
	}
	for _, c := range counts.IMs {
		out = append(out, conversation{ID: c.ID, Latest: c.Latest, Mentions: c.MentionCount, Kind: "dm"})
	}
	for _, c := range counts.MPIMs {
		out = append(out, conversation{ID: c.ID, Latest: c.Latest, Mentions: c.MentionCount, Kind: "group"})
	}
	return out
}

func buildEvent(c conversation, msg slack.Message, where string, usersMap map[string]slack.User) map[string]interface{} {
	kind := "message"
	if c.Kind == "dm" {
		kind = "dm"
	}
	if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
		kind = "thread_reply"
	}

	event := map[string]interface{}{
		"handle":  handle.Message(c.ID, msg.Timestamp),
		"kind":    kind,
		"where":   where,
		"who":     getUserName(msg.User, usersMap),
		"when":    formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
		"ts":      msg.Timestamp,
		"preview": preview(msg.Text),
	}

	if msg.ThreadTimestamp != "" {
		event["thread"] = handle.Thread(c.ID, msg.ThreadTimestamp)
	}
	if msg.ReplyCount > 0 {
		event["replyCount"] = msg.ReplyCount
	}
	if len(msg.Files) > 0 {
		event["hasFiles"] = len(msg.Files)
	}
	return event
}

// preview keeps an event small. Reading the whole message is what read is for.
func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const max = 160
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func describeConversation(ctx context.Context, apiProvider *provider.ApiProvider, c conversation) string {
	if name := apiProvider.ResolveChannelName(ctx, c.ID); name != "" {
		if c.Kind == "dm" {
			return "@" + name
		}
		return "#" + name
	}
	return c.ID
}

func describeWindow(firstLook bool) string {
	if firstLook {
		return "last 24h for conversations with no recorded position"
	}
	return "since your recorded position"
}
