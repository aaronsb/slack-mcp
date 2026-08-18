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
	usersMap := apiProvider.ProvideUsersMap()
	where := conversationLabel(apiProvider, ref.Channel)

	switch ref.Kind {
	case handle.KindThread:
		return readThread(ctx, api, ref.Channel, ref.TS, where, usersMap, limit)

	case handle.KindMessage:
		// A message that turns out to have replies is more usefully read as its
		// thread — the reply is rarely comprehensible alone.
		replies, _, _, err := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID: ref.Channel,
			Timestamp: ref.TS,
			Limit:     limit,
		})
		if err == nil && len(replies) > 1 {
			return readThread(ctx, api, ref.Channel, ref.TS, where, usersMap, limit)
		}
		if err == nil && len(replies) == 1 {
			return messagesResult(where, "message", ref.Channel, replies, usersMap, false)
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
		return messagesResult(where, "conversation", ref.Channel, msgs, usersMap, resp.HasMore)
	}

	return &FeatureResult{Success: false, Message: "Unrecognised handle."}, nil
}

func readThread(ctx context.Context, api *slack.Client, channel, threadTS, where string, usersMap map[string]slack.User, limit int) (*FeatureResult, error) {
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
	return messagesResult(where, "thread", channel, msgs, usersMap, false)
}

func messagesResult(where, kind, channel string, msgs []slack.Message, usersMap map[string]slack.User, more bool) (*FeatureResult, error) {
	out := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]interface{}{
			"handle": handle.Message(channel, m.Timestamp),
			"who":    getUserName(m.User, usersMap),
			"when":   formatTimestamp(parseSlackTimestamp(m.Timestamp)),
			"text":   m.Text,
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
			},
		},
	}
	if lastHandle != "" {
		result.NextActions = []string{
			fmt.Sprintf("Record that you have read this: ack handle='%s'", lastHandle),
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
	candidates := resolveCandidates(apiProvider, description)

	switch len(candidates) {
	case 0:
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Nothing here matches %q.", description),
			Data: map[string]interface{}{
				"found":    false,
				"searched": "channels and direct messages you are in",
			},
			Guidance: "Name a channel, a person, or use a handle from a poll event. " +
				"For message text rather than a place, use search.",
		}, nil

	case 1:
		return readRef(ctx, apiProvider, api, handle.Ref{
			Kind:    handle.KindConversation,
			Channel: candidates[0].id,
		}, limit)
	}

	// Ambiguity is an answer, not a failure. One cheap round trip inside the
	// agent's loop replaces a human deciding which coordinate to paste.
	options := make([]map[string]interface{}, 0, len(candidates))
	for _, c := range candidates {
		options = append(options, map[string]interface{}{
			"handle": handle.Conversation(c.id),
			"where":  c.label,
			"kind":   c.kind,
		})
	}

	return &FeatureResult{
		Success:     true,
		ResultCount: len(options),
		Message:     fmt.Sprintf("%q matches %d places.", description, len(options)),
		Data: map[string]interface{}{
			"ambiguous":  true,
			"candidates": options,
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

// resolveCandidates matches a description against the conversations the user is
// in, using only cached data.
func resolveCandidates(apiProvider *provider.ApiProvider, description string) []candidate {
	needle := strings.ToLower(strings.TrimSpace(description))
	needle = strings.TrimPrefix(needle, "#")
	needle = strings.TrimPrefix(needle, "@")
	if needle == "" {
		return nil
	}

	users := apiProvider.ProvideUsersMap()
	var found []candidate

	for _, ch := range apiProvider.GetCachedChannels() {
		var label, kind string
		var haystacks []string

		switch {
		case ch.IsIM:
			u, ok := users[ch.User]
			if !ok {
				continue
			}
			label, kind = "@"+displayName(u), "dm"
			haystacks = []string{u.Name, u.RealName, u.Profile.DisplayName}
		case ch.IsMpIM:
			label, kind = groupName(ch.Name, nil), "group"
			haystacks = []string{ch.Name}
		default:
			if ch.Name == "" {
				continue
			}
			label, kind = "#"+ch.Name, "channel"
			haystacks = []string{ch.Name, ch.Purpose.Value, ch.Topic.Value}
		}

		if score := matchScore(needle, haystacks); score > 0 {
			found = append(found, candidate{id: ch.ID, label: label, kind: kind, score: score})
		}
	}

	// Exact matches first, then by label so the order is stable.
	sort.Slice(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].label < found[j].label
	})

	// An exact match wins outright rather than being offered alongside the
	// substring matches it obviously beats.
	if len(found) > 0 && found[0].score == scoreExact {
		exact := found[:0:0]
		for _, c := range found {
			if c.score == scoreExact {
				exact = append(exact, c)
			}
		}
		found = exact
	}

	if len(found) > maxCandidates {
		found = found[:maxCandidates]
	}
	return found
}

const (
	scoreExact = 3
	scoreWord  = 2
	scorePart  = 1
)

// matchScore rates a description against the names a conversation is known by.
func matchScore(needle string, haystacks []string) int {
	best := 0
	for _, h := range haystacks {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		switch {
		case h == needle:
			return scoreExact
		case containsWholeWord(h, needle):
			if best < scoreWord {
				best = scoreWord
			}
		case strings.Contains(h, needle):
			if best < scorePart {
				best = scorePart
			}
		}
	}
	return best
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

// conversationLabel names a conversation from cache, falling back to its ID.
func conversationLabel(apiProvider *provider.ApiProvider, channelID string) string {
	users := apiProvider.ProvideUsersMap()
	for _, ch := range apiProvider.GetCachedChannels() {
		if ch.ID != channelID {
			continue
		}
		switch {
		case ch.IsIM:
			if u, ok := users[ch.User]; ok {
				return "@" + displayName(u)
			}
		case ch.IsMpIM:
			return groupName(ch.Name, nil)
		case ch.Name != "":
			return "#" + ch.Name
		}
	}
	return channelID
}
