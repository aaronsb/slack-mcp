package features

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
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
	if res.RenderAs == "" {
		// A handler that chose its renderer (a person miss renders as
		// estate) keeps it; everything else renders as the feature.
		res.RenderAs = f.Name
	}
	res.Echo = echo
	return res, nil
}

// echoLine states the tool, the mode, and every explicitly-passed scope
// parameter, so the rendered output opens with the effective invocation.
func echoLine(tool, mode string, params map[string]interface{}, keys ...string) string {
	head := tool
	if mode != "" {
		head += " " + mode
	}
	parts := []string{head}
	passed := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := params[k]
		if !ok || v == nil || v == "" || v == false {
			continue
		}
		if list, isList := v.([]interface{}); isList && len(list) == 0 {
			continue
		}
		if list, isList := v.([]string); isList && len(list) == 0 {
			continue
		}
		passed = append(passed, fmt.Sprintf("%s=%v", k, v))
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
	Description: "Conversation content, addressed four ways, plus scheduled=true for what you have scheduled to send (precedence: query and filters beat target-less modes and refuse target; around beats since): target alone reads it in full (a handle, '#channel', '@person', or a description); target+around fetches context around a timestamp; target+since renders a time window with triage; query searches (raw Slack syntax passes through as written; in=, from=, after=, before=, has=, thread= are resolved filters composed onto it). Read-only; never marks anything read.",
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
				"description": "Search instead of fetch: raw Slack search text, passed to Slack as written (a from:@name typed here is NOT resolved or checked; use from= for resolution)",
			},
			"in": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "search: narrow to a '#channel' or '@person' (DM); resolved by name, a miss returns candidates and does not search. Several entries compose as repeated in: clauses; Slack's OR/AND behavior for repeats is unverified",
			},
			"from": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "search: messages by these people; resolved through the person ladder, a miss returns candidates and does not search. Several entries compose as repeated from: clauses; Slack's OR/AND behavior for repeats is unverified",
			},
			"after": map[string]interface{}{
				"type":        "string",
				"description": "search: YYYY-MM-DD (or 3d, 2w). Replaces the default window; not with timeframe",
			},
			"before": map[string]interface{}{
				"type":        "string",
				"description": "search: YYYY-MM-DD (or 3d, 2w)",
			},
			"has": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "search: 'link', 'pin', or a reaction as ':emoji:' (Slack's has::emoji:). Other values (file, star, bare reaction) are unverified and rejected",
			},
			"thread": map[string]interface{}{
				"type":        "boolean",
				"description": "search: only messages in threads (is:thread)",
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
				"description": "search: how far back to search (default 1w); refused together with after=, before=, or a date operator (after:, before:, on:, during:) in query=",
			},
			"scheduled": map[string]interface{}{
				"type":        "boolean",
				"description": "List your pending scheduled messages, soonest first, with handles for say cancel=; target= narrows to one conversation. Your own unsent composer drafts are never shown.",
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

	if scheduled, _ := params["scheduled"].(bool); scheduled {
		if query != "" || hasFilter(params) || around != "" || since != "" {
			return &FeatureResult{
				Success: false,
				Message: "scheduled=true lists scheduled messages; it takes only target= to narrow to one conversation, not query, filters, around, or since.",
			}, nil
		}
		ap, _ := params["_provider"].(*provider.ApiProvider)
		echo := echoLine("messages", "scheduled=true", params, "target")
		res, err := listScheduled(ctx, ap, target)
		if res != nil {
			if res.RenderAs == "" {
				res.RenderAs = "messages-scheduled"
			}
			res.Echo = echo
		}
		return res, err
	}

	switch {
	case query != "" || hasFilter(params):
		if target != "" {
			return &FeatureResult{
				Success:  false,
				Message:  "target= cannot be combined with query= or search filters.",
				Guidance: "To search one place use in='#channel' or in='@person'; to read it, drop the filters.",
			}, nil
		}
		// Echo what ran: the limit as Slack received it, not as passed.
		shown := params
		if lim, ok := explicitLimit(params); ok {
			shown = make(map[string]interface{}, len(params))
			for k, v := range params {
				shown[k] = v
			}
			shown["limit"] = lim
		}
		mode := ""
		if query != "" {
			mode = "query='" + query + "'"
		}
		echo := echoLine("messages", mode, shown, "cursor", "limit", "timeframe", "in", "from", "after", "before", "has", "thread")
		res, err := delegate(ctx, FindDiscussion, params, echo)
		if res != nil {
			res.Echo += res.EchoSuffix
		}
		return res, err
	case target != "" && around != "":
		params["channel"] = conversationOf(target)
		params["messageTs"] = around
		if l, ok := params["limit"].(float64); ok {
			params["count"] = l
		}
		echo := echoLine("messages", "target='"+target+"' around="+around, params, "limit")
		return delegate(ctx, GetContext, params, echo)
	case target != "" && since != "":
		// catch-up resolves the target itself, under the read policy.
		channel := conversationOf(target)
		params["channel"] = channel
		echo := echoLine("messages", "target='"+target+"' since="+since, params, "limit", "cursor")
		return delegate(ctx, CatchUpOnChannel, params, echo)
	case target != "":
		params["handle"] = target
		echo := echoLine("messages", "target='"+target+"'", params, "limit")
		return delegate(ctx, Read, params, echo)
	default:
		return &FeatureResult{
			Success: false,
			Message: "messages needs an address: target='<handle|#channel|@person>' (optionally with around=<ts> or since=<window>), or query='<slack search>' and/or filters (in, from, after, before, has, thread)",
		}, nil
	}
}

