package features

// The body renderer resolves message tags against everything the server
// knows: the users map, the session identity (so the agent can tell a
// mention of its own user — issue #44's hedge), the channel map, and the
// estate fold, which answers for people who have left with their last-known
// name and dated exit. Built once per handler invocation; the map copies
// happen once, not per message.

import (
	"fmt"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

func newBodyRenderer(ap *provider.ApiProvider) func(string) string {
	users := ap.ProvideUsersMap()
	estateUsers := ap.EstateUsers()
	estateChannels := ap.EstateChannels()
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
					if rec, ok := estateUsers[id]; ok && rec.Gone != nil {
						return fmt.Sprintf("@%s (%s %s)", name, rec.Gone.Reason, rec.Gone.At.Format("2006-01-02")), true
					}
					return "@" + name + " (deactivated)", true
				}
				return "@" + name, true
			}
			if rec, ok := estateUsers[id]; ok {
				name := rec.Props.RealName
				if name == "" {
					name = rec.Props.Name
				}
				if rec.Gone != nil {
					return fmt.Sprintf("@%s (%s %s)", name, rec.Gone.Reason, rec.Gone.At.Format("2006-01-02")), true
				}
				return "@" + name, true
			}
			if label != "" {
				return "@" + label, true
			}
			return "", false

		case text.TagChannel:
			if label != "" {
				return "#" + label, true
			}
			if name := ap.ResolveChannelNameCached(id); name != "" {
				return "#" + name, true
			}
			if rec, ok := estateChannels[id]; ok {
				name := rec.Props.Name
				if name == "" {
					return "", false
				}
				if rec.Gone != nil {
					return fmt.Sprintf("#%s (%s %s)", name, rec.Gone.Reason, rec.Gone.At.Format("2006-01-02")), true
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

func displayNameFor(u slack.User) string {
	if u.RealName != "" {
		return u.RealName
	}
	if u.Profile.DisplayName != "" {
		return u.Profile.DisplayName
	}
	return u.Name
}
