package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
	"github.com/slack-go/slack"
)

const (
	// firstLookWindow bounds a conversation with no recorded position.
	//
	// With nothing recorded, every conversation counts as unseen, and reporting
	// all of them would bury the caller. Seeding from Slack's last_read would fix
	// that by reintroducing exactly the coupling ADR-003 removes — a first look
	// would show nothing whenever the human had already caught up in their own
	// client. So the bound is time, and the coverage says so.
	firstLookWindow = 24 * time.Hour

	// maxHydrated caps how many conversations one tick fetches history for.
	maxHydrated = 20

	// historyPageSize and maxHistoryPages bound one conversation's fetch.
	// Together they allow 500 messages since the watermark before a
	// conversation is reported partial.
	historyPageSize = 100
	maxHistoryPages = 5

	// threadRecency bounds which tracked threads a tick re-checks. Each costs
	// one conversations.replies call, so without a bound the tick's cost grows
	// with every thread the agent has ever seen.
	threadRecency = 7 * 24 * time.Hour

	// maxTrackedPolled caps that further, so a busy month cannot make a tick
	// expensive. Threads beyond it are reported as unchecked rather than
	// silently skipped.
	maxTrackedPolled = 10
)

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

// tick accumulates what one poll found, including everything it could not
// reach. Coverage is assembled from this rather than from prose.
type tick struct {
	render func(string) string
	events []map[string]interface{}

	scanned   int
	moved     int
	read      int
	firstLook bool

	// Each of these independently makes the tick incomplete.
	conversationsDropped int // moved past maxHydrated
	conversationsPartial int // more history since the watermark than one tick reads
	conversationsFailed  int // history could not be fetched
	conversationsUnnamed int // no cached name, so reported by ID
	limitReached         bool

	threadsChecked   int
	threadsUnchecked int // tracked, but past the per-tick bound
	threadFeedFailed bool
}

func (t *tick) complete() bool {
	return t.conversationsDropped == 0 &&
		t.conversationsPartial == 0 &&
		t.conversationsFailed == 0 &&
		t.threadsUnchecked == 0 &&
		!t.threadFeedFailed &&
		!t.limitReached
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

	t := &tick{scanned: len(all)}
	moved := detectMoved(store, all, now.Add(-firstLookWindow), t)

	hydrated := moved
	if len(hydrated) > maxHydrated {
		t.conversationsDropped = len(hydrated) - maxHydrated
		hydrated = hydrated[:maxHydrated]
	}

	// Resolve names from cache only. Fetching per conversation would put an
	// unbounded number of conversations.info calls inside a tick whose whole
	// point is being cheap.
	naming := newNameIndex(apiProvider, t)
	usersMap := apiProvider.ProvideUsersMap()
	t.render = newBodyRenderer(apiProvider)

	hydrateConversations(ctx, api, store, hydrated, naming, usersMap, limit, now.Add(-firstLookWindow), t)
	tickThreads(ctx, api, internal, store, naming, usersMap, limit, now, t)

	return buildPollResult(t, counts.Threads.UnreadCountByChannel), nil
}

// detectMoved selects conversations whose newest message is past the agent's
// recorded position, bounding those with no position at all.
func detectMoved(store *watermark.Store, all []conversation, cutoff time.Time, t *tick) []conversation {
	var moved []conversation
	for _, c := range all {
		if !store.Changed(c.ID, c.Latest) {
			continue
		}
		if !store.Known(c.ID) {
			if parseSlackTimestamp(c.Latest).Before(cutoff) {
				// Outside the first-look window. Skipped, and deliberately not
				// counted as a first look: this conversation produces no event,
				// so it can never be acked, and flagging it would make every
				// future tick claim to be a first look forever.
				continue
			}
			t.firstLook = true
		}
		moved = append(moved, c)
	}

	// Newest first, so a truncated tick keeps the most recent movement.
	sort.Slice(moved, func(i, j int) bool {
		return parseSlackTimestamp(moved[i].Latest).After(parseSlackTimestamp(moved[j].Latest))
	})

	t.moved = len(moved)
	return moved
}

