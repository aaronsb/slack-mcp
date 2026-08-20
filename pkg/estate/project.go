package estate

import (
	"github.com/slack-go/slack"
)

// The projections are the estate's whole vocabulary: events carry these
// named field sets, never the slack-go structs. A library upgrade that
// reshapes the upstream struct then cannot produce a spurious delta storm,
// and the indefinitely-retained file holds strictly less personal data than
// the disposable snapshot — email, phone, avatar, and status are excluded
// by ADR-007's decision, not by omission.
//
// Adding a field keeps the schema version: a field absent from an older
// record means unobserved then, never empty, and the diff pass compares
// only what both sides carry. Removing or re-meaning a field bumps the
// version, and old versions stay readable forever because the estate file
// is never rewritten.

// UserProps is estate.user/v1.
type UserProps struct {
	Name        string `json:"name"`
	RealName    string `json:"realName"`
	DisplayName string `json:"displayName"`
	Title       string `json:"title"`
	IsBot       bool   `json:"isBot"`
	Deleted     bool   `json:"deleted"`
	// IsStranger and TeamID identify external (Slack Connect) users, who
	// appear in traffic but never in this workspace's users.list — the
	// absence pass must not tombstone them for that.
	IsStranger bool   `json:"isStranger,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
}

// ChannelProps is estate.channel/v1.
type ChannelProps struct {
	Name       string `json:"name"`
	IsArchived bool   `json:"isArchived"`
	IsMember   bool   `json:"isMember"`
	IsPrivate  bool   `json:"isPrivate"`
	IsIM       bool   `json:"isIM"`
	IsMpim     bool   `json:"isMpim"`
	User       string `json:"user,omitempty"`
	Purpose    string `json:"purpose"`
	// Creator and Created are ADR-008's ownership fields, additive to
	// estate.channel/v1. On records folded from events that predate them
	// they read as zero values — unobserved then — and the next
	// enumeration observes them as an honest first sight.
	Creator string `json:"creator,omitempty"`
	Created int64  `json:"created,omitempty"`
}

// ProjectUser extracts the estate-relevant fields from a slack-go user.
func ProjectUser(u slack.User) UserProps {
	return UserProps{
		Name:        u.Name,
		RealName:    u.RealName,
		DisplayName: u.Profile.DisplayName,
		Title:       u.Profile.Title,
		IsBot:       u.IsBot,
		Deleted:     u.Deleted,
		IsStranger:  u.IsStranger,
		TeamID:      u.TeamID,
	}
}

// ProjectChannel extracts the estate-relevant fields from a slack-go
// channel.
func ProjectChannel(c slack.Channel) ChannelProps {
	return ChannelProps{
		Name:       c.Name,
		IsArchived: c.IsArchived,
		IsMember:   c.IsMember,
		IsPrivate:  c.IsPrivate,
		IsIM:       c.IsIM,
		IsMpim:     c.IsMpIM,
		User:       c.User,
		Purpose:    c.Purpose.Value,
		Creator:    c.Creator,
		Created:    int64(c.Created),
	}
}

// diffUser names the fields that differ, in the projection's JSON
// vocabulary so a ledger line's changed list reads against its rec.
func diffUser(a, b UserProps) []string {
	var out []string
	if a.Name != b.Name {
		out = append(out, "name")
	}
	if a.RealName != b.RealName {
		out = append(out, "realName")
	}
	if a.DisplayName != b.DisplayName {
		out = append(out, "displayName")
	}
	if a.Title != b.Title {
		out = append(out, "title")
	}
	if a.IsBot != b.IsBot {
		out = append(out, "isBot")
	}
	if a.Deleted != b.Deleted {
		out = append(out, "deleted")
	}
	if a.IsStranger != b.IsStranger {
		out = append(out, "isStranger")
	}
	if a.TeamID != b.TeamID {
		out = append(out, "teamId")
	}
	return out
}

func diffChannel(a, b ChannelProps) []string {
	var out []string
	if a.Name != b.Name {
		out = append(out, "name")
	}
	if a.IsArchived != b.IsArchived {
		out = append(out, "isArchived")
	}
	if a.IsMember != b.IsMember {
		out = append(out, "isMember")
	}
	if a.IsPrivate != b.IsPrivate {
		out = append(out, "isPrivate")
	}
	if a.IsIM != b.IsIM {
		out = append(out, "isIM")
	}
	if a.IsMpim != b.IsMpim {
		out = append(out, "isMpim")
	}
	if a.User != b.User {
		out = append(out, "user")
	}
	if a.Purpose != b.Purpose {
		out = append(out, "purpose")
	}
	if a.Creator != b.Creator {
		out = append(out, "creator")
	}
	if a.Created != b.Created {
		out = append(out, "created")
	}
	return out
}
