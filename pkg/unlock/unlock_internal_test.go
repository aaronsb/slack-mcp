package unlock

import (
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

// The strikes key has no name or ID; its log line carries neither an
// empty name nor empty brackets.
func TestLogKeyOmitsWhatAKeyLacks(t *testing.T) {
	if got := logKey(safety.StrikesKey); got != "strikes" {
		t.Errorf("logKey(strikes) = %q, want %q", got, "strikes")
	}
	k := safety.Key{Kind: safety.KeyConversation, ID: "C1", Name: "#eng"}
	if got := logKey(k); got != "conversation #eng (C1)" {
		t.Errorf("logKey(conversation) = %q", got)
	}
}
