package main

import (
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

// A lift has no conversation behind it, so its summary carries no empty
// "()" where a conversation ID would go.
func TestLiftSummaryHasNoEmptyID(t *testing.T) {
	r := safety.Request{
		Tool:        "say",
		Destination: safety.Destination{Kind: safety.DestChannel, Name: "every write"},
		Lift:        []safety.Key{safety.StrikesKey},
	}
	got := summary(r)
	if strings.Contains(got, "()") || !strings.HasPrefix(got, "say to every write") {
		t.Errorf("summary = %q, want no empty ID", got)
	}
}