// conversationOf accepts an inbox event handle where a channel is
// expected: any target that decodes yields its conversation, everything
// else passes through untouched.
func conversationOf(target string) string {
	if ref, err := handle.Decode(target); err == nil && ref.Channel != "" {
		return ref.Channel
	}
	return target
}

// ---- say: contribute content (the one visible write besides mark-read) ----

var Say = &Feature{
	Name:        "say",
	Description: "Contribute content, Slack-visible: text posts a message (to a channel, DM, or thread — a thread reply can also go to the channel); text+at schedules it for Slack to send later, and cancel withdraws a scheduled one; files attaches up to 10 files from the exchange directory to one message, with text as its comment; emoji+messageTs adds or removes a reaction — a reaction is a small say. This is a write; everything it posts is attributed to your user.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Where: '#channel', '@handle' (a DM needs the exact handle, or '@me'), or a channel ID",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "The message to post; with files, the comment the files are shared with",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Attach files, at most 10, as one message: bare names inside the exchange directory (the directory download saves into), not paths. Copy a file in first, or write it with put when your file tools cannot reach that directory. Not with emoji or broadcast.",
			},
			"thread": map[string]interface{}{
				"type":        "string",
				"description": "Thread timestamp to reply into",
			},
			"broadcast": map[string]interface{}{
				"type":        "boolean",
				"description": "With thread: also post the reply to the channel (Slack's 'Also send to #channel'). An error without thread, or with emoji.",
				"default":     false,
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
			"at": map[string]interface{}{
				"type":        "string",
				"description": "With text: schedule the message for Slack to send at this time instead of now, 2 minutes to 120 days out. RFC 3339 with an offset ('2026-10-02T09:00:00-06:00'), a date and time with no offset (read in your Slack profile zone; refused if this machine's zone disagrees), or Unix seconds. Not with files or emoji.",
			},
			"cancel": map[string]interface{}{
				"type":        "string",
				"description": "Withdraw a scheduled message: its handle from say at= or messages scheduled=true. On its own; to is not needed.",
			},
		},
		"required": []string{},
	},
	Handler: sayHandler,
}

func sayHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	ap, _ := params["_provider"].(*provider.ApiProvider)
	if refusal := preWriteLocal(ctx, ap, "say"); refusal != nil {
		return refusal, nil
	}

	to, _ := params["to"].(string)
	text, _ := params["text"].(string)
	emoji, _ := params["emoji"].(string)

	broadcast, _ := params["broadcast"].(bool)
	thread, _ := params["thread"].(string)
	// at= is decided by presence: a blank value must be refused by the
	// parser, never fall through to an immediate post.
	rawAt, hasAt := params["at"]
	hasAt = hasAt && rawAt != nil
	at := atString(rawAt)

	if cancel, _ := params["cancel"].(string); cancel != "" {
		_, hasFiles := params["files"]
		if text != "" || emoji != "" || hasAt || (hasFiles && params["files"] != nil) {
			return &FeatureResult{Success: false, Message: "cancel= stands alone: it withdraws a scheduled message and sends nothing, so it can't be combined with text, files, emoji, or at. Nothing was done."}, nil
		}
		res, err := cancelScheduled(ctx, ap, cancel)
		if res != nil {
			if res.RenderAs == "" {
				res.RenderAs = "say-cancelled"
			}
			res.Echo = "say cancel='" + cancel + "'"
		}
		return res, err
	}
	if to == "" {
		return &FeatureResult{Success: false, Message: "say needs to='#channel' or to='@handle' (or cancel='<handle>' to withdraw a scheduled message)."}, nil
	}

	// files= is checked first and whole: every refusal here and in the
	// upload path's local checks happens before any Slack call.
	if raw, ok := params["files"]; ok && raw != nil {
		refuse := func(msg string) (*FeatureResult, error) {
			return &FeatureResult{Success: false, Message: msg, Guidance: "Nothing was sent, the text included."}, nil
		}
		if emoji != "" {
			return refuse("files attaches to a message; it can't be combined with emoji (a reaction). Send the reaction in its own say.")
		}
		if hasAt {
			return refuse("at can't be combined with files: Slack does not attach files to a scheduled message. Schedule the text alone, or share the files now.")
		}
		if broadcast {
			return refuse("broadcast can't be combined with files: Slack's file share has no 'also send to the channel'. Share the files into the thread without broadcast, or broadcast a text reply on its own.")
		}
		names, err := parseFilesParam(raw)
		if err != nil {
			return refuse(err.Error())
		}
		echo := echoLine("say", "to='"+to+"'", params, "files", "thread")
		res, err := sayFilesHandler(ctx, params, names)
		if res != nil {
			res.RenderAs = "say-files"
			res.Echo = echo
		}
		return res, err
	}

	if broadcast {
		if emoji != "" {
			return &FeatureResult{
				Success: false,
				Message: "broadcast applies to a thread reply with text; it can't be combined with emoji (a reaction). Drop broadcast, or reply with text='...' thread='<ts>' broadcast=true.",
			}, nil
		}
		if thread == "" {
			return &FeatureResult{
				Success: false,
				Message: "broadcast needs thread='<ts>': it sends a thread reply to the channel too. A top-level message already reaches the channel.",
			}, nil
		}
	}

	if hasAt {
		if emoji != "" {
			return &FeatureResult{Success: false, Message: "at schedules a message; it can't be combined with emoji (a reaction). Nothing was scheduled."}, nil
		}
		if text == "" {
			return &FeatureResult{Success: false, Message: "at needs text='...': the message to schedule. Nothing was scheduled."}, nil
		}
		params["at"] = at
		echo := echoLine("say", "to='"+to+"'", params, "at", "thread", "broadcast")
		z := zones{profile: profileZone(ctx, ap), system: systemZone()}
		when, err := parseAt(at, scheduleNow(), z.profile, z.system)
		if err != nil {
			return &FeatureResult{Success: false, Message: err.Error() + " Nothing was scheduled.", Echo: echo}, nil
		}
		res, err := scheduleSend(ctx, ap, params, when, z)
		if res != nil {
			if res.RenderAs == "" {
				res.RenderAs = "say-scheduled"
			}
			res.Echo = echo
		}
		return res, err
	}

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
		echo := echoLine("say", "to='"+to+"'", params, "thread", "broadcast")
		return delegate(ctx, WriteMessage, params, echo)
	default:
		return &FeatureResult{
			Success: false,
			Message: "say needs text='...' (a message), files=['name'] (attachments from the exchange directory), or emoji='...' messageTs='...' (a reaction)",
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
	Description: "Download a file shared in Slack into the exchange directory, the one local directory file parameters can name. filename= is a bare name, not a path; a taken name is saved with a ' (n)' suffix and the result says so.",
	Schema:      DownloadFile.Schema,
	Handler:     downloadHandler,
}

func downloadHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	return delegate(ctx, DownloadFile, params, "")
}
