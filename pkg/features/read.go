package features

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// maxCandidates bounds an ambiguous answer. A list long enough to scroll is a
// list the caller cannot choose from.
const maxCandidates = 5

// Read fetches the full content behind a handle, or resolves a description.
//
// This is the tool that retires the pasted coordinate. A caller either passes a
// handle it was given, or says what it means in the words a person would use;
// it never assembles a channel ID and a timestamp. When a description matches
// more than one thing, the answer is the candidates rather than a failure —
// disambiguation belongs in the agent's loop, not in a human's copy-paste.
var Read = &Feature{
	Name: "read",
	Description: "Read something in full — a message, a thread, or a conversation. " +
		"Takes a handle from a poll event, or a plain description such as " +
		"'the thread with Sarah about the deploy' or '#engineering'. " +
		"Does not mark anything read in Slack.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"handle": map[string]interface{}{
				"type": "string",
				"description": "A handle from a poll event, or a description of what you mean " +
					"('the deploy thread', '@sarah', '#engineering').",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum messages to return (default 50).",
				"default":     50,
			},
		},
		"required": []string{"handle"},
	},
	Handler: readHandler,
}

func readHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	target, _ := params["handle"].(string)
	target = strings.TrimSpace(target)
	if target == "" {
		return &FeatureResult{
			Success:  false,
			Message:  "Nothing to read.",
			Guidance: "Pass a handle from a poll event, or describe what you want in plain words.",
		}, nil
	}

	limit := 50
	if l, ok := params["limit"].(float64); ok && int(l) > 0 {
		limit = int(l)
		if limit > 200 {
			limit = 200
		}
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

	// A handle is unambiguous by construction, so it needs no resolution.
	if ref, err := handle.Decode(target); err == nil {
		return readRef(ctx, apiProvider, api, ref, limit)
	}

	return resolveAndRead(ctx, apiProvider, api, target, limit)
}

// readRef fetches whatever a handle points at.
func readRef(ctx context.Context, apiProvider *provider.ApiProvider, api *slack.Client, ref handle.Ref, limit int) (*FeatureResult, error) {
	where, named := conversationLabel(apiProvider, ref.Channel)

	switch ref.Kind {
	case handle.KindThread:
		return readThread(ctx, apiProvider, api, ref.Channel, ref.TS, where, named, limit)

	case handle.KindMessage:
		// A message that turns out to have replies is more usefully read as its
		// thread — the reply is rarely comprehensible alone.
		replies, _, _, err := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID: ref.Channel,
			Timestamp: ref.TS,
			Limit:     limit,
		})
		if err == nil && len(replies) > 1 {
			return readThread(ctx, apiProvider, api, ref.Channel, ref.TS, where, named, limit)
		}
		if err == nil && len(replies) == 1 {
			return messagesResult(apiProvider, where, "message", ref.Channel, replies, named, false)
		}
		if err == nil {
			// Slack answered and had nothing. Saying "error: <nil>" here
			// described the request rather than the result.
			return &FeatureResult{
				Success:  false,
				Message:  fmt.Sprintf("No such message in %s — it may have been deleted.", where),
				Guidance: "Check inbox view='new' again for a current handle.",
			}, nil
		}
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not read that message in %s: %v", where, err),
		}, nil

	case handle.KindConversation:
		resp, err := api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
			ChannelID: ref.Channel,
			Limit:     limit,
		})
		if err != nil {
			return &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Could not read %s: %v", where, err),
			}, nil
		}
		msgs := resp.Messages
		reverse(msgs)
		return messagesResult(apiProvider, where, "conversation", ref.Channel, msgs, named, resp.HasMore)
	}

	return &FeatureResult{Success: false, Message: "Unrecognised handle."}, nil
}

func readThread(ctx context.Context, apiProvider *provider.ApiProvider, api *slack.Client, channel, threadTS, where string, named bool, limit int) (*FeatureResult, error) {
	msgs, _, _, err := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     limit,
	})
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not read that thread in %s: %v", where, err),
		}, nil
	}
	return messagesResult(apiProvider, where, "thread", channel, msgs, named, false)
}

