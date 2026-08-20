package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The v2 tool surface (ADR-009): eight tools by the assignment rule — verb
// encodes effect, noun encodes domain, parameter encodes scope. The nouns
// here are thin dispatchers over the v1 handlers, which keep their names,
// tests, and rendering; a delegating result carries RenderAs so the
// formatter dispatch follows the delegation, and Echo so the output states
// the effective invocation (an agent cannot distinguish "my parameter
// worked" from "my parameter was dropped" unless the output says what ran).

// delegate invokes a v1 feature and stamps the result for v2 rendering.
func delegate(ctx context.Context, f *Feature, params map[string]interface{}, echo string) (*FeatureResult, error) {
	res, err := f.Handler(ctx, params)
	if err != nil || res == nil {
		return res, err
	}
	res.RenderAs = f.Name
	res.Echo = echo
	return res, nil
}

// echoLine states the tool, the mode, and every explicitly-passed scope
// parameter, so the rendered output opens with the effective invocation.
func echoLine(tool, mode string, params map[string]interface{}, keys ...string) string {
	parts := []string{tool + " " + mode}
	passed := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := params[k]; ok && v != nil && v != "" {
			passed = append(passed, fmt.Sprintf("%s=%v", k, v))
		}
	}
	sort.Strings(passed)
	parts = append(parts, passed...)
	return strings.Join(parts, " ")
}

// ---- inbox: what needs me ----

var Inbox = &Feature{
	Name:        "inbox",
	Description: "What needs you. view='new' reports what changed since your last dismiss (conversations and threads, watermark-tracked). view='unreads' scans unread DMs and channels. view='mentions' collects your @-mentions by urgency. Read-only; nothing here marks anything read.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"view": map[string]interface{}{
				"type":        "string",
				"description": "Which view: 'new' (since last dismiss), 'unreads', 'mentions'",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum items (new: default 50; unreads: per category, max 25; mentions: per page, max 50)",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Continuation cursor from a previous page (mentions)",
			},
			"focus": map[string]interface{}{
				"type":        "string",
				"description": "unreads only: 'all', 'dms', or 'channels'",
			},
			"timeframe": map[string]interface{}{
				"type":        "string",
				"description": "mentions only: how far back to look (default 3d)",
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"description": "new only: watermark scope for multiple independent watchers",
			},
		},
		"required": []string{"view"},
	},
	Handler: inboxHandler,
}

func inboxHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	view, _ := params["view"].(string)
	echo := echoLine("inbox", "view='"+view+"'", params, "limit", "cursor", "focus", "timeframe", "scope")
	switch view {
	case "new":
		return delegate(ctx, Poll, params, echo)
	case "unreads":
		return delegate(ctx, CheckUnreads, params, echo)
	case "mentions":
		return delegate(ctx, CheckMyMentions, params, echo)
	default:
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Unknown view %q. Available views: new (since last dismiss), unreads, mentions", view),
		}, nil
	}
}

// ---- messages: conversation content ----

var Messages = &Feature{
	Name:        "messages",
	Description: "Conversation content, addressed four ways (precedence: query beats target; around beats since): target alone reads it in full (a handle, '#channel', '@person', or a description); target+around fetches context around a timestamp; target+since renders a time window with triage; query searches with full Slack syntax (from:, in:, before:...). Read-only; never marks anything read.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "What to read: a handle from inbox, '#channel', '@person', or a description ('the deploy thread')",
			},
			"around": map[string]interface{}{
				"type":        "string",
				"description": "With target: a message timestamp to fetch context around (thread replies if it starts one)",
			},
			"since": map[string]interface{}{
				"type":        "string",
				"description": "With target: a time window to catch up on ('2h', '1d', '30m') with triage highlighting",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search instead of fetch: full Slack search syntax; the server resolves from:@name through the ladder",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum messages. Per mode: bare target default 50 cap 200; around default 10 cap 100; since default 20 cap 50; ignored for query.",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Continuation cursor from a previous page",
			},
			"timeframe": map[string]interface{}{
				"type":        "string",
				"description": "query only: how far back to search (default 1w)",
			},
		},
		"required": []string{},
	},
	Handler: messagesHandler,
}

func messagesHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	target, _ := params["target"].(string)
	query, _ := params["query"].(string)
	around, _ := params["around"].(string)
	since, _ := params["since"].(string)

	switch {
	case query != "":
		echo := echoLine("messages", "query='"+query+"'", params, "cursor", "timeframe")
		return delegate(ctx, FindDiscussion, params, echo)
	case target != "" && around != "":
		params["channel"] = target
		params["messageTs"] = around
		if l, ok := params["limit"].(float64); ok {
			params["count"] = l
		}
		echo := echoLine("messages", "target='"+target+"' around="+around, params, "limit")
		return delegate(ctx, GetContext, params, echo)
	case target != "" && since != "":
		params["channel"] = target
		echo := echoLine("messages", "target='"+target+"' since="+since, params, "limit", "cursor")
		return delegate(ctx, CatchUpOnChannel, params, echo)
	case target != "":
		params["handle"] = target
		echo := echoLine("messages", "target='"+target+"'", params, "limit")
		return delegate(ctx, Read, params, echo)
	default:
		return &FeatureResult{
			Success: false,
			Message: "messages needs an address: target='<handle|#channel|@person>' (optionally with around=<ts> or since=<window>), or query='<slack search>'",
		}, nil
	}
}

// ---- say: contribute content (the one visible write besides mark-read) ----

var Say = &Feature{
	Name:        "say",
	Description: "Contribute content, Slack-visible: text posts a message (to a channel, DM, or thread); emoji+messageTs adds or removes a reaction — a reaction is a small say. This is a write; everything it posts is attributed to your user.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Where: '#channel', '@person', or a channel ID",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "The message to post",
			},
			"thread": map[string]interface{}{
				"type":        "string",
				"description": "Thread timestamp to reply into",
			},
			"emoji": map[string]interface{}{
				"type":        "string",
				"description": "Reaction mode: emoji name without colons ('thumbsup'); requires messageTs",
			},
			"messageTs": map[string]interface{}{
				"type":        "string",
				"description": "Reaction mode: the message to react to",
			},
			"remove": map[string]interface{}{
				"type":        "boolean",
				"description": "Reaction mode: remove instead of add",
				"default":     false,
			},
		},
		"required": []string{"to"},
	},
	Handler: sayHandler,
}

func sayHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	to, _ := params["to"].(string)
	text, _ := params["text"].(string)
	emoji, _ := params["emoji"].(string)

	switch {
	case emoji != "":
		if ts, _ := params["messageTs"].(string); ts == "" {
			return &FeatureResult{
				Success: false,
				Message: "Reaction mode needs messageTs='<timestamp of the message to react to>'",
			}, nil
		}
		params["channel"] = to
		echo := echoLine("say", "reaction :"+emoji+": to='"+to+"'", params, "messageTs", "remove")
		return delegate(ctx, React, params, echo)
	case text != "":
		params["channel"] = to
		params["message"] = text
		if th, ok := params["thread"].(string); ok && th != "" {
			params["threadTs"] = th
		}
		echo := echoLine("say", "to='"+to+"'", params, "thread")
		return delegate(ctx, WriteMessage, params, echo)
	default:
		return &FeatureResult{
			Success: false,
			Message: "say needs text='...' (a message) or emoji='...' messageTs='...' (a reaction)",
		}, nil
	}
}

// ---- renamed verbs: same handlers, names that state the effect ----

// Dismiss advances the private watermark — ack's handler under a name
// that says what it means. Zero Slack effect; no receipts.
var Dismiss = &Feature{
	Name:        "dismiss",
	Description: "Mark inbox items as handled — privately. Advances your local watermark so inbox view='new' stops reporting them. Invisible to Slack: no read receipts, no presence signal. (mark-read is the public counterpart.)",
	Schema:      Ack.Schema,
	Handler:     dismissHandler,
}

func dismissHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	return delegate(ctx, Ack, params, "")
}

// Auth is auth-setup under its shorter name.
var Auth = &Feature{
	Name:        "auth",
	Description: "Interactive credential setup: walks through capturing Slack session tokens via a localhost helper. Tokens never leave this machine.",
	Schema:      AuthSetup.Schema,
	Handler:     authHandler,
}

func authHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	return delegate(ctx, AuthSetup, params, "")
}

// Download is download-file under its shorter name.
var Download = &Feature{
	Name:        "download",
	Description: "Download a file shared in Slack to the local filesystem.",
	Schema:      DownloadFile.Schema,
	Handler:     downloadHandler,
}

func downloadHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	return delegate(ctx, DownloadFile, params, "")
}
