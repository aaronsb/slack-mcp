package features

import (
	"context"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// The seams for outbound safety (ADR-013). Its order is: the strike lock,
// ADR-012's name and file checks, the destination's quarantine, the
// scanner, the approval gate; every refusal comes before any content
// reaches Slack and before any conversations.open. The two hooks below are
// where those steps attach; until ADR-013 lands they pass everything.
//
//	preWriteLocal  first thing in every Slack-visible write (say in every
//	               mode, mark-read): the strike lock.
//	preSend        after the local checks and the non-opening destination
//	               resolution, before openDestination and the send, for say
//	               text and say files= alike: quarantine, scanner, gate.

// preWriteLocal runs before anything else in a Slack-visible write. A
// non-nil result refuses the call as-is. No-op until ADR-013's strike lock.
var preWriteLocal = func(ctx context.Context) *FeatureResult {
	return nil
}

// uploadFile is one file as read from the exchange directory. Data is the
// only copy of the bytes: what preSend reads is what is uploaded.
type uploadFile struct {
	Name string
	Type string
	Data []byte
}

// outbound is everything a say will send, assembled before any Slack call
// that carries content.
type outbound struct {
	// Thread is the thread ts, or empty for a top-level message.
	Thread    string
	Broadcast bool
	// Text is the message or comment as the caller supplied it; Fallback is
	// the mrkdwn text as it will be sent (NormalizeMrkdwn): the message
	// text of a post, or the initial_comment of an upload. Both are empty
	// for files alone.
	Text     string
	Fallback string
	// RichText is the rich_text block a text post sends beside Fallback,
	// or nil (an upload, or text the converter left empty).
	RichText *slack.RichTextBlock
	// Files are an upload's files in the order named, bytes included;
	// empty for a text post.
	Files []uploadFile
}

// preSend sees a say's destination and content after every local check and
// before any Slack call that opens a conversation or carries content.
// dest.ConvID is empty for a person with no DM yet; nothing has opened one.
// A non-nil result refuses the call as-is.
var preSend = func(ctx context.Context, ap *provider.ApiProvider, dest *resolvedDestination, out *outbound) *FeatureResult {
	return nil
}