func messagesResult(apiProvider *provider.ApiProvider, where, kind, channel string, msgs []slack.Message, named, more bool) (*FeatureResult, error) {
	observeTraffic(apiProvider, channel, msgs)
	r := newMessageRenderer(apiProvider)
	out := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		rm := r.Render(m)
		entry := map[string]interface{}{
			"handle": handle.Message(channel, m.Timestamp),
			"who":    rm.Author,
			"when":   formatTimestamp(parseSlackTimestamp(m.Timestamp)),
			"text":   rm.Body,
		}
		if len(rm.Unresolved) > 0 {
			entry["unresolved"] = rm.Unresolved
		}
		if len(m.Files) > 0 {
			files := make([]map[string]interface{}, 0, len(m.Files))
			for _, f := range m.Files {
				files = append(files, map[string]interface{}{
					"id": f.ID, "name": f.Name, "mimetype": f.Mimetype, "size": f.Size,
				})
			}
			entry["files"] = files
		}
		out = append(out, entry)
	}

	var lastHandle string
	if len(out) > 0 {
		lastHandle, _ = out[len(out)-1]["handle"].(string)
	}

	result := &FeatureResult{
		Success:     true,
		ResultCount: len(out),
		Message:     fmt.Sprintf("%d messages from %s.", len(out), where),
		Data: map[string]interface{}{
			"kind":     kind,
			"where":    where,
			"messages": out,
			"coverage": map[string]interface{}{
				"complete": !more,
				// False means the channel cache had no name for this
				// conversation, so `where` is a raw ID rather than a name.
				"named": named,
			},
		},
	}
	if lastHandle != "" {
		result.NextActions = []string{
			fmt.Sprintf("Record that you have read this: dismiss handle='%s'", lastHandle),
		}
	}
	if more {
		result.Guidance = "Older messages remain beyond this window."
	}
	return result, nil
}

// resolveAndRead turns a description into something readable, or into the
// choices it could be.
func resolveAndRead(ctx context.Context, apiProvider *provider.ApiProvider, api *slack.Client, description string, limit int) (*FeatureResult, error) {
	candidates, total := resolveCandidates(apiProvider, description)

	if len(candidates) == 0 {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Nothing here matches %q.", description),
			Data: map[string]interface{}{
				"found":    false,
				"searched": "names of channels, people, and group DMs you are in",
			},
			Guidance: "Name a channel, a person, or use a handle from a poll event. " +
				"For message text rather than a place, use search.",
		}, nil
	}

	// A single match resolves only if the description matched a NAME. A match
	// found in a channel's topic or purpose is a suggestion — resolving on one
	// would silently read a conversation the caller did not ask for.
	if len(candidates) == 1 && candidates[0].score > scoreDescription {
		return readRef(ctx, apiProvider, api, handle.Ref{
			Kind:    handle.KindConversation,
			Channel: candidates[0].id,
		}, limit)
	}

	// Ambiguity is an answer, not a failure. One cheap round trip inside the
	// agent's loop replaces a human deciding which coordinate to paste.
	options := make([]map[string]interface{}, 0, len(candidates))
	for _, c := range candidates {
		option := map[string]interface{}{
			"handle": handle.Conversation(c.id),
			"where":  c.label,
			"kind":   c.kind,
		}
		if c.score == scoreDescription {
			option["matchedOn"] = "topic or purpose, not the name"
		}
		options = append(options, option)
	}

	message := fmt.Sprintf("%q matches %d places.", description, total)
	if total > len(options) {
		message = fmt.Sprintf("%q matches %d places; showing %d.", description, total, len(options))
	}

	return &FeatureResult{
		Success:     true,
		ResultCount: len(options),
		Message:     message,
		Data: map[string]interface{}{
			"ambiguous":    true,
			"candidates":   options,
			"totalMatches": total,
			"truncated":    total > len(options),
		},
		Guidance: "Read one by passing its handle.",
	}, nil
}

type candidate struct {
	id    string
	label string
	kind  string
	score int
}

const (
	scoreExactName = 4
	scoreWordName  = 3
	scorePartName  = 2
	// scoreDescription rates a match found only in a channel's topic or
	// purpose. It never resolves on its own: "sarah" appearing in #general's
	// topic must not silently become the answer to a question about Sarah.
	// Reading the wrong conversation is worse than reading none.
	scoreDescription = 1
)

// resolveCandidates matches a description against the conversations the user is
// in, using only cached data. It returns the candidates and how many there were
// before truncation, so a caller is never shown a shortened list as if it were
// the whole one.
//
// Two passes. The whole phrase first, because "deploy" naming a channel exactly
// is a far stronger signal than the words it happens to contain. Only when the
// phrase matches nothing does it fall back to the significant words, and those
// are offered as choices rather than resolved — "the thread with Sarah about
// the deploy" names two things, and picking one of them silently would be
// guessing.
func resolveCandidates(apiProvider *provider.ApiProvider, description string) ([]candidate, int) {
	needles := needlesFor(description)
	if len(needles) == 0 {
		return nil, 0
	}

	// Pass one: the phrase as written.
	if found := matchConversations(apiProvider, needles[:1]); len(found) > 0 {
		return finish(found, true)
	}

	// Pass two: the words worth matching on.
	if len(needles) == 1 {
		return nil, 0
	}
	return finish(matchConversations(apiProvider, needles[1:]), false)
}

