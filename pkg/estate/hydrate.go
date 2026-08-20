package estate

import (
	"github.com/slack-go/slack"
)

// Hydration rebuilds slack-go structs from fold records, for when the
// snapshot cache has lost an entity the estate still knows. The projection
// is lossy by design, so a hydrated struct is a skeleton: enough for
// resolution and rendering, repaired to full on the next on-demand fetch.

// HydrateUser builds a skeleton slack.User from a fold record. A tombstoned
// record hydrates with Deleted set, so the existing render and filter paths
// treat it as the dated fact it is.
func HydrateUser(rec UserRecord) slack.User {
	var u slack.User
	u.ID = rec.ID
	u.Name = rec.Props.Name
	u.RealName = rec.Props.RealName
	u.Profile.DisplayName = rec.Props.DisplayName
	u.Profile.Title = rec.Props.Title
	u.IsBot = rec.Props.IsBot
	u.Deleted = rec.Props.Deleted || rec.Gone != nil
	return u
}

// HydrateChannel builds a skeleton slack.Channel from a fold record.
func HydrateChannel(rec ChannelRecord) slack.Channel {
	var c slack.Channel
	c.ID = rec.ID
	c.Name = rec.Props.Name
	c.IsArchived = rec.Props.IsArchived
	c.IsMember = rec.Props.IsMember
	c.IsPrivate = rec.Props.IsPrivate
	c.IsIM = rec.Props.IsIM
	c.IsMpIM = rec.Props.IsMpim
	c.User = rec.Props.User
	c.Purpose.Value = rec.Props.Purpose
	c.IsChannel = !rec.Props.IsIM && !rec.Props.IsMpim
	return c
}
