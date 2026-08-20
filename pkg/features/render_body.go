package features

// The body renderer resolves message tags against everything the server
// knows: the users map, the session identity (so the agent can tell a
// mention of its own user — issue #44's hedge), the channel map, and the
// estate fold, which answers for people who have left with their last-known
// name and dated exit. Built once per handler invocation; the map copies
// happen once, not per message.

import (
	"fmt"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

func newBodyRenderer(ap *provider.ApiProvider) func(string) string {
	users := ap.ProvideUsersMap()
	selfID := ""
	if id := ap.ProvideIdentity(); id != nil {
		selfID = id.UserID
	}

	resolve := func(kind text.TagKind, id, label string) (string, bool) {
		switch kind {
		case text.TagUser:
			if u, ok := users[id]; ok {
				name := displayNameFor(u)
				if id == selfID {
					return "@" + name + " (you)", true
				}
				if u.Deleted {
					if rec, ok := ap.EstateUser(id); ok && rec.Gone != nil {
						return fmt.Sprintf("@%s (%s)", name, goneNote(rec.Gone)), true
					}
					return "@" + name + " (deactivated)", true
				}
				return "@" + name, true
			}
			if rec, ok := ap.EstateUser(id); ok {
				name := rec.Props.RealName
				if name == "" {
					name = rec.Props.Name
				}
				if name == "" {
					return "", false
				}
				if rec.Gone != nil {
					return fmt.Sprintf("@%s (%s)", name, goneNote(rec.Gone)), true
				}
				return "@" + name, true
			}
			if label != "" {
				return "@" + label, true
			}
			return "", false

		case text.TagChannel:
			// The cached name outranks the tag's send-time label: labels
			// freeze at send time, so a renamed channel would otherwise
			// render under its stale name forever.
			if name := ap.ResolveChannelNameCached(id); name != "" {
				return "#" + name, true
			}
			if label != "" {
				return "#" + label, true
			}
			if rec, ok := ap.EstateChannel(id); ok {
				name := rec.Props.Name
				if name == "" {
					return "", false
				}
				if rec.Gone != nil {
					return fmt.Sprintf("#%s (%s)", name, goneNote(rec.Gone)), true
				}
				return "#" + name, true
			}
			return "", false

		default:
			return "", false
		}
	}

	return func(s string) string {
		return text.ResolveTags(s, resolve)
	}
}

// goneNote renders a tombstone in ADR-007's interval vocabulary: the exit
// happened in (NotBefore, At], and a point date is claimed only when the
// ledger holds no earlier bound.
func goneNote(g *estate.Tombstone) string {
	at := g.At.Format("2006-01-02")
	if g.NotBefore.IsZero() || g.NotBefore.Format("2006-01-02") == at {
		return g.Reason + " " + at
	}
	return fmt.Sprintf("%s between %s and %s", g.Reason, g.NotBefore.Format("2006-01-02"), at)
}

// displayNameFor is the one name-fallback order every renderer shares:
// RealName, then profile display name, then handle.
func displayNameFor(u slack.User) string {
	if u.RealName != "" {
		return u.RealName
	}
	if u.Profile.DisplayName != "" {
		return u.Profile.DisplayName
	}
	return u.Name
}
