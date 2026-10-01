package safety

import "github.com/slack-go/slack"

// Org is the account's own organization, from auth.test: the team ID, the
// enterprise ID when there is one, and the account's own user ID.
type Org struct {
	TeamID       string
	EnterpriseID string
	UserID       string
}

// UserExternal reports whether a person is outside the organization
// (ADR-013, gate case 1): is_stranger, or a team other than the own team,
// unless both sit in the same Enterprise Grid organization. A user with no
// team ID fails closed as external.
func (o Org) UserExternal(u *slack.User) bool {
	if u == nil {
		return true
	}
	if u.ID == o.UserID {
		return false
	}
	if u.IsStranger {
		return true
	}
	if u.TeamID == o.TeamID && u.TeamID != "" {
		return false
	}
	return !(o.EnterpriseID != "" && u.Enterprise.EnterpriseID == o.EnterpriseID)
}

// ChannelExternal reports whether a channel is shared outside the
// organization: is_ext_shared, is_pending_ext_shared, or is_shared without
// is_org_shared (ambiguous, so external).
func ChannelExternal(ch *slack.Channel) bool {
	if ch == nil {
		return true
	}
	return ch.IsExtShared || ch.IsPendingExtShared || (ch.IsShared && !ch.IsOrgShared)
}

// ChannelParties returns a channel's external parties: the team IDs it is
// shared, connected, or pending-shared with, less the own team and, inside
// Grid, the sibling teams the channel lists as internal. Sorted.
func (o Org) ChannelParties(ch *slack.Channel) []string {
	if ch == nil {
		return nil
	}
	internal := map[string]bool{o.TeamID: true}
	if o.EnterpriseID != "" {
		for _, t := range ch.InternalTeamIDs {
			internal[t] = true
		}
	}
	var all []string
	all = append(all, ch.SharedTeamIDs...)
	all = append(all, ch.ConnectedTeamIDs...)
	all = append(all, ch.PendingShared...)
	var out []string
	for _, t := range all {
		if !internal[t] {
			out = append(out, t)
		}
	}
	return normStrings(out)
}
