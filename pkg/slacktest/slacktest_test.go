package slacktest_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// The harness exists to make call counts assertable. Prove that a count taken
// after Quiesce stays stable, rather than being topped up by a background
// bootstrap request that had not landed yet.
func TestQuiesceSettlesBootstrapTraffic(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)

	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	srv.ResetCalls()

	// Anything still in flight from bootstrap would arrive during this window.
	time.Sleep(250 * time.Millisecond)

	for _, method := range []string{
		"auth.test", "users.list", "users.conversations",
		"conversations.list", "conversations.history", "client.counts",
	} {
		if got := srv.Calls(method); got != 0 {
			t.Errorf("%s was called %d times after Quiesce; bootstrap had not settled", method, got)
		}
	}
}

// A provider must start warm: channels resolvable without waiting on the
// background member-channel fetch.
func TestProviderStartsWarm(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)

	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	if got := ap.ResolveChannelID("eng"); got != "C1" {
		t.Errorf("ResolveChannelID(%q) = %q, want C1 — seeded cache did not load", "eng", got)
	}
}

// Overriding an endpoint must win over the default fixture, and the counter
// must still record the call.
func TestHandleOverridesFixture(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C9", "1782246000.000000", "1782246118.543969", true, 3),
			},
			nil,
		)
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	counts, err := ap.ProvideInternalClient().GetClientCounts(context.Background())
	if err != nil {
		t.Fatalf("client.counts: %v", err)
	}
	if len(counts.Channels) != 1 || counts.Channels[0].ID != "C9" {
		t.Fatalf("override did not win over the default fixture: %+v", counts.Channels)
	}
	if got := srv.Calls("client.counts"); got != 1 {
		t.Errorf("client.counts counted %d times, want 1", got)
	}
}