// hydrateConversations fetches the messages behind each moved conversation.
func hydrateConversations(
	ctx context.Context,
	api *slack.Client,
	store *watermark.Store,
	conversations []conversation,
	naming func(conversation) string,
	usersMap map[string]slack.User,
	limit int,
	cutoff time.Time,
	t *tick,
) {
	t.events = make([]map[string]interface{}, 0, limit)

	for _, c := range conversations {
		if len(t.events) >= limit {
			t.limitReached = true
			return
		}

		oldest := fmt.Sprintf("%d.000000", cutoff.Unix())
		if w, ok := store.Conversation(c.ID); ok {
			oldest = w.LastShownTS
		}

		where := naming(c)
		messages, overflow, err := fetchSince(ctx, api, c.ID, oldest, c.Latest)
		t.read++

		if err != nil {
			// One unreadable conversation must not cost the rest of the tick —
			// but losing it entirely is not a complete tick either.
			t.conversationsFailed++
			t.events = append(t.events, map[string]interface{}{
				"handle": handle.Conversation(c.ID),
				"kind":   "unreadable",
				"where":  where,
				"error":  err.Error(),
			})
			continue
		}
		if overflow {
			// More since the watermark than one tick reads. Report the backlog
			// rather than a slice missing its oldest half.
			t.conversationsPartial++
			t.events = append(t.events, map[string]interface{}{
				"handle": handle.Conversation(c.ID),
				"kind":   "backlog",
				"where":  where,
				"preview": fmt.Sprintf("More than %d messages since your position — more than one tick reads.",
					historyPageSize*maxHistoryPages),
			})
			continue
		}

		for _, msg := range messages {
			if len(t.events) >= limit {
				t.limitReached = true
				return
			}
			t.events = append(t.events, buildEvent(c, msg, where, usersMap, t.render))
		}
	}
}

// fetchSince returns every message in (oldest, latest], oldest-first.
//
// Slack returns history newest-first and its cursor pages BACKWARDS in time, so
// the last page reached is the oldest. Stopping early therefore holds the newest
// slice and is missing the messages nearest the watermark — exactly the ones the
// agent has not seen. Returning that slice would look like a complete answer
// while omitting the oldest, so when the range does not fit, this returns
// nothing and says so. The caller reports a backlog instead of a partial read.
//
// latest is pinned to the timestamp the tick observed rather than left open, so
// a conversation being actively posted to cannot push its own tail away faster
// than the pages arrive.
func fetchSince(ctx context.Context, api *slack.Client, channelID, oldest, latest string) ([]slack.Message, bool, error) {
	var collected []slack.Message
	cursor := ""

	for page := 0; page < maxHistoryPages; page++ {
		resp, err := api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
			ChannelID: channelID,
			Oldest:    oldest,
			Latest:    latest,
			Inclusive: true,
			Limit:     historyPageSize,
			Cursor:    cursor,
		})
		if err != nil {
			return nil, false, err
		}
		collected = append(collected, resp.Messages...)

		cursor = resp.ResponseMetaData.NextCursor
		if !resp.HasMore || cursor == "" {
			// Reached the oldest end of the range. Hand back oldest-first so a
			// reader follows the conversation forwards.
			reverse(collected)
			return dropAtOrBefore(collected, oldest), false, nil
		}
	}

	// Budget exhausted with older messages still unread. Discard rather than
	// return a window whose missing half is the half that matters.
	return nil, true, nil
}

// dropAtOrBefore removes messages at or older than the position already seen.
//
// Slack's `inclusive` applies to BOTH ends of a range, and the fetch needs it
// for `latest` so the newest message is not cut off. That also re-includes the
// message sitting exactly on the watermark, which the agent has already been
// shown — so it would be re-reported on every tick forever. Filtering here
// keeps the boundary agreeing with what the store considers changed.
func dropAtOrBefore(msgs []slack.Message, position string) []slack.Message {
	if position == "" {
		return msgs
	}
	out := msgs[:0]
	for _, m := range msgs {
		if watermark.After(m.Timestamp, position) {
			out = append(out, m)
		}
	}
	return out
}

func reverse(msgs []slack.Message) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