// finish orders candidates, drops the weak ones when strong ones exist, and
// truncates.
func finish(found []candidate, collapseExact bool) ([]candidate, int) {
	if len(found) == 0 {
		return nil, 0
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].label < found[j].label
	})

	// A match found only in topic or purpose text is a suggestion. Where any
	// conversation matched by NAME, the topic matches are noise and drop out.
	if found[0].score > scoreDescription {
		named := found[:0:0]
		for _, c := range found {
			if c.score > scoreDescription {
				named = append(named, c)
			}
		}
		found = named
	}

	// An exact name beats the weaker matches of the same term outright. This
	// only applies to a single term: across several words, an exact match on
	// one must not suppress what the others found.
	if collapseExact && found[0].score == scoreExactName {
		exact := found[:0:0]
		for _, c := range found {
			if c.score == scoreExactName {
				exact = append(exact, c)
			}
		}
		found = exact
	}

	total := len(found)
	if len(found) > maxCandidates {
		found = found[:maxCandidates]
	}
	return found, total
}

// matchConversations scores every cached conversation against the given terms.
func matchConversations(apiProvider *provider.ApiProvider, needles []string) []candidate {
	users := apiProvider.ProvideUsersMap()
	best := make(map[string]candidate)

	for _, ch := range apiProvider.GetCachedChannels() {
		var label, kind string
		var names, descriptions []string

		switch {
		case ch.IsIM:
			u, ok := users[ch.User]
			if !ok {
				continue
			}
			label, kind = "@"+displayName(u), "dm"
			names = []string{u.Name, u.RealName, u.Profile.DisplayName}
		case ch.IsMpIM:
			label, kind = groupName(ch.Name, nil), "group"
			names = []string{ch.Name}
		default:
			if ch.Name == "" {
				continue
			}
			label, kind = "#"+ch.Name, "channel"
			names = []string{ch.Name}
			descriptions = []string{ch.Purpose.Value, ch.Topic.Value}
		}

		score := 0
		for _, needle := range needles {
			if s := nameScore(needle, names); s > score {
				score = s
			}
			if score < scoreDescription && matchesAny(needle, descriptions) {
				score = scoreDescription
			}
		}
		if score == 0 {
			continue
		}
		if existing, ok := best[ch.ID]; !ok || score > existing.score {
			best[ch.ID] = candidate{id: ch.ID, label: label, kind: kind, score: score}
		}
	}

	found := make([]candidate, 0, len(best))
	for _, c := range best {
		found = append(found, c)
	}
	return found
}

// needlesFor turns a description into the terms worth matching on. A caller
// writing "the thread with Sarah about the deploy" means Sarah and the deploy;
// matching the sentence whole finds nothing.
func needlesFor(description string) []string {
	normalized := strings.ToLower(strings.TrimSpace(description))
	normalized = strings.TrimPrefix(normalized, "#")
	normalized = strings.TrimPrefix(normalized, "@")
	if normalized == "" {
		return nil
	}

	// The whole phrase first, so an exact channel name containing spaces or
	// hyphens still wins outright.
	needles := []string{normalized}

	if len(strings.Fields(normalized)) < 2 {
		return needles
	}
	for _, token := range strings.FieldsFunc(normalized, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\'' || r == '"'
	}) {
		token = strings.Trim(token, "-_.")
		if len(token) < 3 || isStopWord(token) {
			continue
		}
		needles = append(needles, token)
	}
	return needles
}

var stopWords = map[string]bool{
	"the": true, "and": true, "with": true, "about": true, "from": true,
	"that": true, "this": true, "for": true, "our": true, "his": true,
	"her": true, "their": true, "thread": true, "message": true,
	"conversation": true, "chat": true, "channel": true, "dm": true,
	"discussion": true, "was": true, "were": true, "had": true,
}

func isStopWord(s string) bool { return stopWords[s] }

// nameScore rates a term against the names a conversation is known by.
func nameScore(needle string, names []string) int {
	best := 0
	for _, h := range names {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		switch {
		case h == needle:
			return scoreExactName
		case containsWholeWord(h, needle):
			if best < scoreWordName {
				best = scoreWordName
			}
		case strings.Contains(h, needle):
			if best < scorePartName {
				best = scorePartName
			}
		}
	}
	return best
}

func matchesAny(needle string, haystacks []string) bool {
	for _, h := range haystacks {
		if h != "" && strings.Contains(strings.ToLower(h), needle) {
			return true
		}
	}
	return false
}

func containsWholeWord(haystack, needle string) bool {
	for _, field := range strings.FieldsFunc(haystack, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '.' || r == ','
	}) {
		if field == needle {
			return true
		}
	}
	return false
}

// conversationLabel names a conversation from cache. It returns the raw ID and
// false when the cache has no name for it — a cold start reports every
// conversation by ID, which is the one thing this server promises not to do, so
// the caller says when it happens rather than letting an ID look like a name.
func conversationLabel(apiProvider *provider.ApiProvider, channelID string) (string, bool) {
	users := apiProvider.ProvideUsersMap()
	for _, ch := range apiProvider.GetCachedChannels() {
		if ch.ID != channelID {
			continue
		}
		switch {
		case ch.IsIM:
			if u, ok := users[ch.User]; ok {
				return "@" + displayName(u), true
			}
		case ch.IsMpIM:
			return groupName(ch.Name, nil), true
		case ch.Name != "":
			return "#" + ch.Name, true
		}
	}
	return channelID, false
}
