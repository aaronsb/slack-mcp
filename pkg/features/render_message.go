package features

// renderMessage is ADR-004's seam (issue #63): one normalizer turning every
// slack.Message into an author and a body, so no formatter assembles
// message content by hand. The body comes from a rich_text block when the
// message has one (Slack regenerates the text field from it with every
// newline replaced by a space), else from the text field, else from other
// Block Kit blocks or attachments flattened to text (issue #45: the
// highest-signal announcement posts carried no top-level text and rendered
// blank), else from the names of its files. Tags resolve through the same
// resolver the body renderer uses; tags that do not resolve stay raw in the
// body and are also reported in Unresolved, ADR-005's repair queue. The
// three entities Slack escapes are decoded last.

import (
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

// RenderedMessage is a message normalized for rendering.
type RenderedMessage struct {
	AuthorID   string
	Author     string
	Body       string
	Unresolved []string
	TS         string
}

// messageRenderer carries the once-per-invocation state: the users map,
// the session identity, and the tag resolver.
type messageRenderer struct {
	ap      *provider.ApiProvider
	users   map[string]slack.User
	selfID  string
	resolve func(kind text.TagKind, id, label string) (string, bool)
	// ext numbers unnamed externals for the life of this renderer (one
	// response).
	ext externalNamer
}

func newMessageRenderer(ap *provider.ApiProvider) *messageRenderer {
	users := ap.ProvideUsersMap()
	selfID := ""
	if id := ap.ProvideIdentity(); id != nil {
		selfID = id.UserID
	}
	return &messageRenderer{
		ap:      ap,
		users:   users,
		selfID:  selfID,
		resolve: newTagResolver(ap, users, selfID),
	}
}

// Render normalizes one message.
func (r *messageRenderer) Render(m slack.Message) RenderedMessage {
	blocks := flattenBlocks(m.Blocks)
	var body string
	switch {
	case blocks != "" && text.RichTextToMrkdwn(m.Blocks.BlockSet) != "":
		body = blocks
	case strings.TrimSpace(m.Text) != "" && m.Text != noPreview:
		body = m.Text
	case blocks != "":
		body = blocks
	default:
		body = flattenAttachments(m.Attachments)
	}
	if strings.TrimSpace(body) == "" {
		body = fileLabel(m.Files)
	}
	resolved, unresolved := text.ResolveTagsReport(body, r.resolve)
	return RenderedMessage{
		AuthorID:   m.User,
		Author:     r.authorName(m),
		Body:       text.Unescape(resolved),
		Unresolved: unresolved,
		TS:         m.Timestamp,
	}
}

// RenderText resolves tags in a bare string — for surfaces that carry text
// without a full message (topics, purposes, thread previews).
func (r *messageRenderer) RenderText(s string) string {
	out, _ := text.ResolveTagsReport(s, r.resolve)
	return text.Unescape(out)
}

// noPreview is the text Slack stores for a blocks-only app message.
const noPreview = "[no preview available]"

// fileLabel names a file-only message's files, so it never renders blank.
func fileLabel(files []slack.File) string {
	if len(files) == 0 {
		return ""
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
		if names[i] == "" {
			names[i] = f.Title
		}
	}
	return "[file: " + strings.Join(names, ", ") + "]"
}

// authorName is the one attribution chain: users map (with the self
// marker), estate fold for the departed, then the bot identity fields a
// blocks-only announcement usually carries, then the raw ID.
func (r *messageRenderer) authorName(m slack.Message) string {
	if m.User != "" {
		return r.authorByID(m.User, m.Username)
	}
	if m.Username != "" {
		return m.Username + " (app)"
	}
	if m.BotProfile != nil && m.BotProfile.Name != "" {
		return m.BotProfile.Name + " (app)"
	}
	return "unknown"
}

// AuthorByID attributes a bare user ID through the same chain, for
// surfaces that carry an ID without a full message (thread-feed roots).
// The unknown-ID form matches userLabel so every view labels the same
// person the same way.
func (r *messageRenderer) AuthorByID(id string) string {
	return r.authorByID(id, "")
}

// authorByID is AuthorByID with the message's own username as a last
// named source before an unnamed external is numbered.
func (r *messageRenderer) authorByID(id, username string) string {
	if u, ok := r.users[id]; ok {
		name := displayNameFor(u)
		if id == r.selfID {
			return name + " (you)"
		}
		if u.Deleted {
			return name + " (departed)"
		}
		return name
	}
	if rec, ok := r.ap.EstateUser(id); ok {
		name := rec.Props.RealName
		if name == "" {
			name = rec.Props.Name
		}
		if name != "" {
			if rec.Gone != nil {
				return name + " (departed)"
			}
			return name
		}
	}
	if username != "" {
		return username
	}
	return r.ext.label(id)
}

// flattenBlocks renders Block Kit content as plain text. User and channel
// elements re-emit tag syntax so the normal resolver handles them; unknown
// block types are skipped rather than guessed at.
func flattenBlocks(blocks slack.Blocks) string {
	var parts []string
	for _, b := range blocks.BlockSet {
		if s := flattenBlock(b); strings.TrimSpace(s) != "" {
			parts = append(parts, strings.TrimSpace(s))
		}
	}
	return strings.Join(parts, "\n")
}

func flattenBlock(b slack.Block) string {
	switch blk := b.(type) {
	case *slack.SectionBlock:
		var parts []string
		if blk.Text != nil {
			parts = append(parts, blk.Text.Text)
		}
		for _, f := range blk.Fields {
			if f != nil {
				parts = append(parts, f.Text)
			}
		}
		return strings.Join(parts, "\n")
	case *slack.HeaderBlock:
		if blk.Text != nil {
			return blk.Text.Text
		}
	case *slack.ContextBlock:
		var parts []string
		for _, el := range blk.ContextElements.Elements {
			if t, ok := el.(*slack.TextBlockObject); ok {
				parts = append(parts, t.Text)
			}
		}
		return strings.Join(parts, " ")
	case *slack.ImageBlock:
		if blk.AltText != "" {
			return "[image: " + blk.AltText + "]"
		}
	case *slack.RichTextBlock:
		return text.RichTextToMrkdwn([]slack.Block{blk})
	}
	return ""
}

// flattenAttachments falls back to legacy attachments: the fallback line,
// or title plus text.
func flattenAttachments(atts []slack.Attachment) string {
	var parts []string
	for _, a := range atts {
		switch {
		case a.Fallback != "":
			parts = append(parts, a.Fallback)
		case a.Title != "" || a.Text != "":
			parts = append(parts, strings.TrimSpace(strings.TrimSpace(a.Title)+"\n"+a.Text))
		}
	}
	return strings.Join(parts, "\n")
}
