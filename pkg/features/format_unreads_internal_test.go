package features

import (
	"fmt"
	"strings"
	"testing"
)

// A DM's history window runs oldest first and can hold read messages, so
// the render shows the newest five and counts the rest as earlier, never as
// more unreads; channel rows carry their own label.
func TestUnreadsShowNewestDMsAndLabeledConversations(t *testing.T) {
	var msgs []map[string]interface{}
	for i := 1; i <= 8; i++ {
		msgs = append(msgs, map[string]interface{}{"user": "Bob", "text": fmt.Sprintf("message %d", i), "timestamp": fmt.Sprintf("May %d", i)})
	}
	res := &FeatureResult{Success: true, Data: map[string]interface{}{
		"unreads": map[string]interface{}{
			"dms": []map[string]interface{}{{"author": "Bob", "unreadCount": 3, "messages": msgs}},
			"mentions": []map[string]interface{}{
				{"channel": "group: alice, bockeliea", "author": "Alice", "message": "hi", "timestamp": "now"},
			},
			"channels": []map[string]interface{}{{"channel": "#eng", "lastMessage": "ship it"}},
		},
	}}
	out := formatUnreads(res)

	for _, want := range []string{"(3 earlier messages not shown)", "message 8", "message 4", "group: alice, bockeliea | Alice", "#eng\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"Bob: message 3\n", "more messages", "#group:", "##eng"} {
		if strings.Contains(out, bad) {
			t.Errorf("render has %q:\n%s", bad, out)
		}
	}
}