func buildPollResult(t *tick, threadActivity map[string]int) *FeatureResult {
	result := &FeatureResult{
		Success:     true,
		ResultCount: len(t.events),
		Data: map[string]interface{}{
			"changed": len(t.events) > 0,
			"events":  t.events,
			"coverage": map[string]interface{}{
				"conversationsScanned": t.scanned,
				"conversationsMoved":   t.moved,
				"conversationsRead":    t.read,
				"conversationsDropped": t.conversationsDropped,
				"conversationsPartial": t.conversationsPartial,
				"conversationsFailed":  t.conversationsFailed,
				"conversationsUnnamed": t.conversationsUnnamed,
				"limitReached":         t.limitReached,
				"threadsChecked":       t.threadsChecked,
				"threadsUnchecked":     t.threadsUnchecked,
				"complete":             t.complete(),
				"firstLook":            t.firstLook,
				"window":               describeWindow(t.firstLook),
			},
			"threadActivityByChannel": threadActivity,
		},
	}

	if len(t.events) == 0 {
		result.Message = fmt.Sprintf("Nothing new across %d conversations.", t.scanned)
		result.Guidance = "Nothing moved past your position. Poll again later."
		return result
	}

	result.Message = fmt.Sprintf("%d new messages across %d conversations.", len(t.events), t.read)
	result.NextActions = []string{
		"Read one in full: read handle='<handle from an event>'",
		"Record that you have seen these: ack handle='<handle>'",
	}

	var notes []string
	if t.conversationsDropped > 0 {
		notes = append(notes, fmt.Sprintf("%d further conversations moved but were not read this tick", t.conversationsDropped))
	}
	if t.conversationsPartial > 0 {
		notes = append(notes, fmt.Sprintf("%d conversations have a backlog larger than one tick reads", t.conversationsPartial))
	}
	if t.conversationsFailed > 0 {
		notes = append(notes, fmt.Sprintf("%d conversations could not be read", t.conversationsFailed))
	}
	if t.conversationsUnnamed > 0 {
		notes = append(notes, fmt.Sprintf("%d conversations are shown by ID because the channel cache is still warming", t.conversationsUnnamed))
	}
	if t.limitReached {
		notes = append(notes, "the event limit was reached")
	}
	if len(notes) > 0 {
		result.Guidance = capitalize(notes[0]) + strings.Join(prefixEach("; ", notes[1:]), "") +
			". Ack what you have seen, then poll again."
	}
	if t.firstLook {
		result.Guidance = strings.TrimSpace(result.Guidance +
			" This is a first look at conversations with no recorded position, bounded to the last 24 hours.")
	}
	if t.threadsUnchecked > 0 {
		result.Guidance = strings.TrimSpace(result.Guidance +
			fmt.Sprintf(" %d tracked threads were not re-checked this tick.", t.threadsUnchecked))
	}
	if t.threadFeedFailed {
		result.Guidance = strings.TrimSpace(result.Guidance +
			" The thread feed could not be read, so new threads may be missing.")
	}

	return result
}

// newNameIndex builds a conversation-ID to display-name map from what is
// already cached, so naming costs no requests.
//
// Slack does not populate Name on IM channel objects, so a DM resolves through
// the user it is with. Without that, every DM would fall back to a raw ID.
func newNameIndex(apiProvider *provider.ApiProvider, t *tick) func(conversation) string {
	users := apiProvider.ProvideUsersMap()
	byName := make(map[string]string, len(users))
	for _, u := range users {
		byName[u.Name] = displayName(u)
	}

	names := make(map[string]string)
	for _, ch := range apiProvider.GetCachedChannels() {
		switch {
		case ch.IsIM:
			if u, ok := users[ch.User]; ok {
				names[ch.ID] = "@" + displayName(u)
			}
		case ch.IsMpIM:
			names[ch.ID] = groupName(ch.Name, byName)
		case ch.Name != "":
			names[ch.ID] = "#" + ch.Name
		}
	}

	return func(c conversation) string {
		if name, ok := names[c.ID]; ok {
			return name
		}
		// A cold cache means every conversation reports as an ID, which is the
		// one thing this server promises not to do. Count it so the tick says
		// so instead of quietly looking like nonsense.
		t.conversationsUnnamed++
		return c.ID
	}
}

// groupName turns Slack's internal group-DM name into the people in it.
// The wire format is "mpdm-alice--bob--carol-1", which is not something to show
// anyone.
func groupName(raw string, byName map[string]string) string {
	trimmed := strings.TrimPrefix(raw, "mpdm-")
	if i := strings.LastIndex(trimmed, "-"); i > 0 {
		trimmed = trimmed[:i]
	}

	parts := strings.Split(trimmed, "--")
	if raw == "" || len(parts) == 0 {
		return "group DM"
	}

	people := make([]string, 0, len(parts))
	for _, p := range parts {
		if display, ok := byName[p]; ok {
			people = append(people, display)
		} else if p != "" {
			people = append(people, p)
		}
	}
	if len(people) == 0 {
		return "group DM"
	}
	return "group: " + strings.Join(people, ", ")
}

func displayName(u slack.User) string {
	return displayNameFor(u)
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

func buildEvent(c conversation, msg slack.Message, where string, usersMap map[string]slack.User, render func(string) string) map[string]interface{} {
	kind := "message"
	if c.Kind == "dm" {
		kind = "dm"
	}
	if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
		kind = "thread_reply"
	}

	// No raw channel ID and no raw timestamp: the handle carries both, and a
	// coordinate in the output is a coordinate the model will try to reuse.
	event := map[string]interface{}{
		"handle":  handle.Message(c.ID, msg.Timestamp),
		"kind":    kind,
		"where":   where,
		"who":     getUserName(msg.User, usersMap),
		"when":    formatTimestamp(parseSlackTimestamp(msg.Timestamp)),
		"preview": preview(render(msg.Text)),
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

// capitalize upper-cases the first rune. Slicing the first byte instead would
// mangle any note starting with a multi-byte rune, and panic on an empty one.
func capitalize(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+utf8.RuneLen(r):]
	}
	return s
}

func prefixEach(sep string, items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, sep+item)
	}
	return out
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

func describeWindow(firstLook bool) string {
	if firstLook {
		return "since your recorded position; last 24h where there is none"
	}
	return "since your recorded position"
}
