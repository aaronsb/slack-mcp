package features

import (
	"fmt"
	"strings"
	"testing"
)

// A DM's history window runs oldest first and can hold read messages, so
// the render shows the newest five, marks the unread ones, and counts the
// rest as earlier, never as more unreads; channel rows carry their own label.
func TestUnreadsShowNewestDMsAndLabeledConversations(t *testing.T) {
	var msgs []map[string]interface{}
	for i := 1; i <= 8; i++ {
		msgs = append(msgs, map[string]interface{}{"user": "Bob", "text": fmt.Sprintf("message %d", i), "timestamp": fmt.Sprintf("May %d", i), "unread": i > 6})
	}
	res := &FeatureResult{Success: true, Data: map[string]interface{}{
		"unreads": map[string]interface{}{
			"dms": []map[string]interface{}{{"author": "Bob", "unreadCount": 2, "messages": msgs}},
			"mentions": []map[string]interface{}{
				{"channel": "group: alice, bockeliea", "author": "Alice", "message": "hi", "timestamp": "now"},
			},
			"channels": []map[string]interface{}{{"channel": "#eng", "lastMessage": "ship it"}},
		},
	}}
	out := formatUnreads(res)

	for _, want := range []string{"(2 unread)", "(3 earlier messages not shown)", "● May 8 | Bob: message 8", "● May 7 | Bob: message 7", "  May 6 | Bob: message 6", "  May 4 | Bob: message 4", "group: alice, bockeliea | Alice", "#eng\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"Bob: message 3\n", "● May 6", "more messages", "#group:", "##eng"} {
		if strings.Contains(out, bad) {
			t.Errorf("render has %q:\n%s", bad, out)
		}
	}
}

// Mention rows carry their own label, so a group DM is never shown as
// "#group: …" or by Slack's mpdm-… wire name.
func TestMentionsRenderTheConversationLabel(t *testing.T) {
	res := &FeatureResult{Success: true, Data: map[string]interface{}{
		"mentions": []map[string]interface{}{
			{"channel": "group: alice, bockeliea", "author": "Alice", "message": "hi", "timestamp": "now"},
			{"channel": "#eng", "author": "Bo", "message": "yo", "timestamp": "now"},
		},
	}}
	out := formatMentions(res)
	if !strings.Contains(out, "group: alice, bockeliea | Alice") || !strings.Contains(out, "#eng | Bo") || strings.Contains(out, "##eng") || strings.Contains(out, "#group") {
		t.Errorf("mention labels wrong:\n%s", out)
	}
}

// Every unread shows, up to ten, even past the newest five; a window that
// is all unread with more behind it counts as "at least".
func TestUnreadsShowEveryUnreadUpToTen(t *testing.T) {
	var msgs []map[string]interface{}
	for i := 1; i <= 20; i++ {
		msgs = append(msgs, map[string]interface{}{"user": "Bob", "text": fmt.Sprintf("m%d", i), "timestamp": fmt.Sprintf("t%d", i), "unread": true})
	}
	res := &FeatureResult{Success: true, Data: map[string]interface{}{
		"unreads": map[string]interface{}{
			"dms": []map[string]interface{}{{"author": "Bob", "unreadCount": 20, "unreadAtLeast": true, "messages": msgs}},
		},
	}}
	out := formatUnreads(res)
	for _, want := range []string{"(20+ unread)", "(10 earlier messages not shown)", "● t11 | Bob: m11", "● t20 | Bob: m20"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "t10 |") {
		t.Errorf("render shows more than ten:\n%s", out)
	}
}
