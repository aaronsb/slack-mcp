package features

import (
	"testing"

	"github.com/slack-go/slack"
)

// One rule decides a mention for every view: a join event for yourself
// carries your tag but is not a mention, nor is your own message.
func TestMentionsUser(t *testing.T) {
	msg := func(user, subtype, text string) slack.Message {
		var m slack.Message
		m.User, m.SubType, m.Text = user, subtype, text
		return m
	}
	for _, tc := range []struct {
		name string
		m    slack.Message
		want bool
	}{
		{"someone mentions me", msg("U2", "", "<@UME> can you look?"), true},
		{"my own join event", msg("UME", "channel_join", "<@UME> has joined the channel"), false},
		{"added to a private channel", msg("UME", "group_join", "<@UME> has joined the group"), false},
		{"a join event naming me by another author", msg("U2", "channel_join", "<@U2> has joined with <@UME>"), false},
		{"my own message naming me", msg("UME", "", "note to <@UME>"), false},
		{"someone else mentioned", msg("U2", "", "<@U3> ping"), false},
		{"a prefix of my ID", msg("U2", "", "<@UMEX> ping"), false},
	} {
		if got := mentionsUser(tc.m, "UME"); got != tc.want {
			t.Errorf("%s: mentionsUser = %v, want %v", tc.name, got, tc.want)
		}
	}
	if mentionsUser(msg("U2", "", "<@> hi"), "") {
		t.Error("an unknown self ID matched an empty tag")
	}
}
